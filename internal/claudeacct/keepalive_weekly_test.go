package claudeacct

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// weekCase is one account's usage in the weekly keepalive tests.
type weekCase struct {
	// five is how long the running five-hour window has left; 0 is idle.
	five time.Duration
	// week is the seven-day utilization and weekIn how long its window has
	// left: 0 is a window with no reset (not running), negative one whose
	// reset has passed. noWeek reports no seven-day window at all.
	week   float64
	weekIn time.Duration
	noWeek bool
	backup bool
	// priority orders backups.
	priority int
}

func (c weekCase) usage(now time.Time) *Usage {
	u := &Usage{FiveHour: &Window{}}
	if c.five > 0 {
		u.FiveHour = &Window{Utilization: 20, ResetsAt: now.Add(c.five)}
	}
	if !c.noWeek {
		u.SevenDay = &Window{Utilization: c.week}
		if c.weekIn != 0 {
			u.SevenDay.ResetsAt = now.Add(c.weekIn)
		}
	}
	return u
}

func usageBody(u *Usage) string {
	window := func(w *Window) string {
		if w == nil {
			return "null"
		}
		if w.ResetsAt.IsZero() {
			return fmt.Sprintf(`{"utilization":%g,"resets_at":null}`, w.Utilization)
		}
		return fmt.Sprintf(`{"utilization":%g,"resets_at":%q}`, w.Utilization, w.ResetsAt.Format(time.RFC3339))
	}
	return fmt.Sprintf(`{"five_hour":%s,"seven_day":%s}`, window(u.FiveHour), window(u.SevenDay))
}

const testDay = 24 * time.Hour

// weekAccounts is an in-memory store for the pure plan tests.
func weekAccounts(now time.Time, cases ...weekCase) *Accounts {
	accounts := &Accounts{}
	for i, c := range cases {
		s := Slot{Number: i + 1, AccountUUID: fmt.Sprint("a", i+1), Email: fmt.Sprintf("u%d@example.com", i+1), Priority: c.priority}
		if c.backup {
			s.Role = RoleBackup
		}
		s.LastUsage = &UsageCache{Usage: c.usage(now), FetchedAt: now, AttemptAt: now}
		accounts.Slots = append(accounts.Slots, s)
	}
	return accounts
}

func weekView(t *testing.T, result KeepAliveResult, n int) KeepAliveSlotView {
	t.Helper()
	for _, v := range result.Slots {
		if v.Slot == n {
			return v
		}
	}
	t.Fatalf("slot %d not in result %+v", n, result.Slots)
	return KeepAliveSlotView{}
}

func dueSlots(result KeepAliveResult) []int {
	var out []int
	for _, v := range result.Slots {
		if v.Due && v.Skipped == "" {
			out = append(out, v.Slot)
		}
	}
	return out
}

var weeklyActive = KeepAliveOptions{Scope: KeepAliveScopeWeeklyActive, Policy: AutoPolicy{Threshold: 90}}

