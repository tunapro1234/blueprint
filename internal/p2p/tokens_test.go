package p2p

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"blueprint/internal/audit"

	"github.com/libp2p/go-libp2p/core/peer"
)

func allowTokens(n *Node, alias string) {
	p := n.Config.Peers[alias]
	p.AllowTokens = true
	n.Config.Peers[alias] = p
}

func sampleTokens(since int) TokensReport {
	return TokensReport{SinceDays: since, Rows: []TokensRow{
		{Agent: "worker", Src: "claude", Model: "claude-opus-4-8", Requests: 10, In: 1000, Out: 2000, CacheWrite: 50, CacheRead: 500,
			SubRequests: 4, SubIn: 400, SubOut: 800, SubCacheWrite: 10, SubCacheRead: 100},
		{Agent: "server-main", Src: "claude", Model: "claude-haiku-5-5", Requests: 3, In: 300, Out: 600},
	}}
}

func TestTokensProtocolRoundTrip(t *testing.T) {
	a, b := pair(t)
	allowTokens(b, "a")
	b.ResolveTokens = func(since int) TokensReport { return sampleTokens(since) }

	got, err := a.callTokens(context.Background(), b.Host.ID(), 7)
	if err != nil {
		t.Fatal(err)
	}
	// The responder never labels its own machine; the caller does.
	if got.Machine != "" {
		t.Fatalf("responder leaked machine label: %q", got.Machine)
	}
	if len(got.Rows) != 2 || got.Rows[0].Agent != "worker" || got.Rows[0].Out != 2000 {
		t.Fatalf("rows round-tripped wrong: %+v", got.Rows)
	}
	if got.Rows[0].SubOut != 800 {
		t.Fatalf("subagent split dropped: %+v", got.Rows[0])
	}
}

func TestTokensProtocolDeniedWithoutAllowTokens(t *testing.T) {
	a, b := pair(t)
	// b knows a as a peer but has not granted AllowTokens.
	b.ResolveTokens = func(since int) TokensReport { return sampleTokens(since) }
	if _, err := a.callTokens(context.Background(), b.Host.ID(), 7); err == nil {
		t.Fatal("peer read tokens without AllowTokens")
	}
	if ev, _ := audit.Read(b.Root, audit.Filter{Kind: "p2p.tokens.denied"}); len(ev) != 1 {
		t.Fatalf("expected one denied-audit line, got %+v", ev)
	}
}

func TestTokensProtocolRefusesUnknownPeer(t *testing.T) {
	a, b := pair(t)
	delete(b.Config.Peers, "a")
	if _, err := a.callTokens(context.Background(), b.Host.ID(), 7); err == nil {
		t.Fatal("unconfigured peer read tokens")
	}
	// An unconfigured identity is reset before any audit write.
	if ev, _ := audit.Read(b.Root, audit.Filter{}); len(ev) != 0 {
		t.Fatalf("unconfigured token read wrote audit lines: %+v", ev)
	}
}

func TestTokensProtocolRejectsOutOfRangeWindow(t *testing.T) {
	a, b := pair(t)
	allowTokens(b, "a")
	b.ResolveTokens = func(since int) TokensReport { return sampleTokens(since) }
	for _, since := range []int{0, -1, MaxTokensSinceDays + 1} {
		if _, err := a.callTokens(context.Background(), b.Host.ID(), since); err == nil {
			t.Errorf("window %d was accepted", since)
		}
	}
}

func TestFleetTokensMergesLocalAndPeers(t *testing.T) {
	a, b := pair(t)
	allowTokens(b, "a")
	a.ResolveTokens = func(since int) TokensReport {
		return TokensReport{Rows: []TokensRow{{Agent: "local-worker", Src: "claude", Model: "claude-sonnet-5-5", Out: 100}}}
	}
	b.ResolveTokens = func(since int) TokensReport { return sampleTokens(since) }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Host.Connect(ctx, peer.AddrInfo{ID: b.Host.ID(), Addrs: b.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}

	report := a.fleetTokens(ctx, 7)
	if report.SinceDays != 7 || len(report.Machines) != 2 {
		t.Fatalf("expected local + 1 peer, got %+v", report)
	}
	if report.Machines[0].Machine != "local" || len(report.Machines[0].Rows) != 1 {
		t.Fatalf("local machine wrong: %+v", report.Machines[0])
	}
	if report.Machines[1].Machine != "b" || report.Machines[1].Error != "" || len(report.Machines[1].Rows) != 2 {
		t.Fatalf("peer machine wrong: %+v", report.Machines[1])
	}
}

func TestFleetTokensMarksDeniedPeer(t *testing.T) {
	a, b := pair(t)
	// b is connected and configured but has NOT granted AllowTokens.
	a.ResolveTokens = func(since int) TokensReport { return TokensReport{} }
	b.ResolveTokens = func(since int) TokensReport { return sampleTokens(since) }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Host.Connect(ctx, peer.AddrInfo{ID: b.Host.ID(), Addrs: b.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	report := a.fleetTokens(ctx, 7)
	if len(report.Machines) != 2 {
		t.Fatalf("expected local + 1 peer, got %+v", report)
	}
	pr := report.Machines[1]
	if pr.Machine != "b" || pr.Error == "" || len(pr.Rows) != 0 {
		t.Fatalf("denied peer should carry an error and no rows: %+v", pr)
	}
}

func TestTokensReportWireOmitsEmptyFields(t *testing.T) {
	// A not-found / errored report must not emit rows; a counts-only row must
	// not carry anything prompt-shaped.
	wire, err := json.Marshal(TokensReport{Machine: "b", SinceDays: 7, Error: "token store unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) != `{"machine":"b","since_days":7,"error":"token store unavailable"}` {
		t.Fatalf("errored report wire changed: %s", wire)
	}
}
