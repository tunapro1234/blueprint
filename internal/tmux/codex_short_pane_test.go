package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func codex36x15(transcript string, rows []string) string {
	if len(rows) > 11 {
		panic("composer does not fit the fixture")
	}
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

func TestCodexShortPaneTailIsSubmittedAndVerified(t *testing.T) {
	tail := "already uses https://example.test/docs. " + strings.Repeat("Verify each response and report any mismatch with supporting detail. ", 4)
	message := "[lead] " + strings.Repeat("The request includes background context and asks for an explicit verification of each item. ", 5) + tail
	start := strings.Index(message, "already uses https://")
	if start < 0 {
		t.Fatal("fixture message has no endpoint tail")
	}
	rows := wrapText(message[start:], 34)
	paneTail := codex36x15("", rows)
	if len(strings.Split(paneTail, "\n")) != 15 || !strings.HasPrefix(strings.Split(paneTail, "\n")[2], "› already uses https://") {
		t.Fatalf("fixture does not match the 36x15 tail-only capture:\n%s", paneTail)
	}
	box, top, ok := codexComposerBox(paneTail)
	if !ok || top != 2 || !composerBoxScrolled(paneTail, box, top) {
		t.Fatalf("tail viewport was not recognized as scrolled: ok=%v top=%d", ok, top)
	}
	if got := testClient(&sendHarness{}).pasteIntegrity(paneTail, message); got != pasteUnreadable {
		t.Fatalf("pasteIntegrity=%v, want pasteUnreadable for the scrolled viewport", got)
	}
	empty := modernCodexPane("Ask Codex to do anything")
	h := &sendHarness{
		command:  "codex",
		captures: []string{empty, empty, paneTail, empty},
		activities: []string{
			"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n",
		},
	}
	if err := testClient(h).Send(context.Background(), "target", message); err != nil {
		t.Fatalf("Send returned %v for the intact tail in a viewport-filling Codex composer", err)
	}
	if got := countInjections(h.mutations); got != 1 {
		t.Fatalf("paste count=%d, want one injection: %v", got, h.mutations)
	}
	if got := countEnter(h.mutations); got != 1 {
		t.Fatalf("Enter count=%d, want one submit after the tail view: %v", got, h.mutations)
	}
}

func TestCodexShortPaneTailWithTranscriptAboveIsRefused(t *testing.T) {
	tail := "already uses https://example.test/docs. " + strings.Repeat("Verify each response and report any mismatch with supporting detail. ", 4)
	message := "[lead] " + strings.Repeat("The request includes background context and asks for an explicit verification of each item. ", 5) + tail
	start := strings.Index(message, "already uses https://")
	rows := wrapText(message[start:], 34)
	paneTail := codex36x15("Earlier transcript: the request asks for background and a safety review.", rows)
	box, top, ok := codexComposerBox(paneTail)
	if !ok || codexComposerViewportFillsPane(paneTail, top) {
		t.Fatalf("real transcript above the tail was treated as an empty screen: ok=%v top=%d box=%q", ok, top, box)
	}
	empty := modernCodexPane("Ask Codex to do anything")
	h := &sendHarness{
		command: "codex",
		captures: []string{
			empty, empty,
			paneTail, paneTail, paneTail,
			empty,
			paneTail, paneTail, paneTail,
		},
		activities: []string{
			"target\t900\n", "target\t900\n", "target\t900\n",
			"target\t900\n", "target\t900\n", "target\t900\n",
		},
	}
	if err := testClient(h).Send(context.Background(), "target", message); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Send returned %v, want ErrNotReady for a torn tail with transcript visible above", err)
	}
	if got := countEnter(h.mutations); got != 0 {
		t.Fatalf("Enter count=%d, want no submit on a possibly torn paste: %v", got, h.mutations)
	}
	if got := countInjections(h.mutations); got != 2 {
		t.Fatalf("paste count=%d, want exactly one repair retry before refusal: %v", got, h.mutations)
	}
}

func TestCodexShortPanePrefixRemainsIncomplete(t *testing.T) {
	message := "[lead] " + strings.Repeat("The beginning of a message must not be submitted while the rest is missing. ", 8)
	rows := wrapText(message[:160], 34)
	pane := codex36x15("", rows)
	if got := testClient(&sendHarness{}).pasteIntegrity(pane, message); got != pasteIncomplete {
		t.Fatalf("pasteIntegrity=%v, want pasteIncomplete for a visible prefix", got)
	}
}

func TestCodexShortPaneRuleLeavesOtherComposerReadersUnchanged(t *testing.T) {
	tail := strings.Repeat("visible composer tail content ", 3)
	for _, test := range []struct {
		name string
		pane string
	}{
		{name: "claude", pane: claudePane("❯ " + tail)},
		{name: "opencode", pane: openCodePane([]string{tail}, false)},
		{name: "hermes", pane: hermesPane(hermesIdleTypedRow)},
	} {
		t.Run(test.name, func(t *testing.T) {
			box, top, ok := composerBoxAt(test.pane)
			if !ok {
				t.Fatal("captured-style composer was not recognized")
			}
			if composerBoxScrolled(test.pane, box, top) {
				t.Fatalf("unmeasured %s composer was newly treated as scrolled", test.name)
			}
		})
	}
}
