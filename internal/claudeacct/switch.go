package claudeacct

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Switch strategies.
const (
	StrategyRotation      = ""
	StrategyBest          = "best"
	StrategyNextAvailable = "next-available"
)

// SwitchOptions selects the target of a switch.
type SwitchOptions struct {
	// Selector is a slot number, email or alias; empty picks by Strategy.
	Selector string
	Strategy string
	DryRun   bool
	// Cooldown is recorded in auto-state so auto switching waits after any
	// switch, manual or automatic.
	Cooldown time.Duration
}

// SwitchResult describes a switch (or what a dry run would do).
type SwitchResult struct {
	From      int    `json:"from,omitempty"`
	To        Slot   `json:"to"`
	DryRun    bool   `json:"dryRun,omitempty"`
	Captured  bool   `json:"captured,omitempty"`
	Displaced string `json:"displaced,omitempty"`
	Refreshed bool   `json:"refreshed,omitempty"`
}

// ErrNoTarget means no stored account is eligible.
var ErrNoTarget = errors.New("no other usable stored account to switch to")

// Switch activates another stored account for Claude Code.
func (m *Manager) Switch(ctx context.Context, opts SwitchOptions) (SwitchResult, error) {
	switch opts.Strategy {
	case StrategyRotation, StrategyBest, StrategyNextAvailable:
	default:
		return SwitchResult{}, fmt.Errorf("unknown strategy %q (use best or next-available)", opts.Strategy)
	}
	if opts.Selector != "" && opts.Strategy != "" {
		return SwitchResult{}, errors.New("give a slot or a strategy, not both")
	}
	unlock, err := m.lockStore(ctx)
	if err != nil {
		return SwitchResult{}, err
	}
	defer unlock()
	accounts, err := m.Store.Load()
	if err != nil {
		return SwitchResult{}, err
	}
	live, err := m.readLive()
	if err != nil {
		return SwitchResult{}, err
	}
	var candidates []int
	explicit := opts.Selector != ""
	if explicit {
		slot, err := resolveSelector(accounts, opts.Selector)
		if err != nil {
			return SwitchResult{}, err
		}
		if current := live.liveSlot(accounts); current != nil && current.Number == slot.Number {
			return SwitchResult{}, fmt.Errorf("slot %d (%s) is already the active Claude login", slot.Number, slot.Email)
		}
		switch {
		case slot.Dead:
			return SwitchResult{}, fmt.Errorf("slot %d (%s) has a dead login; log in with claude and run bp account add again", slot.Number, slot.Email)
		case slot.Disabled:
			return SwitchResult{}, fmt.Errorf("slot %d (%s) is disabled; run bp account enable %d first", slot.Number, slot.Email, slot.Number)
		}
		candidates = []int{slot.Number}
	} else {
		if opts.Strategy != StrategyRotation && !opts.DryRun {
			m.refreshDueUsage(ctx, accounts, live, ListMaxAge)
		}
		candidates = strategyCandidates(accounts, live, opts.Strategy, m.now())
	}
	return m.switchAmong(ctx, accounts, live, candidates, explicit, opts)
}

// refreshDueUsage refetches stale usage before a usage-based choice.
func (m *Manager) refreshDueUsage(ctx context.Context, accounts *Accounts, live *liveLogin, maxAge time.Duration) {
	now := m.now()
	var due []int
	for _, s := range accounts.Slots {
		if s.Usable() && s.LastUsage.due(now, maxAge) {
			due = append(due, s.Number)
		}
	}
	if len(due) > 0 {
		m.refreshUsage(ctx, accounts, live, due)
		_ = m.Store.Save(accounts)
	}
}

// switchAmong tries candidates in order; a candidate whose login turns out
// dead is skipped unless it was named explicitly. Caller holds the store
// lock.
func (m *Manager) switchAmong(ctx context.Context, accounts *Accounts, live *liveLogin, candidates []int, explicit bool, opts SwitchOptions) (SwitchResult, error) {
	if len(candidates) == 0 {
		return SwitchResult{}, ErrNoTarget
	}
	from := 0
	if current := live.liveSlot(accounts); current != nil {
		from = current.Number
	}
	if opts.DryRun {
		return SwitchResult{From: from, To: *accounts.Slot(candidates[0]), DryRun: true}, nil
	}
	var lastErr error
	for _, n := range candidates {
		result, err := m.activate(ctx, accounts, n, opts.Cooldown)
		if err == nil {
			result.From = from
			return result, nil
		}
		lastErr = err
		var refreshErr *RefreshError
		if explicit || !errors.As(err, &refreshErr) || refreshErr.Kind == "invalid_client" {
			return SwitchResult{}, err
		}
	}
	return SwitchResult{}, fmt.Errorf("%w (last error: %v)", ErrNoTarget, lastErr)
}

