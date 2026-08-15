package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestBusySanityAlarmDue(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	stamp := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	// Observation started long ago in every case but the fresh-install one.
	old := stamp(30 * 24 * time.Hour)

	cases := []struct {
		name  string
		state busySanityState
		want  bool
	}{
		{
			// The 2026-08-15 shape: panes working all day, Busy silent throughout.
			name:  "panes moved while busy stayed silent",
			state: busySanityState{Since: old, LastActivitySeen: stamp(2 * time.Hour)},
			want:  true,
		},
		{
			// The detector is doing its job; nothing to report.
			name:  "busy seen recently",
			state: busySanityState{Since: old, LastActivitySeen: stamp(2 * time.Hour), LastBusySeen: stamp(3 * time.Hour)},
			want:  false,
		},
		{
			// A genuinely idle fleet must never be reported as a broken detector.
			name:  "no activity either",
			state: busySanityState{Since: old, LastActivitySeen: stamp(40 * time.Hour)},
			want:  false,
		},
		{
			// Said once a day, not once an hour.
			name:  "already alarmed inside the window",
			state: busySanityState{Since: old, LastActivitySeen: stamp(2 * time.Hour), LastAlarm: stamp(3 * time.Hour)},
			want:  false,
		},
		{
			name:  "alarm cooldown expired",
			state: busySanityState{Since: old, LastActivitySeen: stamp(2 * time.Hour), LastAlarm: stamp(30 * time.Hour)},
			want:  true,
		},
		{
			// A fresh install has an hour of evidence, not a day: staying quiet is
			// the only honest answer.
			name:  "not enough observation yet",
			state: busySanityState{Since: stamp(time.Hour), LastActivitySeen: stamp(10 * time.Minute)},
			want:  false,
		},
		{
			name:  "no state at all",
			state: busySanityState{},
			want:  false,
		},
		{
			// Stale busy evidence is exactly the drift being looked for.
			name:  "busy last seen before the window",
			state: busySanityState{Since: old, LastActivitySeen: stamp(2 * time.Hour), LastBusySeen: stamp(50 * time.Hour)},
			want:  true,
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

func TestBusySanityStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "busy-sanity.json")
	// A missing file must read as "no evidence", not as an error the loop has to
	// handle: the first sweep after an install starts the observation window.
	if got := readBusySanity(path); got.Since != "" || len(got.PaneHashes) != 0 {
		t.Fatalf("missing file did not read as empty state: %+v", got)
	}
	want := busySanityState{Since: "2026-08-15T12:00:00Z", LastBusySeen: "2026-08-15T12:30:00Z", PaneHashes: map[string]string{"server-main": paneHash("pane body")}}
	if err := writeBusySanity(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readBusySanity(path)
	if got.Since != want.Since || got.LastBusySeen != want.LastBusySeen || got.PaneHashes["server-main"] != want.PaneHashes["server-main"] {
		t.Fatalf("round trip lost state: %+v", got)
	}
	if paneHash("pane body") == paneHash("pane body changed") {
		t.Fatal("different pane content produced the same fingerprint")
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
