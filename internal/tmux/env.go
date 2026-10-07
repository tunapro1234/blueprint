package tmux

import (
	"context"
	"sort"
	"strings"
)

// ClaudeSessionEnv lists the variables Claude Code exports to the commands it
// runs for one session (its Bash tool, hooks, children). A tmux server started
// from such a command keeps them in its global environment and hands them to
// every pane it creates. A Claude Code started there then believes it is a
// nested child: it reuses a foreign session id and messaging socket and, with
// CLAUDE_CODE_CHILD_SESSION set, turns transcript saving off. bp launches are
// top-level agents, so these never belong in their environment.
//
// CLAUDE_CONFIG_DIR and user settings such as CLAUDE_CODE_MAX_* are not on the
// list: they are configuration, not session identity.
var ClaudeSessionEnv = []string{
	"AI_AGENT",
	"CLAUDECODE",
	"CLAUDE_CODE_BRIDGE_SESSION_ID",
	"CLAUDE_CODE_CHILD_SESSION",
	"CLAUDE_CODE_ENTRYPOINT",
	"CLAUDE_CODE_EXECPATH",
	"CLAUDE_CODE_INVOKED_SKILLS",
	"CLAUDE_CODE_MESSAGING_SOCKET",
	"CLAUDE_CODE_MESSAGING_TOKEN",
	"CLAUDE_CODE_SESSION_ATTENDED",
	"CLAUDE_CODE_SESSION_ID",
	"CLAUDE_EFFORT",
	"CLAUDE_PID",
}

func isClaudeSessionEnv(key string) bool {
	for _, name := range ClaudeSessionEnv {
		if key == name {
			return true
		}
	}
	return false
}

// ScrubClaudeSessionEnv returns env (KEY=value entries) without the inherited
// Claude Code session variables, and the sorted names it removed.
func ScrubClaudeSessionEnv(env []string) ([]string, []string) {
	kept := make([]string, 0, len(env))
	var removed []string
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if isClaudeSessionEnv(key) {
			removed = append(removed, key)
			continue
		}
		kept = append(kept, entry)
	}
	sort.Strings(removed)
	return kept, removed
}

// GlobalClaudeSessionEnv reports which Claude Code session variables the tmux
// server's global environment holds. Only names are returned; the values
// include a messaging token.
func (c *Client) GlobalClaudeSessionEnv(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, nil, "show-environment", "-g")
	if err != nil {
		return nil, err
	}
	var found []string
	for _, line := range strings.Split(string(out), "\n") {
		// "-NAME" marks a variable removed from the environment.
		key, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && isClaudeSessionEnv(key) {
			found = append(found, key)
		}
	}
	sort.Strings(found)
	return found, nil
}
