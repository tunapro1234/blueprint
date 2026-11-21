package claudeacct

import (
	"fmt"
	"sort"
	"strconv"
	"time"
)

// Account roles. A preferred account is used by auto switching whenever one
// has room, before any normal account; a backup account only while every
// normal account is used up, and auto switching returns to a normal account
// as soon as one has room again. Manual switching may pick any account.
const (
	RoleNormal    = "normal"
	RoleBackup    = "backup"
	RolePreferred = "preferred"
)

// Backup switch orders (AutoPolicy.BackupOrder).
const (
	// SwitchOrderAfterNormals uses a backup only when every normal account
	// is used up: A1 A2 A3 B1 B2 B3.
	SwitchOrderAfterNormals = "after-normals"
	// SwitchOrderInterleave goes to the next backup whenever the active
	// normal account is used up: A1 B1 A2 B2 A3 B3.
	SwitchOrderInterleave = "interleave"
)

// IsBackup reports whether the slot is a backup account.
func (s Slot) IsBackup() bool { return s.Role == RoleBackup }

// IsPreferred reports whether the slot is a preferred account. A preferred
// account is not a backup: whatever only tells backups from the rest
// (keepalive stagger, SkipBackups) treats it as a normal account.
func (s Slot) IsPreferred() bool { return s.Role == RolePreferred }

// RoleName is the slot's role for display: "normal" when unset.
func (s Slot) RoleName() string {
	switch {
	case s.IsBackup():
		return RoleBackup
	case s.IsPreferred():
		return RolePreferred
	}
	return RoleNormal
}

// ValidRole reports whether role can be stored on a slot.
func ValidRole(role string) bool {
	return role == RoleNormal || role == RoleBackup || role == RolePreferred
}

func hasBackups(accounts *Accounts) bool {
	for _, s := range accounts.Slots {
		if s.IsBackup() {
			return true
		}
	}
	return false
}

func hasPreferred(accounts *Accounts) bool {
	for _, s := range accounts.Slots {
		if s.IsPreferred() {
			return true
		}
	}
	return false
}

// withRole is accounts restricted to the slots of one role (RoleName).
func withRole(accounts *Accounts, role string) *Accounts {
	return filterSlots(accounts, func(s Slot) bool { return s.RoleName() == role })
}

// withoutRole is accounts without the slots of one role (RoleName).
func withoutRole(accounts *Accounts, role string) *Accounts {
	return filterSlots(accounts, func(s Slot) bool { return s.RoleName() != role })
}

func filterSlots(accounts *Accounts, keep func(Slot) bool) *Accounts {
	out := &Accounts{Version: accounts.Version, Active: accounts.Active}
	for _, s := range accounts.Slots {
		if keep(s) {
			out.Slots = append(out.Slots, s)
		}
	}
	return out
}

// backupTargets lists the usable backup accounts other than from with room
// under their own limit, by backup priority (lower first), then slot number.
func backupTargets(accounts *Accounts, from int, policy AutoPolicy, now time.Time) []rankedTarget {
	var list []rankedTarget
	priority := map[int]int{}
	for _, s := range accounts.Slots {
		if s.Number == from || !s.IsBackup() || !s.Usable() {
			continue
		}
		use, known := policy.use(s, now)
		if !known || use.room() <= 0 {
			continue
		}
		priority[s.Number] = s.Priority
		list = append(list, newRankedTarget(s.Number, use, policy.resetOf(s, now)))
	}
	sort.SliceStable(list, func(i, j int) bool {
		if priority[list[i].n] != priority[list[j].n] {
			return priority[list[i].n] < priority[list[j].n]
		}
		return list[i].n < list[j].n
	})
	return list
}

// normalTargets lists the usable normal accounts other than from with room,
// best first by the policy (soonest reset, or most room).
func normalTargets(accounts *Accounts, from int, policy AutoPolicy, now time.Time) []rankedTarget {
	return tierTargets(withRole(accounts, RoleNormal), from, policy, now)
}

// preferredTargets lists the usable preferred accounts other than from with
// room, in the order normalTargets gives normal accounts.
func preferredTargets(accounts *Accounts, from int, policy AutoPolicy, now time.Time) []rankedTarget {
	return tierTargets(withRole(accounts, RolePreferred), from, policy, now)
}

// tierTargets lists the usable accounts other than from with room, best first
// by the policy (soonest reset, or most room), without the hysteresis
// rankTargets applies.
func tierTargets(accounts *Accounts, from int, policy AutoPolicy, now time.Time) []rankedTarget {
	list := soonestTargets(accounts, from, policy, now)
	if !policy.soonest() {
		sort.SliceStable(list, func(i, j int) bool { return list[i].room > list[j].room })
	}
	return list
}

