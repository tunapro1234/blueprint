package book

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"blueprint/internal/identity"
)

var threadUUID = regexp.MustCompile(`^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

// ThreadIdentity uses explicit identity pins, never cwd or the latest rollout.
// Parent links come from Codex session metadata. They identify a subagent's
// origin but do not grant it the parent's name or hierarchy authority.
func ThreadIdentity(ctx context.Context, paths []string, home, id string) identity.Identity {
	unknown := identity.Identity{Label: "codex?:" + id, ThreadID: id, Source: "codex-unmapped"}
	if !threadUUID.MatchString(id) {
		unknown.Label = identity.Unknown
		return unknown
	}
	fleet, err := LoadFleet(paths)
	if err != nil {
		return unknown
	}
	original := id
	seen := map[string]bool{}
	for depth := 0; depth < 8 && !seen[id]; depth++ {
		seen[id] = true
		if ctx != nil && ctx.Err() != nil {
			return unknown
		}
		parent, err := threadParent(home, id)
		if err != nil {
			return unknown
		}
		if parent != "" {
			id = parent
			continue
		}
		matched := ""
		for _, agent := range fleet.Agents {
			pin := agent.IdentityThreadID
			if agent.Launch != nil && agent.Launch.Codex && agent.Launch.ResumeID != "" {
				if pin != "" && pin != agent.Launch.ResumeID {
					continue
				}
				// Launch discovery can use cwd; it is not identity evidence.
			}
			if pin == id {
				if matched != "" {
					return unknown
				}
				matched = agent.Name
			}
		}
		if matched == "" {
			return unknown
		}
		if original != id {
			return identity.Identity{Label: matched + "/subagent:" + original, ThreadID: original, Parent: matched, Certain: true, Source: "codex-subagent"}
		}
		return identity.Identity{Label: matched, ThreadID: id, Certain: true, Source: "codex-thread"}
	}
	return unknown
}

func threadParent(home, id string) (string, error) {
	if !threadUUID.MatchString(id) {
		return "", fmt.Errorf("invalid thread")
	}
	matches, err := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "*-"+id+".jsonl"))
	if err != nil || len(matches) != 1 {
		return "", fmt.Errorf("thread metadata missing or ambiguous")
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	if !scanner.Scan() {
		return "", fmt.Errorf("unreadable session metadata")
	}
	var row struct {
		Type    string `json:"type"`
		Payload struct {
			ID     string          `json:"id"`
			Source json.RawMessage `json:"source"`
		} `json:"payload"`
	}
	if json.Unmarshal(scanner.Bytes(), &row) != nil || row.Type != "session_meta" || row.Payload.ID != id {
		return "", fmt.Errorf("thread metadata mismatch")
	}
	var source string
	if json.Unmarshal(row.Payload.Source, &source) == nil {
		switch source {
		case "cli", "vscode", "appServer", "app-server":
			return "", nil
		}
		return "", fmt.Errorf("not an interactive thread")
	}
	var sourceObject struct {
		Subagent struct {
			Spawn struct {
				Parent string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	}
	if json.Unmarshal(row.Payload.Source, &sourceObject) == nil && threadUUID.MatchString(sourceObject.Subagent.Spawn.Parent) {
		return sourceObject.Subagent.Spawn.Parent, nil
	}
	return "", fmt.Errorf("unknown thread origin")
}
