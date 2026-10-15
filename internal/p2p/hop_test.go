package p2p

import (
	"bytes"
	"context"
	"os"
	"testing"
)

// hopPair wires a → b like pair() and opts b in to receive hops from a.
func hopPair(t *testing.T, allow bool, cap int64) (*Node, *Node) {
	t.Helper()
	a, b := pair(t)
	p := b.Config.Peers["a"]
	p.AllowHop = allow
	b.Config.Peers["a"] = p
	b.Config.HopMaxBytes = cap
	return a, b
}

func hopHeader(body []byte) HopHeader {
	return HopHeader{
		Channel:    testID,
		Agent:      "worker-1",
		Harness:    "claude",
		Format:     1,
		TotalBytes: int64(len(body)),
		FileCount:  2,
		SourceHost: "server",
	}
}

func TestSendHopStagesForReview(t *testing.T) {
	a, b := hopPair(t, true, 0)
	body := bytes.Repeat([]byte("bundle-bytes;"), 4096)

	res, err := a.SendHop(context.Background(), "b", hopHeader(body), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Accepted || res.ID != testID {
		t.Fatalf("not accepted: %+v", res)
	}

	pending, err := ListHopPending(b.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending=%d want 1", len(pending))
	}
	m := pending[0]
	if m.State != "pending" || m.PeerAlias != "a" || m.Header.Agent != "worker-1" {
		t.Fatalf("meta: %+v", m)
	}
	got, err := os.ReadFile(HopBundlePath(b.Root, m))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("staged bundle differs: %d vs %d bytes", len(got), len(body))
	}
}

func TestSendHopRejectedWhenNotAllowed(t *testing.T) {
	a, b := hopPair(t, false, 0)
	body := []byte("x")
	res, err := a.SendHop(context.Background(), "b", hopHeader(body), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted || res.Error == "" {
		t.Fatalf("expected refusal, got %+v", res)
	}
	pending, _ := ListHopPending(b.Root)
	if len(pending) != 0 {
		t.Fatalf("nothing should be staged, got %d", len(pending))
	}
}

func TestSendHopIdempotentRedelivery(t *testing.T) {
	a, b := hopPair(t, true, 0)
	body := []byte("once-and-only-once")
	h := hopHeader(body)

	for i := 0; i < 3; i++ {
		res, err := a.SendHop(context.Background(), "b", h, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		if !res.Accepted || res.ID != testID {
			t.Fatalf("send %d: %+v", i, res)
		}
	}
	pending, _ := ListHopPending(b.Root)
	if len(pending) != 1 {
		t.Fatalf("redelivery staged %d copies, want 1", len(pending))
	}
}

func TestSendHopRefusesOversize(t *testing.T) {
	// Receiver caps at 16 bytes; a larger body must be refused and left unstaged.
	a, b := hopPair(t, true, 16)
	body := bytes.Repeat([]byte("A"), 64)
	h := hopHeader(body)
	h.TotalBytes = 1 // lie about the size so the cap is enforced on actual bytes

	// The receiver cuts the stream once the cap is passed, so the sender sees
	// either a refusal result or a transport error — never an acceptance.
	res, err := a.SendHop(context.Background(), "b", h, bytes.NewReader(body))
	if err == nil && res.Accepted {
		t.Fatalf("oversize body should not be accepted, got %+v", res)
	}
	pending, _ := ListHopPending(b.Root)
	if len(pending) != 0 {
		t.Fatalf("oversize body left %d staged dirs", len(pending))
	}
}

func TestSendHopSenderCapGuardsDeclaredSize(t *testing.T) {
	// The sender refuses before dialing when the declared size is over its cap.
	a, _ := hopPair(t, true, 16)
	a.Config.HopMaxBytes = 16
	h := hopHeader(nil)
	h.TotalBytes = 1 << 20
	if _, err := a.SendHop(context.Background(), "b", h, bytes.NewReader(nil)); err == nil {
		t.Fatal("sender should refuse an over-cap declared size")
	}
}