// resolveSelector finds a slot by number, alias or email.
func resolveSelector(accounts *Accounts, selector string) (*Slot, error) {
	if n, err := strconv.Atoi(selector); err == nil {
		if slot := accounts.Slot(n); slot != nil {
			return slot, nil
		}
		return nil, fmt.Errorf("no account in slot %d", n)
	}
	for i := range accounts.Slots {
		if strings.EqualFold(accounts.Slots[i].Alias, selector) {
			return &accounts.Slots[i], nil
		}
	}
	var matches []*Slot
	for i := range accounts.Slots {
		if strings.EqualFold(accounts.Slots[i].Email, selector) {
			matches = append(matches, &accounts.Slots[i])
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("no stored account matches %q", selector)
	default:
		var numbers []string
		for _, s := range matches {
			numbers = append(numbers, strconv.Itoa(s.Number))
		}
		return nil, fmt.Errorf("%s is stored in several organizations (slots %s); use the slot number", selector, strings.Join(numbers, ", "))
	}
}

// rotation lists usable slots other than the live one, starting after it.
func rotation(accounts *Accounts, live *liveLogin) []Slot {
	start := accounts.Active
	if current := live.liveSlot(accounts); current != nil {
		start = current.Number
	}
	var after, before []Slot
	for _, s := range accounts.Slots {
		if !s.Usable() || s.Number == start {
			continue
		}
		if current := live.liveSlot(accounts); current != nil && current.Number == s.Number {
			continue
		}
		if s.Number > start {
			after = append(after, s)
		} else {
			before = append(before, s)
		}
	}
	return append(after, before...)
}

// strategyCandidates orders candidate slots for a strategy.
//   - rotation: the next usable slot after the active one, wrapping.
//   - best: lowest max(5h, 7d); accounts without usage come last.
//   - next-available: rotation order, skipping accounts known to be at
//     100% in either window.
func strategyCandidates(accounts *Accounts, live *liveLogin, strategy string, now time.Time) []int {
	order := rotation(accounts, live)
	var out []int
	switch strategy {
	case StrategyBest:
		type ranked struct {
			n     int
			value float64
			known bool
		}
		var list []ranked
		for _, s := range order {
			value, known := slotUsage(s).Max(now)
			list = append(list, ranked{s.Number, value, known})
		}
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].known != list[j].known {
				return list[i].known
			}
			return list[i].known && list[i].value < list[j].value
		})
		for _, r := range list {
			out = append(out, r.n)
		}
	case StrategyNextAvailable:
		for _, s := range order {
			u := slotUsage(s)
			if u != nil && (u.FiveHour != nil && u.FiveHour.effective(now) >= 100 || u.SevenDay != nil && u.SevenDay.effective(now) >= 100) {
				continue
			}
			out = append(out, s.Number)
		}
	default:
		for _, s := range order {
			out = append(out, s.Number)
		}
	}
	return out
}

func slotUsage(s Slot) *Usage {
	if s.LastUsage == nil {
		return nil
	}
	return s.LastUsage.Usage
}

