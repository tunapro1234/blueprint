package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTypingUsesOnlyLastPrompt(t *testing.T) {
	pane := "❯ old message\nresponse\n  ❯ \u00a0  \t\n"
	if Typing(pane) {
		t.Fatal("empty final composer was reported as typing")
	}
	pane += "output\n  ❯ new message\n"
	if !Typing(pane) {
		t.Fatal("non-empty final composer was not reported as typing")
	}
}

func TestTypingRecognizesCodexPrompt(t *testing.T) {
	if Typing("output\n  › \u00a0 \t\n") {
		t.Fatal("empty Codex composer was reported as typing")
	}
	if !Typing("output\n  › draft message\n") {
		t.Fatal("non-empty Codex composer was not reported as typing")
	}
}

func TestTypingNoPrompt(t *testing.T) {
	if Typing("normal output\n❯\u00a0\nmore output") {
		t.Fatal("NBSP-only composer must be empty")
	}
}

func TestBusyRequiresLiveIndicator(t *testing.T) {
	// Live indicators: spinner timer or the ⏵ footer.
	if !Busy("✻ Working… (23s · Esc to interrupt)") {
		t.Fatal("spinner line should be busy")
	}
	if !Busy("⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt") {
		t.Fatal("footer line should be busy")
	}
	// Quoted prose, bare phrase and background-shell footers are NOT busy.
	if Busy(`transcript quoting "esc to interrupt" in a rule message`) ||
		Busy("Esc to interrupt") ||
		Busy("⏵⏵ bypass permissions on · 2 shells · esc to interrupt") ||
		Busy("ready") {
		t.Fatal("false positive busy")
	}
}

func TestParseClientActivityUsesLatestMatchingClient(t *testing.T) {
	got := parseClientActivity("other\t100\ntarget\t120\ntarget\t125\nbad\tnope\n", "target")
	if got.Unix() != 125 {
		t.Fatalf("activity=%v", got)
	}
	if got := parseClientActivity("other\t100\n", "target"); !got.Equal(time.Time{}) {
		t.Fatalf("unattached session activity=%v", got)
	}
}

type sendHarness struct {
	captures   []string
	activities []string
	mutations  []string
}

func (h *sendHarness) run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	switch args[0] {
	case "capture-pane":
		value := h.captures[0]
		h.captures = h.captures[1:]
		return []byte(value), nil
	case "list-clients":
		value := h.activities[0]
		h.activities = h.activities[1:]
		return []byte(value), nil
	case "load-buffer", "paste-buffer", "send-keys", "delete-buffer":
		h.mutations = append(h.mutations, strings.Join(args, " "))
		return nil, nil
	default:
		return nil, errors.New("unexpected command: " + strings.Join(args, " "))
	}
}

func testClient(h *sendHarness) *Client {
	return &Client{
		Sleep: func(time.Duration) {},
		Now:   func() time.Time { return time.Unix(1000, 0) },
		exec:  h.run,
	}
}

func TestSendWaitsForStableEmptyComposer(t *testing.T) {
	h := &sendHarness{
		captures:   []string{"❯ \n", "❯ user started typing\n"},
		activities: []string{"target\t900\n"},
	}
	err := testClient(h).Send(context.Background(), "target", "queued")
	if !errors.Is(err, ErrTyping) {
		t.Fatalf("err=%v", err)
	}
	if len(h.mutations) != 0 {
		t.Fatalf("message was injected during typing: %v", h.mutations)
	}
}

func TestSendSettlesLargePasteBeforeSubmitting(t *testing.T) {
	h := &sendHarness{
		captures: []string{
			"❯ \n",
			"❯ \n",
			"❯ [Pasted text #1 +2 lines]\n",
		},
		activities: []string{"target\t900\n", "target\t900\n", "target\t900\n"},
	}
	if err := testClient(h).Send(context.Background(), "target", "line one\nline two"); err != nil {
		t.Fatal(err)
	}
	if len(h.mutations) != 3 || !strings.HasPrefix(h.mutations[0], "load-buffer ") ||
		!strings.HasPrefix(h.mutations[1], "paste-buffer ") || h.mutations[2] != "send-keys -t =target: Enter" {
		t.Fatalf("mutations=%v", h.mutations)
	}
}

func TestSendDoesNotSubmitIfUserTypesAfterInjection(t *testing.T) {
	h := &sendHarness{
		captures:   []string{"❯ \n", "❯ \n", "❯ queued plus user text\n"},
		activities: []string{"target\t900\n", "target\t900\n", "target\t1000\n"},
	}
	if err := testClient(h).Send(context.Background(), "target", "queued"); err != nil {
		t.Fatal(err)
	}
	if len(h.mutations) != 1 || !strings.HasPrefix(h.mutations[0], "send-keys -t =target: -l queued") {
		t.Fatalf("user text was submitted or message was retried: %v", h.mutations)
	}
}
