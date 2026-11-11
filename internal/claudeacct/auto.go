package claudeacct

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AutoHysteresis is how many points better than the active account a target
// must be, so two nearly equal accounts do not flap.
const AutoHysteresis = 10.0

// SoonestResetHysteresis is how much sooner another account's five-hour window
// must reset before proactive soonest-reset switching leaves an active account
// that still has room, so two accounts whose windows reset at nearly the same
// time do not flap.
const SoonestResetHysteresis = 2 * time.Minute

// Auto decision outcomes.
const (
	AutoNone     = "none"
	AutoSwitch   = "switch"
	AutoNoTarget = "no-target"
)

// soonest reports whether the policy drains accounts in reset order.
func (p AutoPolicy) soonest() bool {
	return p.Prefer == StrategySoonestReset || p.Prefer == StrategySoonestWeeklyReset
}

// resetOf is the reset a soonest policy orders by: the seven-day window for
// StrategySoonestWeeklyReset, otherwise the five-hour one.
func (p AutoPolicy) resetOf(s Slot, now time.Time) time.Time {
	if p.Prefer == StrategySoonestWeeklyReset {
		return slotUsage(s).SevenDayResetAt(now)
	}
	return slotUsage(s).FiveHourResetAt(now)
}

// AutoPolicy is the auto-switch configuration.
type AutoPolicy struct {
	// Threshold is the utilization at which an account counts as used up
	// (100 when unset). Without WeeklyThreshold it applies to both windows;
	// with it, to the five-hour window only.
	Threshold float64
	// WeeklyThreshold is the threshold of the seven-day window; unset (0)
	// means Threshold. It may be higher or lower than Threshold.
	WeeklyThreshold float64
	Cooldown        time.Duration
	// Limits caps single accounts below the thresholds, on both windows. Keys
	// are a slot number, an alias or an email; values are utilization
	// percentages.
	Limits map[string]float64
	// Prefer orders the eligible targets. StrategySoonestReset picks the
	// account whose five-hour window resets soonest; any other value (the
	// default) picks the account with the most room left under its limit.
	Prefer string
	// BackupOrder is when backup accounts are used: SwitchOrderAfterNormals
	// (the default) or SwitchOrderInterleave. See decideWithRoles.
	BackupOrder string
	// sevenDayOnly measures room on the seven-day window alone, leaving the
	// five-hour window out. Auto switching never sets it; keepalive's
	// weekly-active scope does, to find the account the weekly policy is draining
	// whatever its five-hour state (see weeklyTarget).
	sevenDayOnly bool
}

// windowUse is a slot's utilization on the window that decides its room: of
// its known windows, the one with the least room under its own limit.
type windowUse struct {
	value float64
	limit float64
	// sevenDay is set when the seven-day window decided.
	sevenDay bool
	// split is set when the slot's five-hour and seven-day limits differ, so
	// texts must name the window.
	split bool
	// threshold is the policy threshold of the deciding window, before the
	// slot's own limit lowered it.
	threshold float64
}

func (w windowUse) room() float64 { return w.limit - w.value }

// use measures a slot: each known window (with sevenDayOnly the seven-day
// window alone) against its own limit, keeping the one with the least room;
// on a tie the five-hour window. ok is false when no measured window is
// known. With equal limits on both windows this is the higher window, as
// Usage.Max.
func (p AutoPolicy) use(s Slot, now time.Time) (w windowUse, ok bool) {
	u := slotUsage(s)
	if u == nil {
		return windowUse{}, false
	}
	five, seven := p.LimitFor(s), p.WeeklyLimitFor(s)
	split := five != seven
	if u.FiveHour != nil && !p.sevenDayOnly {
		w, ok = windowUse{value: u.FiveHour.effective(now), limit: five, split: split, threshold: p.threshold()}, true
	}
	if u.SevenDay != nil {
		weekly := windowUse{value: u.SevenDay.effective(now), limit: seven, sevenDay: true, split: split, threshold: p.weeklyThreshold()}
		if !ok || weekly.room() < w.room() {
			w, ok = weekly, true
		}
	}
	return w, ok
}

// room is how many points the slot has left before its tightest window
// reaches that window's limit (negative when over it); known is false when
// no measured window is known.
func (p AutoPolicy) room(s Slot, now time.Time) (room float64, known bool) {
	w, ok := p.use(s, now)
	return w.room(), ok
}

// usedUp reports whether any known window of the slot is at or over its own
// limit.
func (p AutoPolicy) usedUp(s Slot, now time.Time) bool {
	room, known := p.room(s, now)
	return known && room <= 0
}

