package identity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexOriginDoesNotTrustSharedDaemonOrCommandEnvironment(t *testing.T) {
	const realID = "11111111-1111-1111-1111-111111111111"
	const rootID = "22222222-2222-2222-2222-222222222222"
	for _, tc := range []struct {
		name, command, env, hint string
		want                     bool
	}{
		{"execution helper", "codex-linux-sandbox", realID, realID, true},
		{"forged helper argv", "codex-linux-sandbox", realID, realID, false},
		{"spoofed command env", "codex-linux-sandbox", realID, rootID, false},
		{"shared daemon startup env", "codex\x00app-server", rootID, rootID, false},
		{"shared daemon other thread", "codex\x00app-server", rootID, realID, false},
		{"shared execution server", "codex\x00exec-server", rootID, rootID, false},
		{"plain shell declaration", "sh", realID, realID, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "12")
			_ = os.Mkdir(p, 0700)
			executable := "/opt/codex/codex"
			if tc.name == "forged helper argv" {
				executable = "/bin/bash"
			}
			_ = os.Symlink(executable, filepath.Join(p, "exe"))
			_ = os.WriteFile(filepath.Join(p, "cmdline"), []byte(tc.command+"\x00"), 0600)
			_ = os.WriteFile(filepath.Join(p, "environ"), []byte("TMUX=stale\x00TMUX_PANE=%161\x00CODEX_THREAD_ID="+tc.env+"\x00"), 0600)
			_ = os.WriteFile(filepath.Join(p, "status"), []byte("PPid:\t0\n"), 0600)
			got := codexOrigin(tc.hint, 12, dir)
			if got.Verified != tc.want || got.ThreadID != tc.hint {
				t.Fatalf("%+v", got)
			}
		})
	}
	if codexOrigin(realID, 99, t.TempDir()).Verified {
		t.Fatal("missing process accepted")
	}
}

func TestUnverifiedCodexNeverFallsThroughToRootClaims(t *testing.T) {
	for _, verified := range []bool{false, true} {
		t.Run(fmt.Sprint(verified), func(t *testing.T) {
			setEnv(t, map[string]string{"TMUX": "same-daemon", "AGENT": "server-main", "USER": "server-main"})
			client := &fakeSession{name: "server-main"}
			got := Resolve(context.Background(), client, Options{
				From: "server-main", Fallback: "server-main",
				Origin: func(context.Context) Origin { return Origin{ThreadID: "unknown-thread", Verified: verified} },
			})
			if got.Label == "server-main" || got.Authoritative() || got.Certain || client.calls != 0 {
				t.Fatalf("%+v calls=%d", got, client.calls)
			}
		})
	}
}

func TestDeclaredLabelsCannotAuthorize(t *testing.T) {
	for _, name := range []string{"server-main", "whatsapp", "bp", "ordinary-agent"} {
		setEnv(t, map[string]string{"AGENT": name})
		got := Resolve(context.Background(), nil, Options{})
		if got.Label == name || got.Certain || got.Authoritative() {
			t.Fatalf("claim became identity: %+v", got)
		}
	}
}
