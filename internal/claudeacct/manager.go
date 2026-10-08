package claudeacct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// refreshMargin: an inactive login is refreshed only when its access
	// token expires within this margin (or when switching onto it).
	refreshMargin = 5 * time.Minute
	// ListMaxAge is how old cached usage may be before list refetches it.
	ListMaxAge          = 5 * time.Minute
	defaultUsageTimeout = 5 * time.Second
	storeLockWait       = 30 * time.Second
)

// Manager performs every account operation. Zero-value optional fields fall
// back to production defaults.
type Manager struct {
	Store  *Store
	Env    Env
	Client *Client
	Now    func() time.Time
	// LockTimeout is the per-lock wait for Claude Code's locks (default 9s).
	LockTimeout time.Duration
	// LockTouch is the mtime touch interval while holding them (default 3s).
	LockTouch time.Duration
	// UsageTimeout bounds one usage fetch (default 5s).
	UsageTimeout time.Duration
	// Pinger sends keepalive prompts (default ExecPinger).
	Pinger Pinger

	// writeLive replaces a live Claude Code file; tests inject failures.
	writeLive func(path string, data []byte, mode os.FileMode) error
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) lockOptions() lockOptions {
	opts := lockOptions{timeout: m.LockTimeout, touch: m.LockTouch}
	if opts.timeout <= 0 {
		opts.timeout = defaultLockTimeout
	}
	if opts.touch <= 0 {
		opts.touch = lockTouchInterval
	}
	return opts
}

func (m *Manager) write(path string, data []byte, mode os.FileMode) error {
	if m.writeLive != nil {
		return m.writeLive(path, data, mode)
	}
	return writeAtomic(path, data, mode)
}

func (m *Manager) client() *Client {
	if m.Client != nil {
		return m.Client
	}
	return &Client{Now: m.Now}
}

func (m *Manager) lockStore(ctx context.Context) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, storeLockWait)
	defer cancel()
	return m.Store.Lock(ctx)
}

// liveLogin is Claude Code's current login as read from disk.
type liveLogin struct {
	paths       ClaudePaths
	creds       fileSnapshot
	global      fileSnapshot
	parsed      *Credentials
	account     json.RawMessage
	identity    Identity
	hasIdentity bool
	globalErr   error
}

func (m *Manager) readLive() (*liveLogin, error) {
	paths, err := m.Env.Paths()
	if err != nil {
		return nil, err
	}
	live := &liveLogin{paths: paths}
	if live.creds, err = snapshotFile(paths.CredentialsFile()); err != nil {
		return nil, fmt.Errorf("read Claude credentials: %w", err)
	}
	if live.creds.exists {
		live.parsed, _ = ParseCredentials(live.creds.data)
	}
	if live.global, err = snapshotFile(paths.GlobalConfig); err != nil {
		return nil, fmt.Errorf("read global Claude config: %w", err)
	}
	if live.global.exists && len(strings.TrimSpace(string(live.global.data))) > 0 {
		obj, parseErr := parseOrderedObject(live.global.data)
		if parseErr != nil {
			live.globalErr = errors.New("global Claude config is not valid JSON")
		} else if raw, ok := obj.get("oauthAccount"); ok {
			if id, idErr := parseOAuthAccount(raw); idErr == nil {
				live.account, live.identity, live.hasIdentity = raw, id, true
			}
		}
	}
	return live, nil
}

// liveSlot is the stored slot matching the live identity, or nil.
func (l *liveLogin) liveSlot(accounts *Accounts) *Slot {
	if !l.hasIdentity {
		return nil
	}
	return accounts.ByIdentity(l.identity)
}

// AddResult reports what bp account add stored.
type AddResult struct {
	Slot    Slot
	Updated bool
}

