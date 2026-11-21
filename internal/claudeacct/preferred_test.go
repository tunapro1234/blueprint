package claudeacct

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// prefNow is the clock of the preferred-role tests.
var prefNow = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

// prefSlot is a slot with both windows known: five-hour utilization and time
// to its reset (negative: the reset has passed), seven-day the same.
func prefSlot(n int, role string, five float64, fiveIn time.Duration, week float64, weekIn time.Duration) Slot {
	return Slot{Number: n, AccountUUID: fmt.Sprint("a", n), Role: role,
		LastUsage: &UsageCache{Usage: &Usage{
			FiveHour: &Window{Utilization: five, ResetsAt: prefNow.Add(fiveIn)},
			SevenDay: &Window{Utilization: week, ResetsAt: prefNow.Add(weekIn)},
		}}}
}

func normalSlot(n int, five float64, fiveIn time.Duration, week float64, weekIn time.Duration) Slot {
	return prefSlot(n, "", five, fiveIn, week, weekIn)
}

func preferredSlot(n int, five float64, fiveIn time.Duration, week float64, weekIn time.Duration) Slot {
	return prefSlot(n, RolePreferred, five, fiveIn, week, weekIn)
}

func backupSlot(n, priority int, five float64, fiveIn time.Duration) Slot {
	s := prefSlot(n, RoleBackup, five, fiveIn, 0, 6*testDay)
	s.Priority = priority
	return s
}

// livePolicy is the owner's setup: soonest-weekly-reset, threshold 90, weekly
// threshold 95, interleaved backups.
var livePolicy = AutoPolicy{Threshold: 90, WeeklyThreshold: 95, Cooldown: 5 * time.Minute,
	Prefer: StrategySoonestWeeklyReset, BackupOrder: SwitchOrderInterleave}

// allPolicies are the switch strategies crossed with both backup orders.
func allPolicies() []AutoPolicy {
	var out []AutoPolicy
	for _, prefer := range []string{StrategySoonestWeeklyReset, StrategySoonestReset, ""} {
		for _, order := range []string{SwitchOrderInterleave, SwitchOrderAfterNormals} {
			p := livePolicy
			p.Prefer, p.BackupOrder = prefer, order
			out = append(out, p)
		}
	}
	return out
}

var (
	prefOld    = AutoState{LastSwitchAt: prefNow.Add(-time.Hour)}
	prefRecent = AutoState{LastSwitchAt: prefNow.Add(-time.Minute)}
)

func policyName(p AutoPolicy) string {
	return fmt.Sprintf("prefer %q, order %s", p.Prefer, p.BackupOrder)
}

