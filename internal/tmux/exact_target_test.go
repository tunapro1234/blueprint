package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestExactTargetsDoNotMatchLongerSessionPrefix(t *testing.T) {
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
	if output, err := base("new-session", "-d", "-s", "lead-worker", "sleep", "30").CombinedOutput(); err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = base("kill-server").Run() })

	wrapper := filepath.Join(dir, "tmux-isolated")
	script := "#!/bin/sh\nexec " + strconv.Quote(tmuxPath) + " -S " + strconv.Quote(socket) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client := &Client{Bin: wrapper}
	ctx := context.Background()
	if client.HasSession(ctx, "lead") {
		t.Fatal("lead prefix was treated as an exact live session")
	}
	if err := client.SetOption(ctx, "lead", "@bp_exact_test", "touched"); err == nil {
		t.Fatal("set-option unexpectedly targeted lead-worker")
	}
	output, err := base("show-options", "-qv", "-t", "=lead-worker:", "@bp_exact_test").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("lead-worker option was touched: err=%v output=%q", err, output)
	}
	if err := client.Close(ctx, "lead"); err == nil {
		t.Fatal("close unexpectedly targeted lead-worker")
	}
	if !client.HasSession(ctx, "lead-worker") {
		t.Fatal("operation on lead closed lead-worker")
	}
}

func withoutTmux(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		if strings.HasPrefix(entry, "TMUX=") || strings.HasPrefix(entry, "TMUX_PANE=") {
			continue
		}
		result = append(result, entry)
	}
	return result
}
