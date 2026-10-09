package modules

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// barConflicts refuses when the user's global tmux status line is customized
// and not bp's: bp then offers #(bp bar) instead of restyling sessions.
func barConflicts(env Env) []string {
	if env.Tmux == nil {
		return nil
	}
	var conflicts []string
	for _, option := range []string{"status-right", "status-left"} {
		value, err := env.Tmux("show-options", "-gv", option)
		if err != nil {
			// No server running: nothing to conflict with.
			return nil
		}
		value = strings.TrimRight(value, "\n")
		if isDefaultStatus(option, value) || strings.Contains(value, "bp bar") || strings.Contains(value, "bp name") {
			continue
		}
		conflicts = append(conflicts, "your tmux "+option+" is customized; keep it and add #(bp bar) to it yourself, or enable with --force so bp styles only the sessions it opens")
	}
	return conflicts
}

func isDefaultStatus(option, value string) bool {
	switch option {
	case "status-right":
		// tmux 2.x-3.x defaults all show the pane title and the clock.
		return value == "" || (strings.Contains(value, "pane_title") && strings.Contains(value, "%H:%M"))
	case "status-left":
		return value == "" || value == "[#S] " || value == "[#{session_name}] "
	}
	return false
}

// credentialTools are state paths of other tools that switch Claude logins.
var credentialTools = []string{
	".ccs",                         // ccs (Claude Code switch)
	".claude-swap",                 // claude-swap
	".claude-switch",               // claude-switch
	".config/claude-switch",        // claude-switch (XDG)
	".claude-account-switcher",     // claude-account-switcher
	".config/claude-code-accounts", // claude-code-accounts
}

// accountsConflicts refuses when something else already manages the Claude
// credentials bp would rewrite.
func accountsConflicts(env Env) []string {
	var conflicts []string
	for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if env.getenv(key) != "" {
			conflicts = append(conflicts, key+" is set in the environment; Claude Code uses it instead of the login bp would switch")
		}
	}
	claudeHome := env.getenv("CLAUDE_CONFIG_DIR")
	if claudeHome == "" && env.UserHome != "" {
		claudeHome = filepath.Join(env.UserHome, ".claude")
	}
	if claudeHome != "" {
		if data, err := os.ReadFile(filepath.Join(claudeHome, "settings.json")); err == nil {
			var settings struct {
				APIKeyHelper string `json:"apiKeyHelper"`
			}
			if json.Unmarshal(data, &settings) == nil && settings.APIKeyHelper != "" {
				conflicts = append(conflicts, "Claude settings use apiKeyHelper; another tool supplies the credentials")
			}
		}
	}
	if env.UserHome != "" {
		for _, name := range credentialTools {
			if fileExists(filepath.Join(env.UserHome, name)) {
				conflicts = append(conflicts, "~/"+name+" belongs to another Claude account switcher")
			}
		}
	}
	return conflicts
}

// waConflicts refuses when another WhatsApp Web session runs on this machine
// that is not bp's own bridge: two Baileys connections on one number log each
// other out.
func waConflicts(env Env) []string {
	if env.ProcRoot == "" {
		return nil
	}
	ours := ""
	if env.Config.WAOutbox != "" {
		ours = filepath.Join(filepath.Dir(env.Config.WAOutbox), "bridge.js")
	}
	entries, err := os.ReadDir(env.ProcRoot)
	if err != nil {
		return nil
	}
	var conflicts []string
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		data, err := os.ReadFile(filepath.Join(env.ProcRoot, name, "cmdline"))
		if err != nil || len(data) == 0 {
			continue
		}
		args := strings.Split(string(bytes.TrimRight(data, "\x00")), "\x00")
		line := strings.Join(args, " ")
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "baileys") && !strings.Contains(lower, "whatsapp-web") && !strings.Contains(lower, "whatsapp") {
			continue
		}
		if ours != "" && strings.Contains(line, ours) {
			continue
		}
		if strings.HasPrefix(filepath.Base(args[0]), "bp") {
			continue
		}
		conflicts = append(conflicts, "process "+name+" looks like another WhatsApp session: "+truncate(line, 120))
	}
	return conflicts
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
