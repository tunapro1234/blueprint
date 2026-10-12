package tmux

import (
	"context"
	"errors"
	"testing"
)

// The pre-send turn-open probe is the last gate in readyToSend: the screen has
// already read idle, but a Claude turn can open during the quiet window while it
// streams with no spinner, which the screen cannot see. These pin that gate — a
// provably open turn queues the message, an unreadable transcript fails closed,
// and a closed turn delivers normally — and in every refusing case NOTHING is
// injected into the pane.
func TestSendConsultsTurnOpenProbe(t *testing.T) {
	// Two idle, empty captures and two stale activity stamps carry readyToSend
	// through every screen check, so the probe alone decides. (Now is Unix 1000;
	// an activity at 900 is 100s old, well past clientIdleWindow.)
	idle := func() []string { return []string{"❯ \n", "❯ \n"} }
	old := func() []string { return []string{"target\t900\n", "target\t900\n"} }

	t.Run("queues when a turn opened during the quiet window", func(t *testing.T) {
		h := &sendHarness{captures: idle(), activities: old()}
		c := testClient(h)
		c.TurnOpenProbe = func(context.Context, string) (bool, error) { return true, nil }
		if err := c.Send(context.Background(), "target", "queued"); !errors.Is(err, ErrTyping) {
			t.Fatalf("err=%v, want ErrTyping", err)
		}
		if len(h.mutations) != 0 {
			t.Fatalf("message was injected into an agent whose turn had opened: %v", h.mutations)
		}
	})

	t.Run("fails closed when the transcript cannot be read", func(t *testing.T) {
		h := &sendHarness{captures: idle(), activities: old()}
		c := testClient(h)
		c.TurnOpenProbe = func(context.Context, string) (bool, error) {
			return false, errors.New("incomplete transcript tail")
		}
		if err := c.Send(context.Background(), "target", "queued"); !errors.Is(err, ErrNotReady) {
			t.Fatalf("err=%v, want ErrNotReady", err)
		}
		if len(h.mutations) != 0 {
			t.Fatalf("message was injected while the turn state was unknown: %v", h.mutations)
		}
	})

	t.Run("delivers when the turn is closed", func(t *testing.T) {
		h := &sendHarness{
			captures:   []string{"❯ \n", "❯ \n", "❯ queued\n", "❯ \n"},
			activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
		}
		c := testClient(h)
		consulted := false
		c.TurnOpenProbe = func(context.Context, string) (bool, error) { consulted = true; return false, nil }
		if err := c.Send(context.Background(), "target", "queued"); err != nil {
			t.Fatal(err)
		}
		if !consulted {
			t.Fatal("the probe was never consulted on an ordinary send")
		}
		if countInjections(h.mutations) != 1 {
			t.Fatalf("expected exactly 1 injection, got: %v", h.mutations)
		}
	})

	t.Run("a forced record walks past the probe", func(t *testing.T) {
		h := &sendHarness{
			captures:   []string{"❯ \n", "❯ \n", "❯ queued\n", "❯ \n"},
			activities: []string{"target\t900\n", "target\t900\n", "target\t900\n", "target\t900\n"},
		}
		c := testClient(h)
		consulted := false
		// Returns open: if the gate were wrong and consulted this for a forced
		// send, the delivery would be blocked and inject never happen.
		c.TurnOpenProbe = func(context.Context, string) (bool, error) { consulted = true; return true, nil }
		if err := c.SendForce(context.Background(), "target", "queued"); err != nil {
			t.Fatal(err)
		}
		if consulted {
			t.Fatal("the probe was consulted for a forced send")
		}
		if countInjections(h.mutations) != 1 {
			t.Fatalf("expected exactly 1 injection, got: %v", h.mutations)
		}
	})
}