// LimitFor is the utilization at which a slot's five-hour window counts as
// used up: the threshold (100 when unset), lowered by the slot's own limit.
// Without WeeklyThreshold it is the seven-day limit too.
func (p AutoPolicy) LimitFor(s Slot) float64 {
	return p.lowered(p.threshold(), s)
}

// WeeklyLimitFor is the utilization at which a slot's seven-day window counts
// as used up: the weekly threshold (the threshold when unset), lowered by the
// slot's own limit.
func (p AutoPolicy) WeeklyLimitFor(s Slot) float64 {
	return p.lowered(p.weeklyThreshold(), s)
}

func (p AutoPolicy) lowered(limit float64, s Slot) float64 {
	if own, ok := SlotLimit(p.Limits, s); ok && own < limit {
		limit = own
	}
	return limit
}

func (p AutoPolicy) threshold() float64 {
	if p.Threshold <= 0 || p.Threshold > 100 {
		return 100
	}
	return p.Threshold
}

func (p AutoPolicy) weeklyThreshold() float64 {
	if p.WeeklyThreshold <= 0 || p.WeeklyThreshold > 100 {
		return p.threshold()
	}
	return p.WeeklyThreshold
}

// SlotLimit finds a slot's own limit. A slot number key wins over an alias
// key, and an alias key over an email key; alias and email match without
// regard to case.
func SlotLimit(limits map[string]float64, s Slot) (float64, bool) {
	if len(limits) == 0 {
		return 0, false
	}
	if value, ok := limits[strconv.Itoa(s.Number)]; ok {
		return value, true
	}
	for _, field := range []string{s.Alias, s.Email} {
		if field == "" {
			continue
		}
		for key, value := range limits {
			if strings.EqualFold(strings.TrimSpace(key), field) {
				return value, true
			}
		}
	}
	return 0, false
}

// Decision is what auto switching would do now.
//
// ActiveMax and ActiveLimit are the active account's utilization and limit on
// the window that decides its room (see AutoPolicy.use): the higher window
// when both windows share one limit. ActiveWindow names that window
// ("five-hour" or "seven-day") only when the two limits differ. TargetMax and
// TargetWindow are the target's, measured the same way.
type Decision struct {
	Action       string  `json:"action"`
	Reason       string  `json:"reason"`
	From         int     `json:"from,omitempty"`
	To           int     `json:"to,omitempty"`
	ActiveMax    float64 `json:"activeMax,omitempty"`
	ActiveLimit  float64 `json:"activeLimit,omitempty"`
	ActiveWindow string  `json:"activeWindow,omitempty"`
	TargetMax    float64 `json:"targetMax,omitempty"`
	TargetWindow string  `json:"targetWindow,omitempty"`
	// candidates, when set, are the switch targets best first; otherwise
	// candidatesFor ranks them by the policy.
	candidates []int
}

// Decide is the pure auto-switch decision table. Each account is measured
// against its own limits: each window against its threshold (the weekly
// threshold for the seven-day window), or the account's lower limit.
//
// With StrategySoonestReset the active account should always be the usable
// account, with room under its limit, whose five-hour window resets soonest,
// so usage lands on the window that is about to refresh (see
// decideSoonestReset). Otherwise:
//
//   - no stored account is live, or its usage is unknown: none
//   - the live account's 5h and 7d are both below their limits: none
//   - a switch happened less than the cooldown ago: none
//   - otherwise the usable account with the most room left under its own
//     limit, if that room is at least AutoHysteresis points more than the
//     live account has: switch
//   - no such account: no-target
func Decide(accounts *Accounts, active int, state AutoState, policy AutoPolicy, now time.Time) Decision {
	if active == 0 || accounts.Slot(active) == nil {
		return Decision{Action: AutoNone, Reason: "the live Claude login is not a stored account"}
	}
	current := accounts.Slot(active)
	use, known := policy.use(*current, now)
	if !known {
		return Decision{Action: AutoNone, From: active, Reason: "usage of the active account is unknown"}
	}
	d := Decision{From: active, ActiveMax: use.value, ActiveLimit: use.limit, ActiveWindow: use.windowName()}
	if hasBackups(accounts) {
		return decideWithRoles(accounts, active, *current, use, state, policy, now, d)
	}
	if policy.soonest() {
		return decideSoonestReset(accounts, active, *current, use, state, policy, now, d)
	}
	if use.room() > 0 {
		d.Action, d.Reason = AutoNone, fmt.Sprintf("active account at %s, below %s", use.valueText(), use.limitText())
		return d
	}
	if !state.LastSwitchAt.IsZero() && now.Before(state.LastSwitchAt.Add(policy.Cooldown)) {
		d.Action = AutoNone
		d.Reason = "cooling down for " + FormatDuration(state.LastSwitchAt.Add(policy.Cooldown).Sub(now)) + " after the last switch"
		return d
	}
	targets := rankTargets(accounts, active, use.room(), policy, now)
	if len(targets) == 0 {
		d.Action = AutoNoTarget
		d.Reason = fmt.Sprintf("active account at %s (%s) and no other account has room under its limit", use.valueText(), use.limitText())
		return d
	}
	d.Action = AutoSwitch
	d.target(targets[0])
	d.Reason = fmt.Sprintf("active account at %s (%s); slot %d at %s", use.valueText(), use.limitText(), d.To, d.TargetUsage())
	return d
}

