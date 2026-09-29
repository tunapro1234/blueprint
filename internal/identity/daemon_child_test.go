package identity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	fakeCallerPID = 400
	fakeBridgePID = 300
	fakeDaemonPID = 200
)

func writeFakeProcess(t *testing.T, procRoot string, pid, parent int, executable string, args ...string) {
	t.Helper()
	dir := filepath.Join(procRoot, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	status := fmt.Sprintf("Name:\ttest\nState:\tS (sleeping)\nPPid:\t%d\n", parent)
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0600); err != nil {
		t.Fatal(err)
	}
	if len(args) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(args, "\x00")+"\x00"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if executable != "" {
		if err := os.Symlink(executable, filepath.Join(dir, "exe")); err != nil {
			t.Fatal(err)
		}
	}
}

func writeValidDaemonChild(t *testing.T, procRoot, executable, daemonArg string, daemonParent int) {
	t.Helper()
	writeFakeProcess(t, procRoot, fakeCallerPID, fakeBridgePID, "", "bp", "msg")
	writeFakeProcess(t, procRoot, fakeBridgePID, fakeDaemonPID, "", "node", "/srv/whatsapp/bridge.js")
	writeFakeProcess(t, procRoot, fakeDaemonPID, daemonParent, executable, "/srv/blueprint/bp", daemonArg)
}

func TestDaemonChildIdentityResolution(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
		want  bool
	}{
		{
			name: "daemon grandparent resolves a certain whatsapp identity",
			setup: func(t *testing.T, root string) {
				writeValidDaemonChild(t, root, "/srv/blueprint/bp", "daemon", 1)
			},
			want: true,
		},
		{
			name: "daemon as great-grandparent is not verified",
			setup: func(t *testing.T, root string) {
				writeFakeProcess(t, root, fakeCallerPID, fakeBridgePID, "", "bp", "msg")
				writeFakeProcess(t, root, fakeBridgePID, 250, "", "node", "/srv/whatsapp/bridge.js")
				writeFakeProcess(t, root, 250, fakeDaemonPID, "", "wrapper")
				writeFakeProcess(t, root, fakeDaemonPID, 1, "/srv/blueprint/bp", "/srv/blueprint/bp", "daemon")
			},
		},
		{
			name: "bp grandparent with a different command is not verified",
			setup: func(t *testing.T, root string) {
				writeValidDaemonChild(t, root, "/srv/blueprint/bp", "msg", 1)
			},
		},
		{
			name: "daemon whose parent is not pid one is not verified",
			setup: func(t *testing.T, root string) {
				writeValidDaemonChild(t, root, "/srv/blueprint/bp", "daemon", 99)
			},
		},
		{
			name: "deleted daemon executable is accepted",
			setup: func(t *testing.T, root string) {
				writeValidDaemonChild(t, root, "/srv/blueprint/bp (deleted)", "daemon", 1)
			},
			want: true,
		},
		{
			name:  "unreadable proc fails closed without panic",
			setup: func(*testing.T, string) {},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setEnv(t, map[string]string{"AGENT": "whatsapp"})
			procRoot := t.TempDir()
			test.setup(t, procRoot)
			var checkedPID int
			who := Resolve(context.Background(), nil, Options{
				Origin: noCodexOrigin,
				DaemonChild: func(pid int) bool {
					checkedPID = pid
					return daemonChildAt(procRoot, fakeCallerPID)
				},
			})
			if checkedPID != os.Getpid() {
				t.Fatalf("daemon-child check received pid %d, want current pid %d", checkedPID, os.Getpid())
			}
			if test.want {
				if who.Label != "whatsapp" || !who.Certain || who.Source != "bp-daemon-child" {
					t.Fatalf("identity=%+v, want certain whatsapp from bp-daemon-child", who)
				}
			} else if who.Label != "agent?:whatsapp" || who.Certain || who.Source != "AGENT" {
				t.Fatalf("identity=%+v, want unverified AGENT identity", who)
			}
		})
	}
}
