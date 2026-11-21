package claudeacct

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const testYield = 9 * time.Hour

// yieldPolicies are allPolicies with the preferred yield on.
func yieldPolicies() []AutoPolicy {
	out := allPolicies()
	for i := range out {
		out[i].PreferredYield = testYield
	}
	return out
}

func yieldOff(p AutoPolicy) AutoPolicy {
	p.PreferredYield = 0
	return p
}

type yieldCase struct {
	name       string
	slots      []Slot
	active     int
	state      AutoState
	action     string
	to         int
	candidates []int
	reason     string
}

func runYieldCases(t *testing.T, policies []AutoPolicy, cases []yieldCase) {
	t.Helper()
	for _, policy := range policies {
		for _, c := range cases {
			accounts := &Accounts{Slots: c.slots}
			d := Decide(accounts, c.active, c.state, policy, prefNow)
			if d.Action != c.action || d.To != c.to || !strings.Contains(d.Reason, c.reason) {
				t.Errorf("%s (%s): %+v", c.name, policyName(policy), d)
				continue
			}
			if d.Action == AutoSwitch {
				if got := candidatesFor(accounts, d, policy, prefNow); !reflect.DeepEqual(got, c.candidates) {
					t.Errorf("%s (%s): candidates %v, want %v", c.name, policyName(policy), got, c.candidates)
				}
			}
		}
	}
}

func TestDecidePreferredYield(t *testing.T) {
	// Slot 1's seven-day window resets in 5h (inside the 9h yield); slot 2's
	// five-hour window resets sooner than everyone's, its week in 3 days;
	// slot 3 is preferred with room; slot 4 a backup.
	n1 := normalSlot(1, 40, 2*time.Hour, 60, 5*time.Hour)
	n2 := normalSlot(2, 10, 10*time.Minute, 10, 3*testDay)
	p3 := preferredSlot(3, 20, time.Hour, 20, 5*testDay)
	b4 := backupSlot(4, 1, 0, 3*time.Hour)
	cases := []yieldCase{
		{"active normal inside the yield with room: stays, preferred and sooner five-hour resets do not pull it",
			[]Slot{n1, n2, p3, b4}, 1, prefOld, AutoNone, 0, nil, "active account's seven-day window resets in 5h, within the preferred yield 9h; "},
		{"active preferred with room, a normal inside the yield: switches to it",
			[]Slot{n1, n2, p3, b4}, 3, prefOld, AutoSwitch, 1, []int{1},
			"slot 1's seven-day window resets in 5h, within the preferred yield 9h; slot 1 at seven-day 60%, leaving slot 3 at"},
		{"active preferred, cooldown holds the switch back",
			[]Slot{n1, n2, p3, b4}, 3, prefRecent, AutoNone, 0, nil, "cooling down"},
		{"active normal outside the yield: to the expiring one, then the preferred tier as fallback",
			[]Slot{n1, n2, p3, b4}, 2, prefOld, AutoSwitch, 1, []int{1, 3}, "slot 1's seven-day window resets in 5h"},
		{"active backup: to the expiring one",
			[]Slot{n1, n2, p3, b4}, 4, prefOld, AutoSwitch, 1, []int{1, 3}, "within the preferred yield 9h"},
	}
	runYieldCases(t, yieldPolicies(), cases)

	// A normal account whose week resets in 10h is outside a 9h yield: the
	// preferred account wins as before the yield.
	n1late := normalSlot(1, 40, 2*time.Hour, 60, 10*time.Hour)
	runYieldCases(t, yieldPolicies(), []yieldCase{
		{"outside the yield, on it: to the preferred account", []Slot{n1late, n2, p3, b4}, 1, prefOld, AutoSwitch, 3, []int{3}, "preferred slot 3 has room"},
		{"outside the yield, on the preferred account: stays", []Slot{n1late, n2, p3, b4}, 3, prefOld, AutoNone, 0, nil, "among preferred accounts: "},
	})
}

// The yield's edges: a reset exactly at now+yield is inside, a minute later
// outside; usage exactly at the limit is no room.
func TestPreferredYieldBoundaries(t *testing.T) {
	p3 := preferredSlot(3, 20, time.Hour, 20, 5*testDay)
	policy := livePolicy
	policy.PreferredYield = testYield
	in := func(s Slot) bool {
		for _, r := range expiringTargets(&Accounts{Slots: []Slot{s, p3}}, policy, prefNow) {
			if r.n == s.Number {
				return true
			}
		}
		return false
	}
	for _, c := range []struct {
		name string
		slot Slot
		want bool
	}{
		{"reset exactly at the yield", normalSlot(1, 10, time.Hour, 10, testYield), true},
		{"reset a minute after the yield", normalSlot(1, 10, time.Hour, 10, testYield+time.Minute), false},
		{"reset passed", normalSlot(1, 10, time.Hour, 10, -time.Minute), false},
		{"five-hour exactly at the threshold", normalSlot(1, 90, time.Hour, 10, time.Hour), false},
		{"seven-day exactly at the weekly threshold", normalSlot(1, 10, time.Hour, 95, time.Hour), false},
		{"five-hour just under the threshold", normalSlot(1, 89, time.Hour, 10, time.Hour), true},
		{"preferred account inside the yield", preferredSlot(1, 10, time.Hour, 10, time.Hour), false},
		{"backup inside the yield", prefSlot(1, RoleBackup, 10, time.Hour, 10, time.Hour), false},
	} {
		if got := in(c.slot); got != c.want {
			t.Errorf("%s: expiring %v, want %v", c.name, got, c.want)
		}
	}
	if got := expiringTargets(&Accounts{Slots: []Slot{normalSlot(1, 10, time.Hour, 10, time.Hour), p3}}, livePolicy, prefNow); got != nil {
		t.Fatalf("yield off lists %v", got)
	}
}

