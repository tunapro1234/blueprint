package book

import (
	"testing"

	"blueprint/internal/cache"
)

// strictTurnVerdict is the whole decision the delivery-path turn-open probe
// makes. It must be a SECOND opinion that never blocks what it cannot assess:
// only a provably open Claude turn queues the message, only a genuine read
// failure fails closed, and everything else is deliverable.
func TestStrictTurnVerdict(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name     string
		state    cache.State
		wantOpen bool
		wantErr  bool
	}{
		{
			name:  "no observation at all is deliverable",
			state: cache.State{Runtime: "claude"},
		},
		{
			name:  "a non-Claude pane is a no-op",
			state: cache.State{Runtime: "codex", Activity: &cache.Activity{TurnBusy: &yes, TurnReadErr: "boom"}},
		},
		{
			name:     "a provably open Claude turn queues the message",
			state:    cache.State{Runtime: "claude", Activity: &cache.Activity{TurnBusy: &yes}},
			wantOpen: true,
		},
		{
			name:  "a closed Claude turn is deliverable",
			state: cache.State{Runtime: "claude", Activity: &cache.Activity{TurnBusy: &no}},
		},
		{
			name:    "an unreadable Claude transcript fails closed",
			state:   cache.State{Runtime: "claude", Activity: &cache.Activity{TurnReadErr: "incomplete transcript tail"}},
			wantErr: true,
		},
		{
			name:  "a readable but inconclusive Claude transcript is deliverable",
			state: cache.State{Runtime: "claude", Activity: &cache.Activity{State: "unknown", Reason: "no readable decisive turn event"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			open, err := strictTurnVerdict(tc.state)
			if open != tc.wantOpen {
				t.Fatalf("open=%v, want %v", open, tc.wantOpen)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