func targetNumbers(list []rankedTarget) []int {
	out := make([]int, 0, len(list))
	for _, r := range list {
		out = append(out, r.n)
	}
	return out
}

// recordUsedUp notes in state when the active normal account is used up, so
// the interleave order can tell a normal account whose window has since reset
// from one that was never used. It reports whether state changed. A preferred
// account is not recorded: auto switching returns to one whenever it has
// room, from any account, without looking at this state.
func recordUsedUp(state *AutoState, accounts *Accounts, active int, policy AutoPolicy, now time.Time) bool {
	current := accounts.Slot(active)
	if current == nil || current.RoleName() != RoleNormal || !hasBackups(accounts) {
		return false
	}
	if !policy.usedUp(*current, now) {
		return false
	}
	reset := slotUsage(*current).FiveHourResetAt(now)
	key := strconv.Itoa(active)
	if previous, ok := state.UsedUp[key]; ok && previous.Equal(reset) {
		return false
	}
	if state.UsedUp == nil {
		state.UsedUp = map[string]time.Time{}
	}
	state.UsedUp[key] = reset
	return true
}

// resetNormals are the normal targets whose window, last seen used up, has
// reset since (see AutoState.UsedUp).
func resetNormals(normals []rankedTarget, state AutoState, now time.Time) []rankedTarget {
	var out []rankedTarget
	for _, r := range normals {
		if reset, ok := state.UsedUp[strconv.Itoa(r.n)]; ok && !reset.After(now) {
			out = append(out, r)
		}
	}
	return out
}

// decideWithRoles is Decide when some account is a backup. While the active
// normal account has room, backups do not exist: the ordinary policy runs
// among the normal accounts only.
//
// When the active normal account is used up, SwitchOrderAfterNormals moves to
// another normal account with room and takes the first backup by priority only
// when none has; SwitchOrderInterleave takes the next backup by priority first
// (A1 B1 A2 B2 ...) and another normal only when no backup has room.
//
// On a backup account it returns to a normal account whose window has reset
// as soon as one has room, after the cooldown: with after-normals any normal
// with room (they were all used up), with interleave one that was seen used up
// and has reset since. Otherwise it stays until the backup is used up, then
// takes the next normal account with room, else the next backup by priority.
func decideWithRoles(accounts *Accounts, active int, current Slot, use windowUse, state AutoState, policy AutoPolicy, now time.Time, d Decision) Decision {
	interleave := policy.BackupOrder == SwitchOrderInterleave
	normalsOnly := withRole(accounts, RoleNormal)
	switchTo := func(list []rankedTarget, reason string) Decision {
		if !state.LastSwitchAt.IsZero() && now.Before(state.LastSwitchAt.Add(policy.Cooldown)) {
			d.Action = AutoNone
			d.Reason = "cooling down for " + FormatDuration(state.LastSwitchAt.Add(policy.Cooldown).Sub(now)) + " after the last switch"
			return d
		}
		d.Action, d.candidates = AutoSwitch, targetNumbers(list)
		d.target(list[0])
		d.Reason = reason + fmt.Sprintf("; slot %d at %s", d.To, d.TargetUsage())
		if target := accounts.Slot(d.To); target != nil && target.IsBackup() {
			d.Reason += fmt.Sprintf(" (backup, priority %d)", target.Priority)
		}
		return d
	}
	noTarget := func(why string) Decision {
		d.Action = AutoNoTarget
		d.Reason = why + " and no other account has room under its limit"
		return d
	}
	backups := backupTargets(accounts, active, policy, now)
	if !current.IsBackup() {
		normal := Decide(normalsOnly, active, state, policy, now)
		if use.room() > 0 || normal.Action == AutoNone && !interleave {
			if normal.Action == AutoSwitch {
				normal.candidates = candidatesFor(normalsOnly, normal, policy, now)
			}
			return normal
		}
		why := fmt.Sprintf("active account at %s (%s)", use.valueText(), use.limitText())
		if interleave && len(backups) > 0 {
			return switchTo(backups, why+", interleaving: next backup")
		}
		if normal.Action != AutoNoTarget {
			if normal.Action == AutoSwitch {
				normal.candidates = candidatesFor(normalsOnly, normal, policy, now)
			}
			return normal
		}
		if len(backups) > 0 {
			return switchTo(backups, why+", every normal account is used up")
		}
		return noTarget(why)
	}
	normals := normalTargets(accounts, active, policy, now)
	back := normals
	if interleave {
		back = resetNormals(normals, state, now)
	}
	if len(back) > 0 {
		return switchTo(back, fmt.Sprintf("on backup slot %d; a normal account has room again", active))
	}
	if use.room() > 0 {
		d.Action = AutoNone
		d.Reason = fmt.Sprintf("on backup slot %d at %s; no used-up normal account has reset yet", active, use.valueText())
		return d
	}
	why := fmt.Sprintf("backup slot %d at %s (%s)", active, use.valueText(), use.limitText())
	if len(normals) > 0 {
		return switchTo(normals, why+", next normal account")
	}
	if len(backups) > 0 {
		return switchTo(backups, why+", next backup")
	}
	return noTarget(why)
}