// Add captures the current Claude Code login into a slot. The same account
// and organization as an existing slot updates that slot.
func (m *Manager) Add(ctx context.Context, number int, alias string) (AddResult, error) {
	if number < 0 {
		return AddResult{}, errors.New("slot number must be positive")
	}
	unlock, err := m.lockStore(ctx)
	if err != nil {
		return AddResult{}, err
	}
	defer unlock()
	accounts, err := m.Store.Load()
	if err != nil {
		return AddResult{}, err
	}
	live, err := m.readLive()
	if err != nil {
		return AddResult{}, err
	}
	if live.parsed == nil {
		return AddResult{}, fmt.Errorf("no Claude Code login in %s; run claude and log in first", live.paths.CredentialsFile())
	}
	if live.parsed.Tokens().RefreshToken == "" {
		return AddResult{}, errors.New("the current Claude Code login has no refresh token; it cannot be stored")
	}
	if !live.hasIdentity {
		if live.globalErr != nil {
			return AddResult{}, live.globalErr
		}
		return AddResult{}, fmt.Errorf("%s has no oauthAccount; run claude once after logging in", live.paths.GlobalConfig)
	}
	if alias != "" {
		if err := validateAlias(accounts, alias, 0); err != nil {
			return AddResult{}, err
		}
	}
	slot := accounts.ByIdentity(live.identity)
	updated := slot != nil
	switch {
	case slot != nil && number != 0 && number != slot.Number:
		return AddResult{}, fmt.Errorf("%s is already stored in slot %d", live.identity.Email, slot.Number)
	case slot == nil && number != 0 && accounts.Slot(number) != nil:
		return AddResult{}, fmt.Errorf("slot %d holds a different account; choose another slot", number)
	case slot == nil:
		if number == 0 {
			number = accounts.freeNumber()
		}
		accounts.Slots = append(accounts.Slots, Slot{Number: number, AddedAt: m.now().UTC()})
		accounts.sortSlots()
		slot = accounts.Slot(number)
	}
	if alias != "" {
		if err := validateAlias(accounts, alias, slot.Number); err != nil {
			return AddResult{}, err
		}
		slot.Alias = alias
	}
	id := live.identity
	slot.Email, slot.AccountUUID, slot.OrgUUID, slot.OrgName = id.Email, id.AccountUUID, id.OrgUUID, id.OrgName
	slot.Dead, slot.DeadReason = false, ""
	if err := m.Store.WriteCredentials(slot.Number, live.parsed); err != nil {
		return AddResult{}, fmt.Errorf("store slot %d credentials: %w", slot.Number, err)
	}
	if err := m.Store.WriteAccount(slot.Number, live.account); err != nil {
		return AddResult{}, fmt.Errorf("store slot %d account: %w", slot.Number, err)
	}
	accounts.Active = slot.Number
	result := AddResult{Slot: *slot, Updated: updated}
	if err := m.Store.Save(accounts); err != nil {
		return AddResult{}, err
	}
	return result, nil
}

func validateAlias(accounts *Accounts, alias string, owner int) error {
	if alias == "" || strings.ContainsAny(alias, "@ \t\n/") {
		return fmt.Errorf("invalid alias %q: use letters, digits, - or _", alias)
	}
	if _, err := strconv.Atoi(alias); err == nil {
		return fmt.Errorf("invalid alias %q: a number would be read as a slot", alias)
	}
	for _, slot := range accounts.Slots {
		if slot.Number != owner && strings.EqualFold(slot.Alias, alias) {
			return fmt.Errorf("alias %q is already used by slot %d", alias, slot.Number)
		}
	}
	return nil
}

// mutateSlot runs change on slot n under the store lock and saves.
func (m *Manager) mutateSlot(ctx context.Context, n int, change func(*Accounts, *Slot, *liveLogin) error) (Slot, error) {
	unlock, err := m.lockStore(ctx)
	if err != nil {
		return Slot{}, err
	}
	defer unlock()
	accounts, err := m.Store.Load()
	if err != nil {
		return Slot{}, err
	}
	slot := accounts.Slot(n)
	if slot == nil {
		return Slot{}, fmt.Errorf("no account in slot %d", n)
	}
	live, err := m.readLive()
	if err != nil {
		return Slot{}, err
	}
	if err := change(accounts, slot, live); err != nil {
		return Slot{}, err
	}
	result := *slot
	return result, m.Store.Save(accounts)
}

