package claudeacct

import (
	"testing"
	"time"
)

func fiveHourUsage(util float64, reset time.Time) *Usage {
	return &Usage{FiveHour: &Window{Utilization: util, ResetsAt: reset}}
}

func TestRecordUsageKeepsPreviousFetchOfSameWindow(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 22, 0, 0, 0, time.UTC)
	reset := t0.Add(3 * time.Hour)
	c := recordUsage(nil, t0, fiveHourUsage(60, reset), nil)
	if !c.PrevFetchedAt.IsZero() {
		t.Fatalf("first fetch has no previous: %+v", c)
	}
	c = recordUsage(c, t0.Add(5*time.Minute), fiveHourUsage(75, reset), nil)
	if c.PrevFiveHour != 60 || !c.PrevFetchedAt.Equal(t0) {
		t.Fatalf("previous fetch not kept: %+v", c)
	}
	if rate, ok := c.burnRate(); !ok || rate != 3 {
		t.Fatalf("burn rate = %v, %v; want 3 points/min", rate, ok)
	}
	// A new window (different reset) drops the previous reading.
	c = recordUsage(c, t0.Add(10*time.Minute), fiveHourUsage(2, reset.Add(5*time.Hour)), nil)
	if !c.PrevFetchedAt.IsZero() {
		t.Fatalf("previous reading kept across windows: %+v", c)
	}
	if _, ok := c.burnRate(); ok {
		t.Fatal("burn rate across windows")
	}
}

func TestPollEveryShortensOnlyWhenLimitIsNear(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 22, 0, 0, 0, time.UTC)
	reset := t0.Add(3 * time.Hour)
	every := 5 * time.Minute
	build := func(prev, cur float64) *UsageCache {
		c := recordUsage(nil, t0, fiveHourUsage(prev, reset), nil)
		return recordUsage(c, t0.Add(every), fiveHourUsage(cur, reset), nil)
	}
	now := t0.Add(every + 30*time.Second)
	cases := []struct {
		name      string
		prev, cur float64
		limit     float64
		want      time.Duration
	}{
		{"fast burn near limit", 70, 80, 90, FastPollEvery}, // 2/min: 80+10 reaches 90
		{"slow burn near limit", 84, 85, 90, every},         // 0.2/min: 86 < 90
		{"fast burn far from limit", 20, 30, 90, every},     // 30+10 < 90
		{"already over limit", 80, 95, 90, every},
		{"usage dropped", 50, 40, 90, every},
		{"no limit (poll only)", 70, 80, 0, every},
	}
	for _, tc := range cases {
		if got := build(tc.prev, tc.cur).pollEvery(now, every, tc.limit); got != tc.want {
			t.Errorf("%s: pollEvery = %v, want %v", tc.name, got, tc.want)
		}
	}
	single := recordUsage(nil, t0, fiveHourUsage(89, reset), nil)
	if got := single.pollEvery(now, every, 90); got != every {
		t.Errorf("single reading: pollEvery = %v, want %v", got, every)
	}
}

func TestPollOrderPollsFastBurningActiveEveryMinute(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 22, 0, 0, 0, time.UTC)
	reset := t0.Add(3 * time.Hour)
	every := 5 * time.Minute
	active := recordUsage(nil, t0, fiveHourUsage(70, reset), nil)
	active = recordUsage(active, t0.Add(every), fiveHourUsage(80, reset), nil)
	other := recordUsage(nil, t0.Add(every), fiveHourUsage(0, time.Time{}), nil)
	accounts := &Accounts{Slots: []Slot{{Number: 1, LastUsage: active}, {Number: 2, LastUsage: other}}}
	now := t0.Add(every + FastPollEvery)
	if got := pollOrder(accounts, 1, now, every, 1, 90); len(got) != 1 || got[0] != 1 {
		t.Fatalf("fast-burning active not polled after a minute: %v", got)
	}
	if got := pollOrder(accounts, 1, now, every, 1, 0); len(got) != 0 {
		t.Fatalf("without a limit the regular interval applies: %v", got)
	}
}
