package claudeacct

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestDecideWithBackupAccounts(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	slot := func(n int, five float64, reset time.Duration, role string, priority int) Slot {
		return Slot{Number: n, AccountUUID: fmt.Sprint("a", n), Role: role, Priority: priority,
			LastUsage: &UsageCache{Usage: &Usage{FiveHour: &Window{five, now.Add(reset)}, SevenDay: &Window{0, now.Add(72 * time.Hour)}}}}
	}
	normal := func(n int, five float64, reset time.Duration) Slot { return slot(n, five, reset, "", 0) }
	backup := func(n int, five float64, reset time.Duration, priority int) Slot {
		return slot(n, five, reset, RoleBackup, priority)
	}
	old := AutoState{LastSwitchAt: now.Add(-time.Hour)}
	recent := AutoState{LastSwitchAt: now.Add(-time.Minute)}
	cases := []struct {
		name       string
		slots      []Slot
		active     int
		state      AutoState
		action     string
		to         int
		candidates []int
	}{
		{"normal with room never moves to a sooner backup",
			[]Slot{normal(1, 50, 4*time.Hour), normal(2, 95, time.Hour), backup(4, 0, 10*time.Minute, 1)}, 1, old, AutoNone, 0, nil},
		{"normal to normal first",
			[]Slot{normal(1, 95, time.Hour), normal(2, 20, 3*time.Hour), backup(4, 0, 10*time.Minute, 1)}, 1, old, AutoSwitch, 2, []int{2}},
		{"all normals full: backup by priority",
			[]Slot{normal(1, 95, time.Hour), normal(2, 92, time.Hour), normal(3, 99, time.Hour), backup(4, 0, 0, 2), backup(5, 30, 2*time.Hour, 1)}, 1, old, AutoSwitch, 5, []int{5, 4}},
		{"all normals full, backup within cooldown",
			[]Slot{normal(1, 95, time.Hour), backup(4, 0, 0, 1)}, 1, recent, AutoNone, 0, nil},
		{"on backup, a normal reset: back to normal",
			[]Slot{normal(1, 95, time.Hour), normal(2, 5, 5*time.Hour), normal(3, 99, time.Hour), backup(5, 30, 2*time.Hour, 1)}, 5, old, AutoSwitch, 2, []int{2}},
		{"on backup, a normal reset but cooling down",
			[]Slot{normal(1, 95, time.Hour), normal(2, 5, 5*time.Hour), backup(5, 30, 2*time.Hour, 1)}, 5, recent, AutoNone, 0, nil},
		{"on backup with room, normals full: stay",
			[]Slot{normal(1, 95, time.Hour), normal(2, 91, time.Hour), backup(4, 0, 0, 1), backup(5, 30, 2*time.Hour, 2)}, 5, old, AutoNone, 0, nil},
		{"backup used up: next backup",
			[]Slot{normal(1, 95, time.Hour), backup(4, 0, 0, 2), backup(5, 93, 2*time.Hour, 1)}, 5, old, AutoSwitch, 4, []int{4}},
		{"everything used up",
			[]Slot{normal(1, 95, time.Hour), backup(4, 99, time.Hour, 1)}, 1, old, AutoNoTarget, 0, nil},
	}
	for _, prefer := range []string{StrategySoonestReset, ""} {
		policy := AutoPolicy{Threshold: 90, Cooldown: 5 * time.Minute, Prefer: prefer}
		for _, c := range cases {
			accounts := &Accounts{Slots: c.slots}
			d := Decide(accounts, c.active, c.state, policy, now)
			if d.Action != c.action || d.To != c.to {
				t.Errorf("%s (prefer %q): %+v", c.name, prefer, d)
				continue
			}
			if d.Action == AutoSwitch {
				if got := candidatesFor(accounts, d, policy, now); !reflect.DeepEqual(got, c.candidates) {
					t.Errorf("%s (prefer %q): candidates %v, want %v", c.name, prefer, got, c.candidates)
				}
			}
		}
	}
}

