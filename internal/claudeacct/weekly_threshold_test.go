package claudeacct

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

// twoWindowSlot is a slot with both windows known; the five-hour window resets
// in fiveReset and the seven-day window in weekReset.
func twoWindowSlot(now time.Time, n int, five float64, fiveReset time.Duration, week float64, weekReset time.Duration) Slot {
	return Slot{Number: n, AccountUUID: fmt.Sprint("a", n), Email: fmt.Sprintf("u%d@example.com", n),
		LastUsage: &UsageCache{Usage: &Usage{
			FiveHour: &Window{Utilization: five, ResetsAt: now.Add(fiveReset)},
			SevenDay: &Window{Utilization: week, ResetsAt: now.Add(weekReset)},
		}}}
}

func TestWeeklyThresholdUsedUpPerWindow(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	split := AutoPolicy{Threshold: 90, WeeklyThreshold: 95}
	single := AutoPolicy{Threshold: 90}
	for _, c := range []struct {
		name          string
		five, week    float64
		policy        AutoPolicy
		usedUp        bool
		room          float64
		decidingWeek  bool
		decidingValue float64
	}{
		{"7d 92 under weekly 95", 50, 92, split, false, 3, true, 92},
		{"7d 95 at weekly 95", 50, 95, split, true, 0, true, 95},
		{"5h 90 at threshold 90, 7d low", 90, 10, split, true, 0, false, 90},
		{"5h 89, 7d 94: equal room, the five-hour window decides", 89, 94, split, false, 1, false, 89},
		{"5h 80, 7d 94: the seven-day window has less room", 80, 94, split, false, 1, true, 94},
		{"7d 92 without weekly threshold", 50, 92, single, true, -2, true, 92},
	} {
		s := twoWindowSlot(now, 1, c.five, time.Hour, c.week, 3*testDay)
		if got := c.policy.usedUp(s, now); got != c.usedUp {
			t.Errorf("%s: usedUp %v, want %v", c.name, got, c.usedUp)
		}
		use, known := c.policy.use(s, now)
		if !known || use.room() != c.room || use.sevenDay != c.decidingWeek || use.value != c.decidingValue {
			t.Errorf("%s: use %+v known %v, want room %v seven-day %v value %v", c.name, use, known, c.room, c.decidingWeek, c.decidingValue)
		}
	}
	if got := split.WeeklyLimitFor(Slot{Number: 1}); got != 95 {
		t.Fatalf("weekly limit %v, want 95", got)
	}
	if got := single.WeeklyLimitFor(Slot{Number: 1}); got != 90 {
		t.Fatalf("weekly limit without weekly threshold %v, want the threshold 90", got)
	}
	if got := (AutoPolicy{}).WeeklyLimitFor(Slot{Number: 1}); got != 100 {
		t.Fatalf("weekly limit of an empty policy %v, want 100", got)
	}
}

