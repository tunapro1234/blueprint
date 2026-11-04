package tmux

import (
	"strings"
	"testing"
)

// claudePermissionPane is the bottom of zaganos-egitim on 4 Nov 2026: Claude
// Code (bypass-permissions mode) asking before a Bash command with brace
// expansion. bp reported it as "composer contains foreign text".
const claudePermissionPane = `
────────────────────────────────────────────────────────────
 Bash command
 Tip: auto mode handles these prompts for you — choose "switch to auto mode" below
 Scout week 5 lessons and sources
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 │ cd /srv/probot/egitim/dersler/zaganos-taslak && ls -R hafta-5 | head -40; wc -l hafta-5/ders-2*/ders.md; cat hafta-5/hafta.json; git -C /srv/probot/egitim
 │ status --short -- dersler/zaganos-taslak/hafta-5 | head; ls /srv/probot/egitim-wt-ftc0/egitim/katalog/ | head -40; wc -l
 │ /srv/probot/egitim-wt-ftc0/egitim/katalog/{intake,shooter,gripper}.md /srv/probot/egitim/arastirma/kaynak/biobuzz-manual-v0-9-tr-2026-09-27.txt
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 Brace expansion

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and switch to auto mode · auto mode handles these prompts for you
   3. No

 Esc to cancel · Tab to amend
──────────────────────────────────────── zaganos-egitim ─
`

func TestClaudePermissionPromptIsReportedWithItsCommand(t *testing.T) {
	summary, ok := ClaudePermissionPrompt(claudePermissionPane)
	if !ok {
		t.Fatal("permission prompt not recognised")
	}
	if want := "Bash command: cd /srv/probot/egitim/dersler/zaganos-taslak && ls -R hafta-5"; !strings.HasPrefix(summary, want) {
		t.Fatalf("summary = %q, want prefix %q", summary, want)
	}
	reason := ComposerContentBlockReason(claudePermissionPane, nil)
	if !strings.HasPrefix(reason, BlockedByPermission+" (Bash command: cd /srv/probot/") {
		t.Fatalf("reason = %q", reason)
	}
	if !Dialog(claudePermissionPane) {
		t.Fatal("permission prompt must count as a dialog nobody may type into")
	}
}

func TestClaudePermissionPromptNeedsTheLiveQuestion(t *testing.T) {
	// The same rows scrolled far up into the transcript, under a normal composer.
	pane := claudePermissionPane + strings.Repeat("output line\n", 45) +
		"────────────────────────────────────────────────────────────\n❯ \n────────────────────────────────────────────────────────────\n  -- INSERT -- ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	if summary, ok := ClaudePermissionPrompt(pane); ok {
		t.Fatalf("old prompt in scrollback read as live: %q", summary)
	}
	// A question without the "1. Yes" option list is prose, not the prompt.
	if _, ok := ClaudePermissionPrompt("Do you want to proceed?\nmaybe\nEsc to cancel\n"); ok {
		t.Fatal("question without options read as a prompt")
	}
}