func TestAlternatingRoles(t *testing.T) {
	n, b := false, true
	for _, c := range []struct {
		roles []bool
		want  bool
	}{
		{[]bool{n, b, n, b, n, b}, true},
		{[]bool{b, n, b, n, b, n}, true},
		{[]bool{n, n, b, b, n, b}, false},
		{[]bool{n, b, n, b, n}, true},
		{[]bool{n, n, b, n, b}, true}, // rotation of n b n b n
		{[]bool{n, b, b, n, n}, false},
		{[]bool{n, b, n, n}, true}, // 3+1: n b n n
		{[]bool{n, n, n}, true},
		{[]bool{b, b}, true},
	} {
		if got := alternatingRoles(c.roles); got != c.want {
			t.Errorf("%v: got %v", c.roles, got)
		}
	}
}

// Keepalive keeps backups but alternates the windows: going round the cycle a
// normal start is always followed by a backup start (a b a b).
func TestPlanPhasesAlternatesNormalAndBackupWindows(t *testing.T) {
	now := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 200; round++ {
		var members []keepMember
		for slot := 1; slot <= 6; slot++ {
			ready := now
			if round > 0 {
				ready = now.Add(time.Duration(rng.Intn(300)) * time.Minute)
			}
			members = append(members, keepMember{slot: slot, ready: ready, known: true, backup: slot >= 4})
		}
		plan := planPhases(members)
		assertSpaced(t, plan, 6)
		order := make([]int, 0, 6)
		for slot := range plan.start {
			order = append(order, slot)
		}
		phase := func(slot int) time.Duration {
			return ((plan.start[slot].Sub(plan.anchor) % KeepAlivePeriod) + KeepAlivePeriod) % KeepAlivePeriod
		}
		sort.Slice(order, func(i, j int) bool { return phase(order[i]) < phase(order[j]) })
		roles := make([]bool, len(order))
		for i, slot := range order {
			roles[i] = slot >= 4
		}
		for i := range roles {
			if roles[i] == roles[(i+1)%len(roles)] {
				t.Fatalf("round %d: windows do not alternate: slots %v", round, order)
			}
		}
	}
}

