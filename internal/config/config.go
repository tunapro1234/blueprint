// Package config loads the machine-local blueprint configuration.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"blueprint/internal/ntfy"
	"blueprint/internal/p2p"
	"go.yaml.in/yaml/v3"
)

const LegacyHome = "/srv/blueprint"

// Config contains all paths and feature switches that vary by machine.
type Config struct {
	UpdateCheck      bool                    `json:"updateCheck" yaml:"updateCheck"`
	LocalMouse       bool                    `json:"localMouse" yaml:"localMouse"`
	LocalObservation bool                    `json:"localObservation" yaml:"localObservation"`
	Path             string                  `json:"-" yaml:"-"`
	Home             string                  `json:"-" yaml:"-"`
	Legacy           bool                    `json:"-" yaml:"-"`
	MsgqRoot         string                  `json:"msgqRoot" yaml:"msgqRoot"`
	Agentbooks       []string                `json:"agentbooks" yaml:"agentbooks"`
	TokenAgentbooks  []string                `json:"tokenAgentbooks" yaml:"tokenAgentbooks"`
	StateDir         string                  `json:"stateDir" yaml:"stateDir"`
	WAOutbox         string                  `json:"waOutbox" yaml:"waOutbox"`
	WAStore          string                  `json:"waStore" yaml:"waStore"`
	UsageBin         string                  `json:"usageBin" yaml:"usageBin"`
	UsageHistory     string                  `json:"usageHistory" yaml:"usageHistory"`
	ClipboardDir     string                  `json:"clipboardDir" yaml:"clipboardDir"`
	WABridge         bool                    `json:"waBridge" yaml:"waBridge"`
	Ntfy             *ntfy.Config            `json:"ntfy,omitempty" yaml:"ntfy,omitempty"`
	Fed              *FedConfig              `json:"fed,omitempty" yaml:"fed,omitempty"`
	P2P              *p2p.Config             `json:"p2p,omitempty" yaml:"p2p,omitempty"`
	API              *APIConfig              `json:"api,omitempty" yaml:"api,omitempty"`
	Codex            *CodexConfig            `json:"codex,omitempty" yaml:"codex,omitempty"`
	GuardHooks       *GuardHooksConfig       `json:"guardHooks,omitempty" yaml:"guardHooks,omitempty"`
	CLIUpdates       map[string][]string     `json:"cliUpdates,omitempty" yaml:"cliUpdates,omitempty"`
	Remotes          map[string]RemoteConfig `json:"remotes,omitempty" yaml:"remotes,omitempty"`
	Bar              BarConfig               `json:"bar" yaml:"bar"`
	Lifecycle        LifecycleConfig         `json:"lifecycle" yaml:"lifecycle"`
	Windows          WindowsConfig           `json:"windows" yaml:"windows"`
	ClaudeAccounts   ClaudeAccountsConfig    `json:"claudeAccounts" yaml:"claudeAccounts"`
	// Modules records which opt-in modules are enabled (internal/modules).
	// ModulesSet is false when the file has no modules key: an install from
	// before modules existed, which internal/modules migrates.
	Modules       map[string]bool `json:"modules,omitempty" yaml:"modules,omitempty"`
	ModulesSet    bool            `json:"-" yaml:"-"`
	InvalidConfig string          `json:"-" yaml:"-"`
}

// RemoteConfig describes an interactive bp host. It deliberately contains no
// password, token or private-key material: Identity is only a path to an SSH
// identity file already managed by the user.
type RemoteConfig struct {
	Host      string `json:"host" yaml:"host"`
	Port      int    `json:"port,omitempty" yaml:"port,omitempty"`
	User      string `json:"user,omitempty" yaml:"user,omitempty"`
	Identity  string `json:"identity,omitempty" yaml:"identity,omitempty"`
	Transport string `json:"transport,omitempty" yaml:"transport,omitempty"`
	MoshPorts string `json:"moshPorts,omitempty" yaml:"moshPorts,omitempty"`
	Elevate   string `json:"elevate,omitempty" yaml:"elevate,omitempty"`
}

