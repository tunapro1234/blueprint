package claudeacct

import (
	"fmt"
	"testing"
	"time"
)

// The 6 Nov 2026 accounts: weekly resets huso 1d4h, tuna 2d22h, yigit 5d20h,
// azra 6d20h. Soonest-weekly-reset spends huso first, steps off it while its
// five-hour window is full and comes back once that window resets.
func TestSoonestWeeklyResetStrategy(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	slot := func(n int, alias string, five float64, fiveReset time.Duration, week float64, weekReset time.Duration) Slot {
		s := Slot{Number: n, AccountUUID: fmt.Sprint("a", n), Alias: alias}
		s.LastUsage = &UsageCache{Usage: &Usage{
			FiveHour: &Window{Utilization: five, ResetsAt: now.Add(fiveReset)},
			SevenDay: &Window{Utilization: week, ResetsAt: now.Add(weekReset)},
		}}
		return s
	}
	day := 24 * time.Hour
	policy := AutoPolicy{Threshold: 90, Cooldown: 5 * time.Minute, Prefer: StrategySoonestWeeklyReset}
	old := AutoState{LastSwitchAt: now.Add(-time.Hour)}
	accounts := func(husoFive float64) *Accounts {
		return &Accounts{Slots: []Slot{
			slot(1, "tuna", 0, time.Hour, 58, 2*day+22*time.Hour),
			slot(2, "azra", 0, 2*time.Hour, 0, 6*day+20*time.Hour),
			slot(3, "huso", husoFive, 3*time.Hour, 56, day+4*time.Hour),
			slot(4, "yigit", 6, 30*time.Minute, 3, 5*day+20*time.Hour),
		}}
	}
	// On yigit (5h resets soonest, which soonest-reset would keep): move to huso.
	if d := Decide(accounts(10), 4, old, policy, now); d.Action != AutoSwitch || d.To != 3 {
		t.Fatalf("should move to huso, whose week resets first: %+v", d)
	}
	// On huso with room: stay.
	if d := Decide(accounts(10), 3, old, policy, now); d.Action != AutoNone {
		t.Fatalf("should stay on huso: %+v", d)
	}
	// huso's five hours are used up: next weekly reset is tuna.
	if d := Decide(accounts(95), 3, old, policy, now); d.Action != AutoSwitch || d.To != 1 {
		t.Fatalf("should leave a full five-hour window for tuna: %+v", d)
	}
	// huso's window reset (0% again): come back from tuna.
	if d := Decide(accounts(0), 1, old, policy, now); d.Action != AutoSwitch || d.To != 3 {
		t.Fatalf("should return to huso once its five hours reset: %+v", d)
	}
}