// windowName names the deciding window when the slot's two limits differ,
// and is empty otherwise.
func (w windowUse) windowName() string {
	switch {
	case !w.split:
		return ""
	case w.sevenDay:
		return "seven-day"
	default:
		return "five-hour"
	}
}

// valueText is the utilization for a reason: "92%", or "seven-day 92%" when
// the slot's two limits differ.
func (w windowUse) valueText() string { return usageText(w.windowName(), w.value) }

// limitText is the deciding limit for a reason: "threshold 90%" or "its 40%
// limit" (the slot's own lower limit); the seven-day window's threshold reads
// "weekly threshold 95%" when the slot's two limits differ.
func (w windowUse) limitText() string {
	if w.limit < w.threshold {
		return fmt.Sprintf("its %.0f%% limit", w.limit)
	}
	if w.split && w.sevenDay {
		return fmt.Sprintf("weekly threshold %.0f%%", w.limit)
	}
	return fmt.Sprintf("threshold %.0f%%", w.limit)
}

type rankedTarget struct {
	n     int
	value float64
	room  float64
	reset time.Time
	use   windowUse
}

func newRankedTarget(n int, use windowUse, reset time.Time) rankedTarget {
	return rankedTarget{n: n, value: use.value, room: use.room(), reset: reset, use: use}
}

// rankTargets lists the usable accounts other than from that are below their
// own limit and have at least AutoHysteresis more room than the active
// account. They are ordered by the policy: soonest five-hour reset first for
// StrategySoonestReset, otherwise most room first.
func rankTargets(accounts *Accounts, from int, activeRoom float64, policy AutoPolicy, now time.Time) []rankedTarget {
	var list []rankedTarget
	for _, s := range accounts.Slots {
		if s.Number == from || !s.Usable() {
			continue
		}
		use, known := policy.use(s, now)
		if !known {
			continue
		}
		room := use.room()
		if room <= 0 || room < activeRoom+AutoHysteresis {
			continue
		}
		list = append(list, newRankedTarget(s.Number, use, policy.resetOf(s, now)))
	}
	if policy.soonest() {
		sort.SliceStable(list, func(i, j int) bool {
			if !list[i].reset.Equal(list[j].reset) {
				return soonestResetLess(list[i].reset, list[j].reset)
			}
			return list[i].room > list[j].room
		})
	} else {
		sort.SliceStable(list, func(i, j int) bool { return list[i].room > list[j].room })
	}
	return list
}

// decideSoonestReset implements proactive soonest-reset switching: the active
// account should always be the usable account, with room under its limit, whose
// five-hour window resets soonest, so usage lands on the window that is about to
// refresh. It switches when the active account has no room left, or when another
// usable account with room resets meaningfully sooner (SoonestResetHysteresis),
// subject to the cooldown. An account with no running window (an idle account)
// counts as resetting latest.
func decideSoonestReset(accounts *Accounts, active int, current Slot, use windowUse, state AutoState, policy AutoPolicy, now time.Time, d Decision) Decision {
	activeHasRoom := use.room() > 0
	activeReset := policy.resetOf(current, now)
	targets := soonestTargets(accounts, active, policy, now)
	if len(targets) == 0 {
		if activeHasRoom {
			d.Action, d.Reason = AutoNone, "active account has room and no other account is usable"
		} else {
			d.Action = AutoNoTarget
			d.Reason = fmt.Sprintf("active account at %s (%s) and no other account has room under its limit", use.valueText(), use.limitText())
		}
		return d
	}
	best := targets[0]
	if activeHasRoom && !resetSooner(best.reset, activeReset, SoonestResetHysteresis) {
		d.Action, d.Reason = AutoNone, "active account has room and resets soonest among usable accounts"
		return d
	}
	if !state.LastSwitchAt.IsZero() && now.Before(state.LastSwitchAt.Add(policy.Cooldown)) {
		d.Action = AutoNone
		d.Reason = "cooling down for " + FormatDuration(state.LastSwitchAt.Add(policy.Cooldown).Sub(now)) + " after the last switch"
		return d
	}
	d.Action = AutoSwitch
	d.target(best)
	if activeHasRoom {
		d.Reason = fmt.Sprintf("slot %d resets sooner (%s) than the active account (%s); switching to spend the sooner window first", best.n, resetText(best.reset, now), resetText(activeReset, now))
	} else {
		d.Reason = fmt.Sprintf("active account at %s (%s); slot %d resets soonest with room", use.valueText(), use.limitText(), best.n)
	}
	return d
}

