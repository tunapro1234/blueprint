package msgq

import (
	"context"
	"strings"
	"testing"

	bptmux "blueprint/internal/tmux"
)

type issue16CodexTarget struct{ fakeTarget }

func (f *issue16CodexTarget) SubmitStuck(_ context.Context, _ string, texts []string) (bool, error) {
	for _, text := range texts {
		if _, ours := bptmux.StuckPaste(f.pane, []string{text}); ours {
			f.submitted = append(f.submitted, text)
			f.pane = "• Ran 6 commands\n\n› Ask Codex to do anything\n  gpt-5.6-sol medium fast · /srv/project\n"
			return true, nil
		}
	}
	return false, nil
}

func TestIssue16CodexComposerPasteIsRecognizedAndSubmitted(t *testing.T) {
	message := "[bp] start the migration"
	pane := "• Ran 6 commands · ctrl + t to view transcript\n\n" +
		strings.Repeat("─", 70) + "\n\n" +
		"› " + message + "\n\n" +
		"  gpt-5.6-sol medium fast · /srv/project\n"
	queue := newBoundTestQueue(t.TempDir())
	id, err := enqueueBoundUnverified(t, queue, "target", "sender", message)
	if err != nil {
		t.Fatal(err)
	}
	if _, ours := bptmux.StuckPaste(pane, []string{message}); !ours {
		t.Fatal("fixture text was not recognized in the Codex composer")
	}
	target := &issue16CodexTarget{fakeTarget{sessions: map[string]bool{"target": true}, pane: pane}}
	if err := queue.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.submitted) != 1 || target.submitted[0] != message {
		t.Fatalf("Codex composer text was not submitted: %v", target.submitted)
	}
	status, err := queue.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "DELIVERED") || strings.Contains(status, "not in the composer") {
		t.Fatalf("qstat-style status contradicted the visible composer: %s", status)
	}
}