func TestWeeklyThresholdDecide(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	old := AutoState{LastSwitchAt: now.Add(-time.Hour)}
	accounts := func(five, week float64) *Accounts {
		return &Accounts{Slots: []Slot{
			twoWindowSlot(now, 1, five, 2*time.Hour, week, 3*testDay),
			twoWindowSlot(now, 2, 10, 3*time.Hour, 5, 4*testDay),
		}}
	}
	for _, prefer := range []string{"", StrategySoonestReset, StrategySoonestWeeklyReset} {
		split := AutoPolicy{Threshold: 90, WeeklyThreshold: 95, Prefer: prefer}
		single := AutoPolicy{Threshold: 90, Prefer: prefer}
		if d := Decide(accounts(50, 92), 1, old, split, now); d.Action != AutoNone {
			t.Errorf("prefer %q: 7d 92%% under weekly 95%% must not switch: %+v", prefer, d)
		}
		if d := Decide(accounts(50, 92), 1, old, single, now); d.Action != AutoSwitch || d.To != 2 {
			t.Errorf("prefer %q: 7d 92%% over threshold 90%% without weekly threshold must switch: %+v", prefer, d)
		}
		if d := Decide(accounts(50, 95), 1, old, split, now); d.Action != AutoSwitch || d.To != 2 {
			t.Errorf("prefer %q: 7d 95%% at weekly 95%% must switch: %+v", prefer, d)
		}
		if d := Decide(accounts(90, 10), 1, old, split, now); d.Action != AutoSwitch || d.To != 2 {
			t.Errorf("prefer %q: 5h 90%% at threshold 90%% must switch: %+v", prefer, d)
		}
	}

	// The reason names the deciding window when the limits differ.
	room := AutoPolicy{Threshold: 90, WeeklyThreshold: 95}
	d := Decide(accounts(50, 92), 1, old, room, now)
	if d.Reason != "active account at seven-day 92%, below weekly threshold 95%" || d.ActiveMax != 92 || d.ActiveLimit != 95 || d.ActiveWindow != "seven-day" {
		t.Fatalf("seven-day reason: %+v", d)
	}
	d = Decide(accounts(91, 10), 1, old, room, now)
	if d.Reason != "active account at five-hour 91% (threshold 90%); slot 2 at five-hour 10%" || d.ActiveWindow != "five-hour" || d.TargetMax != 10 {
		t.Fatalf("five-hour reason: %+v", d)
	}
	d = Decide(accounts(50, 96), 1, old, room, now)
	if d.Reason != "active account at seven-day 96% (weekly threshold 95%); slot 2 at five-hour 10%" ||
		d.ActiveUsage() != "seven-day 96%" || d.TargetUsage() != "five-hour 10%" || d.TargetWindow != "five-hour" {
		t.Fatalf("seven-day switch reason: %+v", d)
	}
	// An own limit between the two thresholds reads as the account's limit.
	room.Limits = map[string]float64{"1": 93}
	d = Decide(accounts(50, 93), 1, old, room, now)
	if d.Reason != "active account at seven-day 93% (its 93% limit); slot 2 at five-hour 10%" {
		t.Fatalf("own seven-day limit reason: %+v", d)
	}
}

func TestWeeklyThresholdTargetRoom(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	old := AutoState{LastSwitchAt: now.Add(-time.Hour)}
	slots := []Slot{
		twoWindowSlot(now, 1, 100, 2*time.Hour, 40, 3*testDay),
		twoWindowSlot(now, 2, 0, time.Hour, 92, 2*testDay),
		twoWindowSlot(now, 3, 0, time.Hour, 96, testDay),
	}
	split := AutoPolicy{Threshold: 90, WeeklyThreshold: 95}
	if room, known := split.room(slots[1], now); !known || room != 3 {
		t.Fatalf("7d 92%% room %v known %v, want 3", room, known)
	}
	if room, _ := split.room(slots[2], now); room > 0 {
		t.Fatalf("7d 96%% room %v, want none", room)
	}
	for _, prefer := range []string{"", StrategySoonestReset, StrategySoonestWeeklyReset} {
		policy := split
		policy.Prefer = prefer
		accounts := &Accounts{Slots: slots}
		d := Decide(accounts, 1, old, policy, now)
		if d.Action != AutoSwitch || d.To != 2 || d.TargetMax != 92 {
			t.Errorf("prefer %q: want slot 2 at 92%%: %+v", prefer, d)
			continue
		}
		if got := candidatesFor(accounts, d, policy, now); !reflect.DeepEqual(got, []int{2}) {
			t.Errorf("prefer %q: candidates %v, want [2]", prefer, got)
		}
		if prefer == "" && !strings.HasSuffix(d.Reason, "slot 2 at seven-day 92%") {
			t.Errorf("prefer %q: reason %q", prefer, d.Reason)
		}
		policy.WeeklyThreshold = 0
		if d := Decide(accounts, 1, old, policy, now); d.Action != AutoNoTarget {
			t.Errorf("prefer %q without weekly threshold: want no target: %+v", prefer, d)
		}
	}
}

func TestWeeklyThresholdOwnLimitCapsBothWindows(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := AutoPolicy{Threshold: 90, WeeklyThreshold: 95, Limits: map[string]float64{"1": 80}}
	s := func(five, week float64) Slot { return twoWindowSlot(now, 1, five, time.Hour, week, testDay) }
	if policy.LimitFor(s(0, 0)) != 80 || policy.WeeklyLimitFor(s(0, 0)) != 80 {
		t.Fatalf("limits %v / %v, want 80 / 80", policy.LimitFor(s(0, 0)), policy.WeeklyLimitFor(s(0, 0)))
	}
	if !policy.usedUp(s(50, 80), now) || !policy.usedUp(s(80, 10), now) || policy.usedUp(s(79, 79), now) {
		t.Fatal("an own limit of 80 must cap both windows at 80")
	}
	old := AutoState{LastSwitchAt: now.Add(-time.Hour)}
	accounts := &Accounts{Slots: []Slot{s(50, 81), twoWindowSlot(now, 2, 0, time.Hour, 0, testDay)}}
	// Equal limits on both windows: the reason keeps the single-limit text.
	if d := Decide(accounts, 1, old, policy, now); d.Action != AutoSwitch || d.Reason != "active account at 81% (its 80% limit); slot 2 at five-hour 0%" || d.ActiveWindow != "" {
		t.Fatalf("capped account: %+v", d)
	}
}

