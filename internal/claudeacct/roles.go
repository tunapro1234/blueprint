package claudeacct

import (
	"fmt"
	"sort"
	"strconv"
	"time"
)

// Account roles. A backup account is used by auto switching only while every
// normal account is used up, and auto switching returns to a normal account
// as soon as one has room again. Manual switching may pick any account.
const (
	RoleNormal = "normal"
	RoleBackup = "backup"
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

// RoleName is the slot's role for display: "normal" when unset.
func (s Slot) RoleName() string {
	if s.IsBackup() {
		return RoleBackup
	}
	return RoleNormal
}

// ValidRole reports whether role can be stored on a slot.
func ValidRole(role string) bool { return role == RoleNormal || role == RoleBackup }

func hasBackups(accounts *Accounts) bool {
	for _, s := range accounts.Slots {
		if s.IsBackup() {
			return true
		}
	}
	return false
}

// withRole is accounts restricted to the slots of one role.
func withRole(accounts *Accounts, backup bool) *Accounts {
	out := &Accounts{Version: accounts.Version, Active: accounts.Active}
	for _, s := range accounts.Slots {
		if s.IsBackup() == backup {
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
		value, known := slotUsage(s).Max(now)
		if !known || policy.LimitFor(s)-value <= 0 {
			continue
		}
		priority[s.Number] = s.Priority
		list = append(list, rankedTarget{n: s.Number, value: value, room: policy.LimitFor(s) - value, reset: slotUsage(s).FiveHourResetAt(now)})
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
	list := soonestTargets(withRole(accounts, false), from, policy, now)
	if policy.Prefer != StrategySoonestReset {
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
// from one that was never used. It reports whether state changed.
func recordUsedUp(state *AutoState, accounts *Accounts, active int, policy AutoPolicy, now time.Time) bool {
	current := accounts.Slot(active)
	if current == nil || current.IsBackup() || !hasBackups(accounts) {
		return false
	}
	value, known := slotUsage(*current).Max(now)
	if !known || value < policy.LimitFor(*current) {
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
func decideWithRoles(accounts *Accounts, active int, current Slot, activeMax, limit float64, state AutoState, policy AutoPolicy, now time.Time, d Decision) Decision {
	interleave := policy.BackupOrder == SwitchOrderInterleave
	normalsOnly := withRole(accounts, false)
	switchTo := func(list []rankedTarget, reason string) Decision {
		if !state.LastSwitchAt.IsZero() && now.Before(state.LastSwitchAt.Add(policy.Cooldown)) {
			d.Action = AutoNone
			d.Reason = "cooling down for " + FormatDuration(state.LastSwitchAt.Add(policy.Cooldown).Sub(now)) + " after the last switch"
			return d
		}
		d.Action, d.To, d.TargetMax, d.candidates = AutoSwitch, list[0].n, list[0].value, targetNumbers(list)
		d.Reason = reason + fmt.Sprintf("; slot %d at %.0f%%", d.To, d.TargetMax)
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
		if activeMax < limit || normal.Action == AutoNone && !interleave {
			if normal.Action == AutoSwitch {
				normal.candidates = candidatesFor(normalsOnly, normal, policy, now)
			}
			return normal
		}
		why := fmt.Sprintf("active account at %.0f%% (%s)", activeMax, limitText(limit, policy))
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
	if activeMax < limit {
		d.Action = AutoNone
		d.Reason = fmt.Sprintf("on backup slot %d at %.0f%%; no used-up normal account has reset yet", active, activeMax)
		return d
	}
	why := fmt.Sprintf("backup slot %d at %.0f%% (%s)", active, activeMax, limitText(limit, policy))
	if len(normals) > 0 {
		return switchTo(normals, why+", next normal account")
	}
	if len(backups) > 0 {
		return switchTo(backups, why+", next backup")
	}
	return noTarget(why)
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
