package claudeacct

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
}

// Decision is what auto switching would do now.
type Decision struct {
	Action    string  `json:"action"`
	Reason    string  `json:"reason"`
	From      int     `json:"from,omitempty"`
	To        int     `json:"to,omitempty"`
	ActiveMax float64 `json:"activeMax,omitempty"`
	TargetMax float64 `json:"targetMax,omitempty"`
}

// Decide is the pure auto-switch decision table:
//
//   - no stored account is live, or its usage is unknown: none
//   - the live account's 5h and 7d are both below the threshold: none
//   - a switch happened less than the cooldown ago: none
//   - otherwise the usable account with the lowest max(5h, 7d) that is below
//     the threshold and at least AutoHysteresis points better: switch
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
	d := Decision{From: active, ActiveMax: activeMax}
	if activeMax < policy.Threshold {
		d.Action, d.Reason = AutoNone, fmt.Sprintf("active account at %.0f%%, below %.0f%%", activeMax, policy.Threshold)
		return d
	}
	if !state.LastSwitchAt.IsZero() && now.Before(state.LastSwitchAt.Add(policy.Cooldown)) {
		d.Action = AutoNone
		d.Reason = "cooling down for " + FormatDuration(state.LastSwitchAt.Add(policy.Cooldown).Sub(now)) + " after the last switch"
		return d
	}
	type ranked struct {
		n     int
		value float64
	}
	var list []ranked
	for _, s := range accounts.Slots {
		if s.Number == active || !s.Usable() {
			continue
		}
		value, known := slotUsage(s).Max(now)
		if !known || value >= policy.Threshold || value > activeMax-AutoHysteresis {
			continue
		}
		list = append(list, ranked{s.Number, value})
	}
	if len(list) == 0 {
		d.Action = AutoNoTarget
		d.Reason = fmt.Sprintf("active account at %.0f%% and no other account is below %.0f%%", activeMax, policy.Threshold)
		return d
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].value < list[j].value })
	d.Action, d.To, d.TargetMax = AutoSwitch, list[0].n, list[0].value
	d.Reason = fmt.Sprintf("active account at %.0f%% (threshold %.0f%%); slot %d at %.0f%%", activeMax, policy.Threshold, d.To, d.TargetMax)
	return d
}

// candidatesFor lists auto targets best first (the decision's pick first).
func candidatesFor(accounts *Accounts, d Decision, policy AutoPolicy, now time.Time) []int {
	type ranked struct {
		n     int
		value float64
	}
	var list []ranked
	for _, s := range accounts.Slots {
		if s.Number == d.From || !s.Usable() {
			continue
		}
		value, known := slotUsage(s).Max(now)
		if known && value < policy.Threshold && value <= d.ActiveMax-AutoHysteresis {
			list = append(list, ranked{s.Number, value})
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].value < list[j].value })
	out := make([]int, 0, len(list))
	for _, r := range list {
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
	switched, err := m.switchAmong(ctx, accounts, live, candidates, false, SwitchOptions{Cooldown: opts.Policy.Cooldown})
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
