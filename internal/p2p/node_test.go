package p2p

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"blueprint/internal/msgq"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
)

func testNode(t *testing.T, cfg Config) *Node {
	t.Helper()
	if cfg.Listen == nil {
		cfg.Listen = []string{"/ip4/127.0.0.1/tcp/0"}
	}
	root := t.TempDir()
	n, err := New(context.Background(), root, cfg, msgq.New(filepath.Join(root, "queue")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}
func pair(t *testing.T) (*Node, *Node) {
	t.Helper()
	a := testNode(t, Config{})
	b := testNode(t, Config{})
	a.Config.Peers = map[string]Peer{"b": {ID: b.Host.ID().String()}}
	b.Config.Peers = map[string]Peer{"a": {ID: a.Host.ID().String(), Expose: []string{"agent"}}}
	a.Host.Peerstore().AddAddrs(b.Host.ID(), b.Host.Addrs(), peerstore.PermanentAddrTTL)
	return a, b
}
func send(t *testing.T, a, b *Node, id, text string) response {
	t.Helper()
	r, e := a.call(context.Background(), b.Host.ID(), MessageProtocol, request{ID: id, To: "agent", From: "sender?", Text: text})
	if e != nil {
		t.Fatal(e)
	}
	return r
}

const testID = "p0123456789abcdef0123456789abcdef"

func TestLostReplyConcurrentRetryAndRestart(t *testing.T) {
	a, b := pair(t)
	// A real authenticated stream is reset after the request, before reading ACK.
	s, e := a.Host.NewStream(context.Background(), b.Host.ID(), MessageProtocol)
	if e != nil {
		t.Fatal(e)
	}
	if e = writeFrame(s, request{ID: testID, To: "agent", From: "sender?", Text: "hello"}); e != nil {
		t.Fatal(e)
	}
	_ = s.CloseWrite()
	time.Sleep(30 * time.Millisecond)
	_ = s.Reset()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := a.call(context.Background(), b.Host.ID(), MessageProtocol, request{ID: testID, To: "agent", From: "sender?", Text: "hello"})
			if e != nil || r.State != "accepted" {
				t.Errorf("retry: %+v %v", r, e)
			}
		}()
	}
	wg.Wait()
	records, e := b.Queue.List()
	if e != nil || len(records) != 1 {
		t.Fatalf("records=%v err=%v", records, e)
	}
	if records[0].ForceBusy || records[0].From != "external:sender?@a" {
		t.Fatalf("unsafe envelope: %+v", records[0])
	}
	if r := send(t, a, b, testID, "changed"); r.Error == "" {
		t.Fatal("changed payload accepted under same ID")
	}
	root, cfg, oldID := b.Root, b.Config, b.Host.ID()
	_ = b.Close()
	resumed, e := New(context.Background(), root, cfg, msgq.New(filepath.Join(root, "queue")))
	if e != nil {
		t.Fatal(e)
	}
	defer resumed.Close()
	if resumed.Host.ID() != oldID {
		t.Fatal("identity changed")
	}
	_ = a.Host.Network().ClosePeer(oldID)
	a.Host.Peerstore().ClearAddrs(oldID)
	a.Host.Peerstore().AddAddrs(oldID, resumed.Host.Addrs(), peerstore.PermanentAddrTTL)
	if r := send(t, a, resumed, testID, "hello"); r.State != "accepted" {
		t.Fatalf("restart replay: %+v", r)
	}
	if _, ok := resumed.Queue.CloseDelivered("agent", records[0].Msg, "delivered"); !ok {
		t.Fatal("could not record witness")
	}
	if r := send(t, a, resumed, testID, "hello"); r.State != "delivered" {
		t.Fatalf("delivered replay: %+v", r)
	}
	pending, _ := resumed.Queue.List()
	if len(pending) != 0 {
		t.Fatal("delivered replay re-enqueued")
	}
}