// SetAlias sets or clears (alias "") slot n's alias.
func (m *Manager) SetAlias(ctx context.Context, n int, alias string) (Slot, error) {
	return m.mutateSlot(ctx, n, func(accounts *Accounts, slot *Slot, _ *liveLogin) error {
		if alias != "" {
			if err := validateAlias(accounts, alias, n); err != nil {
				return err
			}
		}
		slot.Alias = alias
		return nil
	})
}

// SetDisabled disables or enables slot n for switching.
func (m *Manager) SetDisabled(ctx context.Context, n int, disabled bool) (Slot, error) {
	return m.mutateSlot(ctx, n, func(_ *Accounts, slot *Slot, _ *liveLogin) error {
		slot.Disabled = disabled
		return nil
	})
}

// Remove forgets slot n. The active slot is refused; the slot's files are
// moved to removed/ rather than deleted.
func (m *Manager) Remove(ctx context.Context, n int) (Slot, error) {
	var removed Slot
	_, err := m.mutateSlot(ctx, n, func(accounts *Accounts, slot *Slot, live *liveLogin) error {
		if current := live.liveSlot(accounts); current != nil && current.Number == n {
			return fmt.Errorf("slot %d is the active Claude login; switch to another account first", n)
		}
		if _, err := m.Store.RetireSlot(n, m.now()); err != nil {
			return fmt.Errorf("retire slot %d files: %w", n, err)
		}
		removed = *slot
		kept := accounts.Slots[:0]
		for _, s := range accounts.Slots {
			if s.Number != n {
				kept = append(kept, s)
			}
		}
		accounts.Slots = kept
		if accounts.Active == n {
			accounts.Active = 0
		}
		return nil
	})
	return removed, err
}

// SlotView is one listed slot. It carries no secrets.
type SlotView struct {
	Slot
	Active       bool   `json:"active"`
	Token        string `json:"token"`
	Subscription string `json:"subscription,omitempty"`
}

// Overview is what list and status show.
type Overview struct {
	ConfigHome   string     `json:"configHome"`
	GlobalConfig string     `json:"globalConfig"`
	Live         *Identity  `json:"live,omitempty"`
	LiveSlot     int        `json:"liveSlot,omitempty"`
	LiveToken    string     `json:"liveToken"`
	Slots        []SlotView `json:"slots"`
	Now          time.Time  `json:"now"`
}

// OverviewOptions selects how much network a listing may use.
type OverviewOptions struct {
	// Fetch refetches usage older than MaxAge; Force refetches everything
	// not in backoff.
	Fetch  bool
	Force  bool
	MaxAge time.Duration
}

// Overview lists every slot with usage, refetching stale usage as asked.
func (m *Manager) Overview(ctx context.Context, opts OverviewOptions) (*Overview, error) {
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
	if opts.Fetch {
		maxAge := opts.MaxAge
		if maxAge <= 0 {
			maxAge = ListMaxAge
		}
		var due []int
		now := m.now()
		for _, slot := range accounts.Slots {
			if !slot.Dead && (opts.Force && !now.Before(backoffEnd(slot.LastUsage)) || slot.LastUsage.due(now, maxAge)) {
				due = append(due, slot.Number)
			}
		}
		if len(due) > 0 {
			m.refreshUsage(ctx, accounts, live, due)
			if err := m.Store.Save(accounts); err != nil {
				return nil, err
			}
		}
	}
	return m.overview(accounts, live), nil
}

func backoffEnd(cache *UsageCache) time.Time {
	if cache == nil {
		return time.Time{}
	}
	return cache.BackoffUntil
}