// LifecycleConfig controls throwaway local registrations. Both defaults are
// enabled; users can independently keep bp run registrations persistent or
// defer archiving until an explicit command.
type LifecycleConfig struct {
	EphemeralDefault bool `json:"ephemeralDefault" yaml:"ephemeralDefault"`
	ArchiveOnClose   bool `json:"archiveOnClose" yaml:"archiveOnClose"`
}

// ClaudeAccountsConfig controls the daemon's automatic Claude account switch
// and keepalive. The switch itself is always available through
// `bp account switch`; the daemon only switches when AutoSwitch is enabled
// and only pings when KeepAlive is enabled. Threshold and the Limits values
// are utilization percentages (1-100); the minute values must be at least
// one. Limits keys are a slot number, an alias or an email.
type ClaudeAccountsConfig struct {
	AutoSwitch      bool           `json:"autoSwitch" yaml:"autoSwitch"`
	Threshold       int            `json:"threshold" yaml:"threshold"`
	CooldownMinutes int            `json:"cooldownMinutes" yaml:"cooldownMinutes"`
	PollMinutes     int            `json:"pollMinutes" yaml:"pollMinutes"`
	Limits          map[string]int `json:"limits,omitempty" yaml:"limits,omitempty"`
	KeepAlive       bool           `json:"keepAlive" yaml:"keepAlive"`
	KeepAliveModel  string         `json:"keepAliveModel" yaml:"keepAliveModel"`
	// SwitchPrefer picks the auto-switch target: "soonest-reset" (the default)
	// drains the account whose five-hour window resets soonest; "room" picks
	// the account with the most room left under its limit.
	SwitchPrefer string `json:"switchPrefer" yaml:"switchPrefer"`
}

// LimitPercents returns Limits as percentages for the account policy, or nil
// when no account has its own limit.
func (c ClaudeAccountsConfig) LimitPercents() map[string]float64 {
	if len(c.Limits) == 0 {
		return nil
	}
	out := make(map[string]float64, len(c.Limits))
	for key, value := range c.Limits {
		out[key] = float64(value)
	}
	return out
}

// DefaultClaudeAccounts is the claudeAccounts block used when none is set.
func DefaultClaudeAccounts() ClaudeAccountsConfig {
	return ClaudeAccountsConfig{AutoSwitch: false, Threshold: 90, CooldownMinutes: 5, PollMinutes: 5, KeepAliveModel: "haiku", SwitchPrefer: "soonest-reset"}
}

// BarConfig controls which metrics appear in the tmux status bar and their order.
type BarConfig struct {
	DefaultColor string   `json:"defaultColor" yaml:"defaultColor"`
	Context      string   `json:"context" yaml:"context"`
	Widgets      []string `json:"widgets" yaml:"widgets"`
}

// WindowsConfig controls compositor integration without enabling it.
type WindowsConfig struct {
	ResetColor string `json:"resetColor" yaml:"resetColor"`
}

// CodexConfig enables the read-only Codex app-server backend and the
// installation-wide Codex policy.
type CodexConfig struct {
	Sockets []string `json:"sockets" yaml:"sockets"`
	// Disabled stops bp from starting new Codex sessions: bp open defaults to
	// Claude, and an explicit Codex launch needs --allow-codex. Agents that
	// are already running, and their recorded relaunch, are only reported.
	Disabled bool `json:"disabled" yaml:"disabled"`
}

// GuardHooksConfig shapes the tool-call tripwire of the guard-hooks module
// (docs/security/guard-hooks-module.md). It matters only while that module is
// enabled.
type GuardHooksConfig struct {
	// Mode is "observe" (alert and let the call run, the default) or "ask"
	// (also ask the human to confirm a tainted secret access).
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
	// Canaries are extra path patterns that always alert when touched.
	Canaries []string `json:"canaries,omitempty" yaml:"canaries,omitempty"`
}

