package claudeacct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"
)

// Slot is one stored account. It holds no secrets: tokens live in
// slots/<n>/credentials.json and the identity object in
// slots/<n>/oauthAccount.json.
type Slot struct {
	Number      int         `json:"number"`
	Email       string      `json:"email"`
	AccountUUID string      `json:"accountUuid"`
	OrgUUID     string      `json:"orgUuid"`
	OrgName     string      `json:"orgName,omitempty"`
	Alias       string      `json:"alias,omitempty"`
	Disabled    bool        `json:"disabled,omitempty"`
	Dead        bool        `json:"dead,omitempty"`
	DeadReason  string      `json:"deadReason,omitempty"`
	AddedAt     time.Time   `json:"addedAt"`
	LastUsage   *UsageCache `json:"lastUsage,omitempty"`
}

// Identity returns the slot's account identity.
func (s Slot) Identity() Identity {
	return Identity{Email: s.Email, AccountUUID: s.AccountUUID, OrgUUID: s.OrgUUID, OrgName: s.OrgName}
}

// Usable reports whether strategies may pick the slot.
func (s Slot) Usable() bool { return !s.Disabled && !s.Dead }

// UsageCache is the last usage answer for a slot plus its failure backoff.
type UsageCache struct {
	Usage *Usage `json:"usage,omitempty"`
	// FetchedAt is when Usage was fetched successfully.
	FetchedAt time.Time `json:"fetchedAt,omitempty"`
	// AttemptAt is the latest fetch attempt, successful or not.
	AttemptAt    time.Time `json:"attemptAt,omitempty"`
	Error        string    `json:"error,omitempty"`
	Failures     int       `json:"failures,omitempty"`
	BackoffUntil time.Time `json:"backoffUntil,omitempty"`
}

// Accounts is accounts.json.
type Accounts struct {
	Version int    `json:"version"`
	Active  int    `json:"active,omitempty"`
	Slots   []Slot `json:"slots"`
}

// Slot returns a pointer into a for slot n.
func (a *Accounts) Slot(n int) *Slot {
	for i := range a.Slots {
		if a.Slots[i].Number == n {
			return &a.Slots[i]
		}
	}
	return nil
}

// ByIdentity returns the slot holding id.
func (a *Accounts) ByIdentity(id Identity) *Slot {
	for i := range a.Slots {
		if a.Slots[i].Identity().Same(id) {
			return &a.Slots[i]
		}
	}
	return nil
}

func (a *Accounts) sortSlots() {
	sort.Slice(a.Slots, func(i, j int) bool { return a.Slots[i].Number < a.Slots[j].Number })
}

func (a *Accounts) freeNumber() int {
	for n := 1; ; n++ {
		if a.Slot(n) == nil {
			return n
		}
	}
}

// AutoState is auto-state.json.
type AutoState struct {
	LastSwitchAt  time.Time `json:"lastSwitchAt,omitempty"`
	LastFrom      int       `json:"lastFrom,omitempty"`
	LastTo        int       `json:"lastTo,omitempty"`
	CooldownUntil time.Time `json:"cooldownUntil,omitempty"`
}

// Store is <stateDir>/claude-accounts.
type Store struct {
	root string
}

// NewStore returns the store under stateDir. Nothing is created until a
// mutation.
func NewStore(stateDir string) *Store {
	return &Store{root: filepath.Join(stateDir, "claude-accounts")}
}

// Root is the store directory.
func (s *Store) Root() string { return s.root }

func (s *Store) accountsFile() string  { return filepath.Join(s.root, "accounts.json") }
func (s *Store) autoStateFile() string { return filepath.Join(s.root, "auto-state.json") }
func (s *Store) slotDir(n int) string  { return filepath.Join(s.root, "slots", strconv.Itoa(n)) }
func (s *Store) slotCredentials(n int) string {
	return filepath.Join(s.slotDir(n), "credentials.json")
}
func (s *Store) slotAccount(n int) string { return filepath.Join(s.slotDir(n), "oauthAccount.json") }

func (s *Store) ensureRoot() error { return ensurePrivateDir(s.root) }

