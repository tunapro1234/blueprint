package main

// Copies of the pane fixture builders of internal/tmux/*_test.go (test files
// cannot be imported). Text is verbatim; keep in sync with the Go tests.

import (
	"strings"

	bptmux "blueprint/internal/tmux"
)

const (
	boxBorderTop    = "──────────────────── probot-main ──"
	boxBorderBottom = "───────────────────────────────────"
	boxStatus       = "  -- INSERT -- ⏵⏵ bypass permissions on (shift+tab to cycle)    /rc"
	boxChip         = "  ⧉  fon-panosu"
	emptyRow        = "❯ "
	stuckMessage    = "[server-main] roadmap review: finish the goal-system section today; hierarchical assignment is missing"
	composerBorder  = "──────────────────────────────\n"
	quotedSpinner   = "  signature ✻ Symbioting… (29s · ↓ 163 tokens). So the door is not closed."
	collapsedChip   = "\x1b[1m›\x1b[0m \x1b[38;5;6m[Pasted Content 1024 chars]\x1b[39m\n  gpt-5.6-sol low · /tmp\n"
	expandedChip    = "\x1b[1m›\x1b[0m \x1b[38;5;6m[Pasted Content 1024\x1b[39m\n  \x1b[38;5;6mchars]\x1b[39mAAAAAAAAAAAAAAAAAAAA\n  gpt-5.6-sol low · /tmp\n"
	ghostComposer   = "\x1b[1m›\x1b[0m \x1b[2mExplain this codebase\x1b[22m\n  gpt-5.6-sol low · /tmp\n"
	busyQueueChip   = "\x1b[2mWorking (12s · esc to interrupt)\x1b[0m\n" +
		"\x1b[0;1m›\x1b[0m \x1b[38;5;6m[Pasted Content 1021 chars]\x1b[39mthe popup star is the order flow.\n" +
		"  \x1b[2mtab to queue message\x1b[0m                                      \x1b[2m30% context left\x1b[0m\n"
	codexMessage = "[probot-outreach] blueprint fixed your bp sending failure (16ed445): delivery was not the problem; the spool was. bp opened the spool for writing to add announcements, and your sandbox mounted /srv/blueprint read-only, so it failed at the first step. Now, if the spool is read-only, the message is sent ALONE without announcements. VERIFICATION REQUEST: send me a short test message when convenient."
	gpt6Message  = "[lead] Your task is ready: read /work/project/brief.md and follow it. Ask the lead if anything is unclear."

	hermesStatusLine   = " ⚕ x-preview-f-free · 2% · 12m               ─ Say and /srv directory..."
	hermesRule         = "────────────────────────────────────────────────────────────────────"
	hermesIdleRow      = "❯ Ask anything, or type / for commands…"
	hermesIdleRowAnsi  = "\x1b[38;5;230m❯ \x1b[3m\x1b[38;5;136mAsk anything, or type / for commands…\x1b[0m"
	hermesGhostRowAnsi = "\x1b[38;5;230m❯ \x1b[3m\x1b[38;5;136mDraft a reply to the last email in my inbox\x1b[0m"
	hermesBusyRow      = "⚕ ❯ msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel"
	hermesBusyTypedRow = "⚕ ❯ there is also a third line"
	hermesIdleTypedRow = "❯ there is also a third line"
	hermesKaomoji      = "  (¬_¬) processing..."

	codexNavigationFixture = "\n› 1. New chat\n  2. Agent command center\n  3. Resume another chat\n"
	codexTrustPane         = `  Folder access
  /work/new
  Trust this folder? Codex can read, edit, and run files here, subject to your permission
  settings. Folder settings can run code automatically, even without a model request. Continue
  only if you trust these files. Your trust decision will be saved.
› 1. Trust and continue
  2. Back to Agent Command Center
  enter continue · esc back
`
	claudeTrustFixture = `Accessing workspace:

 /srv/agent

 Quick safety check: Is this a project you created or one you trust? (Like your own
 code, a well-known open source project, or work from your team).

 ❯ 1. Yes, I trust this folder
   2. No, exit

 Enter to confirm · Esc to cancel
`
	rcMenu       = "Remote Control\n❯ Continue\n  Show QR code\n  Disconnect this session\nEnter to select · Esc to cancel\n"
	rcURL        = "https://claude.ai/code/session_01ABC"
	rcActive     = "❯ /remote-control\n  ⎿  /remote-control is active · Continue here, on your phone, or at " + rcURL + "\n"
	rcDisconnect = "● Remote Control disconnected — signed-in claude.ai account or organization changed on this machine — run /remote-control to start a session for the current account\n"
)

var usageNotices = []string{
	"  ⚠ Usage limit reached · limit resets 5:40pm · clau.de/wrap-up · /upgrade to keep using …",
	"  ⚠ While you wait, start a new cloud session by claiming a $250 credit",
	"  ⚠ /low-priority to continue now at lower priority · uses your weekly limit",
}