// activate is the switch transaction. Caller holds the store lock.
//
//  1. Refresh the target's stored login if it expires soon; the rotated
//     token is persisted before anything else (network happens here, never
//     under Claude Code's locks).
//  2. Take Claude Code's locks and re-read the live login under them.
//  3. Capture the live login back into its slot (identity must match), or
//     keep a displaced copy when it belongs to no slot.
//  4. Write the credentials file atomically (0600), then splice oauthAccount
//     into the global config, preserving other keys and the file mode.
//  5. Record the active slot. Any failure in 4-5 restores both files from
//     the in-memory snapshots.
func (m *Manager) activate(ctx context.Context, accounts *Accounts, n int, cooldown time.Duration) (SwitchResult, error) {
	target := accounts.Slot(n)
	result := SwitchResult{}
	creds, err := m.Store.ReadCredentials(n)
	if err != nil {
		return result, fmt.Errorf("slot %d: stored login unreadable: %w", n, err)
	}
	account, err := m.Store.ReadAccount(n)
	if err != nil {
		return result, fmt.Errorf("slot %d: stored account unreadable: %w", n, err)
	}
	if creds.Tokens().RefreshToken == "" {
		target.Dead, target.DeadReason = true, "no_refresh_token"
		_ = m.Store.Save(accounts)
		return result, &RefreshError{Kind: "no_refresh_token"}
	}
	if expiresWithin(creds.Tokens(), m.now(), refreshMargin) {
		_, err := m.refreshStored(ctx, target, creds)
		if err != nil {
			_ = m.Store.Save(accounts)
			return result, fmt.Errorf("slot %d (%s): %w", n, target.Email, err)
		}
		result.Refreshed = true
	}

	paths, err := m.Env.Paths()
	if err != nil {
		return result, err
	}
	release, err := acquireClaudeLocks(ctx, paths, m.lockOptions())
	if err != nil {
		return result, err
	}
	defer release()
	live, err := m.readLive()
	if err != nil {
		return result, err
	}
	if live.globalErr != nil {
		return result, errors.New("global Claude config is not valid JSON; refusing to rewrite it")
	}
	now := m.now()
	current := live.liveSlot(accounts)
	switch {
	case current != nil && current.Number == n:
		return result, fmt.Errorf("slot %d is already the active Claude login", n)
	case current != nil && live.parsed != nil:
		if err := m.Store.WriteCredentials(current.Number, live.parsed); err != nil {
			return result, fmt.Errorf("capture the live login into slot %d: %w", current.Number, err)
		}
		if err := m.Store.WriteAccount(current.Number, live.account); err != nil {
			return result, fmt.Errorf("capture the live account into slot %d: %w", current.Number, err)
		}
		result.Captured = true
	case live.creds.exists || live.hasIdentity:
		reason := "live login belongs to no stored slot"
		if !live.hasIdentity {
			reason = "live login has no account identity"
		}
		path, err := m.Store.SaveDisplaced(now, reason, live.creds.data, live.account)
		if err != nil {
			return result, fmt.Errorf("keep a copy of the live login: %w", err)
		}
		result.Displaced = path
	}

	newCreds := creds.withSharedFrom(live.parsed).Marshal()
	newGlobal, err := spliceOAuthAccount(live.global.data, live.global.exists, account)
	if err != nil {
		return result, err
	}
	globalMode := live.global.mode
	if !live.global.exists {
		globalMode = secretFileMode
	}
	if err := ensurePrivateDir(live.paths.ConfigHome); err != nil {
		return result, err
	}
	rollback := func(cause error) error {
		var failed []string
		for _, snap := range []fileSnapshot{live.creds, live.global} {
			if err := restoreSnapshot(snap); err != nil {
				failed = append(failed, snap.path)
			}
		}
		if len(failed) > 0 {
			return fmt.Errorf("%v; rollback also failed for %s", cause, strings.Join(failed, ", "))
		}
		return fmt.Errorf("%v; previous login restored", cause)
	}
	if err := m.write(live.paths.CredentialsFile(), newCreds, secretFileMode); err != nil {
		return result, rollback(fmt.Errorf("write Claude credentials: %w", err))
	}
	if err := m.write(live.paths.GlobalConfig, newGlobal, globalMode); err != nil {
		return result, rollback(fmt.Errorf("write global Claude config: %w", err))
	}
	accounts.Active = n
	target.Dead, target.DeadReason = false, ""
	if err := m.Store.Save(accounts); err != nil {
		return result, rollback(fmt.Errorf("record the active slot: %w", err))
	}
	from := 0
	if current != nil {
		from = current.Number
	}
	_ = m.Store.SaveAutoState(AutoState{LastSwitchAt: now.UTC(), LastFrom: from, LastTo: n, CooldownUntil: now.Add(cooldown).UTC()})
	result.To = *target
	return result, nil
}

// restoreSnapshot puts a file back as it was; a file that did not exist is
// removed again.
func restoreSnapshot(snap fileSnapshot) error {
	if !snap.exists {
		if err := os.Remove(snap.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeAtomic(snap.path, snap.data, snap.mode)
}
