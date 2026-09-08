package book

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"blueprint/internal/cache"
	bptmux "blueprint/internal/tmux"
)

// This is first-message readiness, not a fallback for stale/uncertain turns.
// Read the entire small transcript: a tail alone cannot prove no turn occurred.
func claudePreTurn(path, id, cwd string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 || info.Size() > 128*1024 {
		return false
	}
	data := make([]byte, info.Size())
	if _, err = f.ReadAt(data, 0); err != nil {
		return false
	}
	if data[len(data)-1] != '\n' {
		return false
	}
	bound := false
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var row struct {
			Type, Subtype, SessionID, CWD, Content string
			IsSidechain, IsMeta, IsCompactSummary  bool
			Message                                struct{ Content json.RawMessage }
		}
		if json.Unmarshal(line, &row) != nil || row.IsSidechain || row.IsCompactSummary {
			return false
		}
		if row.SessionID != "" {
			if row.SessionID != id {
				return false
			}
			bound = true
		}
		if row.CWD != "" && filepath.Clean(row.CWD) != filepath.Clean(cwd) {
			return false
		}
		switch row.Type {
		case "custom-title", "agent-name", "ai-title", "mode", "permission-mode", "bridge-session", "file-history-snapshot":
		case "system":
			if row.Subtype != "local_command" || !claudeInitialCommand(row.Content) {
				return false
			}
		case "user":
			if row.IsMeta {
				continue
			}
			var content string
			if json.Unmarshal(row.Message.Content, &content) != nil {
				return false
			}
			if !claudeInitialCommand(content) {
				return false
			}

		default:
			// Includes real assistant work, compaction, unknown events and continued-in.
			return false
		}
	}
	return bound
}

func applyClaudePreTurn(state cache.State, a *cache.Activity, cwd, pane string) {
	if state.Runtime != "claude" || a.State != "unknown" || a.ThreadID == "" {
		return
	}
	if a.Reason != "no readable decisive turn event" && a.Reason != "waiting for decisive Claude turn event" {
		return
	}
	if a.Binding != "claude-process-session" && a.Binding != "local-launch-observer" {
		return
	}
	if !bptmux.ClaudeEmptyComposer(pane) || !claudePreTurn(a.TranscriptPath, a.ThreadID, cwd) {
		return
	}
	a.State, a.Source, a.Reason = "idle", "claude-pre-turn", ""
}

func claudeInitialCommand(content string) bool {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "<local-command-stdout>") || strings.HasPrefix(content, "<local-command-caveat>") {
		return true
	}
	command, _, ok := strings.Cut(strings.TrimPrefix(content, "<command-name>"), "</command-name>")
	if !ok || !strings.HasPrefix(content, "<command-name>") {
		return false
	}
	switch command {
	case "/rename", "/model", "/effort", "/color":
		return true
	}
	return false
}