func claudePane(rows ...string) string {
	lines := []string{"  agent: output left from the previous turn", "  ─────── summary ───────"}
	lines = append(lines, boxBorderTop)
	lines = append(lines, rows...)
	lines = append(lines, boxBorderBottom, boxStatus, boxChip, "")
	return strings.Join(lines, "\n")
}

func screenFillingPane(rows ...string) string {
	lines := []string{boxBorderTop}
	lines = append(lines, rows...)
	lines = append(lines, boxBorderBottom, boxStatus, boxChip, "")
	return strings.Join(lines, "\n")
}

func collapsedPane() string {
	return strings.Join([]string{"  agent: output left from the previous turn", emptyRow, boxBorderBottom, boxStatus, boxChip, ""}, "\n")
}

func pickerPane() string {
	return strings.Join([]string{
		"  agent: output left from the previous turn",
		boxBorderBottom,
		"  Which file should we open?",
		"❯ 1. first option",
		"  2. second option",
		boxBorderBottom,
		"  Enter to select · Tab/Arrow keys to navigate · Esc to cancel",
		"",
	}, "\n")
}

func pickerOverStatusPane() string {
	return strings.Join([]string{
		"  agent: output left from the previous turn",
		boxBorderTop,
		"❯ 1. first option",
		boxBorderBottom,
		boxStatus,
		"  Enter to select · Esc to cancel",
		"",
	}, "\n")
}

func busyPane(rows ...string) string {
	return "✻ Working… (23s · Esc to interrupt)\n" + claudePane(rows...)
}

func spinnerPane(transcript ...string) string {
	return strings.Join(transcript, "\n") + "\n" + claudePane(emptyRow)
}

func claudePaneWithUsageLimit(pane string, notices ...string) string {
	lines := strings.Split(pane, "\n")
	for i, line := range lines {
		if line == boxBorderBottom {
			withNotices := append([]string(nil), lines[:i+1]...)
			withNotices = append(withNotices, notices...)
			withNotices = append(withNotices, lines[i+1:]...)
			return strings.Join(withNotices, "\n")
		}
	}
	return pane
}

func codexPaneWith(rows []string) string {
	lines := []string{"• Ran 6 commands · ctrl + t to view transcript", "", strings.Repeat("─", 70), ""}
	for i, row := range rows {
		if i == 0 {
			lines = append(lines, "› "+row)
			continue
		}
		lines = append(lines, "  "+row)
	}
	lines = append(lines, "", "  gpt-5.6-sol medium fast · /srv/probot/out-codex", "")
	return strings.Join(lines, "\n")
}

func wrapText(text string, width int) []string {
	var rows []string
	for len(text) > width {
		rows = append(rows, text[:width])
		text = text[width:]
	}
	return append(rows, text)
}

func modernCodexPane(text string) string {
	return "• Previous answer\n\n› " + text + "\n  gpt-6-astra high · /srv/blueprint\n"
}

func gpt6Pane(composer ...string) string {
	pane := "\x1b[2m  12:28 AM\x1b[0m\n\n\n"
	for i, row := range composer {
		if i == 0 {
			pane += "\x1b[1m\x1b[38;5;215m›\x1b[0m " + row + "\n"
			continue
		}
		pane += "  " + row + "\n"
	}
	return pane + "\n  \x1b[38;5;223mGPT-6-Luna max\x1b[39m · \x1b[38;5;151m/work/project\x1b[39m · \x1b[38;5;211mthread title\x1b[39m"
}

func codex36x15(transcript string, rows []string) string {
	lines := make([]string, 15)
	lines[0] = transcript
	for i, row := range rows {
		if i == 0 {
			lines[i+2] = "› " + row
		} else {
			lines[i+2] = "  " + row
		}
	}
	lines[13] = "  GPT-6-Luna max · /srv/project"
	return strings.Join(lines, "\n")
}

func hermesTranscript() []string {
	return []string{
		"╭─ ⚕ Hermes ───────────────────────────────────────────────────────╮",
		"the /srv directory contains folders such as blueprint, kavram, and outpost.",
		"╰──────────────────────────────────────────────────────────────────╯",
		"────────────────────────────────────────",
		"● Please run `sleep 45` with your terminal tool, then say it is done.",
		"────────────────────────────────────────",
		"",
	}
}

func hermesPane(rows ...string) string {
	lines := hermesTranscript()
	lines = append(lines, hermesStatusLine, hermesRule)
	lines = append(lines, rows...)
	lines = append(lines, hermesRule, "")
	return strings.Join(lines, "\n")
}

