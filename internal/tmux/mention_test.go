package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// liveMentionMessage is the record text of q915605609 (2026-11-21): a pasted
// message whose trailing "@pr" Claude Code's mention picker completed.
const liveMentionMessage = "[server-whatsapp] [kopru] GEC TESLIMAT (15:01Z, ilk teslimat dogrulanamadi, transcriptte yok): [WA:VBAI/PR 26-27|Utku#ACBDD74A83942B40F25C85243FE60D0D] @pr"

// liveMentionPane is that composer as the 80-column pane showed it.
func liveMentionPane() string {
	return claudePane(
		"❯ [server-whatsapp] [kopru] GEC TESLIMAT (15:01Z, ilk teslimat dogrulanamadi,",
		"  transcriptte yok): [WA:VBAI/PR",
		"  26-27|Utku#ACBDD74A83942B40F25C85243FE60D0D] @probot-egitim ",
	)
}

func TestCompletedMentionIsOursButNeverExact(t *testing.T) {
	pane := liveMentionPane()
	for _, want := range []string{liveMentionMessage, liveMentionMessage + " "} {
		if !mentionCompleted(pane, want) {
			t.Fatalf("completion of %q not recognised", want)
		}
		if ExactPaste(pane, []string{want}) {
			t.Fatalf("a completed mention must never read as exact (Enter would send the mention)")
		}
		if !DamagedPaste(pane, []string{want}) {
			t.Fatalf("a completed mention must read as our own undelivered copy")
		}
	}
	// The wide-rune leniency calls a near match exact; a completion must not
	// slip through it.
	wide := "✅ done, see the summary in the channel before the call @pr"
	widePane := claudePane("❯ ✅ done, see the summary in the channel before the call @probot-egitim")
	if ExactPaste(widePane, []string{wide}) || !DamagedPaste(widePane, []string{wide}) {
		t.Fatal("wide-rune message: completed mention was not kept out of the exact verdict")
	}
	// Short messages are recognised too: the shape is exact, not a fuzzy overlap.
	if !DamagedPaste(claudePane("❯ ping @probot-egitim"), []string{"ping @pr"}) {
		t.Fatal("short completed mention was not recognised")
	}
}

func TestCompletedMentionMatchIsNarrow(t *testing.T) {
	cases := map[string]string{
		"words appended after a space":  "❯ please review the queue fix before the deploy @pr thanks",
		"completion containing a space": "❯ please review the queue fix before the deploy @probot egitim",
		"completion with punctuation":   "❯ please review the queue fix before the deploy @pr!!",
		"different prefix":              "❯ please review the queue fix before deploying @probot-egitim",
		"exact text":                    "❯ please review the queue fix before the deploy @pr",
	}
	want := "please review the queue fix before the deploy @pr"
	for name, row := range cases {
		if mentionCompleted(claudePane(row), want) {
			t.Errorf("%s: matched as a mention completion", name)
		}
	}
	if mentionCompleted(claudePane("❯ ping @pr obot"), "ping @pr ") {
		t.Error("text typed after the safe form's space matched")
	}
	if mentionCompleted(claudePane("❯ middle @probot token"), "middle @pr token") {
		t.Error("a message without a trailing mention matched")
	}
}

func TestClearDeliveredErasesACompletedMention(t *testing.T) {
	// Before the fix this returned "composer clear stopped" without pressing a
	// key: the completion read as text appended to our message. Each C-u
	// removes one wrapped row (measured on Claude Code 2.1.330 at 80 columns).
	for _, want := range []string{liveMentionMessage, liveMentionMessage + " "} {
		h := &sendHarness{clearPane: liveMentionPane(), width: 80}
		h.clearRow = removeLastComposerRow
		cleared, err := testClient(h).ClearDelivered(context.Background(), "target", []string{want})
		if err != nil || !cleared {
			t.Fatalf("want %q: cleared=%v err=%v", want, cleared, err)
		}
		if got := countKey(h.mutations, "C-u"); got != 3 {
			t.Fatalf("expected 3 C-u presses (one per row), got %d: %v", got, h.mutations)
		}
		if countEnter(h.mutations) != 0 {
			t.Fatalf("Enter pressed on a completed mention: %v", h.mutations)
		}
		assertNoEscape(t, h.mutations)
		if !ComposerEmpty(h.clearPane) {
			t.Fatalf("composer not empty after the clear")
		}
	}
}

func TestClearDeliveredStillRefusesWordsAfterOurMention(t *testing.T) {
	want := "please review the queue fix before the deploy @pr"
	h := &sendHarness{clearPane: claudePane("❯ " + want + " thanks"), width: 80}
	h.clearRow = removeLastComposerRow
	cleared, err := testClient(h).ClearDelivered(context.Background(), "target", []string{want})
	if cleared || !errors.Is(err, ErrTyping) {
		t.Fatalf("cleared=%v err=%v, want a refusal", cleared, err)
	}
	if got := countKey(h.mutations, "C-u"); got != 0 {
		t.Fatalf("keys pressed on a human's words: %v", h.mutations)
	}
}

func TestSubmitStuckNeverPressesEnterOnACompletedMention(t *testing.T) {
	h := &sendHarness{captures: []string{liveMentionPane()}}
	submitted, err := testClient(h).SubmitStuck(context.Background(), "target", []string{liveMentionMessage})
	if err != nil || submitted || countEnter(h.mutations) != 0 {
		t.Fatalf("submitted=%v err=%v mutations=%v", submitted, err, h.mutations)
	}
}

func TestSendPastesATrailingMentionWithASpace(t *testing.T) {
	h := &sendHarness{
		captures: []string{
			"❯ \n", "❯ \n", // readyToSend
			"❯ [ada] ping @pr\n" + composerBorder, // holds ours -> Enter
			"❯  \n" + composerBorder,              // cleared -> verified
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if err := testClient(h).Send(context.Background(), "target", "[ada] ping @pr"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(h.payloads) != 1 || string(h.payloads[0]) != "[ada] ping @pr " {
		t.Fatalf("pasted %q, want the safe form with one trailing space", h.payloads)
	}
	if !strings.HasSuffix(string(h.payloads[0]), "@pr ") || countEnter(h.mutations) != 1 {
		t.Fatalf("mutations=%v", h.mutations)
	}
}