// decidePreferred is Decide when some account is preferred. With
// AutoPolicy.PreferredYield set, a normal account whose seven-day window
// resets within the yield comes before the preferred accounts (see
// decideExpiring); otherwise, and while no such account has room,
// decideByTier.
func decidePreferred(accounts *Accounts, active int, current Slot, use windowUse, state AutoState, policy AutoPolicy, now time.Time, d Decision) Decision {
	if expiring := expiringTargets(accounts, policy, now); len(expiring) > 0 {
		return decideExpiring(accounts, active, current, use, state, policy, now, d, expiring)
	}
	return decideByTier(accounts, active, current, use, state, policy, now, d)
}

// expiringTargets lists the accounts the preferred yield puts first: usable
// normal accounts (neither preferred nor backup) with room whose seven-day
// window is running and resets within AutoPolicy.PreferredYield, whatever
// the switch strategy; soonest seven-day reset first, then most room. The
// list may hold the active account. It is empty while the yield is off.
func expiringTargets(accounts *Accounts, policy AutoPolicy, now time.Time) []rankedTarget {
	if policy.PreferredYield <= 0 {
		return nil
	}
	var list []rankedTarget
	for _, s := range accounts.Slots {
		if s.RoleName() != RoleNormal || !s.Usable() {
			continue
		}
		use, known := policy.use(s, now)
		if !known || use.room() <= 0 {
			continue
		}
		reset := slotUsage(s).SevenDayResetAt(now)
		if reset.IsZero() || reset.After(now.Add(policy.PreferredYield)) {
			continue
		}
		list = append(list, newRankedTarget(s.Number, use, reset))
	}
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].reset.Equal(list[j].reset) {
			return list[i].reset.Before(list[j].reset)
		}
		return list[i].room > list[j].room
	})
	return list
}

// decideExpiring is decidePreferred while some normal account's seven-day
// window resets within the preferred yield (see expiringTargets): an
// allowance about to reset is lost unless it is spent, so it is spent before
// a preferred account's.
//
// On an expiring account the policy runs among the expiring accounts only,
// so neither a preferred account nor a normal account outside the yield pulls
// auto switching away while it has room (an expiring account always has
// room). On any other account, a preferred one with room included, it
// switches to the first expiring account after the cooldown; after the
// expiring accounts, the candidates are those decideByTier would take.
func decideExpiring(accounts *Accounts, active int, current Slot, use windowUse, state AutoState, policy AutoPolicy, now time.Time, d Decision, expiring []rankedTarget) Decision {
	in := map[int]bool{}
	for _, r := range expiring {
		in[r.n] = true
	}
	yield := FormatDuration(policy.PreferredYield)
	for _, r := range expiring {
		if r.n != active {
			continue
		}
		sub := decideWithin(filterSlots(accounts, func(s Slot) bool { return in[s.Number] }), active, current, use, state, policy, now, d)
		sub.Reason = fmt.Sprintf("active account's seven-day window resets in %s, within the preferred yield %s; ", FormatDuration(r.reset.Sub(now)), yield) + sub.Reason
		return sub
	}
	candidates := targetNumbers(expiring)
	if fallback := decideByTier(accounts, active, current, use, state, policy, now, d); fallback.Action == AutoSwitch {
		for _, n := range candidatesFor(accounts, fallback, policy, now) {
			if !in[n] {
				candidates = append(candidates, n)
			}
		}
	}
	best := expiring[0]
	reason := fmt.Sprintf("slot %d's seven-day window resets in %s, within the preferred yield %s; slot %d at %s, leaving slot %d at %s",
		best.n, FormatDuration(best.reset.Sub(now)), yield, best.n, best.use.valueText(), active, use.valueText())
	return switchDecision(accounts, expiring, candidates, reason, state, policy, now, d)
}

// switchDecision is d switching to list[0] with candidates, or waiting while
// the last switch is under the cooldown.
func switchDecision(accounts *Accounts, list []rankedTarget, candidates []int, reason string, state AutoState, policy AutoPolicy, now time.Time, d Decision) Decision {
	if !state.LastSwitchAt.IsZero() && now.Before(state.LastSwitchAt.Add(policy.Cooldown)) {
		d.Action = AutoNone
		d.Reason = "cooling down for " + FormatDuration(state.LastSwitchAt.Add(policy.Cooldown).Sub(now)) + " after the last switch"
		return d
	}
	d.Action, d.candidates = AutoSwitch, candidates
	d.target(list[0])
	d.Reason = reason
	if target := accounts.Slot(d.To); target != nil && target.IsBackup() {
		d.Reason += fmt.Sprintf(" (backup, priority %d)", target.Priority)
	}
	return d
}