func hermesBusyPane(composer string) string {
	lines := hermesTranscript()
	lines = append(lines, hermesKaomoji, "", hermesStatusLine, hermesRule, composer, hermesRule, "")
	return strings.Join(lines, "\n")
}

func openCodePane(composer []string, busy bool) string {
	lines := []string{
		"  ┃  my previous message is drawn with the same rail in the transcript",
		"",
		"     ▣  Build · Ox Alpha (stealth)",
		"",
		"  ┃",
	}
	for _, row := range composer {
		lines = append(lines, "  ┃  "+row)
	}
	lines = append(lines, "  ┃", "  ┃  Build · Ox Alpha (stealth) OpenRouter", "  ╹"+strings.Repeat("▀", 60))
	if busy {
		lines = append(lines, "   ⬝⬝⬝⬝⬝■■■  esc interrupt               tab agents  ctrl+p commands")
	} else {
		lines = append(lines, "  tab agents  ctrl+p commands")
	}
	lines = append(lines, "", "  /srv/compec/mail-ox:master                                 1.18.23", "")
	return strings.Join(lines, "\n")
}

// paneCase is one corpus screen and the messages a sender would own.
type paneCase struct {
	name  string
	pane  string
	texts []string
}

func basePanes() []paneCase {
	onboarding := bptmux.OnboardingPrompt("agent")
	digits := strings.Repeat("0123456789", 45) + "END!"
	wide := "[bp] 日本語のメッセージ 🚀 テスト、これは十分に長い文章です。確認してください。"
	var onboardingRows []string
	for i, row := range wrapText(onboarding, 70) {
		if i == 0 {
			onboardingRows = append(onboardingRows, "❯ "+row)
		} else {
			onboardingRows = append(onboardingRows, "  "+row)
		}
	}
	var stuckRows []string
	for i, row := range wrapText(stuckMessage, 40) {
		if i == 0 {
			stuckRows = append(stuckRows, "❯ "+row)
		} else {
			stuckRows = append(stuckRows, "  "+row)
		}
	}
	return []paneCase{
		{"empty string", "", nil},
		{"shell prompt", "root@host:~# \n", nil},
		{"plain prompt", "❯ \n", nil},
		{"claude empty", claudePane(emptyRow), []string{stuckMessage}},
		{"claude stuck", claudePane("❯ " + stuckMessage), []string{stuckMessage}},
		{"claude stuck wrapped", claudePane(stuckRows...), []string{stuckMessage}},
		{"claude stuck damaged", claudePane("❯ " + strings.Replace(stuckMessage, "goal-system", "goal-sys", 1)), []string{stuckMessage}},
		{"claude stuck prefix", claudePane("❯ " + stuckMessage[:50]), []string{stuckMessage}},
		{"claude foreign", claudePane("❯ ls -la /srv"), []string{stuckMessage}},
		{"claude paste chip", claudePane("❯ [Pasted text #1 +12 lines]"), []string{stuckMessage}},
		{"claude wide", claudePane("❯ " + wide), []string{wide}},
		{"claude digits tail", claudePane("❯ " + digits[len(digits)-48:]), []string{digits}},
		{"claude onboarding", claudePane(onboardingRows...), []string{onboarding}},
		{"claude onboarding other", claudePane("❯ " + bptmux.OnboardingPrompt("someone-else")), []string{onboarding}},
		{"screen filling", screenFillingPane("❯ " + stuckMessage), []string{stuckMessage}},
		{"collapsed", collapsedPane(), nil},
		{"picker", pickerPane(), nil},
		{"picker over status", pickerOverStatusPane(), nil},
		{"busy", busyPane("❯ " + stuckMessage), []string{stuckMessage}},
		{"busy empty", busyPane(emptyRow), nil},
		{"spinner thinking", spinnerPane("  agent: output", "✻ Baking… (2m 32s · ↓ 6.1k tokens · thought for 6s)"), nil},
		{"spinner no counter", spinnerPane("  agent: output", "· Misting…"), nil},
		{"spinner finished", spinnerPane("  agent: output", "✻ Baked for 6m 19s · 1 shell still running"), nil},
		{"spinner quoted", spinnerPane("  agent: output", quotedSpinner), nil},
		{"spinner legacy", spinnerPane("  agent: output", "✻ Working… (23s · esc to interrupt)"), nil},
		{"background shells", spinnerPane("  agent: output", "⏵⏵ bypass permissions on · 2 shells · esc to interrupt"), nil},
		{"usage limit", claudePaneWithUsageLimit(claudePane(emptyRow), usageNotices...), nil},
		{"usage limit truncated", claudePaneWithUsageLimit(claudePane(emptyRow), "  ⚠ Usage limit reached · limit res…"), nil},
		{"usage limit ansi", claudePaneWithUsageLimit(claudePane(emptyRow), "\x1b[2m  ⚠ uSaGe LiMiT ReAcHeD · limit resets 5:40pm …\x1b[0m"), nil},
		{"usage limit transcript", "  agent: ⚠ Usage limit reached · limit resets 5:40pm\n" + claudePane(emptyRow), nil},
		{"auth expired", "❯  \n" + composerBorder + "\x1b[2m● Login expired · Please run /login\x1b[0m\n", nil},
		{"auth expired typed", "❯ why does ● Login expired · Please run /login show up?\n" + composerBorder, nil},
		{"rc menu", rcMenu, nil},
		{"rc active", rcActive, nil},
		{"rc dropped", rcActive + rcDisconnect, nil},
		{"rc wrapped", "  /remote-control is active · Continue here, on your phone, or at\n  " + rcURL + ".\n", nil},
		{"rc quoted", "> the line reads /remote-control is active · see " + rcURL + "\n", nil},
		{"resume picker", "Resume from summary\n❯ Resume full session\nEnter to select\n", nil},
		{"claude trust", claudeTrustFixture, nil},
		{"codex trust", codexTrustPane, nil},
		{"codex wrapped", codexPaneWith(wrapText(codexMessage, 68)), []string{codexMessage}},
		{"codex idle", codexPaneWith([]string{"Ask Codex to do anything"}), nil},
		{"codex foreign", codexPaneWith(wrapText("could you check this; we need to review the attribute field for the numbers in the list", 68)), []string{codexMessage}},
		{"codex working", "◦ Working (1m 11s • esc to interrupt)\n" + modernCodexPane("Ask Codex to do anything"), nil},
		{"codex modern text", modernCodexPane("[server-main] first line\n  second line with English content"), nil},
		{"codex collapsed chip", collapsedChip, []string{codexMessage}},
		{"codex expanded chip", expandedChip, nil},
		{"codex ghost", ghostComposer, nil},
		{"codex busy queue", busyQueueChip, nil},
		{"codex navigation", codexNavigationFixture, nil},
		{"codex navigation under composer", modernCodexPane("[bp] message") + "  \x1b[2mtab to queue message\x1b[0m\n" + codexNavigationFixture, nil},
		{"gpt6 full", gpt6Pane("[lead] Your task is ready: read /work/project/brief.md and follow", "it. Ask the lead if anything is unclear."), []string{gpt6Message}},
		{"gpt6 empty", gpt6Pane("\x1b[2mAsk Codex to do anything\x1b[0m"), []string{gpt6Message}},
		{"codex short pane", codex36x15("", wrapText(codexMessage[len(codexMessage)-300:], 32)), []string{codexMessage}},
		{"codex short pane transcript", codex36x15("• earlier answer", wrapText(codexMessage[len(codexMessage)-300:], 32)), []string{codexMessage}},
		{"hermes idle", hermesPane(hermesIdleRow), nil},
		{"hermes idle ansi", hermesPane(hermesIdleRowAnsi), nil},
		{"hermes ghost ansi", hermesPane(hermesGhostRowAnsi), nil},
		{"hermes typed", hermesPane(hermesIdleTypedRow), []string{"there is also a third line"}},
		{"hermes multi row", hermesPane("❯ "+stuckMessage[:60], "  "+stuckMessage[60:]), []string{stuckMessage}},
		{"hermes busy", hermesBusyPane(hermesBusyRow), nil},
		{"hermes busy typed", hermesBusyPane(hermesBusyTypedRow), nil},
		{"opencode idle", openCodePane([]string{"Ask anything..."}, false), nil},
		{"opencode busy", openCodePane([]string{"Ask anything..."}, true), nil},
		{"opencode typed", openCodePane([]string{stuckMessage}, false), []string{stuckMessage}},
		{"opencode chip", openCodePane([]string{"[Pasted ~12 lines]"}, false), []string{stuckMessage}},
		{"node dev server", "npm run dev\n> build succeeded\n", nil},
	}
}

// variants derives the edge-case renderings every base screen is also
// checked under.
func variants(base []paneCase) []paneCase {
	filler := strings.Repeat("  agent: intermediate line\n", 40)
	var out []paneCase
	for _, c := range base {
		out = append(out, c)
		for _, v := range []struct {
			suffix string
			pane   string
		}{
			{"crlf", strings.ReplaceAll(c.pane, "\n", "\r\n")},
			{"trailing blank rows", c.pane + "\n\n\n"},
			{"filler above", filler + c.pane},
			{"figure space", strings.ReplaceAll(c.pane, " ", " ")},
			{"ideographic space", strings.ReplaceAll(c.pane, " ", "　")},
			{"tabs", strings.ReplaceAll(c.pane, "❯ ", "❯\t")},
		} {
			if v.pane == c.pane {
				continue
			}
			out = append(out, paneCase{c.name + " / " + v.suffix, v.pane, c.texts})
		}
	}
	return out
}