func validateGuardHooks(g *GuardHooksConfig) error {
	if g == nil {
		return nil
	}
	if g.Mode != "" && g.Mode != "observe" && g.Mode != "ask" {
		return fmt.Errorf("mode must be observe or ask, not %q", g.Mode)
	}
	for _, c := range g.Canaries {
		if strings.TrimSpace(c) == "" || strings.ContainsAny(c, "\x00\n\r") {
			return fmt.Errorf("invalid canary pattern %q", c)
		}
	}
	return nil
}

// CodexDisabled reports whether the Codex policy forbids new Codex sessions.
func (c Config) CodexDisabled() bool {
	return c.Codex != nil && c.Codex.Disabled
}

// FedConfig enables one side of blueprint federation. A nil value leaves
// federation completely disabled.
type FedConfig struct {
	Mode     string `json:"mode" yaml:"mode"`
	Listen   string `json:"listen,omitempty" yaml:"listen,omitempty"`
	Hub      string `json:"hub,omitempty" yaml:"hub,omitempty"`
	PeerName string `json:"peerName" yaml:"peerName"`
	Token    string `json:"token,omitempty" yaml:"token,omitempty"`
	// Expose (client mode) lists the local agents that may RECEIVE federated
	// messages; anything else is dropped and journalled. Empty means no limit,
	// which keeps existing setups working — the hub side has its own per-peer
	// expose list, this is the mirror for the polling side.
	Expose []string `json:"expose,omitempty" yaml:"expose,omitempty"`
}

type overrides struct {
	UpdateCheck      *bool                    `json:"updateCheck" yaml:"updateCheck"`
	LocalMouse       *bool                    `json:"localMouse" yaml:"localMouse"`
	LocalObservation *bool                    `json:"localObservation" yaml:"localObservation"`
	MsgqRoot         *string                  `json:"msgqRoot" yaml:"msgqRoot"`
	Agentbooks       *[]string                `json:"agentbooks" yaml:"agentbooks"`
	TokenAgentbooks  *[]string                `json:"tokenAgentbooks" yaml:"tokenAgentbooks"`
	StateDir         *string                  `json:"stateDir" yaml:"stateDir"`
	WAOutbox         *string                  `json:"waOutbox" yaml:"waOutbox"`
	WAStore          *string                  `json:"waStore" yaml:"waStore"`
	UsageBin         *string                  `json:"usageBin" yaml:"usageBin"`
	UsageHistory     *string                  `json:"usageHistory" yaml:"usageHistory"`
	ClipboardDir     *string                  `json:"clipboardDir" yaml:"clipboardDir"`
	WABridge         *bool                    `json:"waBridge" yaml:"waBridge"`
	Ntfy             *ntfy.Config             `json:"ntfy" yaml:"ntfy"`
	Fed              *FedConfig               `json:"fed" yaml:"fed"`
	P2P              *p2p.Config              `json:"p2p" yaml:"p2p"`
	API              *APIConfig               `json:"api" yaml:"api"`
	Codex            *CodexConfig             `json:"codex" yaml:"codex"`
	GuardHooks       *GuardHooksConfig        `json:"guardHooks" yaml:"guardHooks"`
	Remotes          *map[string]RemoteConfig `json:"remotes" yaml:"remotes"`
	CLIUpdates       *map[string][]string     `json:"cliUpdates" yaml:"cliUpdates"`
	Bar              *barOverrides            `json:"bar" yaml:"bar"`
	Lifecycle        *lifecycleOverrides      `json:"lifecycle" yaml:"lifecycle"`
	Windows          *WindowsConfig           `json:"windows" yaml:"windows"`
	ClaudeAccounts   *claudeAccountsOverrides `json:"claudeAccounts" yaml:"claudeAccounts"`
	Modules          *map[string]bool         `json:"modules" yaml:"modules"`
}

