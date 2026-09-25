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
	"sort"
	"sync"
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
func (n *Node) Serve(ctx context.Context, dispatch func([]string)) error {
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
	mux.HandleFunc("/lookup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", 405)
			return
		}
		query := r.URL.Query().Get("find")
		if !ValidLookupQuery(query) {
			http.Error(w, "invalid lookup query", 400)
			return
		}
		budget := 2 * time.Second
		if value := r.URL.Query().Get("timeout"); value != "" {
			parsed, err := time.ParseDuration(value)
			if err != nil || parsed <= 0 || parsed > 15*time.Second {
				http.Error(w, "invalid lookup timeout", 400)
				return
			}
			budget = parsed
		}
		report := n.lookupPeers(r.Context(), query, budget)
		_ = json.NewEncoder(w).Encode(report)
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
	retry := n.inboundRetry
	if retry <= 0 {
		retry = 30 * time.Second
	}
	ticker := time.NewTicker(retry)
	defer ticker.Stop()
	dispatchPending := func() {
		if dispatch != nil {
			if targets := n.pendingInboundTargets(); len(targets) > 0 {
				dispatch(targets)
			}
		}
	}
	// Recover accepted inbound records after a service restart without waiting
	// for the remote sender to retry.
	dispatchPending()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-done:
			return e
		case <-n.inbound:
			dispatchPending()
		case <-ticker.C:
			dispatchPending()
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

func Lookup(ctx context.Context, root, query string, budget time.Duration) (LookupReport, error) {
	if !ValidLookupQuery(query) {
		return LookupReport{}, fmt.Errorf("invalid lookup query")
	}
	if budget <= 0 || budget > 15*time.Second {
		return LookupReport{}, fmt.Errorf("invalid lookup timeout")
	}
	var report LookupReport
	endpoint := "/lookup?find=" + url.QueryEscape(query) + "&timeout=" + url.QueryEscape(budget.String())
	if err := Control(ctx, root, http.MethodGet, endpoint, &report); err != nil {
		return LookupReport{}, err
	}
	return report, nil
}

func (n *Node) lookupPeers(ctx context.Context, query string, budget time.Duration) LookupReport {
	report := LookupReport{Query: query, Peers: []LookupPeerResult{}}
	aliases := make([]string, 0, len(n.Config.Peers))
	for alias, configured := range n.Config.Peers {
		id, err := peer.Decode(configured.ID)
		if err == nil && len(n.Host.Network().ConnsToPeer(id)) > 0 {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	report.Peers = make([]LookupPeerResult, len(aliases))
	if len(aliases) == 0 {
		return report
	}
	peerBudget := budget - min(100*time.Millisecond, budget/10)
	if peerBudget <= 0 {
		peerBudget = budget
	}
	lookupCtx, cancel := context.WithTimeout(ctx, peerBudget)
	defer cancel()
	var wg sync.WaitGroup
	for index, alias := range aliases {
		index, alias := index, alias
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := LookupPeerResult{Peer: alias}
			configured := n.Config.Peers[alias]
			id, err := peer.Decode(configured.ID)
			if err == nil {
				var response LookupResponse
				response, err = n.callLookup(lookupCtx, id, query)
				if err == nil {
					if response.Found && response.Name != "" && (response.State == "live" || response.State == "closed" || response.State == "archived") {
						result.Found, result.Name, result.State = true, response.Name, response.State
					} else if response.Found {
						err = fmt.Errorf("invalid lookup response")
					}
				}
			}
			if err != nil {
				result.Error = "error"
			}
			report.Peers[index] = result
		}()
	}
	wg.Wait()
	return report
}

func LogPath(root string) string { return filepath.Join(root, "p2p", "service.log") }

// A relay with no inbound messages must not repeatedly probe the whole local
// fleet. Once P2P work exists, the normal dispatcher still owns all ordering.
func (n *Node) pendingInboundTargets() []string {
	rows, err := n.Queue.List()
	if err != nil {
		if n.Log != nil {
			fmt.Fprintln(n.Log, "p2p inbound:", err)
		}
		return nil
	}
	seen := make(map[string]struct{})
	for _, m := range rows {
		if m.Origin != nil && m.Origin.Transport == "libp2p" {
			seen[m.To] = struct{}{}
		}
	}
	targets := make([]string, 0, len(seen))
	for target := range seen {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets
}
