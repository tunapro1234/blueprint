package delivery

import (
	"strings"
	"testing"

	"blueprint/internal/guard"
	"blueprint/internal/msgq"
)

func TestRendererFramesExternalLeavesLocalRaw(t *testing.T) {
	render, err := Renderer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ext := msgq.Message{ID: "q1", From: "worker", Msg: "[worker] do this", Origin: &msgq.Origin{Transport: "libp2p", PeerID: "12D", PeerAlias: "laptop"}}
	out, err := render(ext)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "[worker] ") {
		t.Fatalf("envelope must stay first: %q", out)
	}
	if !strings.Contains(out, guard.BodyPrefix+"do this") {
		t.Fatalf("body must be framed with the untrusted prefix: %q", out)
	}
	if !strings.Contains(out, "untrusted data") {
		t.Fatalf("frame must carry the untrusted-input notice: %q", out)
	}

	// A local caller shares bp's trust domain: no frame.
	local := msgq.Message{ID: "q2", From: "server-main", Msg: "local note", Origin: &msgq.Origin{Transport: "bp-api/http"}}
	if out, err := render(local); err != nil || out != "local note" {
		t.Fatalf("local transport must be unframed: out=%q err=%v", out, err)
	}
	// No Origin at all is a local bp msg.
	none := msgq.Message{ID: "q3", From: "a", Msg: "hi"}
	if out, err := render(none); err != nil || out != "hi" {
		t.Fatalf("origin-less record must be unframed: out=%q err=%v", out, err)
	}
}

func TestRendererIsStableAcrossReload(t *testing.T) {
	dir := t.TempDir()
	m := msgq.Message{ID: "q7", From: "worker", Msg: "[worker] stable text", Origin: &msgq.Origin{Transport: "libp2p", PeerID: "12D", PeerAlias: "laptop"}}

	first, err := Renderer(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := first(m)
	if err != nil {
		t.Fatal(err)
	}
	// A restart reloads the same key from the same state dir, so the same record
	// frames to the same bytes — the delivery witness compares them.
	second, err := Renderer(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := second(m)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("render not stable across reload:\n%q\n%q", a, b)
	}
}