func (m *Manager) overview(accounts *Accounts, live *liveLogin) *Overview {
	now := m.now()
	view := &Overview{ConfigHome: live.paths.ConfigHome, GlobalConfig: live.paths.GlobalConfig, Now: now}
	if live.hasIdentity {
		id := live.identity
		view.Live = &id
	}
	current := live.liveSlot(accounts)
	if current != nil {
		view.LiveSlot = current.Number
	}
	switch {
	case live.parsed != nil:
		view.LiveToken = liveTokenStatus(live.parsed.Tokens(), now)
	case live.creds.exists:
		view.LiveToken = "unreadable"
	default:
		view.LiveToken = "not logged in"
	}
	for _, slot := range accounts.Slots {
		sv := SlotView{Slot: slot, Active: current != nil && current.Number == slot.Number}
		if sv.Active && live.parsed != nil {
			sv.Token = view.LiveToken
			sv.Subscription = live.parsed.SubscriptionType()
		} else if creds, err := m.Store.ReadCredentials(slot.Number); err != nil {
			sv.Token = "missing"
		} else {
			sv.Token = storedTokenStatus(creds.Tokens(), slot, now)
			sv.Subscription = creds.SubscriptionType()
		}
		view.Slots = append(view.Slots, sv)
	}
	return view
}

// refreshUsage fetches usage for the given slots and records the results in
// accounts (the caller saves). The live account uses the live access token
// and is never refreshed; an inactive account is refreshed only when its
// token expires within refreshMargin, and the rotated token is persisted
// before anything else. Caller holds the store lock.
func (m *Manager) refreshUsage(ctx context.Context, accounts *Accounts, live *liveLogin, numbers []int) {
	now := m.now()
	current := live.liveSlot(accounts)
	type job struct {
		slot  *Slot
		token string
	}
	var jobs []job
	for _, n := range numbers {
		slot := accounts.Slot(n)
		if slot == nil || slot.Dead {
			continue
		}
		if current != nil && current.Number == n {
			if live.parsed == nil {
				continue
			}
			tokens := live.parsed.Tokens()
			if tokens.AccessToken == "" || expiredAt(tokens, now) {
				// Never refresh the live login; Claude Code owns it.
				continue
			}
			jobs = append(jobs, job{slot, tokens.AccessToken})
			continue
		}
		creds, err := m.Store.ReadCredentials(n)
		if err != nil {
			continue
		}
		tokens := creds.Tokens()
		if expiresWithin(tokens, now, refreshMargin) {
			rotated, err := m.refreshStored(ctx, slot, creds)
			if err != nil {
				slot.LastUsage = recordUsage(slot.LastUsage, now, nil, err)
				continue
			}
			tokens = rotated
		}
		jobs = append(jobs, job{slot, tokens.AccessToken})
	}
	timeout := m.UsageTimeout
	if timeout <= 0 {
		timeout = defaultUsageTimeout
	}
	type answer struct {
		usage *Usage
		err   error
	}
	answers := make([]answer, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, token string) {
			defer wg.Done()
			fetchCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			usage, err := m.client().FetchUsage(fetchCtx, token)
			answers[i] = answer{usage, err}
		}(i, j.token)
	}
	wg.Wait()
	for i, j := range jobs {
		j.slot.LastUsage = recordUsage(j.slot.LastUsage, m.now(), answers[i].usage, answers[i].err)
	}
}

// refreshStored refreshes an inactive slot's login and persists the rotated
// token at once. A dead login marks the slot dead (the caller saves).
func (m *Manager) refreshStored(ctx context.Context, slot *Slot, creds *Credentials) (Tokens, error) {
	rotated, err := m.client().Refresh(ctx, creds.Tokens())
	if err != nil {
		var refreshErr *RefreshError
		if errors.As(err, &refreshErr) && refreshErr.Dead() {
			slot.Dead, slot.DeadReason = true, refreshErr.Kind
		}
		return Tokens{}, err
	}
	creds.SetTokens(rotated)
	if err := m.Store.WriteCredentials(slot.Number, creds); err != nil {
		// The old refresh token is already spent; say so loudly.
		return Tokens{}, fmt.Errorf("slot %d: refreshed login could not be stored (%v); log in again and re-add it", slot.Number, err)
	}
	return rotated, nil
}

func expiredAt(t Tokens, now time.Time) bool {
	return t.ExpiresAt > 0 && !now.Before(t.Expiry())
}

func expiresWithin(t Tokens, now time.Time, margin time.Duration) bool {
	return t.AccessToken == "" || t.ExpiresAt <= 0 || !now.Add(margin).Before(t.Expiry())
}
