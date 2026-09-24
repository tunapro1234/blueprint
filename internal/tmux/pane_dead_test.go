package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPaneDeadObservesOnlyItsIsolatedTmuxSocket(t *testing.T) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is unavailable")
	}
	socket := filepath.Join(t.TempDir(), "fleet.sock")
	cleanEnv := func() []string {
		env := make([]string, 0, len(os.Environ()))
		for _, value := range os.Environ() {
			if len(value) >= 5 && value[:5] == "TMUX=" || len(value) >= 10 && value[:10] == "TMUX_PANE=" {
				continue
			}
			env = append(env, value)
		}
		return env
	}
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, append([]string{"-S", socket}, args...)...)
		cmd.Env = cleanEnv()
		return cmd.CombinedOutput()
	}
	if _, err := run(context.Background(), "-f", "/dev/null", "new-session", "-d", "-s", "bp-fleet-pane-dead-test"); err != nil {
		t.Skipf("could not start isolated tmux server: %v", err)
	}
	t.Cleanup(func() { _, _ = run(context.Background(), "kill-server") })

	client := &Client{Bin: bin, exec: func(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
		return run(ctx, args...)
	}}
	dead, err := client.PaneDead(context.Background(), "bp-fleet-pane-dead-test")
	if err != nil || dead {
		t.Fatalf("initial pane dead=%v err=%v, want false", dead, err)
	}
	if _, err := run(context.Background(), "set-option", "-w", "-t", "=bp-fleet-pane-dead-test:", "remain-on-exit", "on"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(context.Background(), "respawn-pane", "-k", "-t", "=bp-fleet-pane-dead-test:", "exit 0"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		dead, err = client.PaneDead(context.Background(), "bp-fleet-pane-dead-test")
		if err == nil && dead {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pane did not become dead; last result dead=%v err=%v", dead, err)
}
