// Package modules owns bp's opt-in modules: every feature that changes the
// user's environment (tmux options, shell rc files, daemon jobs, credentials)
// belongs to one module and runs only while that module is enabled.
//
// Enabled state lives in the config file under the "modules" key. What enable
// changed outside bp's own home is recorded in a journal per module
// (<stateDir>/modules/<name>.json) so disable and bp uninstall undo exactly
// that and nothing else.
package modules

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"blueprint/internal/audit"
	"blueprint/internal/config"
	"blueprint/internal/safefile"
)

// Module names.
const (
	Sessions = "sessions"
	Bar      = "bar"
	Accounts = "accounts"
	WA       = "wa"
	UI       = "ui"
	Monitor  = "monitor"
	// GuardHooks is the tool-call tripwire (docs/security/guard-hooks-module.md).
	GuardHooks = "guard-hooks"
	// CompactionHooks restates the bp identity to OpenCode and Hermes agents
	// after a context compaction (docs/security/compaction-hooks-module.md).
	// Claude already does this through its own SessionStart hook.
	CompactionHooks = "compaction-hooks"
)

// Module describes one opt-in feature.
type Module struct {
	Name        string
	Description string
	// Owns lists the config keys whose settings only matter while the module
	// is enabled.
	Owns []string
	// Conflict reports reasons not to enable the module on this machine.
	Conflict func(Env) []string
	// Apply makes the module's environment changes and records each one in the
	// journal. Nil means enabling only flips the config switch.
	Apply func(Env, *Journal) error
	// Notes are printed after enable, for example where the feature shows up.
	Notes string
}

var registry = []Module{
	{
		Name:        Sessions,
		Description: "open, close and resume agents in tmux; shell wrappers so agents survive a closed terminal; the daemon reopens the coordinator",
		Owns:        []string{"lifecycle", "localObservation", "localMouse"},
		Conflict:    sessionsConflicts,
		Apply:       applySessions,
		Notes:       "open a new terminal, or run: . \"$HOME/.config/bp/shell.sh\"",
	},
	{
		Name:        Bar,
		Description: "tmux status bar with live agent state on the sessions bp opens",
		Owns:        []string{"bar"},
		Conflict:    barConflicts,
	},
	{
		Name:        Accounts,
		Description: "several Claude accounts, usage limits, automatic switching and staggered keepalive",
		Owns:        []string{"claudeAccounts"},
		Conflict:    accountsConflicts,
	},
	{
		Name:        WA,
		Description: "WhatsApp bridge supervised by the daemon",
		Owns:        []string{"waBridge", "waOutbox", "waStore"},
		Conflict:    waConflicts,
	},
	{
		Name:        UI,
		Description: "local web interface (dashboard on 127.0.0.1:8787, served by the daemon)",
		Owns:        []string{"usageBin", "usageHistory"},
	},
	{
		Name:        Monitor,
		Description: "server monitoring jobs: usage pulse and watch, model radar, Hermes usage, reset watch, sanity alarms to the coordinator",
		Owns:        []string{"usageBin"},
	},
	{
		Name:        GuardHooks,
		Description: "tool-call tripwire for Claude agents: alert when an agent that recently received outside text touches secrets or canary files",
		Owns:        []string{"guardHooks"},
		Conflict:    guardHooksConflicts,
		Notes:       "applies to Claude agents opened with bp open or bp run from now on (not plain claude); running agents keep their settings until reopened; alerts: bp audit --kind guard.reach",
	},
	{
		Name:        CompactionHooks,
		Description: "restates the bp identity to OpenCode and Hermes agents after a context compaction, the way Claude's own SessionStart hook already does",
		Owns:        []string{"compactionHooks"},
		Conflict:    compactionHooksConflicts,
		Apply:       applyCompactionHooks,
		Notes:       "installs ~/.config/opencode/plugins/bp-compaction.js and a pre_llm_call entry in ~/.hermes/config.yaml; applies to sessions started from now on; Hermes shows a one-time consent prompt for the new hook command (bp disable compaction-hooks removes both; hermes hooks doctor checks consent)",
	},
}

// All returns the registered modules in display order.
func All() []Module {
	return append([]Module(nil), registry...)
}

// Lookup finds a module by name.
func Lookup(name string) (Module, bool) {
	for _, module := range registry {
		if module.Name == name {
			return module, true
		}
	}
	return Module{}, false
}