func TestWeeklyActiveTargetIsSoonestWeeklyResetWithRoom(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	accounts := weekAccounts(now,
		weekCase{week: 50, weekIn: 3 * testDay},
		weekCase{week: 10, weekIn: testDay},
		weekCase{week: 95, weekIn: 12 * time.Hour}, // resets first but is over its limit
		weekCase{week: 0, weekIn: 6 * testDay},
	)
	result := buildKeepAlive(accounts, KeepAliveState{}, now, weeklyActive)
	if result.Scope != KeepAliveScopeWeeklyActive || result.Target != 2 {
		t.Fatalf("scope %q target %d, want slot 2", result.Scope, result.Target)
	}
	// Due at once with three other idle accounts: no grid, no stagger wait.
	if due := dueSlots(result); len(due) != 1 || due[0] != 2 {
		t.Fatalf("due %v, want only the target", due)
	}
	target := weekView(t, result, 2)
	if !target.NextStart.Equal(now) || target.DueReason != KeepAliveDueFiveHour || result.Step != 0 || !result.Anchor.IsZero() {
		t.Fatalf("target %+v step %v anchor %v", target, result.Step, result.Anchor)
	}
	for _, n := range []int{1, 3, 4} {
		if v := weekView(t, result, n); v.Due || !strings.HasPrefix(v.Skipped, "not the weekly-active account") {
			t.Fatalf("slot %d view %+v", n, v)
		}
	}
	if v := weekView(t, result, 3); !strings.Contains(v.Skipped, "95% is at its 90% limit") {
		t.Fatalf("over-limit reason missing: %q", v.Skipped)
	}

	// An account's own limit puts it over: the next weekly reset takes over.
	opts := weeklyActive
	opts.Policy.Limits = map[string]float64{"2": 5}
	if result := buildKeepAlive(accounts, KeepAliveState{}, now, opts); result.Target != 1 {
		t.Fatalf("target %d with slot 2 over its own limit, want 1", result.Target)
	}

	// Five-hour state is left out: a full five-hour window keeps the target.
	accounts.Slot(2).LastUsage.Usage.FiveHour = &Window{Utilization: 100, ResetsAt: now.Add(time.Hour)}
	result = buildKeepAlive(accounts, KeepAliveState{}, now, weeklyActive)
	if v := weekView(t, result, 2); result.Target != 2 || v.Due || !v.NextStart.Equal(now.Add(time.Hour)) {
		t.Fatalf("running target: target %d view %+v", result.Target, v)
	}
	if len(dueSlots(result)) != 0 {
		t.Fatalf("pinged around a running target: %v", dueSlots(result))
	}
}

func TestWeeklyActiveNormalsBeforeBackups(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []weekCase{
		{week: 10, weekIn: 12 * time.Hour, backup: true, priority: 2},
		{week: 10, weekIn: 2 * testDay},
		{week: 10, weekIn: 5 * time.Hour, backup: true, priority: 1},
		{week: 10, weekIn: 4 * testDay},
	}
	if got := buildKeepAlive(weekAccounts(now, cases...), KeepAliveState{}, now, weeklyActive).Target; got != 2 {
		t.Fatalf("target %d, want the normal slot 2 before sooner backups", got)
	}
	// Every normal account is over its limit: the first backup by priority.
	cases[1].week, cases[3].week = 95, 99
	if got := buildKeepAlive(weekAccounts(now, cases...), KeepAliveState{}, now, weeklyActive).Target; got != 3 {
		t.Fatalf("target %d, want backup slot 3 (priority 1)", got)
	}
	// With backups out of keepalive no backup is the target.
	opts := weeklyActive
	opts.SkipBackups = true
	result := buildKeepAlive(weekAccounts(now, cases...), KeepAliveState{}, now, opts)
	if result.Target != 0 || len(dueSlots(result)) != 0 {
		t.Fatalf("target %d due %v with backups skipped", result.Target, dueSlots(result))
	}
	if v := weekView(t, result, 1); !strings.Contains(v.Skipped, "keepalive for backups is off") {
		t.Fatalf("backup reason %q", v.Skipped)
	}
}

func TestWeeklyActiveHoldsTarget(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	accounts := weekAccounts(now, weekCase{week: 10, weekIn: testDay}, weekCase{week: 10, weekIn: 2 * testDay})
	state := KeepAliveState{}
	state.slot(1).HoldUntil = now.Add(5 * time.Minute)
	result := buildKeepAlive(accounts, state, now, weeklyActive)
	if v := weekView(t, result, 1); !v.Due || !strings.HasPrefix(v.Skipped, "held until") {
		t.Fatalf("held target %+v", v)
	}
	if len(dueSlots(result)) != 0 {
		t.Fatalf("held pass has pingable slots %v", dueSlots(result))
	}
}

