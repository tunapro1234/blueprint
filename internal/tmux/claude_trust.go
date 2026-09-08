package tmux

import "strings"

// claudeTrustModal recognizes only the initial workspace picker for the
// explicitly requested directory. Text mentioning trust is not a picker.
// Unknown layouts and a selected refusal remain untouched.
func claudeTrustModal(pane, dir string) bool {
	lines := strings.Split(strings.TrimSpace(pane), "\n")
	header, path, question, accept, refuse, footer := -1, -1, -1, -1, -1, -1
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if isComposerBorder(line) || strings.Contains(line, "bypass permissions") || strings.Contains(line, "-- INSERT --") {
			return false
		}
		switch {
		case line == "Accessing workspace:":
			header = i
		case line == dir:
			path = i
		case strings.HasPrefix(line, "Quick safety check: Is this a project you created or one you trust?"):
			question = i
		case line == "❯ 1. Yes, I trust this folder":
			accept = i
		case line == "2. No, exit" || line == "2. No, continue without these permissions":
			refuse = i
		case line == "Enter to confirm · Esc to cancel" || line == "Enter to select · Esc to cancel":
			footer = i
		default:
			if strings.HasPrefix(line, "❯") || strings.HasPrefix(line, "›") {
				return false
			}
		}
	}
	return header >= 0 && path > header && question > path && accept > question &&
		refuse == accept+1 && footer > refuse && footer == len(lines)-1
}
