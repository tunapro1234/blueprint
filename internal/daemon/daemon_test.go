package daemon

import (
	"blueprint/internal/config"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBusySanityAlarmDue(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	stamp := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	// Observation started long ago in every case but the fresh-install ones.
	old := stamp(30 * 24 * time.Hour)

	cases := []struct {
		name  string
		state busySanityState
		want  bool
	}{
		{
			// The drift shape: the transcript gate keeps finding panes mid-turn and
			// the screen has not once agreed with it, for longer than a working day
			// part. Twelve samples is evidence, eight hours is duration.
			name:  "samples piled up and the gates have not agreed",
			state: busySanityState{Since: old, LastGateAgree: stamp(8 * time.Hour), TurnOpenSamples: 12},
			want:  true,
		},
		{
			// The detector is doing its job. Note that in a healthy fleet the counter
			// is reset by the same sweep that stamps the agreement, so this state is
			// really "samples arrived after the last agreement" — still not drift.
			name:  "gates agreed recently",
			state: busySanityState{Since: old, LastGateAgree: stamp(2 * time.Hour), TurnOpenSamples: 20},
			want:  false,
		},
		{
			// The 2026-08-18 false alarm, in its new clothes: a quiet fleet produces
			// no mid-turn samples, so there is no evidence, so there is no alarm — no
			// matter how long the screen has been silent.
			name:  "quiet fleet leaves no evidence",
			state: busySanityState{Since: old, LastGateAgree: stamp(40 * time.Hour)},
			want:  false,
		},
		{
			// A busy hour can produce twelve samples on its own; that is a burst, not
			// a divergence, and the silence clause is what tells them apart.
			name:  "samples without the silence",
			state: busySanityState{Since: old, LastGateAgree: stamp(90 * time.Minute), TurnOpenSamples: 30},
			want:  false,
		},
		{
			name:  "just under the sample threshold",
			state: busySanityState{Since: old, LastGateAgree: stamp(20 * time.Hour), TurnOpenSamples: 11},
			want:  false,
		},
		{
			// Said once a day, not once an hour.
			name:  "already alarmed inside the cooldown",
			state: busySanityState{Since: old, LastGateAgree: stamp(30 * time.Hour), TurnOpenSamples: 40, LastAlarm: stamp(3 * time.Hour)},
			want:  false,
		},
		{
			name:  "alarm cooldown expired",
			state: busySanityState{Since: old, LastGateAgree: stamp(30 * time.Hour), TurnOpenSamples: 40, LastAlarm: stamp(30 * time.Hour)},
			want:  true,
		},
		{
			// Never agreed, but observation has been running long enough for that to
			// mean something: a detector that drifted before the loop's first sweep
			// must still be reportable.
			name:  "never agreed since observation started",
			state: busySanityState{Since: stamp(9 * time.Hour), TurnOpenSamples: 12},
			want:  true,
		},
		{
			// A fresh install has an hour of evidence: staying quiet is the only
			// honest answer, however many samples that hour happened to produce.
			name:  "not enough observation yet",
			state: busySanityState{Since: stamp(time.Hour), TurnOpenSamples: 20},
			want:  false,
		},
		{
			name:  "no state at all",
			state: busySanityState{},
			want:  false,
		},
		{
			// The old rule's inputs are still recorded, and must not be able to raise
			// an alarm by themselves any more: this is the exact state that fired on
			// 2026-08-18 against a healthy screen signature.
			name:  "activity without busy no longer alarms",
			state: busySanityState{Since: old, LastActivitySeen: stamp(2 * time.Hour), LastBusySeen: stamp(50 * time.Hour)},
			want:  false,
		},
		{
			// Cannot-tell never alarms: with no readable moment to measure the
			// silence from there is no divergence to report.
			name:  "unreadable timestamps",
			state: busySanityState{Since: "yesterday", LastGateAgree: "a while ago", TurnOpenSamples: 99},
			want:  false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.state.alarmDue(now); got != testCase.want {
				t.Fatalf("alarmDue = %v, want %v", got, testCase.want)
			}
		})
	}
}

// TestBusySanityObserveGates covers the accounting the alarm reads: samples add up
// across sweeps, and one screen agreement wipes the slate.
func TestBusySanityObserveGates(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	state := busySanityState{Since: now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)}

	// Three sweeps of unmatched mid-turn evidence, a pane or two each.
	state.observeGates(now.Add(-3*time.Hour), 1, false)
	state.observeGates(now.Add(-2*time.Hour), 2, false)
	state.observeGates(now.Add(-time.Hour), 2, false)
	if state.TurnOpenSamples != 5 {
		t.Fatalf("samples = %d, want 5", state.TurnOpenSamples)
	}
	if state.LastGateAgree != "" {
		t.Fatalf("agreement recorded without one: %q", state.LastGateAgree)
	}

	// A sweep in which one pane satisfied both gates: the count goes to zero even
	// though that same sweep also carried unmatched samples from other panes.
	state.observeGates(now, 5, true)
	if state.TurnOpenSamples != 0 {
		t.Fatalf("agreement did not reset the count: %d", state.TurnOpenSamples)
	}
	if state.LastGateAgree != now.Format(time.RFC3339) {
		t.Fatalf("agreement stamp = %q, want %q", state.LastGateAgree, now.Format(time.RFC3339))
	}
	if state.alarmDue(now.Add(busySanityGateSilence + time.Hour)) {
		t.Fatal("a reset counter still produced an alarm")
	}

	// And it starts counting again from there.
	state.observeGates(now.Add(time.Hour), 2, false)
	if state.TurnOpenSamples != 2 {
		t.Fatalf("samples after reset = %d, want 2", state.TurnOpenSamples)
	}
}

