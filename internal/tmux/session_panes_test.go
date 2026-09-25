package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestParseSessionPanesFollowsPaneProcessRule(t *testing.T) {
	out := "" +
		// Active pane in a background window must not win.
		"one\t1\t$1\t0\t1\tvim\t10\n" +
		"one\t1\t$1\t1\t0\tzsh\t11\n" +
		"one\t1\t$1\t1\t1\tcodex\t12\n" +
		// No active pane reported: the current window's first pane.
		"two\t0\t$2\t1\t0\tbash\t20\n" +
		"two\t0\t$2\t1\t0\tclaude\t21\n" +
		// Unusable pid: listed, process left zero for the caller's fallback.
		"three\t2\t$3\t1\t1\tbash\tx\n" +
		"bad\tnope\t$4\t1\t1\tbash\t40\n" +
		"short\t1\t$5\n" +
		"\n"
	got := parseSessionPanes(out)
	want := []SessionAttachment{
		{Name: "one", Attached: 1, ID: "$1", Process: PaneProcess{Command: "codex", PID: 12}},
		{Name: "two", Attached: 0, ID: "$2", Process: PaneProcess{Command: "bash", PID: 20}},
		{Name: "three", Attached: 2, ID: "$3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSessionPanes=%+v\nwant %+v", got, want)
	}
}

// The one-call listing must name the same process PaneProcess reads for every
// session, including a session whose current window has several panes.
func TestSessionsWithAttachmentsMatchesPaneProcess(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	dir := t.TempDir()
	socket := filepath.Join(dir, "sock")
	base := func(args ...string) *exec.Cmd {
		cmd := exec.Command(tmuxPath, append([]string{"-S", socket}, args...)...)
		cmd.Env = withoutTmux(os.Environ())
		return cmd
	}
	run := func(args ...string) {
		t.Helper()
		if output, err := base(args...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, output)
		}
	}
	wrapper := filepath.Join(dir, "tmux-isolated")
	script := "#!/bin/sh\nexec " + strconv.Quote(tmuxPath) + " -S " + strconv.Quote(socket) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client := &Client{Bin: wrapper}
	ctx := context.Background()
	run("new-session", "-d", "-s", "alpha", "sleep", "60")
	t.Cleanup(func() { _ = base("kill-server").Run() })
	run("split-window", "-t", "=alpha:", "-d", "sleep", "61")
	run("new-window", "-t", "=alpha:", "sleep", "62")
	run("split-window", "-t", "=alpha:", "sleep", "63")
	run("new-session", "-d", "-s", "beta", "sleep", "64")

	sessions, err := client.SessionsWithAttachments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions=%+v, want alpha and beta", sessions)
	}
	for _, session := range sessions {
		want, err := client.PaneProcess(ctx, session.Name)
		if err != nil {
			t.Fatal(err)
		}
		if session.Process != want || session.ID == "" || session.Attached != 0 {
			t.Fatalf("%s: listing=%+v, PaneProcess=%+v", session.Name, session, want)
		}
	}
}
