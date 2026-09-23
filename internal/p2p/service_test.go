package p2p

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"blueprint/internal/msgq"
	"github.com/libp2p/go-libp2p/core/peerstore"
)

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIssue19InboundDispatchTargetsAndBoundedRecovery(t *testing.T) {
	// Short root avoids platform sockaddr_un path-length limits.
	root := t.TempDir()
	n, e := New(context.Background(), root, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}}, msgq.New(filepath.Join(root, "q")))
	if e != nil {
		t.Fatal(e)
	}
	defer n.Close()
	n.Log = io.Discard
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	passes := make(chan []string, 4)
	done := make(chan error, 1)
	go func() { done <- n.Serve(ctx, func(targets []string) { passes <- targets }) }()
	var info Info
	waitFor(t, func() bool { return Control(ctx, root, http.MethodGet, "/status", &info) == nil })
	if info.ID != n.Host.ID().String() {
		t.Fatal("wrong local service identity")
	}
	select {
	case targets := <-passes:
		t.Fatalf("idle relay probed local agents: %v", targets)
	case <-time.After(1100 * time.Millisecond):
	}
	// A local message for another agent must not turn this inbound wake-up into
	// a full-fleet probe; only the peer-supplied target has new inbound work.
	if _, e := n.Queue.Enqueue("unrelated", "local", "local queue item"); e != nil {
		t.Fatal(e)
	}
	if _, err := n.Queue.EnqueueOnceOrigin("fixture", "agent", "external:fixture@test", "hello", &msgq.Origin{Transport: "libp2p"}); err != nil {
		t.Fatal(err)
	}
	n.signalInbound()
	select {
	case targets := <-passes:
		if len(targets) != 1 || targets[0] != "agent" {
			t.Fatalf("dispatch targets=%v want [agent]", targets)
		}
	case <-time.After(time.Second):
		t.Fatal("new inbound message did not wake dispatcher")
	}
	// A blocked inbound record remains pending, but it must not recreate the old
	// one-pass-per-second tmux probe loop. Recovery uses the 30-second interval.
	select {
	case targets := <-passes:
		t.Fatalf("persistent inbound record spun dispatcher: %v", targets)
	case <-time.After(1100 * time.Millisecond):
	}
	if e := Control(ctx, root, http.MethodGet, "/stop", nil); e == nil {
		t.Fatal("GET stopped service")
	}
	if e := Control(ctx, root, http.MethodPost, "/stop", nil); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service failed to stop")
	}
}

func TestRelayRestartRenewsReservation(t *testing.T) {
	relay := testNode(t, Config{Relay: true})
	// Reuse the exact listener after restart, as a deployed relay does.
	relayCfg := relay.Config
	relayCfg.Listen = relay.Addresses()
	addr := relay.Addresses()[0] + "/p2p/" + relay.Host.ID().String()
	a := testNode(t, Config{Rendezvous: []string{addr}})
	b := testNode(t, Config{Rendezvous: []string{addr}})
	a.Config.Peers = map[string]Peer{"b": {ID: b.Host.ID().String()}}
	b.Config.Peers = map[string]Peer{"a": {ID: a.Host.ID().String(), Expose: []string{"agent"}}}
	for _, n := range []*Node{b, a} {
		if e := n.ConnectDiscovery(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	a.Host.Peerstore().ClearAddrs(b.Host.ID())
	a.addAddresses(b.Host.ID(), []string{addr + "/p2p-circuit"}, peerstore.PermanentAddrTTL)
	if r := send(t, a, b, testID, "before restart"); r.State != "accepted" {
		t.Fatal(r)
	}
	_ = a.Host.Network().ClosePeer(b.Host.ID())
	_ = a.Host.Network().ClosePeer(relay.Host.ID())
	_ = b.Host.Network().ClosePeer(relay.Host.ID())
	_ = relay.Close()
	resumed, e := New(context.Background(), relay.Root, relayCfg, relay.Queue)
	if e != nil {
		t.Fatal(e)
	}
	defer resumed.Close()
	for _, n := range []*Node{b, a} {
		if e := n.ConnectDiscovery(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	a.Host.Peerstore().ClearAddrs(b.Host.ID())
	a.addAddresses(b.Host.ID(), []string{addr + "/p2p-circuit"}, peerstore.PermanentAddrTTL)
	if r := send(t, a, b, "p1123456789abcdef0123456789abcdef", "after restart"); r.State != "accepted" {
		t.Fatal(r)
	}
	records, e := b.Queue.List()
	if e != nil || len(records) != 2 {
		t.Fatalf("records=%v %v", records, e)
	}
}

func TestWebSocketTransport(t *testing.T) {
	a := testNode(t, Config{})
	b := testNode(t, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0/ws"}})
	a.Config.Peers = map[string]Peer{"b": {ID: b.Host.ID().String()}}
	b.Config.Peers = map[string]Peer{"a": {ID: a.Host.ID().String(), Expose: []string{"agent"}}}
	a.Host.Peerstore().AddAddrs(b.Host.ID(), b.Host.Addrs(), peerstore.PermanentAddrTTL)
	if r := send(t, a, b, testID, "websocket"); r.State != "accepted" {
		t.Fatal(r)
	}
}
