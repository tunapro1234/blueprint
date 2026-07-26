// Package config loads the machine-local blueprint configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const LegacyHome = "/srv/blueprint"

// Config contains all paths and feature switches that vary by machine.
type Config struct {
	Home         string     `json:"-"`
	Legacy       bool       `json:"-"`
	MsgqRoot     string     `json:"msgqRoot"`
	Agentbooks   []string   `json:"agentbooks"`
	StateDir     string     `json:"stateDir"`
	WAOutbox     string     `json:"waOutbox"`
	WAStore      string     `json:"waStore"`
	UsageBin     string     `json:"usageBin"`
	UsageHistory string     `json:"usageHistory"`
	ClipboardDir string     `json:"clipboardDir"`
	WABridge     bool       `json:"waBridge"`
	Fed          *FedConfig `json:"fed,omitempty"`
}

// FedConfig enables one side of blueprint federation. A nil value leaves
// federation completely disabled.
type FedConfig struct {
	Mode     string `json:"mode"`
	Listen   string `json:"listen,omitempty"`
	Hub      string `json:"hub,omitempty"`
	PeerName string `json:"peerName"`
	Token    string `json:"token,omitempty"`
}

type overrides struct {
	MsgqRoot     *string    `json:"msgqRoot"`
	Agentbooks   *[]string  `json:"agentbooks"`
	StateDir     *string    `json:"stateDir"`
	WAOutbox     *string    `json:"waOutbox"`
	WAStore      *string    `json:"waStore"`
	UsageBin     *string    `json:"usageBin"`
	UsageHistory *string    `json:"usageHistory"`
	ClipboardDir *string    `json:"clipboardDir"`
	WABridge     *bool      `json:"waBridge"`
	Fed          *FedConfig `json:"fed"`
}

// Load resolves BP_HOME and reads its optional config.json.
func Load() (Config, error) {
	return loadWith(os.Getenv, os.Stat, os.UserHomeDir, os.ReadFile)
}

func loadWith(getenv func(string) string, stat func(string) (os.FileInfo, error), userHome func() (string, error), readFile func(string) ([]byte, error)) (Config, error) {
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

	path := filepath.Join(home, "config.json")
	data, err := readFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	var values overrides
	if err := json.Unmarshal(data, &values); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	apply(&result, values)
	if err := validateFed(result.Fed); err != nil {
		return Config{}, fmt.Errorf("parse %s: fed: %w", path, err)
	}
	return result, nil
}

func defaults(home string, legacy bool) Config {
	if legacy {
		return Config{
			Home:         home,
			Legacy:       true,
			MsgqRoot:     "/srv/server-main/msgq",
			Agentbooks:   []string{"/srv/server-main/agentbook.json", "/srv/probot/.orchestration/agentbook.json"},
			StateDir:     "/srv/blueprint/state",
			WAOutbox:     "/srv/whatsapp/outbox",
			WAStore:      "/srv/whatsapp/messages.jsonl",
			UsageBin:     "/srv/server-main/bin",
			UsageHistory: "/srv/server-main/usage/history.jsonl",
			ClipboardDir: "/srv/server-main/clipboard",
			WABridge:     true,
		}
	}
	return Config{
		Home:       home,
		MsgqRoot:   filepath.Join(home, "msgq"),
		Agentbooks: []string{filepath.Join(home, "agentbook.json")},
		StateDir:   filepath.Join(home, "state"),
	}
}

func apply(result *Config, values overrides) {
	if values.MsgqRoot != nil {
		result.MsgqRoot = *values.MsgqRoot
	}
	if values.Agentbooks != nil {
		result.Agentbooks = append([]string(nil), (*values.Agentbooks)...)
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
	if values.Fed != nil {
		value := *values.Fed
		result.Fed = &value
	}
}

func validateFed(value *FedConfig) error {
	if value == nil {
		return nil
	}
	if value.PeerName == "" || strings.Contains(value.PeerName, "@") {
		return fmt.Errorf("peerName must be non-empty and cannot contain @")
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
		if len(value.Token) != 64 {
			return fmt.Errorf("token must be 64 hexadecimal characters")
		}
		for _, char := range value.Token {
			if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
				return fmt.Errorf("token must be 64 hexadecimal characters")
			}
		}
	default:
		return fmt.Errorf("mode must be hub or client")
	}
	return nil
}
