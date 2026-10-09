package p2p

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blueprint/internal/audit"
	"blueprint/internal/msgq"

	"github.com/libp2p/go-libp2p/core/peerstore"
)

func TestLimiterRateAndLoopCap(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	p := Peer{Rate: 6, LoopCap: 5}
	// Burst is max(3, rate/2) = 3; the fourth message in the same instant waits.
	for i := 0; i < 3; i++ {
		if err := l.admit("peer", p, "agent", now, time.Time{}); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	if err := l.admit("peer", p, "agent", now, time.Time{}); err != errRate {
		t.Fatalf("burst over: %v", err)
	}
	// Refill at 6/min: ten seconds gives one token.
	now = now.Add(10 * time.Second)
	if err := l.admit("peer", p, "agent", now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	if err := l.admit("peer", p, "agent", now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// Five messages to one agent inside the window reach the loop cap.
	now = now.Add(10 * time.Second)
	if err := l.admit("peer", p, "agent", now, time.Time{}); err == nil || !strings.Contains(err.Error(), "loop cap") {
		t.Fatalf("loop cap: %v", err)
	}
	// Another agent of the same peer is a different pair.
	if err := l.admit("peer", p, "other", now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// After the window the pair may talk again.
	now = now.Add(LoopWindow)
	if err := l.admit("peer", p, "agent", now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// An owner pause holds the pair regardless of counters.
	if err := l.admit("peer", p, "agent", now, now.Add(time.Minute)); err != errLoop {
		t.Fatalf("pause: %v", err)
	}
}

func TestLoopCapPausesPairUntilOwnerResumes(t *testing.T) {
	a, b := pair(t)
	b.Config.Peers["a"] = Peer{ID: a.Host.ID().String(), Expose: []string{"agent"}, Rate: 600, LoopCap: 3}
	for i := 0; i < 3; i++ {
		if r := send(t, a, b, fmt.Sprintf("p%032x", i), "hello"); r.State != "accepted" {
			t.Fatalf("message %d: %+v", i, r)
		}
	}
	// A replay of an accepted channel is free and still acknowledged.
	if r := send(t, a, b, fmt.Sprintf("p%032x", 0), "hello"); r.State != "accepted" {
		t.Fatalf("replay: %+v", r)
	}
	r := send(t, a, b, fmt.Sprintf("p%032x", 3), "hello")
	if r.Error == "" || !strings.Contains(r.Error, "loop cap") {
		t.Fatalf("over cap: %+v", r)
	}
	pauses, err := ReadPauses(b.Root)
	if err != nil || len(pauses) != 1 {
		t.Fatalf("pauses = %v %v", pauses, err)
	}
	// Retries while paused stay held and are audited once.
	for i := 0; i < 3; i++ {
		if r := send(t, a, b, fmt.Sprintf("p%032x", 3), "hello"); !strings.Contains(r.Error, "loop cap") {
			t.Fatalf("retry while paused: %+v", r)
		}
	}
	paused, _ := audit.Read(b.Root, audit.Filter{Kind: "p2p.loop.paused"})
	rejected, _ := audit.Read(b.Root, audit.Filter{Kind: "p2p.msg.rejected"})
	if len(paused) != 1 || paused[0].Severity != audit.Alert || len(rejected) != 0 {
		t.Fatalf("audit paused=%+v rejected=%+v", paused, rejected)
	}
	if n, err := Resume(b.Root, b.Config, "a", ""); err != nil || n != 1 {
		t.Fatalf("resume = %d %v", n, err)
	}
	if r := send(t, a, b, fmt.Sprintf("p%032x", 3), "hello"); r.State != "accepted" {
		t.Fatalf("after resume: %+v", r)
	}
}

func TestRejectionsAndAcceptanceAreAudited(t *testing.T) {
	a, b := pair(t)
	if r := send(t, a, b, testID, "hello"); r.State != "accepted" {
		t.Fatal(r)
	}
	r, err := a.call(context.Background(), b.Host.ID(), MessageProtocol, request{ID: "p" + strings.Repeat("1", 32), To: "server-main", From: "sender", Text: "hi"})
	if err != nil || r.Error == "" {
		t.Fatalf("unexposed: %+v %v", r, err)
	}
	accepted, _ := audit.Read(b.Root, audit.Filter{Kind: "p2p.msg.accepted"})
	denied, _ := audit.Read(b.Root, audit.Filter{Severity: audit.Alert})
	if len(accepted) != 1 || accepted[0].Peer != "a" || accepted[0].Target != "agent" {
		t.Fatalf("accepted = %+v", accepted)
	}
	if len(denied) != 1 || denied[0].Target != "server-main" || denied[0].Reason != "target is not exposed to this peer" {
		t.Fatalf("denied = %+v", denied)
	}
}

func TestLookupOfHiddenAgentIsAudited(t *testing.T) {
	a, b := pair(t)
	b.ResolveLookup = func(string) LookupResponse { return LookupResponse{Found: true, Name: "server-main", State: "live"} }
	got, err := a.callLookup(context.Background(), b.Host.ID(), "server-main")
	if err != nil || got.Found {
		t.Fatalf("lookup leaked: %+v %v", got, err)
	}
	if ev, _ := audit.Read(b.Root, audit.Filter{Kind: "p2p.lookup.hidden"}); len(ev) != 1 {
		t.Fatalf("audit = %+v", ev)
	}
}

func TestPeerLimitsValidate(t *testing.T) {
	id := "12D3KooWAB9b653acxC6ihu2qXfTQsS7xv2gpc5JZFB2RcpezGcw"
	for _, p := range []Peer{{ID: id, Rate: -1}, {ID: id, Rate: 601}, {ID: id, LoopCap: 1001}} {
		if err := (Config{Peers: map[string]Peer{"x": p}}).Validate(); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
}

func TestLookupRateLimited(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	p := Peer{Rate: 6} // burst = 3
	for i := 0; i < 3; i++ {
		if !l.admitLookup("peer", p, now) {
			t.Fatalf("lookup %d denied in burst", i)
		}
	}
	if l.admitLookup("peer", p, now) {
		t.Fatal("4th lookup in the same instant should be denied")
	}
	// Lookups and messages use separate buckets.
	if err := l.admit("peer", p, "agent", now, time.Time{}); err != nil {
		t.Fatalf("message bucket consumed by lookups: %v", err)
	}
	// Refills over time.
	if !l.admitLookup("peer", p, now.Add(11*time.Second)) {
		t.Fatal("lookup not refilled after 11s")
	}
}

func TestHandleLookupRateLimitAudited(t *testing.T) {
	a, b := pair(t)
	b.Config.Peers["a"] = Peer{ID: a.Host.ID().String(), Expose: []string{"worker"}, Rate: 6}
	b.ResolveLookup = func(string) LookupResponse { return LookupResponse{} }
	ok := 0
	for i := 0; i < 6; i++ {
		if _, err := a.callLookup(context.Background(), b.Host.ID(), "worker"); err == nil {
			ok++
		}
	}
	// Burst is 3; later queries are reset by the limiter (stream reset => error).
	if ok > 3 {
		t.Fatalf("rate limit let %d lookups through", ok)
	}
	if ev, _ := audit.Read(b.Root, audit.Filter{Kind: "p2p.lookup.rejected"}); len(ev) == 0 {
		t.Fatal("rate-limited lookup not audited")
	}
}

// R1: one source writes at most AuditBudgetPerMinute rejection events per
// window; the rest become one p2p.audit.suppressed line, never deletions.
func TestAuditBudgetCountsOverflow(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	written := 0
	for i := 0; i < AuditBudgetPerMinute+20; i++ {
		sev := audit.Warn
		if i == AuditBudgetPerMinute+5 {
			sev = audit.Alert
		}
		ok, reports := l.auditBudget(unconfiguredSource, sev, now.Add(time.Duration(i)*time.Millisecond))
		if len(reports) != 0 {
			t.Fatalf("report inside the window: %+v", reports)
		}
		if ok {
			written++
		}
	}
	if written != AuditBudgetPerMinute {
		t.Fatalf("written = %d, want %d", written, AuditBudgetPerMinute)
	}
	// Another source has its own budget.
	if ok, _ := l.auditBudget("peer-x", audit.Warn, now); !ok {
		t.Fatal("second source charged to the first budget")
	}
	if got := l.flushAudits(now.Add(30 * time.Second)); len(got) != 0 {
		t.Fatalf("flush before the window ended: %+v", got)
	}
	got := l.flushAudits(now.Add(auditWindow))
	if len(got) != 1 || got[0].source != unconfiguredSource || got[0].count != 20 || got[0].worst != audit.Alert {
		t.Fatalf("reports = %+v", got)
	}
	// A new window starts with a fresh budget.
	if ok, _ := l.auditBudget(unconfiguredSource, audit.Warn, now.Add(2*auditWindow)); !ok {
		t.Fatal("budget not renewed")
	}
}

// R1 end to end: a flood of denied requests from unconfigured identities is
// capped on disk and the overflow is reported by FlushAudit.
func TestDeniedFloodIsCappedAndReported(t *testing.T) {
	_, b := pair(t)
	outsider := testNode(t, Config{})
	outsider.Host.Peerstore().AddAddrs(b.Host.ID(), b.Host.Addrs(), peerstore.PermanentAddrTTL)
	const flood = AuditBudgetPerMinute + 15
	for i := 0; i < flood; i++ {
		id := fmt.Sprintf("p%032x", i)
		if r := send(t, outsider, b, id, "hello"); r.Error == "" {
			t.Fatal("unknown peer accepted")
		}
	}
	denied, _ := audit.Read(b.Root, audit.Filter{Kind: "p2p.peer.denied"})
	if len(denied) != AuditBudgetPerMinute {
		t.Fatalf("denied lines = %d, want %d", len(denied), AuditBudgetPerMinute)
	}
	// End the window without waiting a minute.
	b.limits.mu.Lock()
	for _, w := range b.limits.audits {
		w.start = w.start.Add(-auditWindow)
	}
	b.limits.mu.Unlock()
	b.FlushAudit()
	sup, _ := audit.Read(b.Root, audit.Filter{Kind: "p2p.audit.suppressed"})
	if len(sup) != 1 || sup[0].Fields["count"] != "15" || sup[0].Peer != "(unconfigured peers)" || sup[0].Fields["worst"] != denied[0].Severity {
		t.Fatalf("suppressed = %+v", sup)
	}
	// Records already written stay.
	if again, _ := audit.Read(b.Root, audit.Filter{Kind: "p2p.peer.denied"}); len(again) != AuditBudgetPerMinute {
		t.Fatalf("audit lines changed: %d", len(again))
	}
}

func TestOnceEvery(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	if !l.onceEvery("k", time.Minute, now) || l.onceEvery("k", time.Minute, now.Add(59*time.Second)) {
		t.Fatal("onceEvery fired twice in one minute")
	}
	if !l.onceEvery("k", time.Minute, now.Add(time.Minute)) {
		t.Fatal("onceEvery did not fire after a minute")
	}
}

// R2: concurrent Pause and Resume never lose each other's change.
func TestPauseResumeConcurrentNoLostUpdate(t *testing.T) {
	root := t.TempDir()
	const id = "12D3KooWAB9b653acxC6ihu2qXfTQsS7xv2gpc5JZFB2RcpezGcw"
	cfg := Config{Peers: map[string]Peer{"x": {ID: id}}}
	until := time.Now().Add(time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if err := Pause(root, id, fmt.Sprintf("keep%d", i), until); err != nil {
				t.Error(err)
			}
		}(i)
		go func() {
			defer wg.Done()
			if _, err := Resume(root, cfg, "x", "gone"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	p, err := ReadPauses(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 40 {
		t.Fatalf("pauses = %d, want 40 (lost updates)", len(p))
	}
}

// R3: with nothing exposed, lookup answers without resolving.
func TestLookupWithNoExposeSkipsResolve(t *testing.T) {
	a, b := pair(t)
	b.Config.Peers["a"] = Peer{ID: a.Host.ID().String()}
	var called atomic.Bool
	b.ResolveLookup = func(string) LookupResponse {
		called.Store(true)
		return LookupResponse{Found: true, Name: "agent", State: "live"}
	}
	got, err := a.callLookup(context.Background(), b.Host.ID(), "agent")
	if err != nil || got.Found {
		t.Fatalf("lookup = %+v %v", got, err)
	}
	if called.Load() {
		t.Fatal("ResolveLookup ran for a peer with nothing exposed")
	}
}

// An agent that received outside text (logged in messages.jsonl, possibly by
// another process) and then sends to a different P2P peer is a tainted relay.
func TestOutboundAfterTaintAlertsRelay(t *testing.T) {
	a, b := pair(t)
	line := fmt.Sprintf(`{"id":"qf1","to":"worker","from":"external:eve@yigit","ts":%d,"finished":%d,"status":"delivered","peer":"yigit","remote":true}`+"\n", time.Now().Unix(), time.Now().Unix())
	if err := os.MkdirAll(a.Queue.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(msgq.MessageLogPath(a.Queue.Root), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Enqueue(a.Root, a.Config, "b", "agent", "worker", "here is what eve asked for"); err != nil {
		t.Fatal(err)
	}
	if err := a.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	alerts, _ := audit.Read(a.Root, audit.Filter{Kind: "guard.reach.relay"})
	if len(alerts) != 1 || alerts[0].Peer != "yigit" {
		t.Fatalf("relay alerts = %+v", alerts)
	}
	_ = b
}