// Without a weekly threshold, or with one equal to the threshold, every
// decision, candidate list and reason is what a single threshold gives.
func TestWeeklyThresholdUnsetIsUnchanged(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	old := AutoState{LastSwitchAt: now.Add(-time.Hour)}
	day := 24 * time.Hour
	// The 6 Nov accounts of TestSoonestWeeklyResetStrategy.
	nov6 := func(husoFive float64) *Accounts {
		return &Accounts{Slots: []Slot{
			twoWindowSlot(now, 1, 0, time.Hour, 58, 2*day+22*time.Hour),
			twoWindowSlot(now, 2, 0, 2*time.Hour, 0, 6*day+20*time.Hour),
			twoWindowSlot(now, 3, husoFive, 3*time.Hour, 56, day+4*time.Hour),
			twoWindowSlot(now, 4, 6, 30*time.Minute, 3, 5*day+20*time.Hour),
		}}
	}
	for _, c := range []struct {
		name     string
		accounts *Accounts
		active   int
		action   string
		to       int
	}{
		{"on yigit: move to huso", nov6(10), 4, AutoSwitch, 3},
		{"on huso with room: stay", nov6(10), 3, AutoNone, 0},
		{"huso five hours used up: tuna", nov6(95), 3, AutoSwitch, 1},
		{"huso reset: back from tuna", nov6(0), 1, AutoSwitch, 3},
	} {
		var first Decision
		for i, weekly := range []float64{0, 90} {
			policy := AutoPolicy{Threshold: 90, WeeklyThreshold: weekly, Cooldown: 5 * time.Minute, Prefer: StrategySoonestWeeklyReset}
			d := Decide(c.accounts, c.active, old, policy, now)
			if d.Action != c.action || d.To != c.to || d.ActiveWindow != "" {
				t.Errorf("%s (weekly %v): %+v", c.name, weekly, d)
			}
			if i == 0 {
				first = d
			} else if !reflect.DeepEqual(d, first) {
				t.Errorf("%s: weekly = threshold decided %+v, unset %+v", c.name, d, first)
			}
		}
	}

	// Reason texts stay byte-identical.
	two := &Accounts{Slots: []Slot{twoWindowSlot(now, 1, 95, time.Hour, 30, day), twoWindowSlot(now, 2, 20, time.Hour, 10, day)}}
	low := &Accounts{Slots: []Slot{twoWindowSlot(now, 1, 50, time.Hour, 30, day), twoWindowSlot(now, 2, 20, time.Hour, 10, day)}}
	for _, weekly := range []float64{0, 90} {
		policy := AutoPolicy{Threshold: 90, WeeklyThreshold: weekly}
		if d := Decide(two, 1, old, policy, now); d.Reason != "active account at 95% (threshold 90%); slot 2 at 20%" ||
			d.ActiveUsage() != "95%" || d.TargetUsage() != "20%" || d.ActiveWindow != "" || d.TargetWindow != "" {
			t.Errorf("weekly %v: %+v", weekly, d)
		}
		if d := Decide(low, 1, old, policy, now); d.Reason != "active account at 50%, below threshold 90%" {
			t.Errorf("weekly %v: %q", weekly, d.Reason)
		}
		policy.Limits = map[string]float64{"1": 40}
		if d := Decide(low, 1, old, policy, now); d.Reason != "active account at 50% (its 40% limit); slot 2 at 20%" {
			t.Errorf("weekly %v: %q", weekly, d.Reason)
		}
	}

	// Random accounts: room and decisions match the single-limit rule.
	rng := rand.New(rand.NewSource(7))
	window := func() *Window {
		if rng.Intn(5) == 0 {
			return nil
		}
		return &Window{Utilization: float64(rng.Intn(101)), ResetsAt: now.Add(time.Duration(rng.Intn(300)) * time.Minute)}
	}
	for run := 0; run < 500; run++ {
		accounts := &Accounts{}
		count := 2 + rng.Intn(4)
		for n := 1; n <= count; n++ {
			s := Slot{Number: n, AccountUUID: fmt.Sprint("a", n), LastUsage: &UsageCache{Usage: &Usage{FiveHour: window(), SevenDay: window()}}}
			if rng.Intn(3) == 0 {
				s.Role, s.Priority = RoleBackup, rng.Intn(3)
			}
			accounts.Slots = append(accounts.Slots, s)
		}
		limits := map[string]float64{"2": float64(30 + rng.Intn(70))}
		prefer := []string{"", StrategySoonestReset, StrategySoonestWeeklyReset}[rng.Intn(3)]
		order := []string{SwitchOrderAfterNormals, SwitchOrderInterleave}[rng.Intn(2)]
		state := AutoState{LastSwitchAt: now.Add(-time.Hour), UsedUp: map[string]time.Time{"1": now.Add(-time.Minute)}}
		base := AutoPolicy{Threshold: 90, Limits: limits, Prefer: prefer, BackupOrder: order, Cooldown: time.Minute}
		same := base
		same.WeeklyThreshold = 90
		for _, s := range accounts.Slots {
			max, known := slotUsage(s).Max(now)
			room, roomKnown := base.room(s, now)
			if known != roomKnown || known && room != base.LimitFor(s)-max {
				t.Fatalf("run %d slot %d: room %v/%v, want %v/%v", run, s.Number, room, roomKnown, base.LimitFor(s)-max, known)
			}
		}
		active := 1 + rng.Intn(len(accounts.Slots))
		a, b := Decide(accounts, active, state, base, now), Decide(accounts, active, state, same, now)
		if !reflect.DeepEqual(a, b) || a.ActiveWindow != "" {
			t.Fatalf("run %d: unset %+v, equal %+v", run, a, b)
		}
		if a.Action == AutoSwitch && !reflect.DeepEqual(candidatesFor(accounts, a, base, now), candidatesFor(accounts, b, same, now)) {
			t.Fatalf("run %d: candidates differ", run)
		}
	}
}