type claudeAccountsOverrides struct {
	AutoSwitch      *bool           `json:"autoSwitch" yaml:"autoSwitch"`
	Threshold       *int            `json:"threshold" yaml:"threshold"`
	CooldownMinutes *int            `json:"cooldownMinutes" yaml:"cooldownMinutes"`
	PollMinutes     *int            `json:"pollMinutes" yaml:"pollMinutes"`
	Limits          *map[string]int `json:"limits" yaml:"limits"`
	KeepAlive       *bool           `json:"keepAlive" yaml:"keepAlive"`
	KeepAliveModel  *string         `json:"keepAliveModel" yaml:"keepAliveModel"`
	SwitchPrefer    *string         `json:"switchPrefer" yaml:"switchPrefer"`
}

type lifecycleOverrides struct {
	EphemeralDefault *bool `json:"ephemeralDefault" yaml:"ephemeralDefault"`
	ArchiveOnClose   *bool `json:"archiveOnClose" yaml:"archiveOnClose"`
}

type barOverrides struct {
	DefaultColor *string   `json:"defaultColor" yaml:"defaultColor"`
	Context      *string   `json:"context" yaml:"context"`
	Widgets      *[]string `json:"widgets" yaml:"widgets"`
}

// Load resolves BP_HOME and reads one optional config.yaml/config.yml/config.json.
func Load() (Config, error) {
	return loadWithWarning(os.Getenv, os.Stat, os.UserHomeDir, os.ReadFile, os.Stderr)
}

// LoadHome reads the config of the installation at home, ignoring BP_HOME and
// /etc/blueprint/home. Code that re-reads a config it already has uses this so
// it can never switch to another installation.
func LoadHome(home string) (Config, error) {
	if home == "" {
		return Config{}, fmt.Errorf("no bp home")
	}
	getenv := func(key string) string {
		if key == "BP_HOME" {
			return home
		}
		return os.Getenv(key)
	}
	return loadWithWarning(getenv, os.Stat, os.UserHomeDir, os.ReadFile, os.Stderr)
}

func loadWith(getenv func(string) string, stat func(string) (os.FileInfo, error), userHome func() (string, error), readFile func(string) ([]byte, error)) (Config, error) {
	return loadWithWarning(getenv, stat, userHome, readFile, os.Stderr)
}

