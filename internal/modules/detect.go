package modules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"blueprint/internal/config"
)

// Detect reports the modules an install already uses, judged from its config
// and bp's own files. It is the upgrade migration for configs written before
// modules existed and never writes anything.
func Detect(cfg config.Config, env Env) map[string]bool {
	found := map[string]bool{}
	sessions := cfg.Legacy || booksInUse(cfg) || fileExists(filepath.Join(cfg.Home, "main", "onboarding.json"))
	if !sessions && env.UserHome != "" {
		sessions = len(rcLinePresent(env)) > 0
	}
	if sessions {
		found[Sessions] = true
		// bp styles every session it opens today: the bar comes with sessions.
		found[Bar] = true
	}
	if cfg.ClaudeAccounts.AutoSwitch || cfg.ClaudeAccounts.KeepAlive || len(cfg.ClaudeAccounts.Limits) > 0 || accountsStored(cfg) {
		found[Accounts] = true
	}
	// bp wa needs only the outbox; the bridge may run elsewhere.
	if cfg.WAOutbox != "" {
		found[WA] = true
	}
	if cfg.Legacy || cfg.UsageBin != "" || cfg.UsageHistory != "" {
		found[UI] = true
	}
	if cfg.Legacy {
		found[Monitor] = true
	}
	return found
}

// booksInUse reports a book with agents or a real coordinator. The installer's
// empty placeholder book ({"orchestrator":"local","agents":[]}) is not use.
func booksInUse(cfg config.Config) bool {
	for _, path := range cfg.Agentbooks {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var book struct {
			Orchestrator string            `json:"orchestrator"`
			Agents       []json.RawMessage `json:"agents"`
		}
		if json.Unmarshal(data, &book) != nil {
			// An unreadable book still means someone uses it.
			return true
		}
		if len(book.Agents) > 0 || (book.Orchestrator != "" && book.Orchestrator != "local") {
			return true
		}
	}
	return false
}

func accountsStored(cfg config.Config) bool {
	if cfg.StateDir == "" {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(cfg.StateDir, "claude-accounts", "slots"))
	return err == nil && len(entries) > 0
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// recordExisting journals environment changes an older bp made before
// journals existed, so disable and uninstall can remove them later.
func recordExisting(env Env, enabled map[string]bool) error {
	cfg := env.Config
	if cfg.StateDir == "" {
		return nil
	}
	if enabled[Sessions] && env.UserHome != "" {
		journal, err := LoadJournal(cfg, Sessions)
		if err != nil {
			return err
		}
		if data, err := os.ReadFile(ShellPath(env.UserHome)); err == nil && strings.Contains(string(data), ShellMarker) {
			journal.Record(Change{Kind: KindFile, Path: ShellPath(env.UserHome), Marker: ShellMarker})
		}
		for _, path := range rcLinePresent(env) {
			journal.Record(Change{Kind: KindLine, Path: path, Line: RCLine})
		}
		if err := journal.Save(cfg); err != nil {
			return err
		}
	}
	return nil
}