// The expiring account fills its five-hour window: it leaves the expiring
// list, the preferred account is used, and it comes back once that window
// resets while its week is still inside the yield.
func TestPreferredYieldFiveHourUsedUpAndBack(t *testing.T) {
	accounts := func(five float64, fiveIn time.Duration) *Accounts {
		return &Accounts{Slots: []Slot{
			normalSlot(1, five, fiveIn, 60, 6*time.Hour),
			normalSlot(2, 10, time.Hour, 10, 3*testDay),
			preferredSlot(3, 20, time.Hour, 20, 5*testDay),
		}}
	}
	for _, policy := range yieldPolicies() {
		d := Decide(accounts(93, 2*time.Hour), 1, prefOld, policy, prefNow)
		if d.Action != AutoSwitch || d.To != 3 {
			t.Errorf("%s: expiring account used up: %+v, want the preferred slot 3", policyName(policy), d)
			continue
		}
		if d := Decide(accounts(93, 2*time.Hour), 3, prefOld, policy, prefNow); d.Action != AutoNone {
			t.Errorf("%s: left the preferred account while the expiring one is full: %+v", policyName(policy), d)
		}
		d = Decide(accounts(93, -time.Minute), 3, prefOld, policy, prefNow)
		if d.Action != AutoSwitch || d.To != 1 || !strings.Contains(d.Reason, "resets in 6h, within the preferred yield 9h") {
			t.Errorf("%s: did not return to the expiring account: %+v", policyName(policy), d)
		}
	}
}

// Accounts the yield does not count give the decision the yield-off policy
// gives: an expiring account without a running seven-day window, with
// unknown usage, disabled, or a backup or preferred account inside the yield.
func TestPreferredYieldIgnoresNonCandidates(t *testing.T) {
	noWeek := normalSlot(1, 10, time.Hour, 10, time.Hour)
	noWeek.LastUsage.Usage.SevenDay = &Window{Utilization: 10}
	noSevenDay := normalSlot(1, 10, time.Hour, 10, time.Hour)
	noSevenDay.LastUsage.Usage.SevenDay = nil
	unknown := normalSlot(1, 10, time.Hour, 10, time.Hour)
	unknown.LastUsage = nil
	disabled := normalSlot(1, 10, time.Hour, 10, time.Hour)
	disabled.Disabled = true
	backup := prefSlot(1, RoleBackup, 10, time.Hour, 10, time.Hour)
	for name, slot := range map[string]Slot{"week not running": noWeek, "no seven-day window": noSevenDay,
		"unknown usage": unknown, "disabled": disabled, "backup inside the yield": backup} {
		accounts := &Accounts{Slots: []Slot{
			slot,
			normalSlot(2, 30, 2*time.Hour, 20, 3*testDay),
			preferredSlot(3, 20, time.Hour, 20, 5*testDay),
			backupSlot(4, 1, 0, 3*time.Hour),
		}}
		for _, policy := range yieldPolicies() {
			for _, active := range []int{1, 2, 3, 4} {
				for _, state := range []AutoState{prefOld, prefRecent} {
					got := Decide(accounts, active, state, policy, prefNow)
					want := Decide(accounts, active, state, yieldOff(policy), prefNow)
					if !sameDecision(got, want) {
						t.Errorf("%s, %s, active %d:\n got %+v\nwant %+v", name, policyName(policy), active, got, want)
					}
					if got.Action == AutoSwitch && !reflect.DeepEqual(candidatesFor(accounts, got, policy, prefNow), candidatesFor(accounts, want, yieldOff(policy), prefNow)) {
						t.Errorf("%s, %s, active %d: candidates differ", name, policyName(policy), active)
					}
				}
			}
		}
	}
}

