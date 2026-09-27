package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

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

func TestUsageLimitDetectionUsesClaudeFooter(t *testing.T) {
	fullNotice := "  ⚠ Usage limit reached · limit resets 5:40pm · clau.de/wrap-up · /upgrade to keep using …"
	cases := []struct {
		name string
		pane string
		want bool
	}{
		{"incident screen", claudePaneWithUsageLimit(claudePane(
			"❯ [blueprint] [blueprint] q696812417 delivered at 09:18, complete and once; writer is",
			"  working on it. Cause: ...",
			"  no action needed from you.",
		), fullNotice,
			"  ⚠ While you wait, start a new cloud session by claiming a $250 credit",
			"  ⚠ /low-priority to continue now at lower priority · uses your weekly limit"), true},
		{"transcript only", "  agent: ⚠ Usage limit reached · limit resets 5:40pm\n" + claudePane(emptyRow), false},
		{"truncated notice", claudePaneWithUsageLimit(claudePane(emptyRow), "  ⚠ Usage limit reached · limit res…"), true},
		{"normal idle Claude", claudePane(emptyRow), false},
		{"ANSI and mixed case", claudePaneWithUsageLimit(claudePane(emptyRow), "\x1b[2m  ⚠ uSaGe LiMiT ReAcHeD · limit resets 5:40pm …\x1b[0m"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := UsageLimitReason(tc.pane)
			if got := reason != ""; got != tc.want {
				t.Fatalf("UsageLimitReason() = %q, want match=%v", reason, tc.want)
			}
		})
	}
	if got := UsageLimitReason(claudePaneWithUsageLimit(claudePane(emptyRow), fullNotice)); !strings.Contains(got, "limit resets 5:40pm") {
		t.Fatalf("reason %q does not include visible reset text", got)
	}
}

func TestSendRefusesUsageLimitedClaudeBeforePaste(t *testing.T) {
	terminal := &claudeViewportTerminal{width: 80, visibleRows: 8, usageLimited: true}
	err := terminal.client().Send(context.Background(), "target", "please do the task")
	if !errors.Is(err, ErrUsageLimited) {
		t.Fatalf("Send() error = %v, want ErrUsageLimited", err)
	}
	if terminal.pastes != 0 || len(terminal.keys) != 0 {
		t.Fatalf("usage-limited pane was touched: pastes=%d keys=%v", terminal.pastes, terminal.keys)
	}
}

func TestSendClearsOwnedComposerWhenUsageLimitAppearsAfterEnter(t *testing.T) {
	message := "please do the task"
	terminal := &claudeViewportTerminal{width: 80, visibleRows: 8, limitAfterEnter: true}
	err := terminal.client().Send(context.Background(), "target", message)
	if !errors.Is(err, ErrUsageLimited) {
		t.Fatalf("Send() error = %v, want ErrUsageLimited", err)
	}
	if errors.Is(err, ErrUnverified) {
		t.Fatalf("Send() error = %v, want a proven usage-limit wait", err)
	}
	if terminal.pastes != 1 || countClaudeEnter(terminal.keys) != 1 || terminal.clears != 1 {
		t.Fatalf("pastes=%d keys=%v clears=%d, want one paste, one Enter, and one owned clear", terminal.pastes, terminal.keys, terminal.clears)
	}
	if terminal.composer != "" || len(terminal.submitted) != 0 {
		t.Fatalf("composer=%q submitted=%q, want cleared without delivery", terminal.composer, terminal.submitted)
	}
}
