package claudeacct

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A profile is a separate Claude Code config home for one stored account, so
// several accounts can run side by side: an agent started with
// CLAUDE_CONFIG_DIR=<profile> uses that account while every other agent keeps
// the default home and its switchable login.
//
// A profile holds its own login, made by `claude auth login` inside it. It
// never receives a copy of a slot's tokens: Claude Code rotates the refresh
// token on every refresh, so two homes sharing one token lineage would log
// each other out.
//
// Everything that is not account state is linked to the base home, so
// transcripts, resume, settings, skills and history are the same in every
// profile and bp's readers of ~/.claude/projects keep working unchanged.

// ProfileMarker is the file that marks a directory as a bp account profile.
const ProfileMarker = ".bp-account-profile"

// DefaultAccount is the binding that selects the default home explicitly and
// stops inheritance from a parent's binding.
const DefaultAccount = "default"

// profileShared lists the base-home entries a profile links to. Credentials,
// the global config (.claude.json) and Claude Code's own account-scoped state
// (daemon, policy limits, remote settings, caches) are deliberately absent:
// they stay per profile.
var profileShared = []struct {
	name string
	dir  bool
}{
	{"projects", true},
	{"sessions", true},
	{"session-env", true},
	{"file-history", true},
	{"shell-snapshots", true},
	{"paste-cache", true},
	{"uploads", true},
	{"tasks", true},
	{"todos", true},
	{"plans", true},
	{"skills", true},
	{"commands", true},
	{"agents", true},
	{"plugins", true},
	{"output-styles", true},
	{"ide", true},
	{"settings.json", false},
	{"CLAUDE.md", false},
	{"history.jsonl", false},
}

type profileMarker struct {
	Version     int    `json:"version"`
	Email       string `json:"email"`
	AccountUUID string `json:"accountUuid"`
	// Base is the CLAUDE_CONFIG_DIR of the default home, empty for ~/.claude.
	Base string `json:"base,omitempty"`
}

// readProfileMarker returns the marker of dir, or ok=false when dir is not a
// profile.
func readProfileMarker(dir string) (profileMarker, bool) {
	data, err := os.ReadFile(filepath.Join(dir, ProfileMarker))
	if err != nil {
		return profileMarker{}, false
	}
	var marker profileMarker
	if json.Unmarshal(data, &marker) != nil {
		return profileMarker{}, false
	}
	return marker, true
}

// IsProfile reports whether dir is a bp account profile.
func IsProfile(dir string) bool {
	_, ok := readProfileMarker(dir)
	return ok
}

// DefaultConfigDir is the CLAUDE_CONFIG_DIR a launch on the default home
// must carry; set=false means the variable must be absent. A process running
// inside a profile (an agent bound to an account) has CLAUDE_CONFIG_DIR set
// to that profile, and the default home is the base the profile was made
// from, not the profile itself.
func (e Env) DefaultConfigDir() (value string, set bool) {
	getenv := e.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	dir := getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		return "", false
	}
	if marker, ok := readProfileMarker(filepath.Clean(dir)); ok {
		return marker.Base, marker.Base != ""
	}
	return dir, true
}

// ProfileDir is the profile directory of an account.
func (s *Store) ProfileDir(accountUUID string) string {
	return filepath.Join(s.root, "profiles", accountUUID)
}

// Profile describes one account profile.
type Profile struct {
	Dir  string `json:"dir"`
	Slot Slot   `json:"slot"`
	// Identity is the login the profile holds, if any.
	Identity    *Identity `json:"identity,omitempty"`
	HasLogin    bool      `json:"hasLogin"`
	Matches     bool      `json:"matches"`
	Subscribed  string    `json:"subscriptionType,omitempty"`
	TokenStatus string    `json:"tokenStatus,omitempty"`
	// Diverged lists shared entries the profile holds as its own files
	// instead of links to the base home.
	Diverged []string `json:"diverged,omitempty"`
}

// Ready reports whether an agent can be launched on the profile.
func (p Profile) Ready() bool { return p.HasLogin && p.Matches }

// Problem explains why the profile is not ready, or "".
func (p Profile) Problem() string {
	label := slotLabel(p.Slot)
	switch {
	case !p.HasLogin:
		return fmt.Sprintf("account %s has no login in its profile; run: bp account login %s", label, selectorFor(p.Slot))
	case !p.Matches:
		holder := "another account"
		if p.Identity != nil && p.Identity.Email != "" {
			holder = p.Identity.Email
		}
		return fmt.Sprintf("the profile of account %s is logged in as %s; run: bp account login %s", label, holder, selectorFor(p.Slot))
	}
	return ""
}

func selectorFor(s Slot) string {
	if s.Alias != "" {
		return s.Alias
	}
	return fmt.Sprint(s.Number)
}

// Resolve finds the stored account a selector (slot number, alias or email)
// names.
func (m *Manager) Resolve(selector string) (Slot, error) {
	accounts, err := m.Store.Load()
	if err != nil {
		return Slot{}, err
	}
	slot, err := resolveSelector(accounts, selector)
	if err != nil {
		return Slot{}, err
	}
	return *slot, nil
}