// soonestTargets lists the usable accounts other than from that have room under
// their own limit, ordered by soonest five-hour reset first (an unknown reset
// last). Unlike rankTargets it does not require more room than the active
// account: proactive soonest-reset switching is driven by reset time, not room.
func soonestTargets(accounts *Accounts, from int, policy AutoPolicy, now time.Time) []rankedTarget {
	var list []rankedTarget
	for _, s := range accounts.Slots {
		if s.Number == from || !s.Usable() {
			continue
		}
		use, known := policy.use(s, now)
		if !known || use.room() <= 0 {
			continue
		}
		list = append(list, newRankedTarget(s.Number, use, policy.resetOf(s, now)))
	}
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].reset.Equal(list[j].reset) {
			return soonestResetLess(list[i].reset, list[j].reset)
		}
		return list[i].room > list[j].room
	})
	return list
}

// resetSooner reports whether target resets meaningfully before active, by at
// least margin. An account with no running window (zero reset) never counts as
// sooner; an idle active account (zero reset) is always beaten by a target that
// has a running window.
func resetSooner(target, active time.Time, margin time.Duration) bool {
	if target.IsZero() {
		return false
	}
	if active.IsZero() {
		return true
	}
	return target.Before(active.Add(-margin))
}

// resetText renders a five-hour reset time relative to now for a decision
// reason.
func resetText(reset, now time.Time) string {
	if reset.IsZero() {
		return "no running window"
	}
	if d := reset.Sub(now); d > 0 {
		return "in " + FormatDuration(d)
	}
	return "now"
}

// ActiveUsage renders ActiveMax for a message: "92%", or "seven-day 92%" when
// the active account's two limits differ.
func (d Decision) ActiveUsage() string { return usageText(d.ActiveWindow, d.ActiveMax) }

// TargetUsage renders TargetMax like ActiveUsage.
func (d Decision) TargetUsage() string { return usageText(d.TargetWindow, d.TargetMax) }

func usageText(window string, value float64) string {
	if window != "" {
		return fmt.Sprintf("%s %.0f%%", window, value)
	}
	return fmt.Sprintf("%.0f%%", value)
}

// target records the switch target on the decision.
func (d *Decision) target(r rankedTarget) {
	d.To, d.TargetMax, d.TargetWindow = r.n, r.value, r.use.windowName()
}

// candidatesFor lists auto targets best first (the decision's pick first).
func candidatesFor(accounts *Accounts, d Decision, policy AutoPolicy, now time.Time) []int {
	if d.candidates != nil {
		return d.candidates
	}
	var targets []rankedTarget
	if policy.soonest() {
		targets = soonestTargets(accounts, d.From, policy, now)
	} else {
		targets = rankTargets(accounts, d.From, d.ActiveLimit-d.ActiveMax, policy, now)
	}
	var out []int
	for _, r := range targets {
		out = append(out, r.n)
	}
	return out
}

// AutoOptions controls one auto pass.
type AutoOptions struct {
	Policy AutoPolicy
	DryRun bool
	// PollEvery is how stale an account's usage may be before it is
	// refetched.
	PollEvery time.Duration
	// MaxPolls limits refetches in this pass; 0 means every due account.
	// The daemon passes 1 so it polls at most one account per tick.
	MaxPolls int
}

// AutoResult is the outcome of one pass.
type AutoResult struct {
	Decision Decision      `json:"decision"`
	Switched *SwitchResult `json:"switched,omitempty"`
	Polled   []int         `json:"polled,omitempty"`
}

