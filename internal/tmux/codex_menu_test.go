package tmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const codexNavigationFixture = "\n› 1. New chat\n  2. Agent command center\n  3. Resume another chat\n"

func TestCodexNavigationDoesNotReceiveMessages(t *testing.T) {
	for _, mode := range []string{"normal", "insert"} {
		h := &codexTerminal{mode: mode, menu: true, draft: "[bp] existing message"}
		c, ctx := h.client(), context.Background()
		if err := c.Send(ctx, "agent", "new message"); !errors.Is(err, ErrDialog) {
			t.Fatal(err)
		}
		if _, err := c.SubmitStuck(ctx, "agent", []string{h.draft}); !errors.Is(err, ErrDialog) {
			t.Fatal(err)
		}
		if _, err := c.ClearDelivered(ctx, "agent", []string{h.draft}); !errors.Is(err, ErrDialog) {
			t.Fatal(err)
		}
		if len(h.keys) != 0 || h.pasted != 0 || h.draft != "[bp] existing message" {
			t.Fatalf("menu or draft touched: %+v", h)
		}
	}
	// ANSI selection styling and trailing blank rows do not hide the menu.
	if !paneDialog(strings.Replace(codexNavigationFixture, "New chat", "\x1b[36mNew chat\x1b[0m", 1) + "\n\n") {
		t.Fatal("styled menu missed")
	}
	if paneDialog(modernCodexPane("Tell me about New chat and Agent command center")) {
		t.Fatal("ordinary prose classified as menu")
	}
}

func TestCodexNavigationAppearingDuringDeliveryIsUnverified(t *testing.T) {
	for _, afterEnter := range []bool{false, true} {
		h := &codexTerminal{menuAfterPaste: !afterEnter, menuAfterEnter: afterEnter}
		if err := h.client().Send(context.Background(), "agent", "[bp] message"); !errors.Is(err, ErrUnverified) {
			t.Fatal(err)
		}
		wantKeys := 0
		if afterEnter {
			wantKeys = 1
		}
		if len(h.keys) != wantKeys || len(h.submitted) != 0 || h.pasted != 1 {
			t.Fatalf("retry or false submit: %+v", h)
		}
	}
	h := &codexTerminal{draft: "[bp] hanging message", menuAfterEnter: true}
	if ok, err := h.client().SubmitStuck(context.Background(), "agent", []string{h.draft}); ok || !errors.Is(err, ErrUnverified) {
		t.Fatalf("menu mistaken for delivery: %v %v", ok, err)
	}
	if len(h.keys) != 1 {
		t.Fatalf("retried navigation Enter: %v", h.keys)
	}
}

func TestCodexNavigationOverridesNativeQueueHint(t *testing.T) {
	h := &codexTerminal{}
	pane := modernCodexPane("[bp] message") + "  \x1b[2mtab to queue message\x1b[0m\n" + codexNavigationFixture
	if !codexBusyQueue(pane) {
		t.Fatal("fixture lacks native queue hint")
	}
	if got := h.client().submit(context.Background(), "=agent:", "agent", "[bp] message", &pane); got != sendUnverified {
		t.Fatalf("verdict: %v", got)
	}
	if len(h.keys) != 0 {
		t.Fatalf("menu received queue Tab: %v", h.keys)
	}
}
