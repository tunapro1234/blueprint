package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"blueprint/internal/cache"
	bpconfig "blueprint/internal/config"
)

func TestLocalObservationPreservesCLISettingsAndStatusCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	writeBarTestFile(t, filepath.Join(home, ".claude/settings.json"), `{"statusLine":{"type":"command","command":"printf ORIGINAL","padding":2},"model":"do-not-change"}`)
	a := &app{config: bpconfig.Config{LocalObservation: true, StateDir: filepath.Join(home, "state")}}
	args, path, err := a.prepareLocalObservation("claude", []string{"--dangerously-skip-permissions", "--settings", `{"effortLevel":"medium","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo EXISTING"}]}]}}`, "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 4 || args[2] != "--dangerously-skip-permissions" || args[3] != "hello world" {
		t.Fatal(args)
	}
	data, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err = json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["effortLevel"] != "medium" || !strings.Contains(string(data), "EXISTING") || strings.Contains(string(data), "do-not-change") {
		t.Fatal(string(data))
	}
	original, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "status-command"))
	if string(original) != "printf ORIGINAL" {
		t.Fatal(string(original))
	}
	if settings["statusLine"].(map[string]any)["padding"] != float64(2) {
		t.Fatal(settings)
	}
	args, path, err = a.prepareLocalObservation("codex", []string{"--remote", "unix://", "--model", "gpt-6-astra"})
	if err != nil || path != "" || len(args) != 4 {
		t.Fatal(args, path, err)
	}
	a.config.LocalObservation = false
	args, path, err = a.prepareLocalObservation("claude", []string{"--resume", "a-thread"})
	if err != nil || path != "" || len(args) != 2 {
		t.Fatal(args, path, err)
	}
}

func TestObserverPreservesStatusIOAndInvalidatesPreCompactContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observation.json")
	writeBarTestFile(t, filepath.Join(dir, "pid"), strconv.Itoa(os.Getpid()))
	writeBarTestFile(t, filepath.Join(dir, "status-command"), "cat")
	id := "11111111-1111-1111-1111-111111111111"
	payload := map[string]any{"session_id": id, "transcript_path": filepath.Join(dir, id+".jsonl"), "cwd": dir,
		"model":          map[string]string{"id": "claude-fable-5-1"},
		"context_window": map[string]any{"context_window_size": 1000000, "current_usage": map[string]int{"input_tokens": 20000, "cache_read_input_tokens": 30000}}}
	oldIn, oldOut := os.Stdin, os.Stdout
	t.Cleanup(func() { os.Stdin, os.Stdout = oldIn, oldOut })
	output := testOutput(t)
	os.Stdout = output
	run := func(status bool) []byte {
		t.Helper()
		data, _ := json.Marshal(payload)
		input, err := os.CreateTemp(dir, "input-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { input.Close() })
		if _, err = input.Write(data); err != nil {
			t.Fatal(err)
		}
		_, _ = input.Seek(0, 0)
		os.Stdin = input
		args := []string{path, "claude"}
		if status {
			args = append(args, "status")
		}
		if err = observe(args); err != nil {
			t.Fatal(err)
		}
		return data
	}
	data := run(true)
	printed, _ := os.ReadFile(output.Name())
	if string(printed) != string(data) {
		t.Fatal("existing status command input/output changed")
	}
	binding := &cache.LocalBinding{Path: path, PID: os.Getpid(), Harness: "claude"}
	o, err := cache.ReadLocalObservation(binding, os.Getpid())
	if err != nil || o.Context == nil || *o.Context != 50000 || o.Window != 1000000 {
		t.Fatal(o, err)
	}
	payload["hook_event_name"], payload["source"], payload["model"] = "SessionStart", "compact", "claude-fable-5-1"
	run(false)
	o, err = cache.ReadLocalObservation(binding, os.Getpid())
	if err != nil || o.Context != nil || o.Window != 0 {
		t.Fatal("old context survived compact", o, err)
	}
}
