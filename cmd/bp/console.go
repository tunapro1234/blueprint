package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"blueprint/internal/dashboard"
	"blueprint/internal/modules"
)

// console serves the fleet console — the ui module's SPA plus its /v1 endpoints
// (fleet, tasks, usage, workflows) — on a loopback-only address and opens it in
// a browser. It is a user-invoked local tool: the listener binds 127.0.0.1 and
// the endpoints carry no auth of their own, the same loopback trust model as the
// local usage dashboard. The always-on daemon and its token-gated API are
// untouched; this is a separate short-lived server over the same live state
// (one source of truth: the agentbook, tmux runtime, usage logs, workflow store).
func (a *app) console(args []string) error {
	port := 8788
	openInBrowserOnStart := true
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--port":
			if index+1 >= len(args) {
				return fmt.Errorf("usage: bp console [--port N] [--no-open]")
			}
			index++
			value, err := strconv.Atoi(args[index])
			if err != nil || value < 0 || value > 65535 {
				return fmt.Errorf("invalid --port: %s", args[index])
			}
			port = value
		case "--no-open":
			openInBrowserOnStart = false
		case "-h", "--help":
			fmt.Fprintln(a.out, "usage: bp console [--port N] [--no-open]")
			return nil
		default:
			return fmt.Errorf("unknown console option: %s", args[index])
		}
	}

	if !modules.EnabledIn(a.config, modules.UI) {
		fmt.Fprintln(a.err, "note: the ui module is not enabled; serving the console for this session anyway (run `bp modules enable ui` to keep it).")
	}

	// The console endpoints and the SPA share one loopback origin so the SPA's
	// same-origin /v1 calls resolve without CORS. The token is injected empty:
	// these local endpoints need none, so the SPA issues tokenless same-origin
	// requests.
	mux := a.consoleMux()
	spa, err := dashboard.ConsoleHandlerWithToken("")
	if err != nil {
		return fmt.Errorf("console bundle: %w (build it with `npm ci && npm run build` in ui/console)", err)
	}
	mux.Handle("/", spa)

	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("listen on 127.0.0.1:%d: %w", port, err)
	}
	url := "http://" + listener.Addr().String()
	fmt.Fprintf(a.out, "bp console serving at %s (Ctrl-C to stop)\n", url)

	ctx, stop := signal.NotifyContext(a.ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if openInBrowserOnStart {
		openInBrowser(url)
	}

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// openInBrowser best-effort launches the platform browser on url. A failure is
// ignored: the URL is already printed, so the user can open it by hand.
func openInBrowser(url string) {
	var name string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name = "explorer"
	default:
		name = "xdg-open"
	}
	_ = exec.Command(name, url).Start()
}