func loadWithWarning(getenv func(string) string, stat func(string) (os.FileInfo, error), userHome func() (string, error), readFile func(string) ([]byte, error), warning io.Writer) (Config, error) {
	home := getenv("BP_HOME")
	if home == "" {
		// A server installation explicitly selects its home. Merely cloning the
		// repository under /srv/blueprint must never enable server integrations.
		data, err := readFile("/etc/blueprint/home")
		if err == nil {
			home = strings.TrimSpace(string(data))
			if !filepath.IsAbs(home) || strings.ContainsAny(home, "\r\n\x00") {
				return Config{}, fmt.Errorf("invalid /etc/blueprint/home: expected one absolute directory")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("read /etc/blueprint/home: %w", err)
		} else {
			user, err := userHome()
			if err != nil {
				return Config{}, fmt.Errorf("find home directory: %w", err)
			}
			home = filepath.Join(user, ".blueprint")
		}
	}

	home = filepath.Clean(home)
	legacy := home == LegacyHome
	result := defaults(home, legacy)

	var path string
	var data []byte
	for _, name := range []string{"config.yaml", "config.yml", "config.json"} {
		candidate := filepath.Join(home, name)
		content, err := readFile(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Config{}, fmt.Errorf("read %s: %w", candidate, err)
		}
		if path != "" {
			return Config{}, fmt.Errorf("multiple bp configs: %s and %s; keep one active file (no implicit merging)", path, candidate)
		}
		path, data = candidate, content
	}
	if path == "" {
		return result, nil
	}
	result.Path = path
	var values overrides
	if filepath.Ext(path) != ".json" {
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&values); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("parse %s: %w", path, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("parse %s: expected a single YAML document", path)
		}
	} else if err := json.Unmarshal(data, &values); err != nil {
		parseErr := fmt.Errorf("parse %s: %w", path, err)
		result.InvalidConfig = parseErr.Error()
		if warning != nil {
			fmt.Fprintf(warning, "config.json is invalid, falling back to defaults: %v\n", parseErr)
		}
		return result, nil
	}
	apply(&result, values)
	if result.Bar.DefaultColor != "" {
		if _, err := ColorIndex(result.Bar.DefaultColor); err != nil {
			return Config{}, fmt.Errorf("bar.defaultColor: %w", err)
		}
	}
	if _, err := ColorIndex(result.Windows.ResetColor); err != nil {
		return Config{}, fmt.Errorf("windows.resetColor: %w", err)
	}
	if filepath.Ext(path) != ".json" {
		user, err := userHome()
		if err != nil {
			return Config{}, fmt.Errorf("find home directory: %w", err)
		}
		resolvePaths(&result, user)
		if result.Bar.Context != "used" && result.Bar.Context != "remaining" {
			return Config{}, fmt.Errorf("bar.context must be used or remaining")
		}
		for _, widget := range result.Bar.Widgets {
			switch widget {
			case "ctx", "temp", "queue", "model", "quota", "talk", "clock":
			default:
				return Config{}, fmt.Errorf("parse %s: unknown bar widget %q", path, widget)
			}
		}
	}
	if err := validateFed(result.Fed); err != nil {
		return Config{}, fmt.Errorf("parse %s: fed: %w", path, err)
	}
	if result.P2P != nil {
		if err := result.P2P.Validate(); err != nil {
			return Config{}, fmt.Errorf("parse %s: p2p: %w", path, err)
		}
	}
	if err := validateAPI(result.API); err != nil {
		return Config{}, fmt.Errorf("parse %s: api: %w", path, err)
	}
	if err := validateRemotes(result.Remotes); err != nil {
		return Config{}, fmt.Errorf("parse %s: remotes: %w", path, err)
	}
	if err := validateClaudeAccounts(result.ClaudeAccounts); err != nil {
		return Config{}, fmt.Errorf("parse %s: claudeAccounts: %w", path, err)
	}
	if err := validateGuardHooks(result.GuardHooks); err != nil {
		return Config{}, fmt.Errorf("parse %s: guardHooks: %w", path, err)
	}
	return result, nil
}

// YAML paths are relative to BP_HOME, never to the current agent's directory.
// Only ~/ is expanded; configuration values are never executed as shell text.
func resolvePaths(c *Config, user string) {
	resolve := func(p string) string {
		if p == "" {
			return ""
		}
		if strings.HasPrefix(p, "~/") {
			return filepath.Join(user, p[2:])
		}
		if filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
		return filepath.Join(c.Home, p)
	}
	for _, p := range []*string{&c.MsgqRoot, &c.StateDir, &c.WAOutbox, &c.WAStore, &c.UsageBin, &c.UsageHistory, &c.ClipboardDir} {
		*p = resolve(*p)
	}
	for _, list := range [][]string{c.Agentbooks, c.TokenAgentbooks} {
		for i, p := range list {
			list[i] = resolve(p)
		}
	}
	if c.Codex != nil {
		for i, p := range c.Codex.Sockets {
			c.Codex.Sockets[i] = resolve(p)
		}
	}
	for name, remote := range c.Remotes {
		remote.Identity = resolve(remote.Identity)
		c.Remotes[name] = remote
	}
}

