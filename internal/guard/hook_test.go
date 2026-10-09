package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hookInput(t *testing.T, tool string, input any) HookInput {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return HookInput{ToolName: tool, ToolInput: raw}
}

func writeLog(t *testing.T, lines ...map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "messages.jsonl")
	var b strings.Builder
	for _, l := range lines {
		data, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func unixf(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func TestFlattenCollectsNestedStrings(t *testing.T) {
	got := Flatten(json.RawMessage(`{"b":"two","a":{"x":["one",1,true]},"c":null}`))
	if got != "one\ntwo" {
		t.Fatalf("Flatten = %q", got)
	}
	if Flatten(nil) != "" {
		t.Fatal("empty input must flatten to empty")
	}
}

func TestHookMatchSecretsAndCanaries(t *testing.T) {
	cfg := HookConfig{Home: "/home/u", Canaries: []string{"~/canary/token.txt"}}
	cases := []struct {
		tool   string
		input  any
		hit    bool
		canary bool
	}{
		{"Read", map[string]string{"file_path": "/home/u/.ssh/id_ed25519"}, true, false},
		{"Bash", map[string]string{"command": "cat ~/.claude/.credentials.json"}, true, false},
		{"Read", map[string]string{"file_path": "/home/u/canary/token.txt"}, true, true},
		{"Grep", map[string]string{"pattern": "x", "path": "~/canary/token.txt"}, true, true},
		{"Read", map[string]string{"file_path": "/home/u/src/main.go"}, false, false},
		{"Bash", map[string]string{"command": "go test ./internal/guard"}, false, false},
	}
	for _, c := range cases {
		_, canary, hit := cfg.Match(hookInput(t, c.tool, c.input))
		if hit != c.hit || canary != c.canary {
			t.Errorf("%s %v: hit=%v canary=%v, want %v %v", c.tool, c.input, hit, canary, c.hit, c.canary)
		}
	}
}

func TestTaintFromFindsNewestOutsideDelivery(t *testing.T) {
	now := time.Now()
	path := writeLog(t,
		map[string]any{"id": "q1", "to": "alice", "from": "bob", "ts": unixf(now.Add(-5 * time.Minute)), "finished": unixf(now.Add(-5 * time.Minute)), "status": "delivered"},
		map[string]any{"id": "q2", "to": "alice", "from": "external:eve@peer", "ts": unixf(now.Add(-50 * time.Minute)), "finished": unixf(now.Add(-50 * time.Minute)), "status": "delivered", "peer": "peer", "remote": true},
		map[string]any{"id": "q3", "to": "alice", "from": "external:eve@peer", "ts": unixf(now.Add(-10 * time.Minute)), "finished": unixf(now.Add(-10 * time.Minute)), "status": "delivered", "peer": "peer", "peerId": "12D3Koo", "remote": true},
		map[string]any{"id": "q4", "to": "alice", "from": "external:eve@peer", "ts": unixf(now.Add(-2 * time.Minute)), "finished": unixf(now.Add(-2 * time.Minute)), "status": "canceled (by operator)", "remote": true},
		map[string]any{"id": "q5", "to": "alicex", "from": "external:eve@peer", "ts": unixf(now), "finished": unixf(now), "status": "delivered", "remote": true},
	)
	taint, found, err := TaintFrom(path, "alice", now.Add(-DefaultTaintWindow))
	if err != nil || !found {
		t.Fatalf("TaintFrom: found=%v err=%v", found, err)
	}
	if taint.ID != "q3" || taint.Peer != "peer 12D3Koo" {
		t.Fatalf("taint = %+v, want q3 from peer 12D3Koo", taint)
	}
	if _, found, _ := TaintFrom(path, "bob", now.Add(-time.Hour)); found {
		t.Fatal("bob received nothing from outside")
	}
	if _, found, err := TaintFrom(filepath.Join(t.TempDir(), "missing.jsonl"), "alice", now); found || err != nil {
		t.Fatalf("missing log: found=%v err=%v", found, err)
	}
}

func TestTaintFromReadsOnlyTheTail(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "messages.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	filler, _ := json.Marshal(map[string]any{"id": "f", "to": "other", "from": "x", "msg": strings.Repeat("a", 900), "status": "delivered"})
	for i := 0; i < 1500; i++ { // about 1.4 MB, more than the tail
		f.Write(append(filler, '\n'))
	}
	line, _ := json.Marshal(map[string]any{"id": "qz", "to": "alice", "from": "external:eve@peer", "ts": unixf(now), "finished": unixf(now), "status": "delivered", "remote": true})
	f.Write(append(line, '\n'))
	f.Close()
	taint, found, err := TaintFrom(path, "alice", now.Add(-time.Minute))
	if err != nil || !found || taint.ID != "qz" {
		t.Fatalf("tail read: %+v found=%v err=%v", taint, found, err)
	}
}

func TestHookEvaluate(t *testing.T) {
	now := time.Now()
	taint := &Taint{Peer: "peer", ID: "q3", At: now.Add(-10 * time.Minute)}
	in := hookInput(t, "Read", map[string]string{"file_path": "/home/u/.ssh/id_ed25519"})
	observe := HookConfig{Mode: HookObserve}
	ask := HookConfig{Mode: HookAsk}

	if r := observe.Evaluate(in, "alice", "Read: .ssh/", false, nil, now); r.Alert != nil || r.Output != nil {
		t.Fatalf("untainted secret access must be silent: %+v", r)
	}
	r := observe.Evaluate(in, "alice", "Read: .ssh/", false, taint, now)
	if r.Alert == nil || r.Alert.Rule != "tainted-secret-access" || r.Alert.Severity != High || r.Alert.Channel != "q3" || r.Output != nil {
		t.Fatalf("tainted observe: %+v", r)
	}
	if !strings.Contains(r.Alert.Summary, "10 min ago") {
		t.Fatalf("summary %q", r.Alert.Summary)
	}
	r = ask.Evaluate(in, "alice", "Read: .ssh/", false, taint, now)
	var out struct {
		HookSpecificOutput struct {
			HookEventName, PermissionDecision, PermissionDecisionReason string
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(r.Output, &out); err != nil {
		t.Fatalf("ask output %q: %v", r.Output, err)
	}
	if out.HookSpecificOutput.HookEventName != "PreToolUse" || out.HookSpecificOutput.PermissionDecision != "ask" ||
		!strings.Contains(out.HookSpecificOutput.PermissionDecisionReason, "peer") {
		t.Fatalf("ask output %+v", out)
	}
	r = ask.Evaluate(in, "", "Read: canary", true, nil, now)
	if r.Alert == nil || r.Alert.Rule != "canary-access" || r.Output != nil {
		t.Fatalf("untainted canary must alert without asking: %+v", r)
	}
	if !strings.Contains(r.Alert.Summary, "an unknown agent") {
		t.Fatalf("summary %q", r.Alert.Summary)
	}
}

func TestAuditSinkMapsCanary(t *testing.T) {
	if m := alertKinds["canary-access"]; m.kind != "guard.reach.canary" {
		t.Fatalf("canary-access maps to %q", m.kind)
	}
}

// Taint from another process reaches Watch through MessageLogTaint: a P2P
// message delivered to "main" (logged by the p2p service) makes a later
// outbound to a different peer, seen by the API process, a tainted relay.
func TestWatchTaintSourceCrossesProcesses(t *testing.T) {
	now := time.Now()
	path := writeLog(t,
		map[string]any{"id": "qp1", "to": "main", "from": "external:eve@laptop", "ts": unixf(now.Add(-5 * time.Minute)), "finished": unixf(now.Add(-5 * time.Minute)), "status": "delivered", "peer": "laptop", "peerId": "12D3Koo", "remote": true},
	)
	rec := &recorder{}
	w := NewWatch(WatchConfig{TaintSource: MessageLogTaint(path)}, rec)
	w.Observe(Event{Time: now, Kind: EvOutbound, Agent: "main", Peer: "laptop"})
	if len(rec.alerts) != 0 {
		t.Fatalf("a reply to the tainting peer is not a relay: %+v", rec.alerts)
	}
	w.Observe(Event{Time: now, Kind: EvOutbound, Agent: "main", Peer: "gateway"})
	if len(rec.alerts) != 1 || rec.alerts[0].Rule != "tainted-relay" || rec.alerts[0].Peer != "laptop" || rec.alerts[0].Channel != "qp1" {
		t.Fatalf("relay alert: %+v", rec.alerts)
	}
	w.Observe(Event{Time: now, Kind: EvSensitive, Agent: "main", Target: ".credentials.json"})
	if len(rec.alerts) != 2 || rec.alerts[1].Rule != "tainted-secret-access" {
		t.Fatalf("secret alert: %+v", rec.alerts)
	}
	// An agent the log never names stays untainted; without a source nothing
	// changes.
	w.Observe(Event{Time: now, Kind: EvOutbound, Agent: "other", Peer: "gateway"})
	plain := &recorder{}
	NewWatch(WatchConfig{}, plain).Observe(Event{Time: now, Kind: EvOutbound, Agent: "main", Peer: "gateway"})
	if len(rec.alerts) != 2 || len(plain.alerts) != 0 {
		t.Fatalf("alerts = %+v / %+v", rec.alerts, plain.alerts)
	}
}
