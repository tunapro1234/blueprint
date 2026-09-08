package book

import (
	"blueprint/internal/identity"
	bptmux "blueprint/internal/tmux"
	"context"
	"path/filepath"
)

// LocalThreadHint makes an unverified caller readable. Pane ownership and held
// locks identify the local CLI, not which of its threads issued this execution.
// These hints deliberately never grant hierarchy authority or write a pin.
func LocalThreadHint(ctx context.Context, client *bptmux.Client, paths []string, home, id string) identity.Identity {
	if client == nil || !threadUUID.MatchString(id) {
		return identity.Identity{}
	}
	session, err := client.DisplaySession(ctx)
	if err != nil {
		return identity.Identity{}
	}
	fleet, err := LoadFleet(paths)
	if err != nil {
		return identity.Identity{}
	}
	agent, ok := fleet.Agents[session]
	if !ok || agent.Local == nil || agent.Local.Harness != "codex" || filepath.Clean(agent.Local.Home) != filepath.Clean(home) || !identity.ValidName(agent.Name) {
		return identity.Identity{}
	}
	process, err := client.PaneProcess(ctx, session)
	if err != nil {
		return identity.Identity{}
	}
	for name, other := range fleet.Agents {
		if name != session && other.Local != nil && other.Local.PID == process.PID {
			return identity.Identity{}
		}
	}
	observation, err := localCodexObservation(agent.Local, process.PID)
	if err != nil {
		return identity.Identity{}
	}
	return localLineageHint(home, agent.Name, observation.SessionID, id)
}

func localLineageHint(home, name, root, id string) identity.Identity {
	original := id
	seen := map[string]bool{}
	for depth := 0; depth < 8 && !seen[id]; depth++ {
		seen[id] = true
		parent, err := threadParent(home, id)
		if err != nil {
			break
		}
		if parent != "" {
			id = parent
			continue
		}
		if id != root {
			break
		}
		who := identity.Identity{Label: name + "?", ThreadID: original, Source: "codex-local-hint"}
		if original != root {
			who.Label = name + "/subagent:" + original + "?"
			who.Parent = name
		}
		return who
	}
	return identity.Identity{}
}
