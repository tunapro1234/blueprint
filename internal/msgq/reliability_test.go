package msgq

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
	if err != nil || rec.Status != StatusHangingComposer || !rec.Notified || !strings.Contains(rec.Reason, "conflicting thread") {
		t.Fatal(rec, err)
	}
	notices, err := q.List()
	if err != nil || len(notices) != 1 || notices[0].To != "sender" || !strings.Contains(notices[0].Msg, "ownership is unverified") {
		t.Fatalf("unknown ownership was not reported to the sender: notices=%+v err=%v", notices, err)
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
	cancelDone := make(chan error, 1)
	go func() {
		_, cancelErr := other.Cancel(id)
		cancelDone <- cancelErr
	}()
	select {
	case err = <-cancelDone:
		t.Fatalf("cancel returned while the delivery pass held the lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(target.proceed)
	if dispatchErr := <-finished; dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if err = <-cancelDone; err == nil {
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

func TestCancelWaitsForRunningPassAndTakesSeveralIDs(t *testing.T) {
	root := t.TempDir()
	q := New(root)
	first, err := q.Enqueue("target", "sender", "first message")
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.Enqueue("target", "sender", "second message")
	if err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenFile(filepath.Join(root, ".dispatch.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	type result struct {
		canceled []string
		err      error
	}
	done := make(chan result, 1)
	go func() {
		canceled, err := New(root).Cancel(first, "q000000000", second)
		done <- result{canceled, err}
	}()
	select {
	case got := <-done:
		t.Fatalf("cancel did not wait for the held dispatch lock: %+v", got)
	case <-time.After(300 * time.Millisecond):
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	var got result
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel never took the released lock")
	}
	if len(got.canceled) != 2 || got.canceled[0] != first || got.canceled[1] != second {
		t.Fatalf("canceled = %v", got.canceled)
	}
	if got.err == nil || !strings.Contains(got.err.Error(), "no pending message q000000000") {
		t.Fatalf("missing id not reported: %v", got.err)
	}
	for _, id := range []string{first, second} {
		rec, err := q.Record(id)
		if err != nil || rec.Status != "canceled (by operator)" {
			t.Fatal(id, rec, err)
		}
	}
}

func TestCancelGivesUpWithoutChangesWhenPassNeverEnds(t *testing.T) {
	old := cancelLockWait
	cancelLockWait = 150 * time.Millisecond
	defer func() { cancelLockWait = old }()
	root := t.TempDir()
	q := New(root)
	id, err := q.Enqueue("target", "sender", "kept message")
	if err != nil {
		t.Fatal(err)
	}
	held, err := os.OpenFile(filepath.Join(root, ".dispatch.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	canceled, err := q.Cancel(id)
	if err == nil || len(canceled) != 0 || !strings.Contains(err.Error(), "delivery pass still running") {
		t.Fatalf("canceled=%v err=%v", canceled, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "pending", id+".json")); statErr != nil {
		t.Fatalf("pending record changed: %v", statErr)
	}
}

func TestDispatchYieldsToWaitingOperator(t *testing.T) {
	root := t.TempDir()
	q := New(root)
	id, err := q.Enqueue("target", "sender", stuckText)
	if err != nil {
		t.Fatal(err)
	}
	operator, err := os.OpenFile(filepath.Join(root, ".operator.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer operator.Close()
	if err := syscall.Flock(int(operator.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	target := &fakeTarget{alive: true, pane: composerPane("")}
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if rec, err := q.Record(id); err != nil || rec.Status == "delivered" {
		t.Fatalf("pass ran while an operator was waiting: %+v %v", rec, err)
	}
	if err := syscall.Flock(int(operator.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := q.Dispatch(context.Background(), target, nil); err != nil {
		t.Fatal(err)
	}
	if rec, err := q.Record(id); err != nil || rec.Status != "delivered" {
		t.Fatalf("pass after the operator left: %+v %v", rec, err)
	}
}

func TestCancelWinsAgainstBackToBackPasses(t *testing.T) {
	root := t.TempDir()
	q := New(root)
	id, err := q.Enqueue("other", "sender", "cancel me")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a daemon whose passes follow each other with no gap: each
	// pass holds the dispatch lock for 100ms and re-takes it at once.
	stop := make(chan struct{})
	var passes sync.WaitGroup
	passes.Add(1)
	go func() {
		defer passes.Done()
		daemon := New(root)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if daemon.operatorWaiting() {
				time.Sleep(time.Millisecond)
				continue
			}
			lock, err := os.OpenFile(filepath.Join(root, ".dispatch.lock"), os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				return
			}
			if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
				time.Sleep(100 * time.Millisecond)
				syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
			}
			lock.Close()
		}
	}()
	defer func() { close(stop); passes.Wait() }()
	time.Sleep(20 * time.Millisecond)
	old := cancelLockWait
	cancelLockWait = 3 * time.Second
	defer func() { cancelLockWait = old }()
	canceled, err := New(root).Cancel(id)
	if err != nil || len(canceled) != 1 {
		t.Fatalf("canceled=%v err=%v", canceled, err)
	}
}