// Soonest-weekly-reset returns to the account whose week resets soonest while
// its seven-day usage is under the weekly threshold.
func TestWeeklyThresholdSoonestWeeklyResetReturns(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	old := AutoState{LastSwitchAt: now.Add(-time.Hour)}
	day := 24 * time.Hour
	accounts := &Accounts{Slots: []Slot{
		twoWindowSlot(now, 1, 0, time.Hour, 58, 2*day+22*time.Hour),
		twoWindowSlot(now, 2, 0, 2*time.Hour, 0, 6*day+20*time.Hour),
		twoWindowSlot(now, 3, 0, 3*time.Hour, 92, day+4*time.Hour),
	}}
	policy := AutoPolicy{Threshold: 90, WeeklyThreshold: 95, Cooldown: 5 * time.Minute, Prefer: StrategySoonestWeeklyReset}
	if d := Decide(accounts, 1, old, policy, now); d.Action != AutoSwitch || d.To != 3 {
		t.Fatalf("should return to slot 3 at 7d 92%% under weekly 95%%: %+v", d)
	}
	policy.WeeklyThreshold = 0
	if d := Decide(accounts, 1, old, policy, now); d.Action != AutoNone {
		t.Fatalf("without weekly threshold slot 3 is used up and slot 1 stays: %+v", d)
	}
}

