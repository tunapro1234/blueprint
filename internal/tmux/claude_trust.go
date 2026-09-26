package tmux

import "strings"

// claudeTrustAction is the one key Open may press on Claude's workspace trust
// modal for the requested directory.
type claudeTrustAction int

const (
	claudeTrustNone    claudeTrustAction = iota
	claudeTrustConfirm                   // approval selected: Enter
	claudeTrustSelect                    // refusal selected, approval right below: Down
)

// claudeTrustMaxKeys bounds the keys Open presses on the trust modal. Claude
// ignores input for a moment after drawing it (refuseInput, measured ~0.5s
// with 2.1.283), so a press can be lost and is repeated after re-reading.
const claudeTrustMaxKeys = 6

// claudeTrustModal recognizes only the initial workspace picker for the
// explicitly requested directory. Text mentioning trust is not a picker.
// Claude 2.1.263 numbered the options with the approval first and selected;
// 2.1.283 hides the numbers and puts "No, exit" first and selected. The
// selection wraps, so the refusal is moved off only when the approval sits
// directly below it and only one Down is sent per reading of the screen.
// Unknown layouts remain untouched.
func claudeTrustModal(pane, dir string) claudeTrustAction {
	lines := strings.Split(strings.TrimSpace(pane), "\n")
	header, path, question, accept, refuse, footer := -1, -1, -1, -1, -1, -1
	acceptSelected, refuseSelected := false, false
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		// 2.1.283 draws a rule above the modal; a rule inside it is a composer.
		if isComposerBorder(line) && header < 0 {
			continue
		}
		if isComposerBorder(line) || strings.Contains(line, "bypass permissions") || strings.Contains(line, "-- INSERT --") {
			return claudeTrustNone
		}
		selected := strings.HasPrefix(line, "❯")
		option := strings.TrimSpace(strings.TrimPrefix(line, "❯"))
		switch {
		case line == "Accessing workspace:":
			header = i
		case line == dir:
			path = i
		case strings.HasPrefix(line, "Quick safety check: Is this a project you created or one you trust?"):
			question = i
		case option == "1. Yes, I trust this folder" || option == "Yes, I trust this folder":
			accept, acceptSelected = i, selected
		case option == "2. No, exit" || option == "2. No, continue without these permissions" ||
			option == "No, exit" || option == "No, continue without these permissions":
			refuse, refuseSelected = i, selected
		case line == "Enter to confirm · Esc to cancel" || line == "Enter to select · Esc to cancel":
			footer = i
		default:
			if selected || strings.HasPrefix(line, "›") {
				return claudeTrustNone
			}
		}
	}
	if header < 0 || path <= header || question <= path || accept <= question || refuse <= question ||
		footer != len(lines)-1 || footer <= accept || footer <= refuse {
		return claudeTrustNone
	}
	switch {
	case acceptSelected && !refuseSelected && (refuse == accept+1 || accept == refuse+1):
		return claudeTrustConfirm
	case refuseSelected && !acceptSelected && accept == refuse+1:
		return claudeTrustSelect
	}
	return claudeTrustNone
}

// codexTrustMaxEnters bounds how often Open presses Enter on a Codex
// folder-trust modal that is still showing after an earlier press.
const codexTrustMaxEnters = 5

// codexTrustModal recognizes Codex's first-run folder trust screen for the
// explicitly requested directory with "Trust and continue" selected. Its
// selection marker is the same "›" as the Codex composer, so readiness must
// rule this screen out before treating "›" as an idle prompt.
func codexTrustModal(pane, dir string) bool {
	header, path, question, accept, back, footer := -1, -1, -1, -1, -1, -1
	for i, raw := range strings.Split(strings.TrimSpace(pane), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "Folder access":
			header = i
		case line == dir:
			path = i
		case strings.HasPrefix(line, "Trust this folder?"):
			question = i
		case line == "› 1. Trust and continue":
			accept = i
		case strings.HasPrefix(line, "2. "):
			back = i
		case line == "enter continue · esc back":
			footer = i
		default:
			if strings.HasPrefix(line, "›") {
				return false
			}
		}
	}
	return header >= 0 && path > header && question > path && accept > question &&
		back == accept+1 && footer > back
}

// codexTrustScreen reports any visible Codex trust question, recognized or
// not: trust wording plus an option list whose first entry carries the "›"
// selection marker. While it shows, that "›" is not a composer.
func codexTrustScreen(pane string) bool {
	if !strings.Contains(strings.ToLower(pane), "trust") {
		return false
	}
	for _, raw := range strings.Split(pane, "\n") {
		if strings.HasPrefix(strings.TrimSpace(raw), "› 1. ") {
			return true
		}
	}
	return false
}
