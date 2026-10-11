package book

// console.go assembles the read view and the hierarchy edit the fleet console
// (the local web UI, docs/fleet-console.md WS1) needs, on top of the agentbook
// and the one runtime observation every other surface already uses. It adds no
// new notion of agent state: status is copied verbatim from the observation,
// never recomputed here, so an unknown runtime stays unknown.

import (
	"errors"
	"fmt"
	"time"

	"blueprint/internal/cache"
)

// ConsoleAgent is one agent as the fleet console shows it. Status and
// StatusReason come straight from the runtime observation (book.RuntimeFor in
// production); HasTurn reports whether LastTurnAt is known rather than guessing
// a zero time.
type ConsoleAgent struct {
	Name         string
	Parent       string
	Role         string
	Host         string
	Runtime      string
	Status       string
	StatusReason string
	LastTurnAt   time.Time
	HasTurn      bool
	Folder       string
	Open         bool
}

// ConsoleTree builds the console view of fleet in stable name order.
//
// observe returns the runtime state for one agent; in production it is
// book.RuntimeFor (or its batched RuntimeForWithSessions form) over a tmux
// client. Status and StatusReason are taken from that observation verbatim: an
// agent with no observation, or an observation that could not classify the
// pane, stays "unknown". open names the agents that have a live session, and
// localHost is the host reported for agents that do not record a remote.
func ConsoleTree(fleet Fleet, open map[string]bool, localHost string, observe func(name string) cache.State) []ConsoleAgent {
	names := fleet.SortedNames()
	out := make([]ConsoleAgent, 0, len(names))
	for _, name := range names {
		agent := fleet.Agents[name]
		state := observe(name)
		status, reason := "unknown", ""
		if state.Activity != nil {
			status = state.Activity.State
			reason = state.Activity.Reason
		}
		runtime := state.Runtime
		if runtime == "" {
			// A closed agent has no observed harness; the agentbook still knows
			// which one it is configured to run. This is a recorded fact, not a
			// guessed status.
			runtime = configuredHarness(agent)
		}
		folder := FirstPath(agent.Folder)
		if folder == "" {
			folder = agent.Folder
		}
		entry := ConsoleAgent{
			Name:         name,
			Parent:       fleet.Parents[name],
			Role:         agent.Role,
			Host:         consoleHost(agent, localHost),
			Runtime:      runtime,
			Status:       status,
			StatusReason: reason,
			Folder:       folder,
			Open:         open[name],
		}
		if state.TurnKnown && !state.TurnAt.IsZero() {
			entry.LastTurnAt, entry.HasTurn = state.TurnAt, true
		}
		out = append(out, entry)
	}
	return out
}

// consoleHost reports where an agent physically runs: the remote it was launched
// on (bp open --remote <host>), or the local machine otherwise. An agent records
// its remote in its launch options, so a remote agent is attributed to the peer
// it lives on rather than to this host.
func consoleHost(agent Agent, localHost string) string {
	if agent.Launch != nil && agent.Launch.Remote != "" {
		return agent.Launch.Remote
	}
	return localHost
}

// configuredHarness is the harness an agent is configured to launch, read from
// its recorded launch options. It is only a display fallback for closed agents;
// a live agent's harness comes from the observation.
func configuredHarness(agent Agent) string {
	if agent.Launch == nil {
		return ""
	}
	switch {
	case agent.Launch.Hermes:
		return "hermes"
	case agent.Launch.OpenCode:
		return "opencode"
	case agent.Launch.Codex:
		return "codex"
	default:
		return "claude"
	}
}

// Reparent moves name under parent in the agentbook and returns the previous
// parent. An empty parent detaches name, so it falls back under the fleet root
// (or the book's configured default parent). It validates that name, and a
// non-empty parent, are registered, and refuses any change that would make the
// hierarchy loop. Only a registered agent can be reparented; a live but
// unregistered agent has no agentbook row to edit and reads as ErrUnknownAgent.
//
// The write goes through the same locked, atomic agentbook mutation as every
// other hierarchy edit, and leaves a record a previous release still reads: the
// parent is a plain field that has always been optional.
func Reparent(paths []string, name, parent string) (previous string, err error) {
	fleet, err := LoadFleet(Paths(paths))
	if err != nil {
		return "", err
	}
	if _, ok := fleet.Agents[name]; !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownAgent, name)
	}
	if name == fleet.Root {
		return "", fmt.Errorf("%w: %s", ErrReparentRoot, name)
	}
	previous = fleet.Parents[name]
	if parent == "" {
		if previous == "" {
			return previous, nil
		}
		return previous, mutate(paths, name, func(agent map[string]any) bool {
			if current, _ := agent["parent"].(string); current == "" {
				return false
			}
			agent["parent"] = ""
			return true
		})
	}
	if _, ok := fleet.Agents[parent]; !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownParent, parent)
	}
	if name == parent {
		return "", fmt.Errorf("%w: %s under itself", ErrReparentCycle, name)
	}
	// Walk up from the proposed parent: if we reach name, parent sits below it
	// and the move would create a cycle.
	for at := parent; at != ""; at = fleet.Parents[at] {
		if at == name {
			return "", fmt.Errorf("%w: %s is below %s", ErrReparentCycle, parent, name)
		}
		if at == fleet.Root {
			break
		}
	}
	if previous == parent {
		return previous, nil
	}
	return previous, mutate(paths, name, func(agent map[string]any) bool {
		agent["parent"] = parent
		return true
	})
}

// Reparent validation errors, so a transport can map each to the right status.
var (
	ErrUnknownAgent  = errors.New("unknown agent")
	ErrUnknownParent = errors.New("unknown parent")
	ErrReparentRoot  = errors.New("agent is the root of the tree and has no parent")
	ErrReparentCycle = errors.New("reparent would create a cycle")
)
