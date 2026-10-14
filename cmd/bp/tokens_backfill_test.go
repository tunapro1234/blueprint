package main

import (
	"testing"
	"time"
)

// The one-time subagent backfill is heavy, so it is refused during the evening
// heat window (20:00-22:59 Europe/Istanbul) unless forced.
func TestBackfillHeatWindow(t *testing.T) {
	ist := time.FixedZone("Europe/Istanbul", 3*60*60)
	cases := []struct {
		hour    int
		blocked bool
	}{
		{0, false}, {14, false}, {19, false},
		{20, true}, {21, true}, {22, true},
		{23, false},
	}
	for _, tc := range cases {
		now := time.Date(2026, 10, 14, tc.hour, 30, 0, 0, ist)
		if got := backfillHeatWindow(now); got != tc.blocked {
			t.Errorf("backfillHeatWindow(%02d:30)=%v, want %v", tc.hour, got, tc.blocked)
		}
	}
}
