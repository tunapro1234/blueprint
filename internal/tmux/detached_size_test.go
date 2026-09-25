package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSizeDetachedWindowKeepsSizeAndReleasesManualSizing(t *testing.T) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is unavailable")
	}
	socket := filepath.Join(t.TempDir(), "size.sock")
	env := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "TMUX=") || strings.HasPrefix(value, "TMUX_PANE=") {
			continue
		}
		env = append(env, value)
	}
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, append([]string{"-S", socket}, args...)...)
		cmd.Env = env
		return cmd.CombinedOutput()
	}
	ctx := context.Background()
	// A tiny window stands in for the one tmux derives from small tiled clients.
	if _, err := run(ctx, "-f", "/dev/null", "new-session", "-d", "-s", "fresh", "-x", "38", "-y", "14"); err != nil {
		t.Skipf("could not start isolated tmux server: %v", err)
	}
	t.Cleanup(func() { _, _ = run(context.Background(), "kill-server") })

	client := &Client{Bin: bin, exec: func(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
		return run(ctx, args...)
	}}
	client.sizeDetachedWindow(ctx, "fresh")

	size, err := run(ctx, "display-message", "-p", "-t", "=fresh:", "#{window_width}x#{window_height}")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(size)); got != "200x50" {
		t.Fatalf("window size=%s, want 200x50", got)
	}
	// The window must not stay pinned: an attaching client has to take it over.
	option, err := run(ctx, "show-options", "-w", "-t", "=fresh:", "window-size")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(option)) != "" {
		t.Fatalf("window-level window-size left set: %q", option)
	}
}

func TestOpenSizesOnlyANewSession(t *testing.T) {
	for _, tc := range []struct {
		pane string
		want bool
	}{{"", true}, {"zsh", false}} {
		var calls []string
		created := false
		h := &openHarness{paneCommand: tc.pane}
		client := openClient(h)
		inner := client.exec
		client.exec = func(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(args, " "))
			if args[0] == "new-session" {
				created = true
			}
			if tc.pane == "" && args[0] == "has-session" && !created {
				return nil, errNoSession
			}
			return inner(ctx, stdin, args...)
		}
		if err := client.Open(context.Background(), "agent", "/srv/agent", OpenOptions{NoPrompt: true}, nil); err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(calls, "\n")
		resized := strings.Contains(joined, "resize-window -t =agent: -x 200 -y 50") &&
			strings.Contains(joined, "set-option -wu -t =agent: window-size")
		if resized != tc.want {
			t.Fatalf("pane=%q resized=%v, want %v:\n%s", tc.pane, resized, tc.want, joined)
		}
	}
}