func defaults(home string, legacy bool) Config {
	bar := BarConfig{Context: "used", Widgets: []string{"ctx", "temp", "queue", "model", "quota"}}
	lifecycle := LifecycleConfig{EphemeralDefault: true, ArchiveOnClose: true}
	windows := WindowsConfig{ResetColor: "white"}
	cliUpdates := defaultCLIUpdates()
	if legacy {
		return Config{
			Home:     home,
			Legacy:   true,
			MsgqRoot: "/srv/server-main/msgq",
			// One book for the whole fleet. Several books still work when
			// config.json lists them; the default no longer assumes any.
			Agentbooks:       []string{"/srv/server-main/agentbook.json"},
			TokenAgentbooks:  []string{"/srv/server-main/agentbook.json"},
			StateDir:         "/srv/blueprint/state",
			WAOutbox:         "/srv/whatsapp/outbox",
			WAStore:          "/srv/whatsapp/messages.jsonl",
			UsageBin:         "/srv/server-main/bin",
			UsageHistory:     "/srv/server-main/usage/history.jsonl",
			ClipboardDir:     "/srv/server-main/clipboard",
			WABridge:         true,
			Bar:              bar,
			Lifecycle:        lifecycle,
			Windows:          windows,
			ClaudeAccounts:   DefaultClaudeAccounts(),
			LocalObservation: true,
			LocalMouse:       true,
			UpdateCheck:      true,
			CLIUpdates:       cliUpdates,
		}
	}
	return Config{
		Home:             home,
		MsgqRoot:         filepath.Join(home, "msgq"),
		Agentbooks:       []string{filepath.Join(home, "agentbook.json")},
		TokenAgentbooks:  []string{filepath.Join(home, "agentbook.json")},
		StateDir:         filepath.Join(home, "state"),
		Bar:              bar,
		Lifecycle:        lifecycle,
		Windows:          windows,
		ClaudeAccounts:   DefaultClaudeAccounts(),
		LocalObservation: true,
		LocalMouse:       true,
		UpdateCheck:      true,
		CLIUpdates:       cliUpdates,
	}
}

func defaultCLIUpdates() map[string][]string {
	return map[string][]string{
		"claude":   {"claude", "update"},
		"codex":    {"npm", "install", "-g", "@openai/codex@latest"},
		"opencode": {"opencode", "upgrade"},
		"hermes":   {"hermes", "update"},
	}
}

