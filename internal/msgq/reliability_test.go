package msgq

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecoveryMustHonorUnknownRuntime(t *testing.T) {
	q := New(t.TempDir())
	q.RuntimeBlock = func(string, bool) string { return "runtime unknown: conflicting thread bindings" }
	id, err := q.EnqueueUnverified("target", "sender", stuckText)
	if err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane(stuckText)}
	if err = q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if len(target.submitted) != 0 || len(target.cleared) != 0 || target.calls != 0 {
		t.Fatal("recovery bypassed unknown runtime")
	}
	rec, err := q.Record(id)
	if err != nil || !strings.Contains(rec.Reason, "conflicting thread") {
		t.Fatal(rec, err)
	}
}

type heldSendTarget struct {
	fakeTarget
	entered, proceed chan struct{}
}

func (f *heldSendTarget) Send(ctx context.Context, to, text string) error {
	close(f.entered)
	<-f.proceed
	return f.fakeTarget.Send(ctx, to, text)
}
func TestCancelCannotClaimSuccessDuringAnotherDispatcherSend(t *testing.T) {
	root := t.TempDir()
	q := New(root)
	other := New(root)
	id, err := q.Enqueue("target", "sender", stuckText)
	if err != nil {
		t.Fatal(err)
	}
	target := &heldSendTarget{fakeTarget: fakeTarget{alive: true, pane: composerPane("")}, entered: make(chan struct{}), proceed: make(chan struct{})}
	finished := make(chan error, 1)
	go func() { finished <- q.Dispatch(context.Background(), target, nil) }()
	select {
	case <-target.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher never reached send")
	}
	err = other.Cancel(id)
	close(target.proceed)
	if dispatchErr := <-finished; dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if err == nil {
		t.Fatal("cancel falsely succeeded after the send had begun")
	}
	rec, err := other.Record(id)
	if err != nil || rec.Status != "delivered" {
		t.Fatal(rec, err)
	}
}

type crashSendTarget struct {
	fakeTarget
	root string
}

func (f *crashSendTarget) Send(_ context.Context, _, text string) error {
	if err := os.WriteFile(filepath.Join(f.root, "accepted"), []byte(text), 0600); err != nil {
		panic(err)
	}
	os.Exit(86)
	return nil
}
func TestDispatcherCrashHelper(t *testing.T) {
	root := os.Getenv("BP_TEST_DISPATCH_CRASH_ROOT")
	if root == "" {
		return
	}
	q := New(root)
	_ = q.Dispatch(context.Background(), &crashSendTarget{fakeTarget: fakeTarget{alive: true, pane: composerPane("")}, root: root}, nil)
	t.Fatal("crash helper did not send")
}
func TestCrashAfterSendCannotRepasteBeforeTranscriptArrives(t *testing.T) {
	root := t.TempDir()
	q := New(root)
	id, err := q.Enqueue("target", "sender", stuckText)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestDispatcherCrashHelper$")
	child.Env = append(os.Environ(), "BP_TEST_DISPATCH_CRASH_ROOT="+root)
	err = child.Run()
	if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 86 {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "accepted")); err != nil {
		t.Fatal(err)
	}
	restarted := New(root)
	restarted.Witness = func(string, string, time.Time) bool { return false } // transcript is delayed
	target := &fakeTarget{alive: true, pane: composerPane("")}
	if err = restarted.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if target.calls != 0 || len(target.submitted) != 0 {
		t.Fatal("crash window caused duplicate delivery")
	}
	rec, err := restarted.Record(id)
	if err != nil || !rec.NoRepaste {
		t.Fatal(rec, err)
	}
}

func TestConcurrentLocalSendersShareOnePendingChannel(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	ids := make(chan string, 24)
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := New(root).EnqueueUnique("target", "sender", stuckText, false, time.Minute)
			ids <- id
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatal("concurrent sends forked channels", seen)
	}
	rows, err := New(root).List()
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
}

func TestRecoveryRejectsLegacyAndChangedConversationBindings(t *testing.T) {
	for _, mode := range []string{"legacy", "changed-thread", "changed-runtime", "changed-pane", "unknown", "same"} {
		t.Run(mode, func(t *testing.T) {
			q := New(t.TempDir())
			current := "codex:thread-a:100"
			original := current
			switch mode {
			case "legacy":
				original = ""
			case "changed-thread":
				current = "codex:thread-b:100"
			case "changed-runtime":
				current = "claude:thread-a:100"
			case "changed-pane":
				current = "codex:thread-a:101"
			case "unknown":
				current = ""
			}
			q.Binding = func(string) string { return current }
			q.RuntimeBlock = func(string, bool) string { return "" }
			id, err := q.EnqueueUnverified("target", "sender", stuckText)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(q.pending(), id+".json")
			r, _ := read(path)
			r.AttemptBinding = original
			if err = writePending(path, r); err != nil {
				t.Fatal(err)
			}
			target := &fakeTarget{alive: true, pane: composerPane(stuckText)}
			if err = q.Dispatch(context.Background(), target, nil); err != nil {
				t.Fatal(err)
			}
			if mode == "same" {
				if len(target.submitted) != 1 {
					t.Fatal("verified original paste did not finish")
				}
			} else if len(target.submitted) != 0 || len(target.cleared) != 0 || target.calls != 0 {
				t.Fatal("changed conversation was touched")
			}
		})
	}
}