func TestWeeklyThresholdWeeklyActiveTarget(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	build := func(week float64, weekly float64) KeepAliveResult {
		accounts := weekAccounts(now,
			weekCase{week: 50, weekIn: 3 * testDay},
			weekCase{week: week, weekIn: testDay},
		)
		opts := weeklyActive
		opts.Policy.WeeklyThreshold = weekly
		return buildKeepAlive(accounts, KeepAliveState{}, now, opts)
	}
	if result := build(92, 95); result.Target != 2 {
		t.Fatalf("7d 92%% under weekly 95%%: target %d, want 2", result.Target)
	}
	result := build(95, 95)
	if result.Target != 1 {
		t.Fatalf("7d 95%% at weekly 95%%: target %d, want 1", result.Target)
	}
	if v := weekView(t, result, 2); v.Skipped != "not the weekly-active account; seven-day usage 95% is at its 95% limit" {
		t.Fatalf("skip reason %q", v.Skipped)
	}
	result = build(92, 0)
	if result.Target != 1 {
		t.Fatalf("7d 92%% without weekly threshold: target %d, want 1", result.Target)
	}
	if v := weekView(t, result, 2); v.Skipped != "not the weekly-active account; seven-day usage 92% is at its 90% limit" {
		t.Fatalf("skip reason %q", v.Skipped)
	}
	// A full five-hour window is left out of the weekly-active choice either way.
	accounts := weekAccounts(now, weekCase{week: 50, weekIn: 3 * testDay}, weekCase{week: 92, weekIn: testDay})
	accounts.Slot(2).LastUsage.Usage.FiveHour = &Window{Utilization: 100, ResetsAt: now.Add(time.Hour)}
	opts := weeklyActive
	opts.Policy.WeeklyThreshold = 95
	if result := buildKeepAlive(accounts, KeepAliveState{}, now, opts); result.Target != 2 {
		t.Fatalf("full five-hour window: target %d, want 2", result.Target)
	}
}

func TestWeeklyThresholdBackupOrders(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	old := AutoState{LastSwitchAt: now.Add(-time.Hour)}
	normal := func(n int, five, week float64) Slot { return twoWindowSlot(now, n, five, time.Hour, week, 3*testDay) }
	backup := func(n int, five, week float64, priority int) Slot {
		s := twoWindowSlot(now, n, five, 2*time.Hour, week, 4*testDay)
		s.Role, s.Priority = RoleBackup, priority
		return s
	}
	for _, order := range []string{SwitchOrderAfterNormals, SwitchOrderInterleave} {
		for _, prefer := range []string{"", StrategySoonestReset} {
			split := AutoPolicy{Threshold: 90, WeeklyThreshold: 95, Cooldown: 5 * time.Minute, Prefer: prefer, BackupOrder: order}
			single := split
			single.WeeklyThreshold = 0
			name := fmt.Sprintf("%s/%q", order, prefer)

			// Active normal at 7d 92%: it has room under weekly 95%.
			onNormal := &Accounts{Slots: []Slot{normal(1, 50, 92), normal(2, 0, 10), backup(3, 0, 0, 1)}}
			if d := Decide(onNormal, 1, old, split, now); d.Action != AutoNone {
				t.Errorf("%s: normal at 7d 92%% must stay: %+v", name, d)
			}
			want := 2
			if order == SwitchOrderInterleave {
				want = 3
			}
			if d := Decide(onNormal, 1, old, single, now); d.Action != AutoSwitch || d.To != want {
				t.Errorf("%s: without weekly threshold want slot %d: %+v", name, want, d)
			}

			// On a backup, the only normal is at 7d 92% after its five hours
			// were used up and reset: it has room again.
			state := AutoState{LastSwitchAt: now.Add(-time.Hour), UsedUp: map[string]time.Time{"1": now.Add(-time.Minute)}}
			onBackup := &Accounts{Slots: []Slot{normal(1, 0, 92), backup(3, 30, 20, 1)}}
			if d := Decide(onBackup, 3, state, split, now); d.Action != AutoSwitch || d.To != 1 ||
				d.Reason != "on backup slot 3; a normal account has room again; slot 1 at seven-day 92%" {
				t.Errorf("%s: back to the normal at 7d 92%%: %+v", name, d)
			}
			if d := Decide(onBackup, 3, state, single, now); d.Action != AutoNone {
				t.Errorf("%s: without weekly threshold the normal is used up, stay on the backup: %+v", name, d)
			}
		}
	}
	// recordUsedUp follows the per-window limits.
	accounts := &Accounts{Slots: []Slot{normal(1, 50, 92), backup(3, 0, 0, 1)}}
	var state AutoState
	if recordUsedUp(&state, accounts, 1, AutoPolicy{Threshold: 90, WeeklyThreshold: 95}, now) {
		t.Fatalf("7d 92%% under weekly 95%% recorded as used up: %+v", state)
	}
	if !recordUsedUp(&state, accounts, 1, AutoPolicy{Threshold: 90}, now) {
		t.Fatal("7d 92% over threshold 90% not recorded as used up")
	}
}