// AutoOnce refreshes due usage, decides, and switches when the decision
// says so (unless DryRun).
func (m *Manager) AutoOnce(ctx context.Context, opts AutoOptions) (AutoResult, error) {
	unlock, err := m.lockStore(ctx)
	if err != nil {
		return AutoResult{}, err
	}
	defer unlock()
	accounts, err := m.Store.Load()
	if err != nil {
		return AutoResult{}, err
	}
	live, err := m.readLive()
	if err != nil {
		return AutoResult{}, err
	}
	state, err := m.Store.LoadAutoState()
	if err != nil {
		return AutoResult{}, err
	}
	if !opts.DryRun {
		if err := m.syncLive(accounts, live); err != nil {
			return AutoResult{}, err
		}
	}
	active := 0
	if current := live.liveSlot(accounts); current != nil {
		active = current.Number
	}
	var result AutoResult
	if !opts.DryRun {
		limit := 0.0
		if current := accounts.Slot(active); current != nil {
			limit = opts.Policy.LimitFor(*current)
		}
		result.Polled = pollOrder(accounts, active, m.now(), opts.PollEvery, opts.MaxPolls, limit)
		if len(result.Polled) > 0 {
			m.refreshUsage(ctx, accounts, live, result.Polled)
			if err := m.Store.Save(accounts); err != nil {
				return result, err
			}
		}
	}
	now := m.now()
	if !opts.DryRun && recordUsedUp(&state, accounts, active, opts.Policy, now) {
		if err := m.Store.SaveAutoState(state); err != nil {
			return result, err
		}
	}
	result.Decision = Decide(accounts, active, state, opts.Policy, now)
	if result.Decision.Action != AutoSwitch || opts.DryRun {
		return result, nil
	}
	candidates := candidatesFor(accounts, result.Decision, opts.Policy, now)
	switched, err := m.switchAmong(ctx, accounts, live, candidates, false, SwitchOptions{Cooldown: opts.Policy.Cooldown, Limits: opts.Policy.Limits})
	if err != nil {
		if errors.Is(err, ErrNoTarget) {
			result.Decision.Action, result.Decision.Reason = AutoNoTarget, err.Error()
			return result, nil
		}
		return result, err
	}
	result.Decision.To = switched.To.Number
	result.Switched = &switched
	return result, nil
}

// PollOnce refreshes due usage without deciding anything: the daemon's pass
// when keepalive runs without auto switching. It polls like AutoOnce.
func (m *Manager) PollOnce(ctx context.Context, every time.Duration, max int) ([]int, error) {
	unlock, err := m.lockStore(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	accounts, err := m.Store.Load()
	if err != nil {
		return nil, err
	}
	live, err := m.readLive()
	if err != nil {
		return nil, err
	}
	active := 0
	if current := live.liveSlot(accounts); current != nil {
		active = current.Number
	}
	if err := m.syncLive(accounts, live); err != nil {
		return nil, err
	}
	polled := pollOrder(accounts, active, m.now(), every, max, 0)
	if len(polled) == 0 {
		return nil, nil
	}
	m.refreshUsage(ctx, accounts, live, polled)
	return polled, m.Store.Save(accounts)
}

// pollOrder picks which accounts to refetch: the active account first when
// due, then the others whose data is oldest. Dead accounts and accounts in
// backoff are skipped. With activeLimit > 0 the active account is polled every
// FastPollEvery while its burn rate would carry it to that limit before the
// next regular poll, so a fast-burning account is switched before it hits
// 100%.
func pollOrder(accounts *Accounts, active int, now time.Time, every time.Duration, max int, activeLimit float64) []int {
	if every <= 0 {
		every = ListMaxAge
	}
	type due struct {
		n    int
		last time.Time
	}
	var list []due
	for _, s := range accounts.Slots {
		maxAge := every
		if s.Number == active {
			maxAge = s.LastUsage.pollEvery(now, every, activeLimit)
		}
		if s.Dead || !s.LastUsage.due(now, maxAge) {
			continue
		}
		if s.Number != active && s.Disabled {
			continue
		}
		var last time.Time
		if s.LastUsage != nil {
			last = s.LastUsage.AttemptAt
		}
		list = append(list, due{s.Number, last})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if (list[i].n == active) != (list[j].n == active) {
			return list[i].n == active
		}
		return list[i].last.Before(list[j].last)
	})
	var out []int
	for _, d := range list {
		if max > 0 && len(out) == max {
			break
		}
		out = append(out, d.n)
	}
	return out
}