// Names returns the registered module names.
func Names() []string {
	names := make([]string, 0, len(registry))
	for _, module := range registry {
		names = append(names, module.Name)
	}
	return names
}

// Env is everything a module hook may look at. Tests replace every field.
type Env struct {
	Config   config.Config
	UserHome string
	Getenv   func(string) string
	// Tmux runs a tmux command against the user's default server. Nil means
	// tmux is unavailable.
	Tmux func(args ...string) (string, error)
	// ProcRoot is /proc; empty disables process scans.
	ProcRoot string
	// Shell selects the rc files the sessions module edits (bash or zsh).
	Shell string
	// Wrappers adds the optional lush/rush helpers to shell.sh.
	Wrappers bool
	// Actor names who asked for the change, for the audit log.
	Actor string
}

func (e Env) getenv(key string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(key)
}

// EnabledIn reports whether a module is enabled for cfg. A config written
// before modules existed has no modules key; then the answer comes from
// detection, so behavior is the same with or without the migration write.
func EnabledIn(cfg config.Config, name string) bool {
	if cfg.ModulesSet {
		return cfg.Modules[name]
	}
	return Detect(cfg, detectEnv(cfg))[name]
}

var (
	current   config.Config
	currentMu sync.RWMutex
	initDone  bool
)

// Enabled is the shared-contract check: features that change the user's
// environment call it first. It reads the config passed to Init.
func Enabled(name string) bool {
	currentMu.RLock()
	cfg, ready := current, initDone
	currentMu.RUnlock()
	if !ready {
		loaded, err := config.Load()
		if err != nil {
			return false
		}
		cfg = loaded
	}
	return EnabledIn(cfg, name)
}

// Init records cfg for Enabled and migrates a config written before modules
// existed: the modules the install already uses are detected and written to
// the config once. If the write fails the detected set is still used, so
// behavior never depends on the write. It returns cfg with Modules filled.
func Init(cfg config.Config) config.Config {
	if !cfg.ModulesSet && cfg.InvalidConfig == "" {
		detected := Detect(cfg, detectEnv(cfg))
		cfg.Modules = detected
		cfg.ModulesSet = true
		if cfg.Path != "" {
			_ = migrate(cfg, detected)
		}
	}
	currentMu.Lock()
	current, initDone = cfg, true
	currentMu.Unlock()
	return cfg
}

func detectEnv(cfg config.Config) Env {
	home, _ := os.UserHomeDir()
	return Env{Config: cfg, UserHome: home, Getenv: os.Getenv}
}

func migrate(cfg config.Config, detected map[string]bool) error {
	unlock, err := lock(cfg)
	if err != nil {
		return err
	}
	defer unlock()
	// Another process may have migrated while this one waited.
	fresh, err := config.LoadHome(cfg.Home)
	if err == nil && fresh.Path == cfg.Path && fresh.ModulesSet {
		return nil
	}
	env := Env{Config: cfg, UserHome: detectEnv(cfg).UserHome, Getenv: os.Getenv}
	if err := recordExisting(env, detected); err != nil {
		return err
	}
	if err := config.SetModules(cfg.Path, detected); err != nil {
		return err
	}
	var names []string
	for _, name := range Names() {
		if detected[name] {
			names = append(names, name)
		}
	}
	auditEvent(env, audit.Event{Kind: "module.migrate", Reason: "config had no modules key; recorded the modules in use", Fields: map[string]string{"enabled": strings.Join(names, ","), "config": cfg.Path}})
	return nil
}

// auditEvent records a module decision in the audit log. A logging failure
// never undoes the change; it is reported on stderr.
func auditEvent(env Env, event audit.Event) {
	if event.Actor == "" {
		event.Actor = env.Actor
	}
	if err := audit.Append(env.Config.StateDir, event); err != nil {
		fmt.Fprintf(os.Stderr, "warning: audit log: %v\n", err)
	}
}

func journalDir(cfg config.Config) string {
	return filepath.Join(cfg.StateDir, "modules")
}

// reread loads the config again under the modules lock so a switch written
// by a concurrent bp is not lost. On error the caller's copy is kept.
func reread(cfg config.Config) config.Config {
	if cfg.Home == "" || cfg.Path == "" {
		return cfg
	}
	fresh, err := config.LoadHome(cfg.Home)
	if err != nil || fresh.Path != cfg.Path {
		return cfg
	}
	if !fresh.ModulesSet && cfg.ModulesSet {
		// The migrated set lives only in memory (read-only config): keep it.
		fresh.Modules, fresh.ModulesSet = cfg.Modules, cfg.ModulesSet
	}
	return fresh
}