func TestDecidePreferredAccounts(t *testing.T) {
	type decideCase struct {
		name       string
		slots      []Slot
		active     int
		state      AutoState
		action     string
		to         int
		candidates []int
		reason     string
	}
	// The normal accounts reset sooner (both windows) and have more room than
	// the preferred one, so no strategy would pick the preferred account on
	// its own.
	n1 := normalSlot(1, 20, time.Hour, 10, testDay)
	n2 := normalSlot(2, 10, 2*time.Hour, 20, 2*testDay)
	p3 := preferredSlot(3, 50, 4*time.Hour, 60, 5*testDay)
	b4 := backupSlot(4, 1, 0, 3*time.Hour)
	// full copies the slot with its five-hour window at 95%.
	full := func(s Slot) Slot {
		usage := *s.LastUsage.Usage
		five := *usage.FiveHour
		five.Utilization = 95
		usage.FiveHour = &five
		s.LastUsage = &UsageCache{Usage: &usage}
		return s
	}
	cases := []decideCase{
		{"non-preferred active with room: switch to the preferred account",
			[]Slot{n1, n2, p3, b4}, 1, prefOld, AutoSwitch, 3, []int{3}, "preferred slot 3 has room at seven-day 60%; leaving slot 1 at five-hour 20%"},
		{"cooldown holds the switch to the preferred account back",
			[]Slot{n1, n2, p3, b4}, 1, prefRecent, AutoNone, 0, nil, "cooling down"},
		{"on a backup with room: switch to the preferred account",
			[]Slot{n1, n2, p3, b4}, 4, prefOld, AutoSwitch, 3, []int{3}, "preferred slot 3 has room"},
		{"preferred active with room, normals reset sooner: stays",
			[]Slot{n1, n2, p3, b4}, 3, prefOld, AutoNone, 0, nil, "among preferred accounts: "},
		{"preferred active with room, within cooldown: stays",
			[]Slot{n1, n2, p3, b4}, 3, prefRecent, AutoNone, 0, nil, "among preferred accounts: "},
		{"preferred used up, all normals used up: backup",
			[]Slot{full(n1), full(n2), full(p3), b4}, 3, prefOld, AutoSwitch, 4, []int{4}, "(backup, priority 1)"},
		{"everything used up: no target",
			[]Slot{full(n1), full(n2), full(p3), full(b4)}, 3, prefOld, AutoNoTarget, 0, nil, "no other account has room"},
		{"non-preferred used up, preferred has room: preferred first, then the usual fallback",
			[]Slot{full(n1), full(n2), p3, b4}, 1, prefOld, AutoSwitch, 3, []int{3, 4}, "active account at five-hour 95% (threshold 90%); preferred slot 3 has room"},
	}
	for _, policy := range allPolicies() {
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

// The preferred account's five-hour window fills: auto switching takes the
// policy's normal target, and comes straight back once that window resets.
func TestDecidePreferredUsedUpAndBack(t *testing.T) {
	accounts := func(preferredFive float64, preferredIn time.Duration) *Accounts {
		return &Accounts{Slots: []Slot{
			normalSlot(1, 40, time.Hour, 30, 4*testDay),    // most room, latest weekly reset
			normalSlot(2, 60, 30*time.Minute, 70, testDay), // soonest resets on both windows
			preferredSlot(3, preferredFive, preferredIn, 50, 5*testDay),
			backupSlot(4, 1, 0, 3*time.Hour),
		}}
	}
	for _, policy := range allPolicies() {
		// The normal account the policy itself picks from slot 3's place.
		want := 2
		if !policy.soonest() {
			want = 1
		}
		d := Decide(accounts(92, 2*time.Hour), 3, prefOld, policy, prefNow)
		if d.Action != AutoSwitch || d.To != want || !strings.Contains(d.Reason, "preferred slot 3 at five-hour 92% (threshold 90%), no other preferred account has room") {
			t.Errorf("%s: preferred used up: %+v, want slot %d", policyName(policy), d, want)
			continue
		}
		if got := candidatesFor(accounts(92, 2*time.Hour), d, policy, prefNow); got[0] != want || got[len(got)-1] != 4 || len(got) != 3 {
			t.Errorf("%s: candidates %v: normals by the policy, then the backup", policyName(policy), got)
		}
		// Still used up: stays on the normal account.
		if d := Decide(accounts(92, 2*time.Hour), want, prefOld, policy, prefNow); d.Action != AutoNone {
			t.Errorf("%s: left the normal account while the preferred one is full: %+v", policyName(policy), d)
		}
		// The window has reset (its reset is in the past): straight back.
		d = Decide(accounts(92, -time.Minute), want, prefOld, policy, prefNow)
		if d.Action != AutoSwitch || d.To != 3 || d.TargetMax != 50 {
			t.Errorf("%s: did not return to the reset preferred account: %+v", policyName(policy), d)
		}
	}
}

// A preferred account used up on its seven-day window is not a target: the
// decision is the one the same accounts give without that slot.
func TestDecidePreferredUsedUpWeeklyIsIgnored(t *testing.T) {
	sets := map[string][]Slot{
		"normals only": {
			normalSlot(1, 20, time.Hour, 10, 3*testDay),
			normalSlot(2, 95, 2*time.Hour, 20, testDay),
			normalSlot(3, 0, 0, 5, 2*testDay),
		},
		"with backups": {
			normalSlot(1, 92, time.Hour, 10, 3*testDay),
			normalSlot(2, 95, 2*time.Hour, 20, testDay),
			normalSlot(3, 30, time.Hour, 50, 2*testDay),
			backupSlot(4, 2, 0, 3*time.Hour),
			backupSlot(5, 1, 40, time.Hour),
		},
	}
	weekFull := preferredSlot(9, 0, 0, 96, 12*time.Hour)
	unknown := preferredSlot(9, 0, 0, 0, testDay)
	unknown.LastUsage = nil
	disabled := preferredSlot(9, 0, 0, 0, testDay)
	disabled.Disabled = true
	dead := preferredSlot(9, 0, 0, 0, testDay)
	dead.Dead = true
	ignored := map[string]Slot{"seven-day used up": weekFull, "unknown usage": unknown, "disabled": disabled, "dead": dead}
	states := map[string]AutoState{"old": prefOld, "recent": prefRecent,
		"used up seen": {LastSwitchAt: prefNow.Add(-time.Hour), UsedUp: map[string]time.Time{"1": prefNow.Add(-time.Minute)}}}
	for setName, slots := range sets {
		for slotName, extra := range ignored {
			for stateName, state := range states {
				for _, policy := range allPolicies() {
					without := &Accounts{Slots: slots}
					with := &Accounts{Slots: append(append([]Slot{}, slots...), extra)}
					for _, active := range []int{1, 2, 3, 4, 5} {
						if without.Slot(active) == nil {
							continue
						}
						want := Decide(without, active, state, policy, prefNow)
						got := Decide(with, active, state, policy, prefNow)
						name := fmt.Sprintf("%s, %s, state %s, %s, active %d", setName, slotName, stateName, policyName(policy), active)
						if !sameDecision(got, want) {
							t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
							continue
						}
						if got.Action == AutoSwitch {
							if g, w := candidatesFor(with, got, policy, prefNow), candidatesFor(without, want, policy, prefNow); !reflect.DeepEqual(g, w) {
								t.Errorf("%s: candidates %v, want %v", name, g, w)
							}
						}
					}
				}
			}
		}
	}
}

// sameDecision compares the exported fields of two decisions.
func sameDecision(a, b Decision) bool {
	a.candidates, b.candidates = nil, nil
	return reflect.DeepEqual(a, b)
}

// Two preferred accounts are ordered by the policy between themselves.
func TestDecideTwoPreferredAccounts(t *testing.T) {
	slots := func() []Slot {
		return []Slot{
			normalSlot(1, 0, 10*time.Minute, 0, 12*time.Hour),
			preferredSlot(2, 50, time.Hour, 50, 4*testDay),    // more room
			preferredSlot(3, 70, 30*time.Minute, 70, testDay), // resets sooner on both windows
		}
	}
	for _, policy := range allPolicies() {
		first, second := 3, 2
		if !policy.soonest() {
			first, second = 2, 3
		}
		accounts := &Accounts{Slots: slots()}
		d := Decide(accounts, 1, prefOld, policy, prefNow)
		if d.Action != AutoSwitch || d.To != first {
			t.Errorf("%s: from normal: %+v, want slot %d", policyName(policy), d, first)
			continue
		}
		if got := candidatesFor(accounts, d, policy, prefNow); !reflect.DeepEqual(got, []int{first, second}) {
			t.Errorf("%s: candidates %v", policyName(policy), got)
		}
		// On the other preferred account with room: the soonest strategies
		// move to the one resetting sooner; most-room stays while it has room.
		d = Decide(accounts, 2, prefOld, policy, prefNow)
		if policy.soonest() && (d.Action != AutoSwitch || d.To != 3) || !policy.soonest() && d.Action != AutoNone {
			t.Errorf("%s: on slot 2: %+v", policyName(policy), d)
		}
		if d.Action == AutoSwitch {
			if got := candidatesFor(accounts, d, policy, prefNow); !reflect.DeepEqual(got, []int{3}) {
				t.Errorf("%s: never leaves the preferred tier with room, candidates %v", policyName(policy), got)
			}
		}
		// The active preferred account is used up: the other preferred one
		// first, then the normal account.
		accounts.Slot(first).LastUsage.Usage.FiveHour.Utilization = 95
		d = Decide(accounts, first, prefOld, policy, prefNow)
		if d.Action != AutoSwitch || d.To != second || !strings.Contains(d.Reason, "next preferred slot") {
			t.Errorf("%s: used up: %+v", policyName(policy), d)
		}
		if got := candidatesFor(accounts, d, policy, prefNow); !reflect.DeepEqual(got, []int{second, 1}) {
			t.Errorf("%s: used up candidates %v", policyName(policy), got)
		}
	}
}

// A used-up preferred account takes a normal account with room before a
// backup in both backup switch orders, and a backup by priority only when no
// normal account has room.
func TestDecidePreferredUsedUpNormalsBeforeBackups(t *testing.T) {
	for _, policy := range allPolicies() {
		accounts := &Accounts{Slots: []Slot{
			normalSlot(1, 95, time.Hour, 10, testDay),
			normalSlot(2, 30, 2*time.Hour, 20, 2*testDay),
			preferredSlot(3, 93, time.Hour, 50, 5*testDay),
			backupSlot(4, 2, 0, 3*time.Hour),
			backupSlot(5, 1, 10, 10*time.Minute),
		}}
		d := Decide(accounts, 3, prefOld, policy, prefNow)
		if d.Action != AutoSwitch || d.To != 2 {
			t.Errorf("%s: %+v, want normal slot 2", policyName(policy), d)
		}
		if got := candidatesFor(accounts, d, policy, prefNow); !reflect.DeepEqual(got, []int{2, 5, 4}) {
			t.Errorf("%s: candidates %v", policyName(policy), got)
		}
		accounts.Slot(2).LastUsage.Usage.SevenDay.Utilization = 97
		d = Decide(accounts, 3, prefOld, policy, prefNow)
		if d.Action != AutoSwitch || d.To != 5 || !strings.Contains(d.Reason, "every preferred and normal account is used up") {
			t.Errorf("%s: %+v, want backup slot 5 (priority 1)", policyName(policy), d)
		}
		if got := candidatesFor(accounts, d, policy, prefNow); !reflect.DeepEqual(got, []int{5, 4}) {
			t.Errorf("%s: backup candidates %v", policyName(policy), got)
		}
		if d := Decide(accounts, 3, prefRecent, policy, prefNow); d.Action != AutoNone || !strings.Contains(d.Reason, "cooling down") {
			t.Errorf("%s: cooldown: %+v", policyName(policy), d)
		}
	}
}

// The interleave return logic stays about normal accounts: a used-up
// preferred account is not recorded, and on a backup auto switching returns
// to a preferred account with room without it.
func TestRecordUsedUpSkipsPreferred(t *testing.T) {
	accounts := &Accounts{Slots: []Slot{
		normalSlot(1, 95, time.Hour, 10, testDay),
		preferredSlot(2, 95, time.Hour, 10, testDay),
		backupSlot(3, 1, 0, time.Hour),
	}}
	var state AutoState
	if recordUsedUp(&state, accounts, 2, livePolicy, prefNow) || len(state.UsedUp) != 0 {
		t.Fatalf("recorded a preferred account: %+v", state)
	}
	if !recordUsedUp(&state, accounts, 1, livePolicy, prefNow) {
		t.Fatal("used-up normal account not recorded")
	}
	accounts.Slot(2).LastUsage.Usage.FiveHour.ResetsAt = prefNow.Add(-time.Minute)
	if d := Decide(accounts, 3, AutoState{LastSwitchAt: prefOld.LastSwitchAt}, livePolicy, prefNow); d.Action != AutoSwitch || d.To != 2 {
		t.Fatalf("backup did not return to the reset preferred account: %+v", d)
	}
}

func TestPreferredRole(t *testing.T) {
	s := Slot{Role: RolePreferred}
	if !s.IsPreferred() || s.IsBackup() || s.RoleName() != RolePreferred || !ValidRole(RolePreferred) || ValidRole("primary") {
		t.Fatalf("preferred role: %+v", s)
	}
	if (Slot{}).RoleName() != RoleNormal || (Slot{Role: RoleBackup}).RoleName() != RoleBackup {
		t.Fatal("normal and backup role names changed")
	}
}

// The weekly-active keepalive target is the account auto switching drains: a
// preferred one while it has seven-day room, whatever its five-hour state.
func TestWeeklyActiveTargetPrefersPreferred(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []weekCase{
		{week: 10, weekIn: testDay},
		{week: 10, weekIn: 2 * testDay},
		{week: 50, weekIn: 5 * testDay},
		{week: 10, weekIn: 12 * time.Hour, backup: true, priority: 1},
	}
	target := func(preferred map[int]func(*Slot)) int {
		accounts := weekAccounts(now, cases...)
		for n, change := range preferred {
			s := accounts.Slot(n)
			s.Role = RolePreferred
			if change != nil {
				change(s)
			}
		}
		return buildKeepAlive(accounts, KeepAliveState{}, now, weeklyActive).Target
	}
	if got := target(nil); got != 1 {
		t.Fatalf("without a preferred account: target %d, want 1", got)
	}
	if got := target(map[int]func(*Slot){3: nil}); got != 3 {
		t.Fatalf("preferred slot 3 with weekly room: target %d", got)
	}
	fullFive := func(s *Slot) {
		s.LastUsage.Usage.FiveHour = &Window{Utilization: 100, ResetsAt: now.Add(time.Hour)}
	}
	if got := target(map[int]func(*Slot){3: fullFive}); got != 3 {
		t.Fatalf("a full five-hour window keeps the preferred target: target %d", got)
	}
	overWeek := func(s *Slot) { s.LastUsage.Usage.SevenDay.Utilization = 95 }
	if got := target(map[int]func(*Slot){3: overWeek}); got != 1 {
		t.Fatalf("preferred over its weekly limit: target %d, want the normal choice 1", got)
	}
	if got := target(map[int]func(*Slot){2: nil, 3: nil}); got != 2 {
		t.Fatalf("two preferred accounts: target %d, want the sooner weekly reset 2", got)
	}
	result := buildKeepAlive(weekAccounts(now, cases...), KeepAliveState{}, now, weeklyActive)
	if v := weekView(t, result, 3); v.Role != RoleNormal {
		t.Fatalf("role %q", v.Role)
	}
	accounts := weekAccounts(now, cases...)
	accounts.Slot(3).Role = RolePreferred
	result = buildKeepAlive(accounts, KeepAliveState{}, now, weeklyActive)
	if v := weekView(t, result, 3); v.Role != RolePreferred || !v.Due {
		t.Fatalf("preferred target view %+v", v)
	}
	if due := dueSlots(result); !reflect.DeepEqual(due, []int{3}) {
		t.Fatalf("due %v, want only the preferred target", due)
	}
}

// In the all scope a preferred account is planned like a normal one.
func TestKeepAliveAllScopePreferredIsNormal(t *testing.T) {
	now := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	build := func(role string) KeepAliveResult {
		accounts := &Accounts{}
		for n := 1; n <= 4; n++ {
			s := Slot{Number: n, AccountUUID: fmt.Sprint("a", n),
				LastUsage: &UsageCache{FetchedAt: now, Usage: &Usage{FiveHour: &Window{0, time.Time{}}, SevenDay: &Window{0, time.Time{}}}}}
			if n == 4 {
				s.Role = RoleBackup
			}
			if n == 1 {
				s.Role = role
			}
			accounts.Slots = append(accounts.Slots, s)
		}
		return buildKeepAlive(accounts, KeepAliveState{}, now, KeepAliveOptions{SkipBackups: true})
	}
	normal, preferred := build(""), build(RolePreferred)
	if normal.Step != preferred.Step || normal.Step != KeepAlivePeriod/3 {
		t.Fatalf("step %v vs %v", normal.Step, preferred.Step)
	}
	for i := range normal.Slots {
		a, b := normal.Slots[i], preferred.Slots[i]
		b.Role = a.Role
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("slot %d planned differently:\n%+v\n%+v", a.Slot, a, b)
		}
	}
}