func apply(result *Config, values overrides) {
	if values.Modules != nil {
		result.ModulesSet = true
		result.Modules = make(map[string]bool, len(*values.Modules))
		for name, enabled := range *values.Modules {
			result.Modules[name] = enabled
		}
	}
	if values.Lifecycle != nil && values.Lifecycle.EphemeralDefault != nil {
		result.Lifecycle.EphemeralDefault = *values.Lifecycle.EphemeralDefault
	}
	if values.Lifecycle != nil && values.Lifecycle.ArchiveOnClose != nil {
		result.Lifecycle.ArchiveOnClose = *values.Lifecycle.ArchiveOnClose
	}
	if values.Bar != nil && values.Bar.DefaultColor != nil {
		result.Bar.DefaultColor = *values.Bar.DefaultColor
	}
	if values.UpdateCheck != nil {
		result.UpdateCheck = *values.UpdateCheck
	}
	if values.LocalMouse != nil {
		result.LocalMouse = *values.LocalMouse
	}
	if values.LocalObservation != nil {
		result.LocalObservation = *values.LocalObservation
	}
	if values.MsgqRoot != nil {
		result.MsgqRoot = *values.MsgqRoot
	}
	if values.Agentbooks != nil {
		result.Agentbooks = append([]string(nil), (*values.Agentbooks)...)
		if !result.Legacy && values.TokenAgentbooks == nil {
			result.TokenAgentbooks = append([]string(nil), result.Agentbooks...)
		}
	}
	if values.TokenAgentbooks != nil {
		result.TokenAgentbooks = append([]string(nil), (*values.TokenAgentbooks)...)
	}
	if values.StateDir != nil {
		result.StateDir = *values.StateDir
	}
	if values.WAOutbox != nil {
		result.WAOutbox = *values.WAOutbox
	}
	if values.WAStore != nil {
		result.WAStore = *values.WAStore
	}
	if values.UsageBin != nil {
		result.UsageBin = *values.UsageBin
	}
	if values.UsageHistory != nil {
		result.UsageHistory = *values.UsageHistory
	}
	if values.ClipboardDir != nil {
		result.ClipboardDir = *values.ClipboardDir
	}
	if values.WABridge != nil {
		result.WABridge = *values.WABridge
	}
	if values.Ntfy != nil {
		value := *values.Ntfy
		result.Ntfy = &value
	}
	if values.Fed != nil {
		value := *values.Fed
		result.Fed = &value
	}
	if values.P2P != nil {
		value := *values.P2P
		result.P2P = &value
	}
	if values.API != nil {
		value := *values.API
		result.API = &value
	}
	if values.Codex != nil {
		value := *values.Codex
		value.Sockets = append([]string(nil), value.Sockets...)
		result.Codex = &value
	}
	if values.GuardHooks != nil {
		value := *values.GuardHooks
		value.Canaries = append([]string(nil), value.Canaries...)
		result.GuardHooks = &value
	}
	if values.Remotes != nil {
		result.Remotes = make(map[string]RemoteConfig, len(*values.Remotes))
		for name, remote := range *values.Remotes {
			if remote.Transport == "" {
				remote.Transport = "ssh"
			}
			result.Remotes[name] = remote
		}
	}
	if values.CLIUpdates != nil {
		if result.CLIUpdates == nil {
			result.CLIUpdates = map[string][]string{}
		}
		for harness, command := range *values.CLIUpdates {
			result.CLIUpdates[harness] = append([]string(nil), command...)
		}
	}
	if values.Bar != nil && values.Bar.Context != nil {
		result.Bar.Context = *values.Bar.Context
	}
	if values.Bar != nil && values.Bar.Widgets != nil {
		result.Bar.Widgets = append([]string(nil), (*values.Bar.Widgets)...)
	}
	if accounts := values.ClaudeAccounts; accounts != nil {
		if accounts.AutoSwitch != nil {
			result.ClaudeAccounts.AutoSwitch = *accounts.AutoSwitch
		}
		if accounts.Threshold != nil {
			result.ClaudeAccounts.Threshold = *accounts.Threshold
		}
		if accounts.CooldownMinutes != nil {
			result.ClaudeAccounts.CooldownMinutes = *accounts.CooldownMinutes
		}
		if accounts.PollMinutes != nil {
			result.ClaudeAccounts.PollMinutes = *accounts.PollMinutes
		}
		if accounts.Limits != nil {
			limits := make(map[string]int, len(*accounts.Limits))
			for key, value := range *accounts.Limits {
				limits[key] = value
			}
			result.ClaudeAccounts.Limits = limits
		}
		if accounts.KeepAlive != nil {
			result.ClaudeAccounts.KeepAlive = *accounts.KeepAlive
		}
		if accounts.KeepAliveModel != nil {
			result.ClaudeAccounts.KeepAliveModel = *accounts.KeepAliveModel
		}
		if accounts.SwitchPrefer != nil {
			result.ClaudeAccounts.SwitchPrefer = *accounts.SwitchPrefer
		}
	}
	if values.Windows != nil {
		result.Windows = *values.Windows
		if result.Windows.ResetColor == "" {
			result.Windows.ResetColor = "white"
		}
	}
}

func validateClaudeAccounts(value ClaudeAccountsConfig) error {
	if value.Threshold < 1 || value.Threshold > 100 {
		return fmt.Errorf("threshold must be between 1 and 100")
	}
	if value.CooldownMinutes < 1 {
		return fmt.Errorf("cooldownMinutes must be at least 1")
	}
	if value.PollMinutes < 1 {
		return fmt.Errorf("pollMinutes must be at least 1")
	}
	for key, limit := range value.Limits {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("limits keys must name a slot number, alias or email")
		}
		if limit < 1 || limit > 100 {
			return fmt.Errorf("limits[%q] must be between 1 and 100", key)
		}
	}
	if strings.TrimSpace(value.KeepAliveModel) == "" || strings.HasPrefix(strings.TrimSpace(value.KeepAliveModel), "-") {
		return fmt.Errorf("keepAliveModel must name a Claude model")
	}
	switch value.SwitchPrefer {
	case "", "room", "soonest-reset":
	default:
		return fmt.Errorf("switchPrefer must be \"room\" or \"soonest-reset\"")
	}
	return nil
}