// TestBusySanityOneSweepCannotAlarm pins the independence rule: a big fleet caught
// in one unlucky frame — every pane mid-turn, every one of them streaming — must
// not be able to reach the threshold on its own. This is also the state a daemon
// restart meets right after this measure ships, with an old observation start and
// no agreement on record yet.
func TestBusySanityOneSweepCannotAlarm(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	state := busySanityState{Since: now.Add(-3 * 24 * time.Hour).Format(time.RFC3339)}

	state.observeGates(now, 40, false)
	if state.TurnOpenSamples >= busySanityGateSamples {
		t.Fatalf("one sweep reached the threshold alone: %d", state.TurnOpenSamples)
	}
	if state.alarmDue(now) {
		t.Fatal("one sweep raised an alarm")
	}
	// Further sweeps of the same evidence do carry it over the line.
	for hour := 1; hour <= 3; hour++ {
		state.observeGates(now.Add(time.Duration(hour)*time.Hour), 40, false)
	}
	if !state.alarmDue(now.Add(3 * time.Hour)) {
		t.Fatalf("evidence across sweeps did not alarm: %d samples", state.TurnOpenSamples)
	}
}

func TestBusySanityStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "busy-sanity.json")
	// A missing file must read as "no evidence", not as an error the loop has to
	// handle: the first sweep after an install starts the observation window.
	if got := readBusySanity(path); got.Since != "" || len(got.PaneHashes) != 0 {
		t.Fatalf("missing file did not read as empty state: %+v", got)
	}
	want := busySanityState{Since: "2026-08-15T12:00:00Z", LastBusySeen: "2026-08-15T12:30:00Z", LastGateAgree: "2026-08-15T12:30:00Z", TurnOpenSamples: 4, PaneHashes: map[string]string{"server-main": paneHash("pane body")}}
	if err := writeBusySanity(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readBusySanity(path)
	if got.Since != want.Since || got.LastBusySeen != want.LastBusySeen || got.PaneHashes["server-main"] != want.PaneHashes["server-main"] {
		t.Fatalf("round trip lost state: %+v", got)
	}
	if got.LastGateAgree != want.LastGateAgree || got.TurnOpenSamples != want.TurnOpenSamples {
		t.Fatalf("round trip lost the gate measure: %+v", got)
	}
	if paneHash("pane body") == paneHash("pane body changed") {
		t.Fatal("different pane content produced the same fingerprint")
	}
}

// TestBusySanityReadsPreUpgradeState pins the file's backward compatibility: the
// daemon is restarted onto a state file written before the coherence measure
// existed, and must keep the history it finds, read the missing fields as "no
// evidence yet", and stay silent until the new measure has filled them.
func TestBusySanityReadsPreUpgradeState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy-sanity.json")
	old := `{
  "since": "2026-07-15T12:00:00Z",
  "last_busy_seen": "2026-08-16T09:00:00Z",
  "last_activity_seen": "2026-08-18T11:00:00Z",
  "pane_hashes": {"server-main": "0123456789abcdef"},
  "merge_watermark": "2026-08-18T10:55:00Z"
}`
	if err := os.WriteFile(path, []byte(old), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	state := readBusySanity(path)
	if state.Since != "2026-07-15T12:00:00Z" || state.MergeWatermark != "2026-08-18T10:55:00Z" {
		t.Fatalf("pre-upgrade state was not carried over: %+v", state)
	}
	if state.TurnOpenSamples != 0 || state.LastGateAgree != "" {
		t.Fatalf("missing fields did not read as absent: %+v", state)
	}
	// The old file's own contents are exactly the shape that used to alarm.
	if state.alarmDue(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("a pre-upgrade state file alarmed on its first sweep")
	}
	if err := writeBusySanity(path, state); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	// omitempty keeps the file readable during an incident: fields the new measure
	// has not filled in yet do not appear at all.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(data), "turn_open_samples") || strings.Contains(string(data), "last_gate_agree") {
		t.Fatalf("empty gate fields were written out: %s", data)
	}
}

func TestStartLoopHonorsInitialDelay(t *testing.T) {
	service := &Service{}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	service.startLoop(ctx, "test", 80*time.Millisecond, time.Hour, func(context.Context, time.Duration) {
		started <- struct{}{}
	})

	select {
	case <-started:
		cancel()
		service.wg.Wait()
		t.Fatal("work ran before the initial delay")
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		service.wg.Wait()
		t.Fatal("work did not run after the initial delay")
	}
	cancel()
	service.wg.Wait()
}

func TestKeepaliveNeverGuessesHarnessOrThread(t *testing.T) {
	dir := t.TempDir()
	bookPath := filepath.Join(dir, "agentbook.json")
	fake := filepath.Join(dir, "tmux")
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + calls + "'\ncase \"$1\" in\nhas-session) exit 1;;\n*) exit 90;;\nesac\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, launch := range []string{"null", `{"codex":true}`} {
		data := `{"orchestrator":"server-main","agents":[{"name":"server-main","folder":"/srv","launch":` + launch + `}]}`
		if err := os.WriteFile(bookPath, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		s := New(nil, config.Config{Agentbooks: []string{bookPath}, StateDir: dir, MsgqRoot: filepath.Join(dir, "msgq")})
		s.tmux.Bin = fake
		if err := s.keepalive(context.Background()); err == nil {
			t.Fatal("missing launch/thread was guessed")
		}
	}
	data, _ := os.ReadFile(calls)
	if strings.Contains(string(data), "new-session") || strings.Contains(string(data), "send-keys") {
		t.Fatalf("started a replacement agent: %s", data)
	}
}