// Two expiring accounts: the sooner seven-day reset first.
func TestPreferredYieldTwoExpiring(t *testing.T) {
	accounts := &Accounts{Slots: []Slot{
		normalSlot(1, 10, 2*time.Hour, 10, 8*time.Hour),
		normalSlot(2, 50, time.Hour, 50, 3*time.Hour),
		preferredSlot(3, 20, time.Hour, 20, 5*testDay),
	}}
	for _, policy := range yieldPolicies() {
		d := Decide(accounts, 3, prefOld, policy, prefNow)
		if d.Action != AutoSwitch || d.To != 2 {
			t.Errorf("%s: %+v, want slot 2 (resets in 3h)", policyName(policy), d)
			continue
		}
		if got := candidatesFor(accounts, d, policy, prefNow); !reflect.DeepEqual(got, []int{2, 1}) {
			t.Errorf("%s: candidates %v", policyName(policy), got)
		}
	}
	// The live strategy, on the later one: moves to the sooner one, never to
	// the preferred account.
	policy := livePolicy
	policy.PreferredYield = testYield
	if d := Decide(accounts, 1, prefOld, policy, prefNow); d.Action != AutoSwitch || d.To != 2 {
		t.Fatalf("on the later expiring account: %+v", d)
	}
	if d := Decide(accounts, 2, prefOld, policy, prefNow); d.Action != AutoNone {
		t.Fatalf("on the sooner expiring account: %+v", d)
	}
}

// Without a preferred slot the yield changes nothing.
func TestPreferredYieldWithoutPreferredChangesNothing(t *testing.T) {
	sets := [][]Slot{
		{normalSlot(1, 40, 2*time.Hour, 60, 5*time.Hour), normalSlot(2, 10, 10*time.Minute, 10, 3*testDay), normalSlot(3, 95, time.Hour, 20, 2*time.Hour)},
		{normalSlot(1, 92, 2*time.Hour, 60, 5*time.Hour), normalSlot(2, 10, 10*time.Minute, 10, 3*testDay), backupSlot(3, 1, 0, time.Hour), prefSlot(4, RoleBackup, 10, time.Hour, 10, time.Hour)},
	}
	for i, slots := range sets {
		accounts := &Accounts{Slots: slots}
		for _, policy := range yieldPolicies() {
			for _, s := range slots {
				for _, state := range []AutoState{prefOld, prefRecent} {
					got := Decide(accounts, s.Number, state, policy, prefNow)
					want := Decide(accounts, s.Number, state, yieldOff(policy), prefNow)
					if !reflect.DeepEqual(got, want) {
						t.Errorf("set %d, %s, active %d:\n got %+v\nwant %+v", i, policyName(policy), s.Number, got, want)
					}
				}
			}
		}
	}
}

// The weekly-active keepalive target is the account auto switching drains:
// an expiring normal account first when some account is preferred.
func TestWeeklyActiveTargetPreferredYield(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []weekCase{
		{week: 10, weekIn: 5 * time.Hour},
		{week: 10, weekIn: 2 * testDay},
		{week: 50, weekIn: 5 * testDay},
	}
	target := func(yield time.Duration, preferred bool, change func(*Accounts)) int {
		accounts := weekAccounts(now, cases...)
		if preferred {
			accounts.Slot(3).Role = RolePreferred
		}
		if change != nil {
			change(accounts)
		}
		opts := weeklyActive
		opts.Policy.PreferredYield = yield
		return buildKeepAlive(accounts, KeepAliveState{}, now, opts).Target
	}
	if got := target(testYield, true, nil); got != 1 {
		t.Fatalf("expiring slot 1 inside the yield: target %d", got)
	}
	if got := target(0, true, nil); got != 3 {
		t.Fatalf("yield off: target %d, want the preferred slot 3", got)
	}
	if got := target(4*time.Hour, true, nil); got != 3 {
		t.Fatalf("slot 1 outside a 4h yield: target %d", got)
	}
	if got := target(testYield, false, nil); got != 1 {
		t.Fatalf("no preferred account: target %d, want 1 as before", got)
	}
	overWeek := func(a *Accounts) { a.Slot(1).LastUsage.Usage.SevenDay.Utilization = 90 }
	if got := target(testYield, true, overWeek); got != 3 {
		t.Fatalf("expiring slot over its weekly limit: target %d, want the preferred slot", got)
	}
	// The five-hour state is left out: a full five-hour window keeps it.
	fullFive := func(a *Accounts) {
		a.Slot(1).LastUsage.Usage.FiveHour = &Window{Utilization: 100, ResetsAt: now.Add(time.Hour)}
	}
	if got := target(testYield, true, fullFive); got != 1 {
		t.Fatalf("expiring slot with a full five-hour window: target %d", got)
	}
}

func TestPreferredYieldReasonNamesWindow(t *testing.T) {
	policy := livePolicy
	policy.PreferredYield = testYield
	accounts := &Accounts{Slots: []Slot{
		normalSlot(1, 40, 2*time.Hour, 60, 90*time.Minute),
		preferredSlot(3, 20, time.Hour, 20, 5*testDay),
	}}
	d := Decide(accounts, 3, prefOld, policy, prefNow)
	want := "slot 1's seven-day window resets in 1h30m, within the preferred yield 9h; slot 1 at seven-day 60%, leaving slot 3 at five-hour 20%"
	if d.Reason != want {
		t.Fatalf("reason %q\nwant %q", d.Reason, want)
	}
}