func validateFed(value *FedConfig) error {
	if value == nil {
		return nil
	}
	if !validFederationName(value.PeerName) {
		return fmt.Errorf("peerName must contain only A-Z, a-z, 0-9, '.', '_' or '-' and be at most 64 characters")
	}
	switch value.Mode {
	case "hub":
		if value.Listen == "" {
			return fmt.Errorf("listen is required in hub mode")
		}
		host, _, err := net.SplitHostPort(value.Listen)
		if err != nil {
			return fmt.Errorf("invalid listen address: %w", err)
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("listen must use a loopback address")
		}
	case "client":
		if value.Hub == "" {
			return fmt.Errorf("hub is required in client mode")
		}
		parsed, err := url.Parse(value.Hub)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("hub must be an http or https URL")
		}
		if parsed.Scheme == "http" {
			host := strings.ToLower(parsed.Hostname())
			ip := net.ParseIP(host)
			if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				return fmt.Errorf("refusing to send bearer token over plaintext HTTP")
			}
		}
		if len(value.Token) != 64 {
			return fmt.Errorf("token must be 64 hexadecimal characters")
		}
		for _, char := range value.Token {
			if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
				return fmt.Errorf("token must be 64 hexadecimal characters")
			}
		}
		for _, name := range value.Expose {
			if !validFederationName(name) {
				return fmt.Errorf("expose contains invalid agent name %q", name)
			}
		}
	default:
		return fmt.Errorf("mode must be hub or client")
	}
	return nil
}

func validFederationName(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 64 {
		return false
	}
	for _, char := range []byte(value) {
		if !((char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '.' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func validateRemotes(remotes map[string]RemoteConfig) error {
	for name, remote := range remotes {
		if !validFederationName(name) {
			return fmt.Errorf("invalid server name %q", name)
		}
		if remote.Host == "" || strings.HasPrefix(remote.Host, "-") || strings.ContainsAny(remote.Host, " \t\r\n\x00") {
			return fmt.Errorf("%s.host must be one host name or address", name)
		}
		if remote.Port < 0 || remote.Port > 65535 {
			return fmt.Errorf("%s.port must be between 1 and 65535", name)
		}
		if remote.User != "" && !validFederationName(remote.User) {
			return fmt.Errorf("%s.user contains invalid characters", name)
		}
		if strings.ContainsAny(remote.Identity, "\r\n\x00") {
			return fmt.Errorf("%s.identity contains invalid characters", name)
		}
		transport := remote.Transport
		if transport == "" {
			transport = "ssh"
		}
		if transport != "ssh" && transport != "mosh" {
			return fmt.Errorf("%s.transport must be mosh or ssh", name)
		}
		if remote.MoshPorts != "" && !validPortRange(remote.MoshPorts) {
			return fmt.Errorf("%s.moshPorts must be a port or MIN:MAX range", name)
		}
		if strings.ContainsAny(remote.Elevate, "\r\n\x00") {
			return fmt.Errorf("%s.elevate must be one command line", name)
		}
		for _, field := range strings.Fields(remote.Elevate) {
			if !validCommandWord(field) {
				return fmt.Errorf("%s.elevate contains unsupported shell characters", name)
			}
		}
	}
	return nil
}

func validPortRange(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		port, err := strconv.Atoi(part)
		if err != nil || port < 1 || port > 65535 {
			return false
		}
	}
	if len(parts) == 2 {
		first, _ := strconv.Atoi(parts[0])
		last, _ := strconv.Atoi(parts[1])
		return first <= last
	}
	return true
}

func validCommandWord(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._/+:-=@", char)) {
			return false
		}
	}
	return true
}