// decideWithin decides among a subset of the accounts that holds the active
// one, with decideTiers (where the preferred role plays no part), and takes
// the candidates from that subset.
func decideWithin(subset *Accounts, active int, current Slot, use windowUse, state AutoState, policy AutoPolicy, now time.Time, d Decision) Decision {
	sub := decideTiers(subset, active, current, use, state, policy, now, d)
	if sub.Action == AutoSwitch {
		sub.candidates = candidatesFor(subset, sub, policy, now)
	}
	return sub
}

// decideByTier is decidePreferred without the preferred yield. Preferred
// accounts come first, then normal accounts, then backups; each tier is
// ordered by the policy as normal accounts are (see normalTargets).
//
// On an account that is not preferred it switches to the best preferred
// account with room, even while the active account still has room, after the
// cooldown; when no preferred account has room, the decision without
// preferred accounts (decideTiers) runs on the other accounts as if the
// preferred ones did not exist. On a preferred account with room it runs
// among the preferred accounts only, so auto switching never leaves the
// preferred tier while the active account has room. When the active
// preferred account is used up it takes the next preferred account with
// room, else a normal account by the policy, else a backup by priority
// (normals before backups in either backup switch order).
func decideByTier(accounts *Accounts, active int, current Slot, use windowUse, state AutoState, policy AutoPolicy, now time.Time, d Decision) Decision {
	preferred := preferredTargets(accounts, active, policy, now)
	switchTo := func(list []rankedTarget, candidates []int, reason string) Decision {
		return switchDecision(accounts, list, candidates, reason, state, policy, now, d)
	}
	within := func(subset *Accounts) Decision {
		return decideWithin(subset, active, current, use, state, policy, now, d)
	}
	rest := withoutRole(accounts, RolePreferred)
	if !current.IsPreferred() {
		if len(preferred) == 0 {
			return within(rest)
		}
		candidates := targetNumbers(preferred)
		reason := fmt.Sprintf("preferred slot %d has room at %s; leaving slot %d at %s", preferred[0].n, preferred[0].use.valueText(), active, use.valueText())
		if use.room() <= 0 {
			if fallback := within(rest); fallback.Action == AutoSwitch {
				candidates = append(candidates, fallback.candidates...)
			}
			reason = fmt.Sprintf("active account at %s (%s); preferred slot %d has room at %s", use.valueText(), use.limitText(), preferred[0].n, preferred[0].use.valueText())
		}
		return switchTo(preferred, candidates, reason)
	}
	if use.room() > 0 {
		sub := within(withRole(accounts, RolePreferred))
		sub.Reason = "among preferred accounts: " + sub.Reason
		return sub
	}
	why := fmt.Sprintf("preferred slot %d at %s (%s)", active, use.valueText(), use.limitText())
	normals := normalTargets(rest, active, policy, now)
	backups := backupTargets(rest, active, policy, now)
	candidates := targetNumbers(append(append(append([]rankedTarget{}, preferred...), normals...), backups...))
	switch {
	case len(preferred) > 0:
		return switchTo(preferred, candidates, why+fmt.Sprintf("; next preferred slot %d at %s", preferred[0].n, preferred[0].use.valueText()))
	case len(normals) > 0:
		return switchTo(normals, candidates, why+fmt.Sprintf(", no other preferred account has room; slot %d at %s", normals[0].n, normals[0].use.valueText()))
	case len(backups) > 0:
		return switchTo(backups, candidates, why+fmt.Sprintf(", every preferred and normal account is used up; slot %d at %s", backups[0].n, backups[0].use.valueText()))
	}
	d.Action = AutoNoTarget
	d.Reason = why + " and no other account has room under its limit"
	return d
}

// alternatingRoles reports whether roles, read around the keepalive cycle by
// grid position, interleave normal and backup windows (n b n b ..., the
// surplus of the larger group together) for some rotation. With no backups or
// no normals every order qualifies.
func alternatingRoles(roles []bool) bool {
	backups := 0
	for _, b := range roles {
		if b {
			backups++
		}
	}
	n := len(roles)
	if backups == 0 || backups == n {
		return true
	}
	pattern := make([]bool, 0, n)
	normals := n - backups
	for i := 0; i < normals || i < backups; i++ {
		if i < normals {
			pattern = append(pattern, false)
		}
		if i < backups {
			pattern = append(pattern, true)
		}
	}
	for r := 0; r < n; r++ {
		ok := true
		for i := 0; i < n && ok; i++ {
			ok = roles[(r+i)%n] == pattern[i]
		}
		if ok {
			return true
		}
	}
	return false
}
