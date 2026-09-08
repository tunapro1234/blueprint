package tmux

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeCodexModeCannotComeFromToolDescendantsOrWrapperHints(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "embedded", true: "remote"}[remote], func(t *testing.T) {
			proc := t.TempDir()
			write := func(pid, exe, argv, children string) {
				base := filepath.Join(proc, pid)
				if err := os.MkdirAll(filepath.Join(base, "task", pid), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(exe, filepath.Join(base, "exe")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(base, "cmdline"), []byte(argv+"\x00"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(base, "task", pid, "children"), []byte(children), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write("10", "/usr/bin/node", "node\x00--remote\x00unix:///stale-wrapper.sock", "11")
			args := "codex\x00resume\x0011111111-1111-1111-1111-111111111111"
			if remote {
				args += "\x00--remote\x00unix:///actual.sock"
			}
			write("11", "/opt/codex/codex", args, "12")
			write("12", "/opt/codex/codex", "codex\x00--remote\x00unix:///tool.sock\x00resume\x0022222222-2222-2222-2222-222222222222", "")
			got := codexProcessInfo(10, proc)
			expected := ""
			if remote {
				expected = "unix:///actual.sock"
			}
			if !got.Observed || got.Remote != expected || got.ThreadID != "11111111-1111-1111-1111-111111111111" {
				t.Fatal(got)
			}
		})
	}
}
