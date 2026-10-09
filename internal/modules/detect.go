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
	// When the evidence is uncertain, enable: a module wrongly left on behaves
	// as before, one wrongly turned off silently drops a feature in use.
	sessions := cfg.Legacy || booksInUse(cfg) || fileExists(filepath.Join(cfg.Home, "main", "onboarding.json"))
	if !sessions && env.UserHome != "" {
		sessions = len(rcLinePresent(env)) > 0 || bpShellPresent(env.UserHome)
	}
	// A daemon that ever ran served the dashboard and ran the monitor jobs
	// (which still skip themselves when their tools are missing).
	daemon := cfg.StateDir != "" && fileExists(filepath.Join(cfg.StateDir, "jobs.json"))
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
	if cfg.Legacy || daemon || cfg.UsageBin != "" || cfg.UsageHistory != "" {
		found[UI] = true
	}
	if cfg.Legacy || daemon {
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

// bpShellPresent reports the wrappers an older bp setup always wrote, even
// when the rc line sourcing them was removed or lives in a file bp cannot see.
func bpShellPresent(home string) bool {
	data, err := os.ReadFile(ShellPath(home))
	return err == nil && strings.HasPrefix(string(data), ShellMarker) && string(data) != DisabledShell
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
		// Claim shell.sh only while it is exactly a text bp generates; a copy
		// the user edited stays theirs.
		if data, err := os.ReadFile(ShellPath(env.UserHome)); err == nil && generatedShell(string(data)) {
			journal.Record(Change{Kind: KindFile, Path: ShellPath(env.UserHome), Marker: ShellMarker, SHA256: fileSHA256(data)})
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

// DisabledShell is the stub bp setup --disable leaves in shell.sh.
const DisabledShell = ShellMarker + " Disabled; native aliases and records are preserved.\n"

func generatedShell(text string) bool {
	return text == LocalShell || text == LocalShell+CompatibilityWrappers || text == DisabledShell
}