// Seed writes the modules a new config starts with (its template has an
// empty modules map), unless another bp already put modules in it.
func Seed(cfg config.Config, path string, enabled map[string]bool) error {
	if len(enabled) == 0 {
		return nil
	}
	unlock, err := lock(cfg)
	if err != nil {
		return err
	}
	defer unlock()
	if fresh, err := config.LoadHome(cfg.Home); err == nil && len(fresh.Modules) > 0 {
		return nil
	}
	return config.SetModules(path, enabled)
}

func lock(cfg config.Config) (func(), error) {
	dir := journalDir(cfg)
	if cfg.StateDir == "" {
		return nil, errors.New("no state directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		file.Close()
	}, nil
}

// Options control Enable and Disable.
type Options struct {
	Force  bool
	DryRun bool
}

// Result describes what Enable or Disable did (or would do with DryRun).
type Result struct {
	Module    string   `json:"module"`
	Enabled   bool     `json:"enabled"`
	Changed   bool     `json:"changed"`
	Actions   []string `json:"actions"`
	Conflicts []string `json:"conflicts,omitempty"`
	Kept      []string `json:"kept,omitempty"`
	Notes     string   `json:"notes,omitempty"`
}

// ConflictError is returned when enable refuses because of conflicts.
type ConflictError struct {
	Module    string
	Conflicts []string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("not enabling %s:\n  - %s\nrerun with --force to enable it anyway", e.Module, strings.Join(e.Conflicts, "\n  - "))
}

func unknownModule(name string) error {
	return fmt.Errorf("unknown module %q; modules: %s", name, strings.Join(Names(), ", "))
}

// Enable turns a module on: conflict check, environment changes recorded in
// the journal, then the config switch.
func Enable(env Env, name string, options Options) (Result, error) {
	module, ok := Lookup(name)
	if !ok {
		return Result{}, unknownModule(name)
	}
	result := Result{Module: name, Enabled: true, Notes: module.Notes}
	if module.Conflict != nil {
		result.Conflicts = module.Conflict(env)
	}
	if len(result.Conflicts) > 0 && !options.Force {
		return result, &ConflictError{Module: name, Conflicts: result.Conflicts}
	}
	cfg := env.Config
	if options.DryRun {
		journal := &Journal{Module: name, dryRun: true}
		if module.Apply != nil {
			if err := module.Apply(env, journal); err != nil {
				return result, err
			}
		}
		result.Actions = journal.describe()
		if !EnabledIn(cfg, name) {
			result.Actions = append(result.Actions, "set modules."+name+" = true in "+configLabel(cfg))
		}
		return result, nil
	}
	if err := safefile.CheckOwner(env.UserHome); err != nil {
		return result, err
	}
	unlock, err := lock(cfg)
	if err != nil {
		return result, err
	}
	defer unlock()
	// Another bp may have switched a module since env.Config was loaded.
	cfg = reread(cfg)
	env.Config = cfg
	journal, err := LoadJournal(cfg, name)
	if err != nil {
		return result, err
	}
	before := len(journal.Changes)
	if module.Apply != nil {
		if err := module.Apply(env, journal); err != nil {
			_ = journal.Save(cfg)
			return result, err
		}
	}
	if err := journal.Save(cfg); err != nil {
		return result, err
	}
	result.Actions = journal.describeFrom(before)
	wasEnabled := EnabledIn(cfg, name)
	if err := writeSwitch(&cfg, name, true); err != nil {
		return result, err
	}
	if !wasEnabled {
		result.Actions = append(result.Actions, "set modules."+name+" = true in "+configLabel(cfg))
	}
	result.Changed = !wasEnabled || len(journal.Changes) > before
	if result.Changed {
		fields := map[string]string{"changes": strings.Join(result.Actions, "; ")}
		severity := audit.Info
		if len(result.Conflicts) > 0 {
			fields["forcedConflicts"] = strings.Join(result.Conflicts, "; ")
			severity = audit.Warn
		}
		auditEvent(env, audit.Event{Kind: "module.enable", Severity: severity, Target: name, Fields: fields})
	}
	return result, nil
}

// Disable undoes every journaled change of a module, newest first, and turns
// its config switch off. A change the user modified since is left in place
// and reported in Kept.
func Disable(env Env, name string, options Options) (Result, error) {
	if _, ok := Lookup(name); !ok {
		return Result{}, unknownModule(name)
	}
	cfg := env.Config
	result := Result{Module: name, Enabled: false}
	journal, err := LoadJournal(cfg, name)
	if err != nil {
		return result, err
	}
	if options.DryRun {
		for index := len(journal.Changes) - 1; index >= 0; index-- {
			result.Actions = append(result.Actions, "undo: "+journal.Changes[index].String())
		}
		if EnabledIn(cfg, name) {
			result.Actions = append(result.Actions, "set modules."+name+" = false in "+configLabel(cfg))
		}
		return result, nil
	}
	if err := safefile.CheckOwner(env.UserHome); err != nil {
		return result, err
	}
	unlock, err := lock(cfg)
	if err != nil {
		return result, err
	}
	defer unlock()
	cfg = reread(cfg)
	env.Config = cfg
	journal, err = LoadJournal(cfg, name)
	if err != nil {
		return result, err
	}
	actions, kept := journal.Undo(env)
	result.Actions, result.Kept = actions, kept
	if err := journal.Remove(cfg); err != nil {
		return result, err
	}
	wasEnabled := EnabledIn(cfg, name)
	if err := writeSwitch(&cfg, name, false); err != nil {
		return result, err
	}
	if wasEnabled {
		result.Actions = append(result.Actions, "set modules."+name+" = false in "+configLabel(cfg))
	}
	result.Changed = wasEnabled || len(actions) > 0
	if result.Changed {
		fields := map[string]string{"changes": strings.Join(result.Actions, "; ")}
		if len(result.Kept) > 0 {
			fields["kept"] = strings.Join(result.Kept, "; ")
		}
		auditEvent(env, audit.Event{Kind: "module.disable", Target: name, Fields: fields})
	}
	return result, nil
}

func configLabel(cfg config.Config) string {
	if cfg.Path == "" {
		return filepath.Join(cfg.Home, "config.yaml")
	}
	return cfg.Path
}

// writeSwitch stores the full module map with one entry changed. A machine
// without a config file gets bp's default config.yaml first; that file lives
// in bp's own home, not in the user's environment.
func writeSwitch(cfg *config.Config, name string, enabled bool) error {
	modules := map[string]bool{}
	for _, known := range Names() {
		if EnabledIn(*cfg, known) {
			modules[known] = true
		}
	}
	for key, value := range cfg.Modules {
		if _, known := Lookup(key); !known {
			modules[key] = value
		}
	}
	if enabled {
		modules[name] = true
	} else {
		delete(modules, name)
	}
	if cfg.Path == "" {
		path, err := config.InitYAML(cfg.Home)
		if err != nil {
			return err
		}
		cfg.Path = path
	}
	if err := config.SetModules(cfg.Path, modules); err != nil {
		return err
	}
	cfg.Modules, cfg.ModulesSet = modules, true
	currentMu.Lock()
	if initDone && current.Path == cfg.Path {
		current.Modules, current.ModulesSet = modules, true
	}
	currentMu.Unlock()
	return nil
}

// Status is one row of bp modules.
type Status struct {
	Name        string   `json:"name"`
	Enabled     bool     `json:"enabled"`
	Description string   `json:"description"`
	Owns        []string `json:"owns,omitempty"`
	Conflicts   []string `json:"conflicts,omitempty"`
	Changes     []string `json:"changes,omitempty"`
}

// List reports every module with its state, conflicts and recorded changes.
func List(env Env) []Status {
	var rows []Status
	for _, module := range registry {
		row := Status{Name: module.Name, Enabled: EnabledIn(env.Config, module.Name), Description: module.Description, Owns: module.Owns}
		if module.Conflict != nil && !row.Enabled {
			row.Conflicts = module.Conflict(env)
		}
		if journal, err := LoadJournal(env.Config, module.Name); err == nil {
			row.Changes = journal.describe()
		}
		rows = append(rows, row)
	}
	return rows
}

// Unknown lists module names in the config that bp does not know, for example
// ones written by a newer bp.
func Unknown(cfg config.Config) []string {
	var names []string
	for name := range cfg.Modules {
		if _, ok := Lookup(name); !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