// EnsureProfile creates or repairs the profile of a stored account and
// reports its login. It links the shared entries to the default home, seeds
// the profile's global config from the default one (without its
// oauthAccount, which belongs to the default login) and never touches a
// profile's own credentials.
func (m *Manager) EnsureProfile(selector string) (Profile, error) {
	slot, err := m.Resolve(selector)
	if err != nil {
		return Profile{}, err
	}
	if slot.AccountUUID == "" {
		return Profile{}, fmt.Errorf("account %s has no account id", slotLabel(slot))
	}
	base, err := m.Env.Paths()
	if err != nil {
		return Profile{}, err
	}
	baseValue, _ := m.Env.DefaultConfigDir()
	dir := m.Store.ProfileDir(slot.AccountUUID)
	if filepath.Clean(base.ConfigHome) == filepath.Clean(dir) {
		return Profile{}, errors.New("the default Claude home is this profile; refusing to link it to itself")
	}
	if err := ensurePrivateDir(dir); err != nil {
		return Profile{}, err
	}
	marker := profileMarker{Version: 1, Email: slot.Email, AccountUUID: slot.AccountUUID, Base: baseValue}
	current, ok := readProfileMarker(dir)
	if !ok || current != marker {
		data, _ := json.MarshalIndent(marker, "", "  ")
		if err := writeAtomic(filepath.Join(dir, ProfileMarker), append(data, '\n'), secretFileMode); err != nil {
			return Profile{}, err
		}
	}
	var diverged []string
	for _, entry := range profileShared {
		target := filepath.Join(base.ConfigHome, entry.name)
		link := filepath.Join(dir, entry.name)
		if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
			if !entry.dir {
				// A missing shared file is linked once the default home has
				// one; until then Claude Code keeps the profile's own.
				if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink == 0 {
					diverged = append(diverged, entry.name)
				}
				continue
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return Profile{}, err
			}
		}
		info, err := os.Lstat(link)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Symlink(target, link); err != nil {
				return Profile{}, err
			}
		case err != nil:
			return Profile{}, err
		case info.Mode()&os.ModeSymlink != 0:
			if dest, _ := os.Readlink(link); dest != target {
				diverged = append(diverged, entry.name)
			}
		default:
			// Never replaced: it may hold the only copy of something.
			diverged = append(diverged, entry.name)
		}
	}
	if err := seedProfileConfig(base.GlobalConfig, filepath.Join(dir, ".claude.json")); err != nil {
		return Profile{}, err
	}
	profile := readProfile(dir, slot, m.now)
	profile.Diverged = diverged
	return profile, nil
}

// seedProfileConfig copies the default global config into a new profile so
// Claude Code starts there without onboarding, folder trust and MCP setup
// again. The default login's oauthAccount is left out. An existing profile
// config is never overwritten.
func seedProfileConfig(source, target string) error {
	if _, err := os.Lstat(target); err == nil || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := os.ReadFile(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read global Claude config: %w", err)
	}
	obj, err := parseOrderedObject(data)
	if err != nil {
		return errors.New("global Claude config is not valid JSON; refusing to seed a profile from it")
	}
	obj.remove("oauthAccount")
	out, err := obj.marshal(true)
	if err != nil {
		return err
	}
	return writeAtomic(target, out, secretFileMode)
}

func (o *orderedObject) remove(key string) {
	keys, values := o.keys[:0], o.values[:0]
	for i, name := range o.keys {
		if name != key {
			keys, values = append(keys, name), append(values, o.values[i])
		}
	}
	o.keys, o.values = keys, values
}

// readProfile reads the login a profile holds. Token values are only
// inspected for presence and expiry.
func readProfile(dir string, slot Slot, now func() time.Time) Profile {
	profile := Profile{Dir: dir, Slot: slot}
	paths := ClaudePaths{ConfigHome: dir, GlobalConfig: filepath.Join(dir, ".claude.json")}
	if data, err := os.ReadFile(paths.CredentialsFile()); err == nil {
		if creds, err := ParseCredentials(data); err == nil {
			tokens := creds.Tokens()
			profile.HasLogin = tokens.RefreshToken != "" || tokens.AccessToken != ""
			profile.Subscribed = creds.SubscriptionType()
			profile.TokenStatus = liveTokenStatus(tokens, now())
		}
	}
	if data, err := os.ReadFile(paths.GlobalConfig); err == nil {
		if obj, err := parseOrderedObject(data); err == nil {
			if raw, ok := obj.get("oauthAccount"); ok {
				if id, err := parseOAuthAccount(raw); err == nil {
					profile.Identity = &id
					profile.Matches = id.Same(slot.Identity())
				}
			}
		}
	}
	return profile
}

// Profiles reports every stored account's profile without creating any.
func (m *Manager) Profiles() ([]Profile, error) {
	accounts, err := m.Store.Load()
	if err != nil {
		return nil, err
	}
	var out []Profile
	for _, slot := range accounts.Slots {
		if slot.AccountUUID == "" {
			continue
		}
		dir := m.Store.ProfileDir(slot.AccountUUID)
		if !IsProfile(dir) {
			continue
		}
		out = append(out, readProfile(dir, slot, m.now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slot.Number < out[j].Slot.Number })
	return out, nil
}

// LaunchDir resolves an agentbook binding to the CLAUDE_CONFIG_DIR an agent
// must be started with. "" or "default" is the default home (dir ""). Any
// other binding must name a stored account whose profile holds that
// account's login.
func (m *Manager) LaunchDir(binding string) (string, Slot, error) {
	binding = strings.TrimSpace(binding)
	if binding == "" || strings.EqualFold(binding, DefaultAccount) {
		return "", Slot{}, nil
	}
	profile, err := m.EnsureProfile(binding)
	if err != nil {
		return "", Slot{}, err
	}
	if !profile.Ready() {
		return "", profile.Slot, errors.New(profile.Problem())
	}
	return profile.Dir, profile.Slot, nil
}
