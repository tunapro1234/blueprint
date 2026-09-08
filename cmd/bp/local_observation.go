package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"blueprint/internal/cache"
	"blueprint/internal/identity"
	"context"
)

// The CLI calls these observers itself. No prompt, model request, copied
// conversation, global harness setting, or cwd-based session selection.
func (a *app) prepareLocalObservation(harness string, args []string) ([]string, string, error) {
	if !a.config.LocalObservation || (harness != "claude" && harness != "codex") {
		return args, "", nil
	}
	if harness == "codex" {
		for _, arg := range args {
			if arg == "--remote" || strings.HasPrefix(arg, "--remote=") {
				return args, "", nil // a local callback cannot observe another daemon
			}
		}
	}
	root := filepath.Join(a.config.StateDir, "local")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, "", err
	}
	dir, err := os.MkdirTemp(root, "run-")
	if err != nil {
		return nil, "", err
	}
	self, err := os.Executable()
	if err != nil {
		return nil, "", err
	}
	path := filepath.Join(dir, "observation.json")
	command := quoteShell(self) + " _observe " + quoteShell(path) + " " + harness
	if harness == "codex" {
		return args, path, nil
	}
	settings, remaining, err := localClaudeSettings(args)
	if err != nil {
		return nil, "", err
	}
	// Merge only the CLI --settings layer. Claude itself merges it with the
	// user/project hook arrays, preserving permissions and other defaults.
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	groups, _ := hooks["SessionStart"].([]any)
	hooks["SessionStart"] = append(groups, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 5}}})
	settings["hooks"] = hooks
	// Preserve an existing status-line command and its output, while recording
	// only its structured model/context input. Other status-line options survive.
	status, err := localClaudeStatusLine(settings, args)
	if err != nil {
		return nil, "", err
	}
	original, _ := status["command"].(string)
	if err := os.WriteFile(filepath.Join(dir, "status-command"), []byte(original), 0600); err != nil {
		return nil, "", err
	}
	status["type"], status["command"] = "command", command+" status"
	settings["statusLine"] = status
	data, _ := json.Marshal(settings)
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, data, 0600); err != nil {
		return nil, "", err
	}
	return append([]string{"--settings", settingsPath}, remaining...), path, nil
}

func localClaudeSettings(args []string) (map[string]any, []string, error) {
	settings := map[string]any{}
	var remaining []string
	for i := 0; i < len(args); i++ {
		arg, value := args[i], ""
		if arg == "--" {
			remaining = append(remaining, args[i:]...)
			break
		}
		if arg == "--settings" && i+1 < len(args) {
			i++
			value = args[i]
		} else if strings.HasPrefix(arg, "--settings=") {
			value = strings.TrimPrefix(arg, "--settings=")
		} else {
			remaining = append(remaining, arg)
			continue
		}
		data := []byte(value)
		if !strings.HasPrefix(strings.TrimSpace(value), "{") {
			var err error
			data, err = os.ReadFile(value)
			if err != nil {
				return nil, nil, err
			}
		}
		// Commander takes the last --settings value. Do the same before adding
		// our observation fields, so a user override is never discarded.
		settings = map[string]any{}
		if err := json.Unmarshal(data, &settings); err != nil {
			return nil, nil, fmt.Errorf("Claude --settings: %w", err)
		}
	}
	if settings == nil {
		return nil, nil, fmt.Errorf("Claude --settings must be an object")
	}
	return settings, remaining, nil
}

