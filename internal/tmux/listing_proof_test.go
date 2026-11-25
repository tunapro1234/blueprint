package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// clearLocale removes every variable tmux reads to decide whether a client is
// UTF-8, restoring them when the test ends.
func clearLocale(t *testing.T) {
	t.Helper()
	for _, key := range []string{"TMUX", "LC_ALL", "LC_CTYPE", "LANG"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUTF8ArgsFollowsTmuxClientRule(t *testing.T) {
	args := []string{"list-sessions", "-F", "#{session_name}"}
	for _, test := range []struct {
		name string
		env  map[string]string
		want bool // -u prepended
	}{
		{"empty environment", map[string]string{}, true},
		{"inside a pane", map[string]string{"TMUX": "/tmp/tmux-0/default,1,0"}, false},
		{"TMUX set but empty", map[string]string{"TMUX": ""}, false},
		{"UTF-8 LANG", map[string]string{"LANG": "C.UTF-8"}, false},
		{"lowercase utf8 LC_CTYPE", map[string]string{"LC_CTYPE": "en_US.utf8"}, false},
		{"plain C LANG", map[string]string{"LANG": "C"}, true},
		{"POSIX LC_ALL over UTF-8 LANG", map[string]string{"LC_ALL": "POSIX", "LANG": "en_US.UTF-8"}, true},
		{"empty LC_ALL falls through", map[string]string{"LC_ALL": "", "LANG": "en_US.UTF-8"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookup := func(key string) (string, bool) {
				value, ok := test.env[key]
				return value, ok
			}
			got := utf8Args(lookup, args)
			want := args
			if test.want {
				want = append([]string{"-u"}, args...)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("utf8Args=%q, want %q", got, want)
			}
		})
	}
}

// The exec path is what reaches tmux: a caller without a UTF-8 locale must
// send -u, a caller with one must send the command unchanged.
func TestRunSendsUTF8FlagOnlyWithoutUTF8Locale(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + strconv.Quote(log) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client := &Client{Bin: bin}
	read := func() string {
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(data))
	}

	clearLocale(t)
	if _, err := client.Sessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "-u list-sessions -F #{session_name}" {
		t.Fatalf("argv without locale = %q, want -u first", got)
	}

	t.Setenv("LANG", "C.UTF-8")
	if _, err := client.Sessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "list-sessions -F #{session_name}" {
		t.Fatalf("argv with UTF-8 locale = %q, want unchanged", got)
	}
}

// This is the exact 2026-11-19 output: tmux 3.4 answering a non-UTF-8 client
// turns every tab into "_". It parsed as zero sessions and no error, and
// bp status closed every open agent. It must fail the listing instead.
func TestParseSessionPanesRejectsSanitizedListing(t *testing.T) {
	sanitized := "alpha_0_$0_1_1_claude_101\nbeta_1_$1_1_1_codex_202\n"
	if sessions, err := parseSessionPanes(sanitized); err == nil {
		t.Fatalf("sanitized listing parsed as %+v without error", sessions)
	}
	for _, row := range []string{
		"short\t1\t$5\n",
		"eight\t0\t$1\t1\t1\tbash\t10\t1\n",
		"bad\tnope\t$4\t1\t1\tbash\t40\n",
		"\t1\t$4\t1\t1\tbash\t40\n",
		"good\t0\t$1\t1\t1\tbash\t10\nshort\t1\n",
	} {
		if sessions, err := parseSessionPanes(row); err == nil {
			t.Fatalf("malformed listing %q parsed as %+v without error", row, sessions)
		}
	}
	if sessions, err := parseSessionPanes("\n\n"); err != nil || len(sessions) != 0 {
		t.Fatalf("blank listing = %+v, %v; want no sessions and no error", sessions, err)
	}
}

func TestListSessionsSeparatesNoServerFromNoSessions(t *testing.T) {
	for _, test := range []struct {
		name        string
		out         string
		err         error
		wantNames   []string
		wantReached bool
		wantErr     bool
	}{
		{"listed", "alpha\nbeta\n", nil, []string{"alpha", "beta"}, true, false},
		{"no server", "", errors.New("tmux list-sessions: exit status 1: no server running on /tmp/tmux-0/default"), nil, false, false},
		{"no socket after reboot", "", errors.New("tmux list-sessions: exit status 1: error connecting to /tmp/tmux-0/default (No such file or directory)"), nil, false, false},
		{"server without sessions", "", errors.New("tmux list-sessions: exit status 1: no sessions"), nil, true, false},
		{"access denied", "", errors.New("tmux list-sessions: exit status 1: error connecting to /tmp/tmux-0/default (Permission denied)"), nil, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &Client{exec: func(context.Context, []byte, ...string) ([]byte, error) {
				return []byte(test.out), test.err
			}}
			listing, err := client.ListSessions(context.Background())
			if (err != nil) != test.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, test.wantErr)
			}
			if !reflect.DeepEqual(listing.Names, test.wantNames) || listing.Reached != test.wantReached {
				t.Fatalf("listing=%+v, want names %v reached %v", listing, test.wantNames, test.wantReached)
			}
		})
	}
}

func TestStatusSnapshotCarriesReachedAndRejectsSanitizedRows(t *testing.T) {
	respond := func(out string, err error) *Client {
		return &Client{exec: func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
			if args[0] != "list-panes" {
				t.Fatalf("unexpected tmux call %q", args)
			}
			return []byte(out), err
		}}
	}
	ctx := context.Background()

	snapshot, err := respond("", errors.New("tmux list-panes: exit status 1: no server running on /tmp/x")).StatusSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if listing, err := snapshot.ListSessions(ctx); err != nil || listing.Reached || len(listing.Names) != 0 {
		t.Fatalf("no-server snapshot listing=%+v err=%v; want unreached and empty", listing, err)
	}

	snapshot, err = respond("alpha\t0\t$0\t1\t1\tclaude\t101\n", nil).StatusSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if listing, err := snapshot.ListSessions(ctx); err != nil || !listing.Reached || !reflect.DeepEqual(listing.Names, []string{"alpha"}) {
		t.Fatalf("reached snapshot listing=%+v err=%v", listing, err)
	}

	if _, err := respond("alpha_0_$0_1_1_claude_101\n", nil).StatusSnapshot(ctx); err == nil {
		t.Fatal("a sanitized listing produced a status snapshot; want an error")
	}
}

// Regression for the proven mechanism, against a real private tmux server: a
// caller with no locale and no TMUX (as under `env -i`) must still see every
// session in the status snapshot.
func TestStatusSnapshotSeesSessionsFromEmptyLocale(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	dir, err := os.MkdirTemp("", "bpu")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	base := func(args ...string) *exec.Cmd {
		cmd := exec.Command(tmuxPath, append([]string{"-S", socket}, args...)...)
		cmd.Env = withoutTmux(os.Environ())
		return cmd
	}
	for _, name := range []string{"alpha", "beta"} {
		if output, err := base("new-session", "-d", "-s", name, "sleep", "60").CombinedOutput(); err != nil {
			t.Fatalf("new-session %s: %v: %s", name, err, output)
		}
	}
	t.Cleanup(func() { _ = base("kill-server").Run() })
	wrapper := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\nexec " + strconv.Quote(tmuxPath) + " -S " + strconv.Quote(socket) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	clearLocale(t)
	ctx := context.Background()
	snapshot, err := (&Client{Bin: wrapper}).StatusSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listing, err := snapshot.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !listing.Reached || !reflect.DeepEqual(listing.Names, []string{"alpha", "beta"}) {
		t.Fatalf("snapshot from an empty locale = %+v; want alpha and beta from a reached server", listing)
	}
}