// weekFixture stores the cases as slots 1..n (n live) with the same usage
// cached and served; a ping starts a five-hour window and, when none runs, a
// seven-day one.
func weekFixture(t *testing.T, cases ...weekCase) (*fixture, *pingRecorder) {
	t.Helper()
	f := newFixture(t, false)
	f.addAccounts(len(cases))
	accounts, err := f.m.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		n := i + 1
		slot := accounts.Slot(n)
		if c.backup {
			slot.Role, slot.Priority = RoleBackup, c.priority
		}
		u := c.usage(f.now)
		slot.LastUsage = &UsageCache{Usage: u, FetchedAt: f.now, AttemptAt: f.now}
		f.serve(n, u)
	}
	if err := f.m.Store.Save(accounts); err != nil {
		t.Fatal(err)
	}
	rec := &pingRecorder{fail: map[string]error{}}
	rec.after = func(token string) {
		for n := 1; n <= len(cases); n++ {
			if token != f.account(n).access {
				continue
			}
			u := f.served(n)
			u.FiveHour = &Window{Utilization: 1, ResetsAt: f.now.Add(KeepAlivePeriod)}
			if u.SevenDay == nil || !u.SevenDay.ResetsAt.After(f.now) {
				u.SevenDay = &Window{Utilization: 1, ResetsAt: f.now.Add(keepAliveWeek)}
			}
			f.serve(n, u)
		}
	}
	f.m.Pinger = rec.ping
	return f, rec
}

func (f *fixture) serve(n int, u *Usage) {
	f.api.mu.Lock()
	defer f.api.mu.Unlock()
	f.api.usage[f.account(n).access] = usageBody(u)
}

func (f *fixture) served(n int) *Usage {
	f.api.mu.Lock()
	defer f.api.mu.Unlock()
	u, err := ParseUsage([]byte(f.api.usage[f.account(n).access]))
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

// pingedSlots maps recorded ping tokens back to slots.
func (f *fixture) pingedSlots(rec *pingRecorder, count int) []int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []int
	for _, token := range rec.tokens {
		for n := 1; n <= count; n++ {
			if token == f.account(n).access {
				out = append(out, n)
			}
		}
	}
	return out
}

func (f *fixture) usageCallsFor(n int) int {
	_, usage := f.api.calls()
	count := 0
	for _, token := range usage {
		if token == f.account(n).access {
			count++
		}
	}
	return count
}

func TestWeeklyActivePingsOnlyTargetAndRefetchesOnlyForTheChoice(t *testing.T) {
	f, rec := weekFixture(t,
		weekCase{week: 50, weekIn: 3 * testDay},
		weekCase{week: 10, weekIn: testDay},
		weekCase{noWeek: true},
		weekCase{week: 0, weekIn: 6 * testDay},
	)
	// All cached usage is ten minutes old: due for keepAliveFresh, and for
	// ListMaxAge.
	f.now = f.now.Add(10 * time.Minute)
	result, err := f.m.KeepAliveOnce(testCtx(t), weeklyActive)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.pingedSlots(rec, 4); len(got) != 1 || got[0] != 2 {
		t.Fatalf("pinged %v, want only the weekly-active slot 2", got)
	}
	if v := weekView(t, result, 2); !v.Pinged || !v.Started {
		t.Fatalf("target view %+v", v)
	}
	// The target is refetched before its ping (and once after it), slot 3
	// because its seven-day window is unknown; 1 and 4 have known weeks.
	if f.usageCallsFor(1) != 0 || f.usageCallsFor(4) != 0 || f.usageCallsFor(3) != 1 || f.usageCallsFor(2) != 2 {
		t.Fatalf("usage calls 1:%d 2:%d 3:%d 4:%d", f.usageCallsFor(1), f.usageCallsFor(2), f.usageCallsFor(3), f.usageCallsFor(4))
	}
	if v := weekView(t, result, 3); v.Due || v.Skipped != "not the weekly-active account; seven-day window unknown" {
		t.Fatalf("unknown-week view %+v", v)
	}
	// The all-scope grid anchor is left alone.
	if state, _ := f.m.Store.LoadKeepAliveState(); !state.Anchor.IsZero() {
		t.Fatalf("weekly-active pass moved the grid anchor: %v", state.Anchor)
	}
}