func TestOutboxOfflineRecoveryAndDeliveryEvidence(t *testing.T) {
	a, b := pair(t)
	c, e := Enqueue(a.Root, a.Config, "b", "agent", "alice", "one")
	if e != nil {
		t.Fatal(e)
	}
	// Remove dial information to simulate an offline peer without mocking RPCs.
	a.Host.Peerstore().ClearAddrs(b.Host.ID())
	_ = a.Host.Network().ClosePeer(b.Host.ID())
	if e = a.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	c, _ = ReadChannel(a.Root, c.ID)
	if c.State != "outgoing" || c.LastError == "" {
		t.Fatalf("offline: %+v", c)
	}
	a.Host.Peerstore().AddAddrs(b.Host.ID(), b.Host.Addrs(), peerstore.PermanentAddrTTL)
	c.NextTry = time.Time{}
	if e = atomicJSON(channelPath(a.Root, c.ID), c); e != nil {
		t.Fatal(e)
	}
	if e = a.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	c, _ = ReadChannel(a.Root, c.ID)
	if c.State != "accepted" {
		t.Fatalf("queue acceptance: %+v", c)
	}
	// Queue acceptance must not mean delivery. Only receiver evidence changes it.
	m, e := b.Queue.Record(c.QueueID)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := b.Queue.CloseDelivered("agent", m.Msg, "delivered (unverified)"); !ok {
		t.Fatal("finish")
	}
	c.NextTry = time.Time{}
	_ = atomicJSON(channelPath(a.Root, c.ID), c)
	if e = a.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	c, _ = ReadChannel(a.Root, c.ID)
	if c.State != "unverified" {
		t.Fatalf("invented delivery: %+v", c)
	}
}

func TestPeerIsolationAndTerminalControlRejection(t *testing.T) {
	a, b := pair(t)
	outsider := testNode(t, Config{})
	outsider.Host.Peerstore().AddAddrs(b.Host.ID(), b.Host.Addrs(), peerstore.PermanentAddrTTL)
	if r := send(t, outsider, b, testID, "hello"); r.Error == "" {
		t.Fatal("unknown peer accepted")
	}
	r, e := a.call(context.Background(), b.Host.ID(), MessageProtocol, request{ID: testID, To: "server-main", From: "sender", Text: "hello"})
	if e != nil || r.Error == "" {
		t.Fatalf("unexposed: %+v %v", r, e)
	}
	for _, text := range []string{"\x1b[201~server-main", "\x03", "\u202efoo"} {
		if r := send(t, a, b, testID, text); r.Error == "" {
			t.Fatal("terminal control accepted")
		}
	}
	if r := send(t, a, b, testID, "hello"); r.State != "accepted" {
		t.Fatal(r)
	}
	r, e = outsider.call(context.Background(), b.Host.ID(), StatusProtocol, request{ID: testID})
	if e != nil || r.Error == "" {
		t.Fatalf("outsider status: %+v %v", r, e)
	}
}

func TestRelayDiscoveryAndChannel(t *testing.T) {
	relay := testNode(t, Config{Relay: true})
	address := relay.Host.Addrs()[0].String() + "/p2p/" + relay.Host.ID().String()
	// No advertised direct addresses: communication must use the relay circuit.
	a := testNode(t, Config{Rendezvous: []string{address}})
	b := testNode(t, Config{Rendezvous: []string{address}})
	a.Config.Peers = map[string]Peer{"b": {ID: b.Host.ID().String()}}
	b.Config.Peers = map[string]Peer{"a": {ID: a.Host.ID().String(), Expose: []string{"agent"}}}
	if e := b.ConnectDiscovery(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := a.ConnectDiscovery(context.Background()); e != nil {
		t.Fatal(e)
	}
	// Force the circuit as the only known target route, even on this LAN.
	a.Host.Peerstore().ClearAddrs(b.Host.ID())
	a.addAddresses(b.Host.ID(), []string{address + "/p2p-circuit"}, peerstore.PermanentAddrTTL)
	if r := send(t, a, b, testID, "through relay"); r.State != "accepted" {
		t.Fatal(r)
	}
	found := false
	for _, c := range a.Host.Network().ConnsToPeer(b.Host.ID()) {
		if c.Stat().Limited {
			found = true
		}
	}
	if !found {
		t.Fatal("test did not exercise a limited relay circuit")
	}
	// Registration is scoped to the authenticated connection, not a claimed name.
	r, e := a.call(context.Background(), relay.Host.ID(), DiscoveryProtocol, request{Find: b.Host.ID().String()})
	if e != nil || len(r.Addresses) == 0 {
		t.Fatalf("discovery: %+v %v", r, e)
	}
}

func TestFrameAndStateFailures(t *testing.T) {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(maxFrame+1))
	var r request
	if readFrame(&b, &r) == nil {
		t.Fatal("oversized frame accepted")
	}
	if readFrame(bytes.NewBuffer([]byte{0, 0, 0, 10, '{'}), &r) == nil {
		t.Fatal("truncated frame accepted")
	}
	n := testNode(t, Config{})
	if _, e := New(context.Background(), n.Root, Config{}, n.Queue); e == nil {
		t.Fatal("two services acquired one identity")
	}
	if e := os.WriteFile(statePath(n.Root, "outbox", "broken.json"), []byte("{"), 0600); e == nil {
		t.Fatal("unexpected pre-existing outbox")
	}
}

