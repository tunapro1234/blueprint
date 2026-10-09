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

// Auto decision outcomes.
const (
	AutoNone     = "none"
	AutoSwitch   = "switch"
	AutoNoTarget = "no-target"
)

// AutoPolicy is the auto-switch configuration.
type AutoPolicy struct {
	Threshold float64
	Cooldown  time.Duration
	// Limits caps single accounts below the threshold. Keys are a slot
	// number, an alias or an email; values are utilization percentages.
	Limits map[string]float64
	// Prefer orders the eligible targets. StrategySoonestReset picks the
	// account whose five-hour window resets soonest; any other value (the
	// default) picks the account with the most room left under its limit.
	Prefer string
}

// LimitFor is the utilization at which a slot counts as used up: the
// threshold (100 when unset), lowered by the slot's own limit.
func (p AutoPolicy) LimitFor(s Slot) float64 {
	limit := p.threshold()
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
type Decision struct {
	Action      string  `json:"action"`
	Reason      string  `json:"reason"`
	From        int     `json:"from,omitempty"`
	To          int     `json:"to,omitempty"`
	ActiveMax   float64 `json:"activeMax,omitempty"`
	ActiveLimit float64 `json:"activeLimit,omitempty"`
	TargetMax   float64 `json:"targetMax,omitempty"`
}

// Decide is the pure auto-switch decision table. Each account is measured
// against its own limit (the threshold, or the account's lower limit):
//
//   - no stored account is live, or its usage is unknown: none
//   - the live account's 5h and 7d are both below its limit: none
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
	activeMax, known := slotUsage(*current).Max(now)
	if !known {
		return Decision{Action: AutoNone, From: active, Reason: "usage of the active account is unknown"}
	}
	limit := policy.LimitFor(*current)
	d := Decision{From: active, ActiveMax: activeMax, ActiveLimit: limit}
	if activeMax < limit {
		d.Action, d.Reason = AutoNone, fmt.Sprintf("active account at %.0f%%, below %s", activeMax, limitText(limit, policy))
		return d
	}
	if !state.LastSwitchAt.IsZero() && now.Before(state.LastSwitchAt.Add(policy.Cooldown)) {
		d.Action = AutoNone
		d.Reason = "cooling down for " + FormatDuration(state.LastSwitchAt.Add(policy.Cooldown).Sub(now)) + " after the last switch"
		return d
	}
	targets := rankTargets(accounts, active, limit-activeMax, policy, now)
	if len(targets) == 0 {
		d.Action = AutoNoTarget
		d.Reason = fmt.Sprintf("active account at %.0f%% (%s) and no other account has room under its limit", activeMax, limitText(limit, policy))
		return d
	}
	d.Action, d.To, d.TargetMax = AutoSwitch, targets[0].n, targets[0].value
	d.Reason = fmt.Sprintf("active account at %.0f%% (%s); slot %d at %.0f%%", activeMax, limitText(limit, policy), d.To, d.TargetMax)
	return d
}

func limitText(limit float64, policy AutoPolicy) string {
	if limit < policy.threshold() {
		return fmt.Sprintf("its %.0f%% limit", limit)
	}
	return fmt.Sprintf("threshold %.0f%%", limit)
}

type rankedTarget struct {
	n     int
	value float64
	room  float64
	reset time.Time
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
		value, known := slotUsage(s).Max(now)
		if !known {
			continue
		}
		room := policy.LimitFor(s) - value
		if room <= 0 || room < activeRoom+AutoHysteresis {
			continue
		}
		list = append(list, rankedTarget{n: s.Number, value: value, room: room, reset: slotUsage(s).FiveHourReset()})
	}
	if policy.Prefer == StrategySoonestReset {
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

// candidatesFor lists auto targets best first (the decision's pick first).
func candidatesFor(accounts *Accounts, d Decision, policy AutoPolicy, now time.Time) []int {
	var out []int
	for _, r := range rankTargets(accounts, d.From, d.ActiveLimit-d.ActiveMax, policy, now) {
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
	active := 0
	if current := live.liveSlot(accounts); current != nil {
		active = current.Number
	}
	var result AutoResult
	if !opts.DryRun {
		result.Polled = pollOrder(accounts, active, m.now(), opts.PollEvery, opts.MaxPolls)
		if len(result.Polled) > 0 {
			m.refreshUsage(ctx, accounts, live, result.Polled)
			if err := m.Store.Save(accounts); err != nil {
				return result, err
			}
		}
	}
	now := m.now()
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
	polled := pollOrder(accounts, active, m.now(), every, max)
	if len(polled) == 0 {
		return nil, nil
	}
	m.refreshUsage(ctx, accounts, live, polled)
	return polled, m.Store.Save(accounts)
}

// pollOrder picks which accounts to refetch: the active account first when
// due, then the others whose data is oldest. Dead accounts and accounts in
// backoff are skipped.
func pollOrder(accounts *Accounts, active int, now time.Time, every time.Duration, max int) []int {
	if every <= 0 {
		every = ListMaxAge
	}
	type due struct {
		n    int
		last time.Time
	}
	var list []due
	for _, s := range accounts.Slots {
		if s.Dead || !s.LastUsage.due(now, every) {
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