func TestWeeklyActiveNoQualifyingAccountPingsNothing(t *testing.T) {
	f, rec := weekFixture(t, weekCase{week: 95, weekIn: testDay}, weekCase{week: 100, weekIn: 2 * testDay})
	result, err := f.m.KeepAliveOnce(testCtx(t), weeklyActive)
	if err != nil {
		t.Fatal(err)
	}
	if result.Target != 0 || len(rec.tokens) != 0 {
		t.Fatalf("target %d pinged %d", result.Target, len(rec.tokens))
	}
}

func TestKeepAliveUnknownScopeRefused(t *testing.T) {
	f, _ := weekFixture(t, weekCase{week: 10, weekIn: testDay})
	if _, err := f.m.KeepAliveOnce(testCtx(t), KeepAliveOptions{Scope: "weekly"}); err == nil || !strings.Contains(err.Error(), "unknown keepalive scope") {
		t.Fatalf("err %v", err)
	}
}

func TestWeeklyPingDueWhenSevenDayNotRunning(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []weekCase{
		{five: time.Hour, week: 10, weekIn: -time.Minute},    // reset passed
		{five: 2 * time.Hour, week: 0},                       // no reset: not running
		{five: 3 * time.Hour, week: 30, weekIn: 2 * testDay}, // running
		{five: 4 * time.Hour, noWeek: true},                  // unknown
	}
	for _, scope := range []string{KeepAliveScopeAll, KeepAliveScopeWeeklyActive} {
		opts := KeepAliveOptions{Scope: scope, Policy: AutoPolicy{Threshold: 90}, Weekly: true}
		result := buildKeepAlive(weekAccounts(now, cases...), KeepAliveState{}, now, opts)
		if !result.Weekly {
			t.Fatalf("%s: result does not report weekly pings", scope)
		}
		for _, n := range []int{1, 2} {
			if v := weekView(t, result, n); !v.Due || v.Skipped != "" || v.DueReason != KeepAliveDueWeekly || v.WeeklyActive {
				t.Fatalf("%s: slot %d view %+v", scope, n, v)
			}
		}
		if v := weekView(t, result, 3); v.Due || !v.WeeklyActive || !v.WeeklyResetsAt.Equal(now.Add(2*testDay)) {
			t.Fatalf("%s: running week %+v", scope, v)
		}
		if v := weekView(t, result, 4); v.Due || v.WeeklyKnown {
			t.Fatalf("%s: unknown week %+v", scope, v)
		}
		// Without the flag nothing is due: every five-hour window runs.
		opts.Weekly = false
		if due := dueSlots(buildKeepAlive(weekAccounts(now, cases...), KeepAliveState{}, now, opts)); len(due) != 0 {
			t.Fatalf("%s: due %v without weekly pings", scope, due)
		}
	}
	// Backups out of keepalive get no weekly ping either.
	backup := cases[0]
	backup.backup = true
	result := buildKeepAlive(weekAccounts(now, backup), KeepAliveState{}, now, KeepAliveOptions{Weekly: true, SkipBackups: true})
	if v := weekView(t, result, 1); v.Due || !strings.Contains(v.Skipped, "backups is off") {
		t.Fatalf("skipped backup %+v", v)
	}
}

