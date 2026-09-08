package tmux

import "testing"

func TestWriterLockRequiresThisProcessHeldWriteLock(t *testing.T) {
	for _, tc := range []struct {
		line string
		pid  int
		want bool
	}{
		{"lock:\t1: FLOCK ADVISORY WRITE 42 fc:00:6160730 0 EOF", 42, true},
		{"lock:\t1: FLOCK ADVISORY WRITE 42 fc:00:6160730 0 EOF", 43, false},
		{"lock:\t1: FLOCK ADVISORY READ 42 fc:00:6160730 0 EOF", 42, false},
		{"flags: 0100002", 42, false},
	} {
		if got := heldWriterLock(tc.line, tc.pid); got != tc.want {
			t.Fatalf("%q pid %d: %v", tc.line, tc.pid, got)
		}
	}
}

func TestDarwinWriterLSOFRequiresLockAndExactHome(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	data := "p42\nf3\nlW\nn/home/.codex/thread-writer-locks/" + id + ".lock\nf4\nl \nn/home/.codex/thread-writer-locks/22222222-2222-2222-2222-222222222222.lock\nf5\nlW\nn/other/thread-writer-locks/33333333-3333-3333-3333-333333333333.lock\n"
	ids := parseWriterLSOF("/home/.codex", data)
	if len(ids) != 1 || ids[0] != id {
		t.Fatal(ids)
	}
}
