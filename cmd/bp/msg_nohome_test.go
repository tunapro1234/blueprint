package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bpconfig "blueprint/internal/config"
	"blueprint/internal/msgq"
	bptmux "blueprint/internal/tmux"
	"blueprint/internal/userhome"
)

// noHomeMsgFixture is bp msg as a systemd unit runs it: no HOME in the
// environment. Every tmux call, including the ones the agentbook probes make
// through bptmux.New(), reaches a scripted tmux on PATH; the Claude home is a
// temporary directory the test's user database reports, so nothing resolves
// against the real account's home.
type noHomeMsgFixture struct {
	app        *app
	output     *os.File
	calls      func() string
	msgqRoot   string
	transcript string
}

func newNoHomeMsgFixture(t *testing.T, database func() (*user.User, error)) noHomeMsgFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	bin := filepath.Join(root, "bin")
	stateDir := filepath.Join(root, "state")
	for _, dir := range []string{home, work, bin, stateDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Claude Code's directory name for a cwd: every non-alphanumeric byte is '-'.
	munged := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, work)
	projectDir := filepath.Join(home, ".claude", "projects", munged)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(projectDir, "00000000-0000-4000-8000-00000000a1b2.jsonl")
	idle := `{"type":"custom-title","customTitle":"alp"}` + "\n" +
		`{"type":"system","subtype":"turn_duration","timestamp":"` + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano) + `"}` + "\n"
	if err := os.WriteFile(transcript, []byte(idle), 0o600); err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(root, "agentbook.json")
	bookJSON, _ := json.Marshal(map[string]any{"orchestrator": "ada", "agents": []map[string]any{
		{"name": "ada", "status": "open", "folder": root},
		{"name": "alp", "parent": "ada", "status": "open", "folder": work},
	}})
	if err := os.WriteFile(bookPath, bookJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	// A Claude pane at an empty composer. A paste shows in the composer until
	// Enter submits it; the submission lands in the transcript as the user
	// record Claude Code would write, so the witness can confirm it.
	log := filepath.Join(root, "tmux-calls")
	buffer := filepath.Join(root, "buffer")
	composer := filepath.Join(root, "composer")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + quoteShell(log) + "\n" +
		"case \"$1\" in\n" +
		"has-session) exit 0 ;;\n" +
		"list-sessions) echo alp ;;\n" +
		"list-panes) printf '1\\tclaude\\t2147480000\\n' ;;\n" +
		"display-message) echo claude ;;\n" +
		"capture-pane) printf '❯ %s\\n──────────\\n' \"$(cat " + quoteShell(composer) + " 2>/dev/null)\" ;;\n" +
		"load-buffer) cat > " + quoteShell(buffer) + " ;;\n" +
		"paste-buffer) cp " + quoteShell(buffer) + " " + quoteShell(composer) + " ;;\n" +
		"send-keys) case \"$*\" in *Enter*|*C-m*) if [ -s " + quoteShell(composer) + " ]; then " +
		"printf '{\"type\":\"user\",\"timestamp\":\"%s\",\"message\":{\"role\":\"user\",\"content\":\"%s\"}}\\n' " +
		"\"$(date -u +%Y-%m-%dT%H:%M:%S.%3NZ)\" \"$(cat " + quoteShell(composer) + ")\" >> " + quoteShell(transcript) + "; : > " + quoteShell(composer) + "; fi ;; esac ;;\n" +
		"*) : ;;\nesac\n"
	tmuxPath := filepath.Join(bin, "tmux")
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := func() string {
		data, err := os.ReadFile(log)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return string(data)
	}

	t.Setenv("AGENT", "ada")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// t.Setenv records HOME for restoration; the unset then holds only for this test.
	t.Setenv("HOME", "")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	if database == nil {
		database = func() (*user.User, error) { return &user.User{Uid: "0", HomeDir: home}, nil }
	}
	userhome.SetUserDatabaseForTesting(t, database)

	msgqRoot := filepath.Join(stateDir, "msgq")
	out := testOutput(t)
	a := &app{
		ctx:           context.Background(),
		config:        bpconfig.Config{StateDir: stateDir, MsgqRoot: msgqRoot, Agentbooks: []string{bookPath}},
		queue:         msgq.New(msgqRoot),
		tmux:          &bptmux.Client{Bin: tmuxPath, Sleep: func(time.Duration) {}, Now: time.Now},
		out:           out,
		err:           out,
		sessionExists: func(string) bool { return true },
	}
	trustedSenderFixture(a)
	return noHomeMsgFixture{app: a, output: out, calls: calls, msgqRoot: msgqRoot, transcript: transcript}
}

