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
		verified, detected       bool
	}{
		{"execution helper", "codex-linux-sandbox", realID, realID, true, true},
		{"forged helper argv", "codex-linux-sandbox", realID, realID, false, false},
		{"spoofed command env", "codex-linux-sandbox", realID, rootID, false, true},
		{"shared daemon startup env", "codex\x00app-server", rootID, rootID, false, true},
		{"shared daemon other thread", "codex\x00app-server", rootID, realID, false, true},
		{"shared execution server", "codex\x00exec-server", rootID, rootID, false, true},
		{"standalone codex", "codex\x00--yolo", realID, realID, false, true},
		{"plain shell declaration", "sh", realID, realID, false, true},
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
			if got.Verified != tc.verified || got.CodexDetected != tc.detected || got.ThreadID != tc.hint {
				t.Fatalf("%+v", got)
			}
		})
	}
	if codexOrigin(realID, 99, t.TempDir()).Verified {
		t.Fatal("missing process accepted")
	}
}

func TestCodexOriginDetectsMissingThreadWithoutTrustingFallbacks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "12")
	_ = os.Mkdir(p, 0700)
	_ = os.Symlink("/opt/codex/codex", filepath.Join(p, "exe"))
	_ = os.WriteFile(filepath.Join(p, "cmdline"), []byte("codex\x00--yolo\x00"), 0600)
	_ = os.WriteFile(filepath.Join(p, "status"), []byte("PPid:\t0\n"), 0600)
	got := codexOrigin("", 12, dir)
	if !got.CodexDetected || got.Verified || got.ThreadID != "" {
		t.Fatal(got)
	}

	setEnv(t, map[string]string{"TMUX": "same-daemon", "AGENT": "server-main", "USER": "server-main"})
	client := &fakeSession{name: "server-main"}
	who := Resolve(context.Background(), client, Options{From: "server-main", Fallback: "server-main", Origin: func(context.Context) Origin { return got }})
	if who.Label != Unknown || who.Source != "codex-unverified" || who.Authoritative() || client.calls != 0 {
		t.Fatalf("%+v calls=%d", who, client.calls)
	}
}

func TestPSFallbackDetectsCodexButNeverVerifies(t *testing.T) {
	parents := map[int]struct {
		parent int
		name   string
	}{10: {9, "zsh"}, 9: {8, "/opt/bin/codex"}, 8: {1, "node"}}
	detected := codexAncestryPS(context.Background(), 10, func(_ context.Context, pid int) (int, string, bool) {
		p, ok := parents[pid]
		return p.parent, p.name, ok
	})
	if !detected {
		t.Fatal("Codex ancestor not detected")
	}
	if codexAncestryPS(context.Background(), 10, func(context.Context, int) (int, string, bool) { return 1, "sh", true }) {
		t.Fatal("plain shell reported as Codex")
	}
}

func TestLocalHintStaysExplicitlyUncertain(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	got := Resolve(context.Background(), nil, Options{
		Origin: func(context.Context) Origin { return Origin{ThreadID: id, CodexDetected: true} },
		Thread: func(context.Context, string) Identity {
			return Identity{Label: "local/subagent:" + id + "?", Parent: "local", Source: "codex-local-hint"}
		},
	})
	if got.Label != "local/subagent:"+id+"?" || got.ThreadID != id || got.Certain || got.Authoritative() || got.Source != "codex-local-hint" {
		t.Fatal(got)
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
		got := Resolve(context.Background(), nil, Options{Origin: noCodexOrigin})
		if got.Label == name || got.Certain || got.Authoritative() {
			t.Fatalf("claim became identity: %+v", got)
		}
	}
}