func TestWeeklyPingUnknownWeekRefetchedNotOnDryRun(t *testing.T) {
	f, rec := weekFixture(t, weekCase{five: time.Hour, noWeek: true}, weekCase{five: 2 * time.Hour, week: 10, weekIn: testDay})
	// The endpoint now reports slot 1's seven-day window, not running.
	f.serve(1, weekCase{five: time.Hour, week: 0}.usage(f.now))
	f.now = f.now.Add(10 * time.Minute)
	opts := KeepAliveOptions{Weekly: true, DryRun: true}
	result, err := f.m.KeepAliveOnce(testCtx(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, usage := f.api.calls(); len(usage) != 0 || len(rec.tokens) != 0 {
		t.Fatalf("dry run fetched %d usages, pinged %d", len(usage), len(rec.tokens))
	}
	if v := weekView(t, result, 1); v.Due || v.WeeklyKnown {
		t.Fatalf("dry-run view of an unknown week %+v", v)
	}
	opts.DryRun = false
	result, err = f.m.KeepAliveOnce(testCtx(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.pingedSlots(rec, 2); len(got) != 1 || got[0] != 1 {
		t.Fatalf("pinged %v, want slot 1 once its week is known", got)
	}
	if v := weekView(t, result, 1); !v.Started || v.DueReason != KeepAliveDueWeekly || !v.WeeklyActive {
		t.Fatalf("weekly ping view %+v", v)
	}
	if f.usageCallsFor(2) != 0 {
		t.Fatalf("refetched slot 2 whose week is known: %d", f.usageCallsFor(2))
	}
}

func TestWeeklyPingUnconfirmedBacksOffHourly(t *testing.T) {
	f, rec := weekFixture(t,
		weekCase{five: time.Hour, week: 10, weekIn: -time.Hour},   // week expired, no five-hour pings
		weekCase{five: 40 * time.Hour, week: 10, weekIn: testDay}, // the weekly-active target
	)
	// The endpoint never shows a new seven-day window.
	rec.after = nil
	opts := weeklyActive
	opts.Weekly = true
	pings := func() int { return len(f.pingedSlots(rec, 1)) }
	pass := func(step time.Duration) {
		t.Helper()
		f.now = f.now.Add(step)
		if _, err := f.m.KeepAliveOnce(testCtx(t), opts); err != nil {
			t.Fatal(err)
		}
	}
	pass(0)
	state, _ := f.m.Store.LoadKeepAliveState()
	if entry := state.peek(1); pings() != 1 || !entry.Weekly || !entry.Unconfirmed || !entry.HoldUntil.Equal(f.now.Add(keepAliveSettle)) {
		t.Fatalf("after the ping: %d pings, entry %+v", pings(), entry)
	}
	pass(time.Minute)
	pass(9 * time.Minute)
	// Past the settle hold the window still does not show: a weekly failure,
	// held for an hour, not the five-hour retry.
	state, _ = f.m.Store.LoadKeepAliveState()
	entry := state.peek(1)
	if pings() != 1 || entry.WeeklyFailures != 1 || entry.Failures != 0 || !strings.Contains(entry.Error, "no seven-day window started") || !entry.WeeklyHoldUntil.Equal(f.now.Add(keepAliveWeeklyRetry)) {
		t.Fatalf("after settle: %d pings, entry %+v", pings(), entry)
	}
	pass(keepAliveRetry)
	pass(keepAliveWeeklyRetry - keepAliveRetry - time.Minute)
	if pings() != 1 {
		t.Fatalf("weekly ping retried within the hour: %d pings", pings())
	}
	pass(time.Minute)
	if pings() != 2 {
		t.Fatalf("not retried after an hour: %d pings", pings())
	}
	// The second failure doubles the wait.
	pass(keepAliveSettle)
	state, _ = f.m.Store.LoadKeepAliveState()
	if entry := state.peek(1); entry.WeeklyFailures != 2 || !entry.WeeklyHoldUntil.Equal(f.now.Add(2*keepAliveWeeklyRetry)) {
		t.Fatalf("second failure %+v", entry)
	}
	pass(2*keepAliveWeeklyRetry - time.Minute)
	if pings() != 2 {
		t.Fatalf("retried within two hours: %d pings", pings())
	}
	pass(time.Minute)
	if pings() != 3 {
		t.Fatalf("not retried after two hours: %d pings", pings())
	}

	// A window that shows late confirms the ping and ends the backoff.
	f.serve(1, &Usage{FiveHour: &Window{Utilization: 1, ResetsAt: f.now.Add(KeepAlivePeriod)}, SevenDay: &Window{Utilization: 1, ResetsAt: f.now.Add(keepAliveWeek - 30*time.Minute)}})
	pass(keepAliveSettle)
	state, _ = f.m.Store.LoadKeepAliveState()
	if entry := state.peek(1); pings() != 3 || entry.WeeklyFailures != 0 || !entry.WeeklyHoldUntil.IsZero() || entry.Error != "" || entry.Unconfirmed {
		t.Fatalf("late window: %d pings, entry %+v", pings(), entry)
	}

	// The backoff doubles up to a day.
	var k KeepAliveSlot
	now := f.now
	want := []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 16 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	for i, w := range want {
		k.weeklyFailed(now, "x")
		if got := k.WeeklyHoldUntil.Sub(now); got != w {
			t.Fatalf("weekly failure %d waits %v, want %v", i+1, got, w)
		}
	}
	for i := 0; i < 100; i++ {
		k.weeklyFailed(now, "x")
	}
	if got := k.WeeklyHoldUntil.Sub(now); got != keepAliveWeeklyMaxGap {
		t.Fatalf("weekly backoff not capped: %v", got)
	}
}

func TestWeeklyPingSendFailureRetriesInMinutes(t *testing.T) {
	f, rec := weekFixture(t, weekCase{five: time.Hour, week: 10, weekIn: -time.Hour}, weekCase{five: 2 * time.Hour, week: 10, weekIn: testDay})
	rec.fail[f.account(1).access] = fmt.Errorf("claude failed: exit status 1")
	opts := KeepAliveOptions{Weekly: true}
	if _, err := f.m.KeepAliveOnce(testCtx(t), opts); err != nil {
		t.Fatal(err)
	}
	state, _ := f.m.Store.LoadKeepAliveState()
	if entry := state.peek(1); len(rec.tokens) != 1 || entry.Failures != 1 || entry.WeeklyFailures != 0 || !entry.WeeklyHoldUntil.IsZero() || !entry.HoldUntil.Equal(f.now.Add(keepAliveRetry)) {
		t.Fatalf("send failure: %d pings, entry %+v", len(rec.tokens), entry)
	}
	f.now = f.now.Add(keepAliveRetry)
	if _, err := f.m.KeepAliveOnce(testCtx(t), opts); err != nil {
		t.Fatal(err)
	}
	if len(rec.tokens) != 2 {
		t.Fatalf("not retried after %v: %d pings", keepAliveRetry, len(rec.tokens))
	}
}

// A weekly backoff holds weekly pings only: the target's five-hour ping goes
// when its window runs out, and is recorded as a five-hour ping.
func TestWeeklyHoldDoesNotBlockTargetFiveHourPing(t *testing.T) {
	f, rec := weekFixture(t,
		weekCase{week: 10, weekIn: -time.Hour},                        // target: idle, week expired
		weekCase{five: 40 * time.Hour, week: 95, weekIn: 2 * testDay}, // over its limit
		weekCase{five: 40 * time.Hour, week: 20, weekIn: -time.Hour},  // non-target, week expired
	)
	rec.after = nil
	hold := f.now.Add(5 * time.Hour)
	state := KeepAliveState{}
	for _, n := range []int{1, 3} {
		entry := state.slot(n)
		entry.LastPingAt, entry.HoldUntil = f.now.Add(-time.Hour), f.now.Add(-50*time.Minute)
		entry.Error, entry.WeeklyFailures, entry.WeeklyHoldUntil = "the prompt was answered but no seven-day window started", 3, hold
	}
	if err := f.m.Store.SaveKeepAliveState(state); err != nil {
		t.Fatal(err)
	}
	opts := weeklyActive
	opts.Weekly = true
	result, err := f.m.KeepAliveOnce(testCtx(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.pingedSlots(rec, 3); result.Target != 1 || len(got) != 1 || got[0] != 1 {
		t.Fatalf("target %d pinged %v, want only the target's five-hour ping", result.Target, got)
	}
	if v := weekView(t, result, 1); !v.Pinged || v.DueReason != KeepAliveDueFiveHour {
		t.Fatalf("target view %+v", v)
	}
	if v := weekView(t, result, 3); !v.Due || v.Skipped != "held until "+hold.Format(time.RFC3339) || v.DueReason != KeepAliveDueWeekly {
		t.Fatalf("held non-target view %+v", v)
	}
	state, _ = f.m.Store.LoadKeepAliveState()
	if entry := state.peek(1); entry.Weekly || entry.WeeklyFailures != 3 || !entry.WeeklyHoldUntil.Equal(hold) || !entry.LastPingAt.Equal(f.now) {
		t.Fatalf("target entry after its five-hour ping %+v", entry)
	}
}

// keepalive-state.json written before the weekly fields loads and saves
// back byte for byte.
func TestKeepAliveStateFromBeforeWeeklyFieldsUnchanged(t *testing.T) {
	f, _ := weekFixture(t, weekCase{week: 10, weekIn: testDay})
	old := `{
  "slots": {
    "1": {
      "lastPingAt": "2030-01-02T03:04:05Z",
      "error": "claude failed: exit status 1",
      "failures": 2,
      "holdUntil": "2030-01-02T03:08:05Z",
      "unconfirmed": true
    },
    "2": {
      "lastPingAt": "2030-01-02T01:04:05Z",
      "holdUntil": "2030-01-02T01:14:05Z"
    }
  },
  "anchor": "2030-01-02T03:04:05Z"
}
`
	path := filepath.Join(f.m.Store.Root(), "keepalive-state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := f.m.Store.LoadKeepAliveState()
	if err != nil {
		t.Fatal(err)
	}
	if entry := state.peek(1); entry.Failures != 2 || !entry.Unconfirmed || entry.Weekly || entry.WeeklyFailures != 0 || !entry.WeeklyHoldUntil.IsZero() {
		t.Fatalf("loaded %+v", entry)
	}
	if err := f.m.Store.SaveKeepAliveState(state); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != old {
		t.Fatalf("state file changed on a round trip:\n%s", data)
	}
}

// The owner's setup: five-hour pings for the weekly-active account only,
// weekly pings for everyone.
func TestWeeklyActiveWithWeeklyPings(t *testing.T) {
	f, rec := weekFixture(t,
		weekCase{week: 20, weekIn: testDay, five: 0},                  // target, idle
		weekCase{five: 30 * time.Minute, week: 5, weekIn: -time.Hour}, // week expired
		weekCase{week: 40, weekIn: 3 * testDay},                       // week running, idle five hours
	)
	opts := weeklyActive
	opts.Weekly = true
	result, err := f.m.KeepAliveOnce(testCtx(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.pingedSlots(rec, 3); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("pinged %v, want the target and the expired week", got)
	}
	if v := weekView(t, result, 1); v.DueReason != KeepAliveDueFiveHour || !v.Started {
		t.Fatalf("target %+v", v)
	}
	if v := weekView(t, result, 2); v.DueReason != KeepAliveDueWeekly || !v.Started {
		t.Fatalf("weekly ping %+v", v)
	}
	if v := weekView(t, result, 3); v.Due || v.Skipped != "not the weekly-active account" {
		t.Fatalf("running week %+v", v)
	}
	// Slot 2's five-hour window ends: it is not pinged again (its week runs),
	// nor is slot 3; the target's window still runs.
	f.now = f.now.Add(31 * time.Minute)
	if _, err := f.m.KeepAliveOnce(testCtx(t), opts); err != nil {
		t.Fatal(err)
	}
	if got := f.pingedSlots(rec, 3); len(got) != 2 {
		t.Fatalf("pinged %v after the first pass", got)
	}
}