func localClaudeStatusLine(cli map[string]any, args []string) (map[string]any, error) {
	home, _ := os.UserHomeDir()
	configDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if configDir == "" {
		configDir = filepath.Join(home, ".claude")
	}
	cwd, _ := os.Getwd()
	sources := ",user,project,local,"
	for i, arg := range args {
		if arg == "--setting-sources" && i+1 < len(args) {
			sources = "," + args[i+1] + ","
		}
		if strings.HasPrefix(arg, "--setting-sources=") {
			sources = "," + strings.TrimPrefix(arg, "--setting-sources=") + ","
		}
	}
	result := map[string]any{}
	for _, source := range []struct{ name, path string }{{"user", filepath.Join(configDir, "settings.json")}, {"project", filepath.Join(cwd, ".claude/settings.json")}, {"local", filepath.Join(cwd, ".claude/settings.local.json")}} {
		if !strings.Contains(sources, ","+source.name+",") {
			continue
		}
		data, err := os.ReadFile(source.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var settings map[string]any
		if err := json.Unmarshal(data, &settings); err != nil {
			return nil, fmt.Errorf("read Claude settings for status line: %w", err)
		}
		if value, ok := settings["statusLine"].(map[string]any); ok {
			for k, v := range value {
				result[k] = v
			}
		}
	}
	if value, ok := cli["statusLine"].(map[string]any); ok {
		for k, v := range value {
			result[k] = v
		}
	}
	return result, nil
}

// observe is internal and deliberately bypasses app initialization. Status
// callbacks can run inside a CLI sandbox; they need only their launch directory.
func observe(args []string) error {
	if len(args) < 2 || len(args) > 3 || (args[1] != "claude" && args[1] != "codex") {
		return fmt.Errorf("invalid observer invocation")
	}
	path := args[0]
	if !filepath.IsAbs(path) || filepath.Base(path) != "observation.json" {
		return fmt.Errorf("invalid observation path")
	}
	if !identity.CallingPane(context.Background(), localPID(path)) {
		return fmt.Errorf("observer is not a child of this local launch")
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 2<<20))
	if err != nil {
		return err
	}
	var input struct {
		SessionID      string          `json:"session_id"`
		TranscriptPath string          `json:"transcript_path"`
		CWD            string          `json:"cwd"`
		Event          string          `json:"hook_event_name"`
		Model          json.RawMessage `json:"model"`
		Context        struct {
			Size    int `json:"context_window_size"`
			Current *struct {
				Input int `json:"input_tokens"`
				Read  int `json:"cache_read_input_tokens"`
				Write int `json:"cache_creation_input_tokens"`
			} `json:"current_usage"`
		} `json:"context_window"`
	}
	if err := json.Unmarshal(data, &input); err != nil {
		return err
	}
	if input.SessionID == "" || !filepath.IsAbs(input.TranscriptPath) || !filepath.IsAbs(input.CWD) {
		return fmt.Errorf("observer missing session/path/cwd")
	}
	var previous cache.LocalObservation
	if old, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(old, &previous)
	}
	value := cache.LocalObservation{SessionID: input.SessionID, TranscriptPath: input.TranscriptPath, CWD: input.CWD, ObservedAt: time.Now().UTC()}
	if previous.SessionID == value.SessionID {
		value.Model, value.Window, value.Context = previous.Model, previous.Window, previous.Context
	}
	if len(args) == 3 && args[2] == "status" {
		var model struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(input.Model, &model)
		value.Model = model.ID
		value.Window = input.Context.Size
		if u := input.Context.Current; u != nil {
			total := u.Input + u.Read + u.Write
			value.Context = &total
		}
	} else {
		if input.Event != "SessionStart" {
			return fmt.Errorf("unexpected observation event")
		}
		// SessionStart also fires after compact/resume. Its timestamp must
		// never refresh a pre-compact status-line token count.
		value.Context, value.Window = nil, 0
		value.Model = ""
		_ = json.Unmarshal(input.Model, &value.Model)
	}
	encoded, _ := json.Marshal(value)
	f, err := os.CreateTemp(filepath.Dir(path), ".observation-")
	if err != nil {
		return err
	}
	if _, err = f.Write(encoded); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	if len(args) == 3 {
		original, err := os.ReadFile(filepath.Join(filepath.Dir(path), "status-command"))
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(original)) != "" {
			cmd := exec.Command("/bin/sh", "-c", string(original))
			cmd.Stdin = bytes.NewReader(data)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			return cmd.Run()
		}
	}
	return nil
}

func localPID(path string) int {
	data, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "pid"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}