// A unit without HOME used to resolve the Claude transcript directory as the
// relative ".claude/projects", and its own delivery stopped at "runtime unknown:
// Claude transcript directory unavailable" until the daemon (which has HOME)
// delivered it. The home now comes from the user database, so the CLI reads the
// same transcript directory the daemon reads and delivers in-process.
func TestMessageWithoutHOMEDeliversInProcess(t *testing.T) {
	before, wasSet := os.LookupEnv("HOME")
	t.Run("send", func(t *testing.T) {
		f := newNoHomeMsgFixture(t, nil)
		if _, set := os.LookupEnv("HOME"); set {
			t.Fatal("fixture left HOME set")
		}
		err := f.app.message([]string{"alp", "nightly report is ready for review"})
		got := readTestOutput(t, f.output)
		if err != nil {
			t.Fatalf("message: %v\noutput:\n%s\ntmux calls:\n%s", err, got, f.calls())
		}
		if strings.Contains(got, "runtime unknown") || strings.Contains(got, ".claude/projects") {
			t.Fatalf("HOME-less send still reports an unresolved runtime:\n%s", got)
		}
		if !strings.Contains(f.calls(), "paste-buffer") {
			t.Fatalf("message was not pasted in-process:\n%s\ntmux calls:\n%s", got, f.calls())
		}
		if !strings.HasSuffix(got, "\nsent\nRESULT=delivered CHANNEL="+channelOf(t, got)+"\n") {
			transcript, _ := os.ReadFile(f.transcript)
			t.Fatalf("output=%q, want a verified in-process delivery\ntranscript:\n%s", got, transcript)
		}
	})
	if after, set := os.LookupEnv("HOME"); set != wasSet || after != before {
		t.Fatalf("HOME after the test = %q (set=%v), want %q (set=%v)", after, set, before, wasSet)
	}
}

// Without HOME and without a user database home the CLI cannot read Claude
// transcripts at all. It must not guess a relative path, and its output must say
// the message is queued for the daemon instead of "runtime unknown".
func TestMessageWithoutAnyHomeLeavesDeliveryToDaemon(t *testing.T) {
	f := newNoHomeMsgFixture(t, func() (*user.User, error) { return nil, errors.New("no passwd entry") })
	err := f.app.message([]string{"alp", "nightly report is ready for review"})
	got := readTestOutput(t, f.output)
	if err != nil {
		t.Fatalf("message: %v\noutput:\n%s", err, got)
	}
	if strings.Contains(f.calls(), "paste-buffer") {
		t.Fatalf("pasted without a readable transcript:\n%s", f.calls())
	}
	if strings.Contains(got, "runtime unknown") || strings.Contains(got, ".claude/projects") {
		t.Fatalf("output still reads like a failure:\n%s", got)
	}
	if !strings.Contains(got, "QUEUED (channel: q") || !strings.Contains(got, "the bp daemon will deliver it") ||
		!strings.Contains(got, "RESULT=queued CHANNEL=q") {
		t.Fatalf("output does not say the daemon delivers the queued message:\n%s", got)
	}
	if records := queuedMessages(t, f.msgqRoot); len(records) != 1 {
		t.Fatalf("queue records=%v, want the one message", records)
	}
}

func channelOf(t *testing.T, output string) string {
	t.Helper()
	_, rest, ok := strings.Cut(output, "CHANNEL=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0])
}
