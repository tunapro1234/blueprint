package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestClientsParsesAttachedPIDAndSessionRows(t *testing.T) {
	var gotArgs []string
	client := &Client{exec: func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("1234\tworker\nnot-a-pid\tignored\n5678\tunregistered\n"), nil
	}}
	clients, err := client.Clients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []AttachedClient{{PID: 1234, Session: "worker"}, {PID: 5678, Session: "unregistered"}}
	if !reflect.DeepEqual(clients, want) {
		t.Fatalf("clients=%+v, want %+v", clients, want)
	}
	if !reflect.DeepEqual(gotArgs, []string{"list-clients", "-F", "#{client_pid}\t#{session_name}"}) {
		t.Fatalf("tmux args=%q", gotArgs)
	}
}

func TestClientsReadsOnlyAnIsolatedTmuxSocket(t *testing.T) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is unavailable")
	}
	root := t.TempDir()
	socket := filepath.Join(root, "isolated.sock")
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
		all := append([]string{"-S", socket}, args...)
		cmd := exec.CommandContext(ctx, bin, all...)
		cmd.Env = cleanEnv()
		return cmd.CombinedOutput()
	}
	if _, err := run(context.Background(), "-f", "/dev/null", "new-session", "-d", "-s", "bp-window-test"); err != nil {
		t.Skipf("could not start isolated tmux server: %v", err)
	}
	t.Cleanup(func() { _, _ = run(context.Background(), "kill-server") })

	client := &Client{Bin: bin, exec: func(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
		return run(ctx, args...)
	}}
	clients, err := client.Clients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 0 {
		t.Fatalf("new isolated server has attached clients: %+v", clients)
	}
}