// Lock serializes every bp mutation of the store with a flock on
// <root>/.lock. It waits until ctx is done.
func (s *Store) Lock(ctx context.Context) (func(), error) {
	if err := s.ensureRoot(); err != nil {
		return nil, fmt.Errorf("create account store: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(s.root, ".lock"), os.O_RDWR|os.O_CREATE, secretFileMode)
	if err != nil {
		return nil, fmt.Errorf("open account store lock: %w", err)
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = file.Close()
			return nil, fmt.Errorf("lock account store: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, errors.New("another bp account command is running; retry")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Load reads accounts.json; a missing file is an empty store.
func (s *Store) Load() (*Accounts, error) {
	data, err := os.ReadFile(s.accountsFile())
	if errors.Is(err, os.ErrNotExist) {
		return &Accounts{Version: 1}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read account store: %w", err)
	}
	var accounts Accounts
	if err := json.Unmarshal(data, &accounts); err != nil {
		return nil, fmt.Errorf("parse %s: %v", s.accountsFile(), err)
	}
	if accounts.Version == 0 {
		accounts.Version = 1
	}
	accounts.sortSlots()
	return &accounts, nil
}

// Save writes accounts.json atomically with mode 0600.
func (s *Store) Save(accounts *Accounts) error {
	if err := s.ensureRoot(); err != nil {
		return err
	}
	accounts.sortSlots()
	data, err := json.MarshalIndent(accounts, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.accountsFile(), append(data, '\n'), secretFileMode)
}

// ReadCredentials loads slot n's stored login.
func (s *Store) ReadCredentials(n int) (*Credentials, error) {
	data, err := os.ReadFile(s.slotCredentials(n))
	if err != nil {
		return nil, fmt.Errorf("read slot %d credentials: %w", n, err)
	}
	creds, err := ParseCredentials(data)
	if err != nil {
		return nil, fmt.Errorf("slot %d: %w", n, err)
	}
	return creds, nil
}

// ReadAccount loads slot n's raw oauthAccount object.
func (s *Store) ReadAccount(n int) (json.RawMessage, error) {
	data, err := os.ReadFile(s.slotAccount(n))
	if err != nil {
		return nil, fmt.Errorf("read slot %d account: %w", n, err)
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("slot %d account file is not valid JSON", n)
	}
	return json.RawMessage(data), nil
}

// WriteCredentials stores the account-owned part of creds in slot n.
func (s *Store) WriteCredentials(n int, creds *Credentials) error {
	if err := ensurePrivateDir(s.slotDir(n)); err != nil {
		return err
	}
	return writeAtomic(s.slotCredentials(n), creds.accountOwned().Marshal(), secretFileMode)
}

// WriteAccount stores slot n's raw oauthAccount object.
func (s *Store) WriteAccount(n int, account json.RawMessage) error {
	if err := ensurePrivateDir(s.slotDir(n)); err != nil {
		return err
	}
	return writeAtomic(s.slotAccount(n), account, secretFileMode)
}

// RetireSlot moves slot n's files to removed/<timestamp>-slot<n>. Stored
// logins are retired, not deleted, like displaced copies.
func (s *Store) RetireSlot(n int, now time.Time) (string, error) {
	removed := filepath.Join(s.root, "removed")
	if err := ensurePrivateDir(removed); err != nil {
		return "", err
	}
	if _, err := os.Stat(s.slotDir(n)); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	base := fmt.Sprintf("%s-slot%d", now.UTC().Format("20060102T150405.000000000Z"), n)
	for i := 0; ; i++ {
		target := filepath.Join(removed, base)
		if i > 0 {
			target = fmt.Sprintf("%s-%d", target, i)
		}
		if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
			return target, os.Rename(s.slotDir(n), target)
		}
	}
}

// SaveDisplaced keeps a safety copy of a live login bp is about to replace
// without being able to store it in a slot. Copies are never deleted.
func (s *Store) SaveDisplaced(now time.Time, reason string, credentials []byte, account json.RawMessage) (string, error) {
	dir := filepath.Join(s.root, "displaced")
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	record := struct {
		SavedAt      time.Time       `json:"savedAt"`
		Reason       string          `json:"reason"`
		Credentials  json.RawMessage `json:"credentials,omitempty"`
		OAuthAccount json.RawMessage `json:"oauthAccount,omitempty"`
	}{SavedAt: now.UTC(), Reason: reason}
	if json.Valid(credentials) {
		record.Credentials = credentials
	} else if len(credentials) > 0 {
		quoted, _ := json.Marshal(string(credentials))
		record.Credentials = quoted
	}
	if json.Valid(account) {
		record.OAuthAccount = account
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	base := now.UTC().Format("20060102T150405.000000000Z")
	for i := 0; ; i++ {
		name := base + ".json"
		if i > 0 {
			name = fmt.Sprintf("%s-%d.json", base, i)
		}
		path := filepath.Join(dir, name)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, secretFileMode)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err = file.Write(data); err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		return path, err
	}
}

// LoadAutoState reads auto-state.json; missing means never switched.
func (s *Store) LoadAutoState() (AutoState, error) {
	var state AutoState
	data, err := os.ReadFile(s.autoStateFile())
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return AutoState{}, fmt.Errorf("parse %s: %v", s.autoStateFile(), err)
	}
	return state, nil
}

// SaveAutoState writes auto-state.json atomically.
func (s *Store) SaveAutoState(state AutoState) error {
	if err := s.ensureRoot(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.autoStateFile(), append(data, '\n'), secretFileMode)
}