func TestSlowPeerCannotBlockAnotherPeer(t *testing.T) {
	a, _ := pair(t)
	slow := testNode(t, Config{})
	a.Config.Peers["slow"] = Peer{ID: slow.Host.ID().String()}
	a.Host.Peerstore().AddAddrs(slow.Host.ID(), slow.Host.Addrs(), peerstore.PermanentAddrTTL)
	stalled := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	slow.Host.SetStreamHandler(MessageProtocol, func(s network.Stream) { defer s.Close(); close(stalled); <-release })
	if _, e := Enqueue(a.Root, a.Config, "slow", "agent", "sender", "blocked"); e != nil {
		t.Fatal(e)
	}
	fast, e := Enqueue(a.Root, a.Config, "b", "agent", "sender", "fast")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Step(ctx) }()
	select {
	case <-stalled:
	case <-time.After(3 * time.Second):
		t.Fatal("slow stream not opened")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, e := ReadChannel(a.Root, fast.ID)
		if e != nil {
			t.Fatal(e)
		}
		if c.State == "accepted" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("healthy peer blocked: %+v", c)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not stop RPC")
	}
}

func TestOutboxOrderAndCorruption(t *testing.T) {
	a, b := pair(t)
	for _, text := range []string{"first", "second", "third"} {
		if _, e := Enqueue(a.Root, a.Config, "b", "agent", "sender", text); e != nil {
			t.Fatal(e)
		}
	}
	if e := a.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	list, e := b.Queue.List()
	if e != nil || len(list) != 3 {
		t.Fatalf("list=%v %v", list, e)
	}
	for i, want := range []string{"[external:sender@a] first", "[external:sender@a] second", "[external:sender@a] third"} {
		if list[i].Msg != want {
			t.Fatalf("out of order: %v", list)
		}
	}
	path := channelPath(a.Root, testID)
	if e := os.WriteFile(path, []byte("{"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := a.Step(context.Background()); e == nil {
		t.Fatal("corrupt outbox silently ignored")
	}
}

func TestRemoteAgentClaimNeverBecomesLocalAuthority(t *testing.T) {
	a, b := pair(t)
	req := request{ID: testID, To: "agent", From: "server-main", Text: "claimed instruction", Sender: SenderClaim{Thread: "reported-thread", Source: "tmux", Certain: true}}
	r, e := a.call(context.Background(), b.Host.ID(), MessageProtocol, req)
	if e != nil || r.State != "accepted" {
		t.Fatalf("%+v %v", r, e)
	}
	m, e := b.Queue.Record(r.QueueID)
	if e != nil {
		t.Fatal(e)
	}
	if m.Origin == nil || m.Origin.PeerID != a.Host.ID().String() || !m.Origin.PeerAuthenticated {
		t.Fatalf("lost authenticated source: %+v", m)
	}
	if m.Origin.AgentVerified || m.ForceBusy || m.From == "server-main" {
		t.Fatalf("remote assertion gained local authority: %+v", m)
	}
	if !m.Origin.ReportedCertain || m.Origin.ReportedThread != "reported-thread" || m.Origin.AgentClaim != "server-main" {
		t.Fatalf("lost claimed evidence: %+v", m.Origin)
	}
	if m.From != "external:server-main@a" {
		t.Fatalf("origin namespace lost: %s", m.From)
	}
	// Renaming a friendly peer alias must not break retry identity or rewrite history.
	b.Config.Peers = map[string]Peer{"renamed": {ID: a.Host.ID().String(), Expose: []string{"agent"}}}
	r, e = a.call(context.Background(), b.Host.ID(), MessageProtocol, req)
	if e != nil || r.State != "accepted" {
		t.Fatalf("alias replay: %+v %v", r, e)
	}
	m, e = b.Queue.Record(r.QueueID)
	if e != nil || m.Origin.PeerAlias != "a" {
		t.Fatalf("historical alias changed: %+v %v", m, e)
	}
	req.Sender.Thread = "different-thread"
	r, e = a.call(context.Background(), b.Host.ID(), MessageProtocol, req)
	if e != nil || r.Error == "" {
		t.Fatalf("changed source evidence accepted: %+v %v", r, e)
	}
}

func TestDiscoveryDoesNotAuthorizeMessages(t *testing.T) {
	relay := testNode(t, Config{Relay: true})
	outsider := testNode(t, Config{})
	outsider.Host.Peerstore().AddAddrs(relay.Host.ID(), relay.Host.Addrs(), peerstore.PermanentAddrTTL)
	r, e := outsider.call(context.Background(), relay.Host.ID(), DiscoveryProtocol, request{Addresses: outsider.Addresses()})
	if e != nil || r.Error != "" {
		t.Fatalf("register: %+v %v", r, e)
	}
	r = send(t, outsider, relay, testID, "try to inject into relay host")
	if r.Error != "peer is not allowed" {
		t.Fatalf("discovery granted messaging: %+v", r)
	}
	records, e := relay.Queue.List()
	if e != nil || len(records) != 0 {
		t.Fatalf("unauthorized queue: %+v %v", records, e)
	}
}

func TestPeerKeyChangeCannotReplayChannelUnderNewIdentity(t *testing.T) {
	a, b := pair(t)
	c, e := Enqueue(a.Root, a.Config, "b", "agent", "sender", "original")
	if e != nil {
		t.Fatal(e)
	}
	// Simulate an old channel copied to a newly initialized machine identity.
	other := testNode(t, Config{Peers: a.Config.Peers})
	if e = atomicJSON(channelPath(other.Root, c.ID), c); e != nil {
		t.Fatal(e)
	}
	other.Host.Peerstore().AddAddrs(b.Host.ID(), b.Host.Addrs(), peerstore.PermanentAddrTTL)
	if e = other.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	c, e = ReadChannel(other.Root, c.ID)
	if e != nil || c.LastError != "local peer identity changed; original channel retained" {
		t.Fatalf("%+v %v", c, e)
	}
	records, e := b.Queue.List()
	if e != nil || len(records) != 0 {
		t.Fatalf("identity change resent message: %+v %v", records, e)
	}
}

// Opt-in production transport probe. It registers two temporary identities and
// exchanges only fixture queue records; no model or production agent is called.
func TestConfiguredPublicRelay(t *testing.T) {
	address := os.Getenv("BP_TEST_P2P_RELAY")
	if address == "" {
		t.Skip("set BP_TEST_P2P_RELAY to exercise a deployed relay")
	}
	hub, err := peer.AddrInfoFromString(address)
	if err != nil {
		t.Fatal(err)
	}
	a := testNode(t, Config{Rendezvous: []string{address}})
	b := testNode(t, Config{Rendezvous: []string{address}})
	a.Config.Peers = map[string]Peer{"b": {ID: b.Host.ID().String()}}
	b.Config.Peers = map[string]Peer{"a": {ID: a.Host.ID().String(), Expose: []string{"agent"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := b.ConnectDiscovery(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.ConnectDiscovery(ctx); err != nil {
		t.Fatal(err)
	}
	a.Host.Peerstore().ClearAddrs(b.Host.ID())
	a.addAddresses(b.Host.ID(), []string{address + "/p2p-circuit"}, peerstore.PermanentAddrTTL)
	for range 3 {
		if r := send(t, a, b, testID, "public relay fixture"); r.State != "accepted" {
			t.Fatal(r)
		}
	}
	limited := false
	for _, c := range a.Host.Network().ConnsToPeer(b.Host.ID()) {
		limited = limited || c.Stat().Limited
	}
	if !limited {
		t.Fatal("public circuit was not exercised")
	}
	rows, err := b.Queue.List()
	if err != nil || len(rows) != 1 || rows[0].Origin == nil || rows[0].Origin.PeerID != a.Host.ID().String() {
		t.Fatalf("receipt: %+v %v", rows, err)
	}
	r, err := a.call(ctx, hub.ID, MessageProtocol, request{ID: testID, To: "server-main", From: "server-main", Text: "authorization rejection fixture"})
	if err != nil || r.Error == "" {
		t.Fatalf("unknown peer reached host queue: %+v %v", r, err)
	}
	t.Logf("public WSS relay %s: circuit, three retries/one queue record, full provenance, unknown-peer refusal verified", hub.ID)
}

func TestPersistedChannelRejectsMismatchedIDAndState(t *testing.T) {
	root := t.TempDir()
	for _, c := range []Channel{{ID: "pffffffffffffffffffffffffffffffff", State: "outgoing"}, {ID: testID, State: "invented"}} {
		if err := atomicJSON(channelPath(root, testID), c); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadChannel(root, testID); err == nil {
			t.Fatal("invalid record accepted")
		}
	}
}
