package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"blueprint/internal/identity"
	"blueprint/internal/msgq"
	"blueprint/internal/pending"
)

// The hook delivery path (docs/direction.md, "Hooks"): the harness asks bp at a
// turn boundary and bp answers with the messages queued for the agent. Nothing
// is typed into a terminal, so it works for an agent that runs outside tmux,
// and it can never land in a busy turn or on top of a half-written line.
//
// Every failure here is swallowed: a hook that errors shows up in the agent's
// UI and a broken bp must never break the agent. A message bp could not claim
// stays queued for the ordinary delivery pass.

// hookStopCap bounds how many turns in a row a Stop hook may keep the agent
// going without a prompt from its user. Two agents that answer each other at
// every turn boundary would otherwise never stop; the global loop cap (W1)
// protects the network, this protects one session.
const hookStopCap = 8

// hookMaxMessages bounds one hook's delivery so one turn is not flooded; the
// rest are taken at the next boundary.
const hookMaxMessages = 10

type claudeHookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Event          string `json:"hook_event_name"`
	Source         string `json:"source"`
	StopHookActive bool   `json:"stop_hook_active"`
}

// hookCommand runs `bp _hook <harness> [--agent <name>]` with the harness's
// hook payload on stdin.
func (a *app) hookCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bp _hook claude [--agent <name>]")
	}
	harness, rest := args[0], args[1:]
	agent := ""
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--agent" && i+1 < len(rest) {
			agent = rest[i+1]
			i++
		}
	}
	if harness != "claude" {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 2<<20))
	if err != nil {
		return nil
	}
	var input claudeHookInput
	if json.Unmarshal(data, &input) != nil {
		return nil
	}
	if agent == "" {
		agent = a.hookAgent()
	}
	if agent == "" {
		return nil
	}
	out, err := a.claudeHook(agent, input)
	if err != nil {
		fmt.Fprintln(a.err, "bp hook:", err)
		return nil
	}
	if out != nil {
		_ = json.NewEncoder(a.out).Encode(out)
	}
	return nil
}

// hookAgent names the agent the hook runs for. Only a verified label counts: a
// guessed one would hand another agent's messages to this session.
func (a *app) hookAgent() string {
	who := a.senderIdentity()
	if !who.Certain || !identity.ValidName(who.Label) {
		return ""
	}
	return who.Label
}

// claudeHook turns one Claude Code hook event into its JSON answer, or nil
// when there is nothing to say.
func (a *app) claudeHook(agent string, input claudeHookInput) (map[string]any, error) {
	switch input.Event {
	case "UserPromptSubmit":
		a.resetStopCount(input.SessionID)
		text, err := a.claimForHook(agent)
		if err != nil || text == "" {
			return nil, err
		}
		return map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName":     "UserPromptSubmit",
			"additionalContext": text,
		}}, nil
	case "Stop":
		if a.stopCount(input.SessionID) >= hookStopCap {
			return nil, nil
		}
		text, err := a.claimForHook(agent)
		if err != nil || text == "" {
			return nil, err
		}
		a.bumpStopCount(input.SessionID)
		return map[string]any{"decision": "block", "reason": text}, nil
	case "SessionStart":
		a.resetStopCount(input.SessionID)
		// After /compact or /clear the conversation that knew who this agent is
		// is gone; startup and resume carry bp's own launch prompt or the old
		// transcript, so they need nothing.
		if input.Source != "compact" && input.Source != "clear" {
			return nil, nil
		}
		note := identityNote(agent)
		text, err := a.claimForHook(agent)
		if err != nil {
			text = ""
		}
		if text != "" {
			note += "\n\n" + text
		}
		return map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": note,
		}}, nil
	}
	return nil, nil
}

// identityNote is what an agent needs to keep working with bp after its
// context was compacted or cleared.
func identityNote(agent string) string {
	return fmt.Sprintf("[bp] Your context was compacted. You are still the bp agent %q; your queued messages were kept. "+
		"Reply with `bp msg <agent> '<text>'`, see the other agents with `bp status`, and run `bp help` for more.", agent)
}

// claimForHook takes the agent's queued messages (and any offline spool) and
// returns them as one block of text, exactly as terminal delivery would have
// pasted them.
func (a *app) claimForHook(agent string) (string, error) {
	var parts []string
	if snapshot, err := pending.Load(a.config.StateDir, agent); err == nil && len(snapshot.Entries) > 0 {
		if err := pending.Acknowledge(a.config.StateDir, agent, snapshot.Entries); err == nil {
			parts = append(parts, formatDigest(snapshot.Entries, snapshot.Dropped))
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, pending.ErrReadOnly) {
		return "", err
	}
	if a.queue != nil {
		claimed, err := a.queue.Claim(agent, msgq.StatusDeliveredHook, hookMaxMessages)
		if err != nil && len(parts) == 0 {
			return "", err
		}
		for _, message := range claimed {
			parts = append(parts, message.Msg)
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	header := "[bp] 1 message arrived while you were working:"
	if len(parts) > 1 {
		header = fmt.Sprintf("[bp] %d messages arrived while you were working:", len(parts))
	}
	return header + "\n\n" + strings.Join(parts, "\n\n"), nil
}

// The Stop counter lives in a small file per Claude session id, so it survives
// between hook processes and is never shared between sessions.
func (a *app) stopCountPath(session string) string {
	if session == "" || strings.ContainsAny(session, `/\`) || session == "." || session == ".." {
		return ""
	}
	return filepath.Join(a.config.StateDir, "hooks", "claude-stop-"+session)
}

func (a *app) stopCount(session string) int {
	path := a.stopCountPath(session)
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n
}

func (a *app) bumpStopCount(session string) {
	path := a.stopCountPath(session)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(a.stopCount(session)+1)), 0600)
}

func (a *app) resetStopCount(session string) {
	if path := a.stopCountPath(session); path != "" {
		_ = os.Remove(path)
	}
}

// claudeHookGroups returns the hook entries bp adds to a Claude session's
// --settings layer for the hook delivery path.
func claudeHookGroups(command string) map[string]any {
	entry := func() []any {
		return []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 10}}}}
	}
	return map[string]any{
		"UserPromptSubmit": entry(),
		"Stop":             entry(),
		"SessionStart":     entry(),
	}
}