// Interleave: A1 B1 A2 B2 A3 B3, each used until it is over the threshold, and
// back to a normal account once its used-up window has reset.
func TestDecideInterleaveSwitchOrder(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	slot := func(n int, five float64, reset time.Duration, role string, priority int) Slot {
		return Slot{Number: n, AccountUUID: fmt.Sprint("a", n), Role: role, Priority: priority,
			LastUsage: &UsageCache{Usage: &Usage{FiveHour: &Window{five, now.Add(reset)}, SevenDay: &Window{0, now.Add(72 * time.Hour)}}}}
	}
	policy := AutoPolicy{Threshold: 90, Cooldown: 5 * time.Minute, Prefer: StrategySoonestReset, BackupOrder: SwitchOrderInterleave}
	old := AutoState{LastSwitchAt: now.Add(-time.Hour)}
	usedUp := func(entries map[int]time.Duration) AutoState {
		state := old
		state.UsedUp = map[string]time.Time{}
		for n, reset := range entries {
			state.UsedUp[fmt.Sprint(n)] = now.Add(reset)
		}
		return state
	}
	a1 := func(five float64, reset time.Duration) Slot { return slot(1, five, reset, "", 0) }
	a2 := func(five float64) Slot { return slot(2, five, 4*time.Hour, "", 0) }
	b1 := func(five float64) Slot { return slot(4, five, 3*time.Hour, RoleBackup, 1) }
	b2 := func(five float64) Slot { return slot(5, five, 3*time.Hour, RoleBackup, 2) }
	cases := []struct {
		name   string
		slots  []Slot
		active int
		state  AutoState
		action string
		to     int
	}{
		{"A1 with room: backups do not exist", []Slot{a1(50, time.Hour), a2(0), b1(0), b2(0)}, 1, old, AutoNone, 0},
		{"A1 used up: B1 before A2", []Slot{a1(95, time.Hour), a2(0), b1(0), b2(0)}, 1, old, AutoSwitch, 4},
		{"on B1 with room: unused A2 does not pull it back", []Slot{a1(95, time.Hour), a2(0), b1(40), b2(0)}, 4, usedUp(map[int]time.Duration{1: time.Hour}), AutoNone, 0},
		{"B1 used up: A2", []Slot{a1(95, time.Hour), a2(0), b1(95), b2(0)}, 4, usedUp(map[int]time.Duration{1: time.Hour}), AutoSwitch, 2},
		{"A2 used up: B2 (B1 full)", []Slot{a1(95, time.Hour), a2(95), b1(95), b2(0)}, 2, usedUp(map[int]time.Duration{1: time.Hour}), AutoSwitch, 5},
		{"on B1, A1 reset: back to A1", []Slot{a1(0, 5*time.Hour), a2(0), b1(40), b2(0)}, 4, usedUp(map[int]time.Duration{1: -time.Minute}), AutoSwitch, 1},
		{"A used up, no backup left: next A", []Slot{a1(95, time.Hour), a2(0), b1(95), b2(99)}, 1, old, AutoSwitch, 2},
	}
	for _, c := range cases {
		d := Decide(&Accounts{Slots: c.slots}, c.active, c.state, policy, now)
		if d.Action != c.action || d.To != c.to {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
	// after-normals on the same "A1 used up" state goes to A2 instead.
	policy.BackupOrder = SwitchOrderAfterNormals
	if d := Decide(&Accounts{Slots: []Slot{a1(95, time.Hour), a2(0), b1(0), b2(0)}}, 1, old, policy, now); d.Action != AutoSwitch || d.To != 2 {
		t.Errorf("after-normals: %+v", d)
	}
}

func TestRecordUsedUpOnlyForUsedUpActiveNormal(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	slot := func(n int, five float64, role string) Slot {
		return Slot{Number: n, AccountUUID: fmt.Sprint("a", n), Role: role,
			LastUsage: &UsageCache{Usage: &Usage{FiveHour: &Window{five, reset}, SevenDay: &Window{0, reset}}}}
	}
	policy := AutoPolicy{Threshold: 90}
	accounts := &Accounts{Slots: []Slot{slot(1, 95, ""), slot(2, 50, ""), slot(4, 99, RoleBackup)}}
	var state AutoState
	if !recordUsedUp(&state, accounts, 1, policy, now) || !state.UsedUp["1"].Equal(reset) {
		t.Fatalf("used-up active normal not recorded: %+v", state)
	}
	if recordUsedUp(&state, accounts, 1, policy, now) {
		t.Fatal("same window recorded twice")
	}
	if recordUsedUp(&state, accounts, 2, policy, now) || recordUsedUp(&state, accounts, 4, policy, now) {
		t.Fatalf("recorded an account with room or a backup: %+v", state)
	}
}

func TestKeepAliveBackupOptions(t *testing.T) {
	now := time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
	accounts := &Accounts{}
	for n := 1; n <= 6; n++ {
		s := Slot{Number: n, AccountUUID: fmt.Sprint("a", n),
			LastUsage: &UsageCache{FetchedAt: now, Usage: &Usage{FiveHour: &Window{0, time.Time{}}, SevenDay: &Window{0, time.Time{}}}}}
		if n >= 4 {
			s.Role = RoleBackup
		}
		accounts.Slots = append(accounts.Slots, s)
	}
	skip := buildKeepAlive(accounts, KeepAliveState{}, now, KeepAliveOptions{SkipBackups: true})
	if skip.Step != KeepAlivePeriod/3 {
		t.Fatalf("backups still planned: step %v", skip.Step)
	}
	for _, view := range skip.Slots {
		if backup := view.Slot >= 4; backup != (view.Skipped != "") || backup && view.Due {
			t.Fatalf("slot %d: %+v", view.Slot, view)
		}
		if view.Role != map[bool]string{true: RoleBackup, false: RoleNormal}[view.Slot >= 4] {
			t.Fatalf("slot %d role %q", view.Slot, view.Role)
		}
	}
	all := buildKeepAlive(accounts, KeepAliveState{}, now, KeepAliveOptions{})
	if all.Step != KeepAlivePeriod/6 {
		t.Fatalf("step %v", all.Step)
	}
}
