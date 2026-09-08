package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

type PeerStatus struct {
	Name      string `json:"name"`
	ID        string `json:"id"`
	Connected bool   `json:"connected"`
	Path      string `json:"path,omitempty"`
}
type Info struct {
	ID        string       `json:"id"`
	PID       int          `json:"pid"`
	Addresses []string     `json:"addresses"`
	Relay     bool         `json:"relay"`
	Peers     []PeerStatus `json:"peers"`
	Observed  time.Time    `json:"observed"`
}

func (n *Node) Info() Info {
	i := Info{ID: n.Host.ID().String(), PID: os.Getpid(), Addresses: n.Addresses(), Relay: n.Config.Relay, Observed: time.Now().UTC(), Peers: []PeerStatus{}}
	for alias, p := range n.Config.Peers {
		s := PeerStatus{Name: alias, ID: p.ID}
		id, _ := peer.Decode(p.ID)
		for _, c := range n.Host.Network().ConnsToPeer(id) {
			s.Connected = true
			if !c.Stat().Limited {
				s.Path = "direct"
				break
			}
			s.Path = "relay"
		}
		i.Peers = append(i.Peers, s)
	}
	return i
}
func SocketPath(root string) string { return statePath(root, "control.sock") }

// Serve runs only transport and the caller's ordinary guarded queue dispatcher.
// The local control socket is private; no network endpoint can issue BP commands.
func (n *Node) Serve(ctx context.Context, dispatch func()) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	socket := SocketPath(n.Root)
	if info, e := os.Lstat(socket); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket %s", socket)
		}
		if e = os.Remove(socket); e != nil {
			return e
		} // stale socket; exclusive service lock held
	} else if !os.IsNotExist(e) {
		return e
	}
	listener, e := net.Listen("unix", socket)
	if e != nil {
		return e
	}
	defer listener.Close()
	if e = os.Chmod(socket, 0600); e != nil {
		return e
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", 405)
			return
		}
		_ = json.NewEncoder(w).Encode(n.Info())
	})
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", 405)
			return
		}
		p, ok := n.Config.Peers[r.URL.Query().Get("peer")]
		if !ok {
			http.Error(w, "unknown peer", 404)
			return
		}
		id, _ := peer.Decode(p.ID)
		res, e := n.call(r.Context(), id, PingProtocol, request{})
		if e != nil {
			http.Error(w, e.Error(), 502)
			return
		}
		if res.ID != p.ID {
			http.Error(w, "identity mismatch", 502)
			return
		}
		_ = json.NewEncoder(w).Encode(res)
	})
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", 405)
			return
		}
		w.WriteHeader(204)
		cancel()
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	// Discovery can wait for offline rendezvous points; it cannot stop delivery
	// over existing direct connections or processing the local agent queue.
	discovered := make(chan struct{})
	go func() {
		defer close(discovered)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			if e := n.ConnectDiscovery(ctx); e != nil && ctx.Err() == nil && n.Log != nil {
				fmt.Fprintln(n.Log, "p2p discovery:", e)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	transferred := make(chan struct{})
	go func() {
		defer close(transferred)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if e := n.Step(ctx); e != nil && n.Log != nil {
					fmt.Fprintln(n.Log, "p2p outbox:", e)
				}
			}
		}
	}()
	defer func() {
		cancel()
		shutdown, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
		_ = server.Close()
		<-discovered
		<-transferred
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-done:
			return e
		case <-ticker.C:
			if dispatch != nil {
				dispatch()
			}
		}
	}
}

func Control(ctx context.Context, root, method, endpoint string, result any) error {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", SocketPath(root))
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 15 * time.Second}
	req, e := http.NewRequestWithContext(ctx, method, "http://bp"+endpoint, nil)
	if e != nil {
		return e
	}
	res, e := client.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("p2p: %s: %s", res.Status, string(b))
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(res.Body, maxFrame)).Decode(result)
	}
	return nil
}
func Ping(ctx context.Context, root, alias string) error {
	return Control(ctx, root, http.MethodGet, "/ping?peer="+url.QueryEscape(alias), &response{})
}
func LogPath(root string) string { return filepath.Join(root, "p2p", "service.log") }
