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
	"strings"

	"blueprint/internal/ntfy"
	"go.yaml.in/yaml/v3"
)

const LegacyHome = "/srv/blueprint"

// Config contains all paths and feature switches that vary by machine.
type Config struct {
	UpdateCheck      bool         `json:"updateCheck" yaml:"updateCheck"`
	LocalMouse       bool         `json:"localMouse" yaml:"localMouse"`
	LocalObservation bool         `json:"localObservation" yaml:"localObservation"`
	Path             string       `json:"-" yaml:"-"`
	Home             string       `json:"-" yaml:"-"`
	Legacy           bool         `json:"-" yaml:"-"`
	MsgqRoot         string       `json:"msgqRoot" yaml:"msgqRoot"`
	Agentbooks       []string     `json:"agentbooks" yaml:"agentbooks"`
	TokenAgentbooks  []string     `json:"tokenAgentbooks" yaml:"tokenAgentbooks"`
	StateDir         string       `json:"stateDir" yaml:"stateDir"`
	WAOutbox         string       `json:"waOutbox" yaml:"waOutbox"`
	WAStore          string       `json:"waStore" yaml:"waStore"`
	UsageBin         string       `json:"usageBin" yaml:"usageBin"`
	UsageHistory     string       `json:"usageHistory" yaml:"usageHistory"`
	ClipboardDir     string       `json:"clipboardDir" yaml:"clipboardDir"`
	WABridge         bool         `json:"waBridge" yaml:"waBridge"`
	Ntfy             *ntfy.Config `json:"ntfy,omitempty" yaml:"ntfy,omitempty"`
	Fed              *FedConfig   `json:"fed,omitempty" yaml:"fed,omitempty"`
	Codex            *CodexConfig `json:"codex,omitempty" yaml:"codex,omitempty"`
	Bar              BarConfig    `json:"bar" yaml:"bar"`
	InvalidConfig    string       `json:"-" yaml:"-"`
}

// BarConfig controls which metrics appear in the tmux status bar and their order.
type BarConfig struct {
	DefaultColor string   `json:"defaultColor" yaml:"defaultColor"`
	Context      string   `json:"context" yaml:"context"`
	Widgets      []string `json:"widgets" yaml:"widgets"`
}

// CodexConfig enables the read-only Codex app-server backend.
type CodexConfig struct {
	Sockets []string `json:"sockets" yaml:"sockets"`
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
	UpdateCheck      *bool         `json:"updateCheck" yaml:"updateCheck"`
	LocalMouse       *bool         `json:"localMouse" yaml:"localMouse"`
	LocalObservation *bool         `json:"localObservation" yaml:"localObservation"`
	MsgqRoot         *string       `json:"msgqRoot" yaml:"msgqRoot"`
	Agentbooks       *[]string     `json:"agentbooks" yaml:"agentbooks"`
	TokenAgentbooks  *[]string     `json:"tokenAgentbooks" yaml:"tokenAgentbooks"`
	StateDir         *string       `json:"stateDir" yaml:"stateDir"`
	WAOutbox         *string       `json:"waOutbox" yaml:"waOutbox"`
	WAStore          *string       `json:"waStore" yaml:"waStore"`
	UsageBin         *string       `json:"usageBin" yaml:"usageBin"`
	UsageHistory     *string       `json:"usageHistory" yaml:"usageHistory"`
	ClipboardDir     *string       `json:"clipboardDir" yaml:"clipboardDir"`
	WABridge         *bool         `json:"waBridge" yaml:"waBridge"`
	Ntfy             *ntfy.Config  `json:"ntfy" yaml:"ntfy"`
	Fed              *FedConfig    `json:"fed" yaml:"fed"`
	Codex            *CodexConfig  `json:"codex" yaml:"codex"`
	Bar              *barOverrides `json:"bar" yaml:"bar"`
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

func loadWith(getenv func(string) string, stat func(string) (os.FileInfo, error), userHome func() (string, error), readFile func(string) ([]byte, error)) (Config, error) {
	return loadWithWarning(getenv, stat, userHome, readFile, os.Stderr)
}

func loadWithWarning(getenv func(string) string, stat func(string) (os.FileInfo, error), userHome func() (string, error), readFile func(string) ([]byte, error), warning io.Writer) (Config, error) {
	home := getenv("BP_HOME")
	if home == "" {
		if info, err := stat(LegacyHome); err == nil && info.IsDir() {
			home = LegacyHome
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
}

func defaults(home string, legacy bool) Config {
	bar := BarConfig{Context: "used", Widgets: []string{"ctx", "temp", "queue", "model", "quota"}}
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
			LocalObservation: true,
			LocalMouse:       true,
			UpdateCheck:      true,
		}
	}
	return Config{
		Home:             home,
		MsgqRoot:         filepath.Join(home, "msgq"),
		Agentbooks:       []string{filepath.Join(home, "agentbook.json")},
		TokenAgentbooks:  []string{filepath.Join(home, "agentbook.json")},
		StateDir:         filepath.Join(home, "state"),
		Bar:              bar,
		LocalObservation: true,
		LocalMouse:       true,
		UpdateCheck:      true,
	}
}

func apply(result *Config, values overrides) {
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
	if values.Codex != nil {
		value := *values.Codex
		value.Sockets = append([]string(nil), value.Sockets...)
		result.Codex = &value
	}
	if values.Bar != nil && values.Bar.Context != nil {
		result.Bar.Context = *values.Bar.Context
	}
	if values.Bar != nil && values.Bar.Widgets != nil {
		result.Bar.Widgets = append([]string(nil), (*values.Bar.Widgets)...)
	}
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
