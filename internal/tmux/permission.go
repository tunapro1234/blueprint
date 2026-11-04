package tmux

import (
	"regexp"
	"strings"
)

// Claude Code asks before some tool calls even in bypass-permissions mode (a
// Bash command with brace expansion, seen on zaganos-egitim on 4 Nov 2026):
//
//	────────────────────────
//	 Bash command
//	 ...
//	 │ wc -l katalog/{intake,shooter,gripper}.md
//	 Brace expansion
//
//	 Do you want to proceed?
//	 ❯ 1. Yes
//	   2. Yes, and switch to auto mode · ...
//	   3. No
//
//	 Esc to cancel · Tab to amend
//
// The composer is gone while this is open, so the pane used to read as
// "composer contains foreign text". It is a question only a human may answer.
var (
	permissionQuestion = regexp.MustCompile(`^\s*Do you want to `)
	permissionYes      = regexp.MustCompile(`^\s*(❯\s*)?1\.\s+Yes\b`)
	permissionRule     = regexp.MustCompile(`^─{20,}`)
)

// permissionTailRows bounds how far up the prompt's question may sit; the
// command block above it can be several rows tall.
const permissionTailRows = 40

// ClaudePermissionPrompt reports whether a Claude pane is waiting on a tool
// permission question and, if so, a one-line summary: the prompt's heading and
// the first line of the command it asks about ("Bash command: cd /srv/...").
func ClaudePermissionPrompt(pane string) (string, bool) {
	lines := trimTrailingBlank(strings.Split(ansiSeq.ReplaceAllString(pane, ""), "\n"))
	from := len(lines) - permissionTailRows
	if from < 0 {
		from = 0
	}
	question, yes, footer := -1, -1, -1
	for i := from; i < len(lines); i++ {
		switch line := lines[i]; {
		case permissionQuestion.MatchString(line):
			question, yes, footer = i, -1, -1
		case question >= 0 && yes < 0 && permissionYes.MatchString(line):
			yes = i
		case yes >= 0 && strings.Contains(line, "Esc to cancel"):
			footer = i
		}
	}
	if question < 0 || yes < 0 || footer < 0 {
		return "", false
	}
	rule := -1
	for i := question - 1; i >= 0; i-- {
		if permissionRule.MatchString(strings.TrimSpace(lines[i])) {
			rule = i
			break
		}
	}
	heading, command := "", ""
	for i := rule + 1; i < question; i++ {
		text := strings.TrimSpace(lines[i])
		if text == "" {
			continue
		}
		if heading == "" {
			heading = text
			continue
		}
		if strings.HasPrefix(text, "│") {
			command = strings.TrimSpace(strings.TrimPrefix(text, "│"))
			break
		}
	}
	if heading == "" {
		heading = strings.TrimSpace(lines[question])
	}
	summary := heading
	if command != "" {
		summary += ": " + command
	}
	if r := []rune(summary); len(r) > 120 {
		summary = string(r[:119]) + "…"
	}
	return summary, true
}
