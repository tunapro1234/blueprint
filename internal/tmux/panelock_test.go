package tmux

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPaneLockKeepsASecondWriterOutOfTheSameComposer(t *testing.T) {
	// The whole point, tested against a real file: while one holder is inside the
	// critical section for a pane, nobody else gets in. flock treats two descriptors
	// of the SAME process as rivals, so one process can prove this on its own —
	// which is also why the lock is documented as non-reentrant.
	dir := t.TempDir()
	release, err := acquirePaneLock(dir, "target", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := acquirePaneLock(dir, "target", 300*time.Millisecond); !errors.Is(err, ErrPaneLocked) {
		t.Fatalf("a second writer got into a locked pane: %v", err)
	}
	if waited := time.Since(started); waited < 250*time.Millisecond {
		t.Fatalf("gave up after %s without waiting out its budget", waited)
	}
	// It is an ErrBusy, because that is how every caller already treats "not now":
	// queue the message, record the reason, try again next pass.
	if _, err := acquirePaneLock(dir, "target", 10*time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Fatalf("ErrPaneLocked does not read as busy: %v", err)
	}
	// A different pane is a different lock: one busy composer must not stop the fleet.
	other, err := acquirePaneLock(dir, "baska-hedef", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("an unrelated pane was blocked: %v", err)
	}
	other()
	// And the lock is handed back on release.
	release()
	again, err := acquirePaneLock(dir, "target", time.Second)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	again()
}

func TestPaneLockFileIsSharedAndPathSafe(t *testing.T) {
	// Two bp processes must resolve the SAME file from the same queue root, and a
	// session name is not a file name: a "/" in one would silently put the lock
	// somewhere it guards nothing.
	dir := t.TempDir()
	release, err := acquirePaneLock(dir, "worktree/topic", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := os.Stat(filepath.Join(dir, paneLockDir, "worktree_topic.lock")); err != nil {
		t.Fatalf("lock file is not where another bp would look for it: %v", err)
	}
}

func TestPaneLockWithoutASharedRootDoesNothing(t *testing.T) {
	// No queue root means no directory two processes could share, which happens in
	// tests and in an app assembled without config. Degrading to a no-op keeps those
	// callers working; it never happens in production, where msgqRoot is always set.
	release, err := acquirePaneLock("", "target", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	release()
	second, err := acquirePaneLock("", "target", time.Millisecond)
	if err != nil {
		t.Fatalf("the no-op lock blocked a caller: %v", err)
	}
	second()
}
