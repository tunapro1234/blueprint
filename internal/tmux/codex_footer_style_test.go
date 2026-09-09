package tmux

import (
	"context"
	"strings"
	"testing"
)

func TestCodexDimFooterSeparatorPreservesWrappedComposerOwnership(t *testing.T) {
	footer := "  \x1b[38;2;246;226;183mgpt-5.6-sol high\x1b[2m\x1b[39m · \x1b[0m\x1b[38;2;171;223;167m/srv\x1b[39m    Vim: Insert"
	pane := "• Context compacted\n\x1b[1m›\x1b[0m [cron?:watch.sh] first row\n  second row\n\n" + footer
	want := "[cron?:watch.sh] first row second row"
	if !composerHoldsMessage(pane, stripSpace(want)) {
		t.Fatal("dim structural separator hid complete composer")
	}
	empty := "• Context compacted\n› \x1b[2mAsk Codex to do anything\x1b[0m\n" + footer
	h := &sendHarness{captures: []string{empty}, activities: []string{"target\t900\n"}}
	if got := testClient(h).submit(context.Background(), "=target:", "target", want, &pane); got != sendVerified || countEnter(h.mutations) != 1 {
		t.Fatalf("result=%v keys=%v", got, h.mutations)
	}
	edited := strings.Replace(pane, "second row", "second row USER DRAFT", 1)
	h = &sendHarness{}
	if got := testClient(h).submit(context.Background(), "=target:", "target", want, &edited); got != sendUnowned || len(h.mutations) != 0 {
		t.Fatalf("draft not preserved: result=%v keys=%v", got, h.mutations)
	}
}
