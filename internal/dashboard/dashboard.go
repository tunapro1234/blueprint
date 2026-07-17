// Package dashboard serves the monitor UI on a loopback HTTP address.
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	DefaultPort     = 8787
	DefaultURL      = "https://monitor.tunapro.xyz"
	DefaultSitePath = "/srv/monitor/site"
	maxIndexSize    = 10 << 20
)

// Options controls a local dashboard server.
type Options struct {
	Port     int
	SitePath string
	URL      string
	Out      io.Writer
	Client   *http.Client
	Open     func(string)
}

// Serve starts a loopback-only dashboard server and blocks until ctx is done.
func Serve(ctx context.Context, options Options) error {
	if options.Port == 0 {
		options.Port = DefaultPort
	}
	if options.SitePath == "" {
		options.SitePath = DefaultSitePath
	}
	if options.URL == "" {
		options.URL = DefaultURL
	}
	if options.Out == nil {
		options.Out = io.Discard
	}
	if options.Client == nil {
		options.Client = &http.Client{Timeout: 15 * time.Second}
	}
	if options.Open == nil {
		options.Open = openBrowser
	}

	handler, err := NewHandler(ctx, options.SitePath, options.URL, options.Client)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", options.Port))
	if err != nil {
		return fmt.Errorf("listen on port %d: %w", options.Port, err)
	}

	address := fmt.Sprintf("http://localhost:%d", options.Port)
	fmt.Fprintln(options.Out, "dashboard:", address)
	go options.Open(address)

	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	shutdownDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		case <-shutdownDone:
		}
	}()

	err = server.Serve(listener)
	close(shutdownDone)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// NewHandler serves sitePath directly when it exists. Otherwise it caches the
// upstream index page and reverse-proxies the dashboard's data and assets.
// refreshOnDemand serializes explicit refreshes so an F5 storm cannot stampede
// the collectors. Ordinary data.json polling must never invoke usage APIs.
type refreshOnDemand struct {
	mu       sync.Mutex
	lastRun  time.Time
	sitePath string
}

const refreshCooldown = 10 * time.Second

func (r *refreshOnDemand) maybeRefresh() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.lastRun) < refreshCooldown {
		return
	}
	r.lastRun = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	pulse := exec.CommandContext(ctx, "/srv/server-main/bin/usage-pulse")
	_ = pulse.Run()
	gen := exec.CommandContext(ctx, "/usr/bin/python3", filepath.Join(r.sitePath, "gen.py"))
	gen.Dir = r.sitePath
	_ = gen.Run()
}

func explicitDataRefresh(request *http.Request) bool {
	return (strings.HasSuffix(request.URL.Path, "/data.json") || request.URL.Path == "/data.json") &&
		request.URL.Query().Get("refresh") == "1"
}

func NewHandler(ctx context.Context, sitePath, rawURL string, client *http.Client) (http.Handler, error) {
	if info, err := os.Stat(sitePath); err == nil && info.IsDir() {
		files := http.FileServer(http.Dir(sitePath))
		fresh := &refreshOnDemand{sitePath: sitePath}
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			// The UI adds refresh=1 only to the first data request after an F5.
			// Its ordinary 60-second poll only reads the generated file.
			if strings.HasSuffix(request.URL.Path, "/data.json") || request.URL.Path == "/data.json" {
				if explicitDataRefresh(request) {
					fresh.maybeRefresh()
				}
				writer.Header().Set("Cache-Control", "no-store")
			}
			files.ServeHTTP(writer, request)
		}), nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect dashboard site: %w", err)
	}

	base, err := parseURL(rawURL)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	indexURL := base.ResolveReference(&url.URL{Path: "index.html"})
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create dashboard request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch dashboard index: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch dashboard index: %s", response.Status)
	}
	index, err := io.ReadAll(io.LimitReader(response.Body, maxIndexSize+1))
	if err != nil {
		return nil, fmt.Errorf("read dashboard index: %w", err)
	}
	if len(index) > maxIndexSize {
		return nil, fmt.Errorf("read dashboard index: response exceeds %d bytes", maxIndexSize)
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/html; charset=utf-8"
	}

	proxy := httputil.NewSingleHostReverseProxy(base)
	if client.Transport != nil {
		proxy.Transport = client.Transport
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, proxyErr error) {
		http.Error(writer, "dashboard upstream unavailable: "+proxyErr.Error(), http.StatusBadGateway)
	}

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/" || request.URL.Path == "/index.html" {
			writer.Header().Set("Content-Type", contentType)
			writer.Header().Set("Cache-Control", "no-store")
			if request.Method == http.MethodHead {
				return
			}
			if request.Method != http.MethodGet {
				writer.Header().Set("Allow", "GET, HEAD")
				http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			_, _ = writer.Write(index)
			return
		}
		proxy.ServeHTTP(writer, request)
	}), nil
}

func parseURL(raw string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid DASH_URL: %w", err)
	}
	if (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("invalid DASH_URL: expected an http(s) URL")
	}
	if base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("invalid DASH_URL: query strings and fragments are not supported")
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	return base, nil
}

func openBrowser(address string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return
	}
	command := exec.Command(path, address)
	if err := command.Start(); err != nil {
		return
	}
	_ = command.Wait()
}
