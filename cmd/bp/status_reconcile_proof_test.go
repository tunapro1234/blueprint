package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"blueprint/internal/cache"
	bptmux "blueprint/internal/tmux"
)

// statusListingTmux is a private fake tmux for bp status: "list-panes -a" and
// "list-sessions" print listing (session rows) and stderrText, then exit with
// code; every other call succeeds silently. A leading -u is skipped, as real
// tmux accepts it before the command.
func statusListingTmux(t *testing.T, listing, stderrText string, code int) *bptmux.Client {
	t.Helper()
	dir := t.TempDir()
	listingFile, stderrFile := filepath.Join(dir, "listing"), filepath.Join(dir, "stderr")
	if err := os.WriteFile(listingFile, []byte(listing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stderrFile, []byte(stderrText), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\n" +
		"[ \"$1\" = -u ] && shift\n" +
		"case \"$1 $2\" in\n" +
		"'list-panes -a'|list-sessions*) cat " + strconv.Quote(listingFile) + "; cat " + strconv.Quote(stderrFile) + " >&2; exit " + strconv.Itoa(code) + " ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return &bptmux.Client{Bin: bin, Sleep: func(time.Duration) {}, Now: time.Now}
}

func proofFleet(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agentbook.json")
	issueWriteBook(t, path, []map[string]any{
		{"name": "server-main", "folder": dir, "status": "open"},
		{"name": "alpha", "folder": dir, "parent": "server-main", "status": "open"},
		{"name": "beta", "folder": dir, "parent": "server-main", "status": "open"},
	})
	return dir, path
}

func requireFleetOpen(t *testing.T, path string) {
	t.Helper()
	for _, name := range []string{"server-main", "alpha", "beta"} {
		if got := bookStatus(t, path, name); got != "open" {
			t.Fatalf("%s=%q; bp status must not close agents it cannot prove gone", name, got)
		}
	}
}

// Regression for 2026-11-19: tmux 3.4 answered a bp status run under env -i
// (no TMUX, no UTF-8 locale) with every tab turned into "_". The status
// snapshot parsed zero sessions, reported no error, and bp status wrote
// "closed" over 103 live agents. The listing must now fail and the book stay.
func TestStatusDoesNotCloseFleetFromSanitizedListing(t *testing.T) {
	dir, path := proofFleet(t)
	sanitized := "server-main_0_$1_1_1_claude_101\nalpha_0_$2_1_1_claude_102\nbeta_1_$3_1_1_codex_103\n"
	a := issueApp(t, path, filepath.Join(dir, "state"), statusListingTmux(t, sanitized, "", 0))
	a.loadCache = func(map[string]string) map[string]cache.State { return nil }
	if err := a.status([]string{"--json"}); err == nil || !strings.Contains(err.Error(), "unparseable row") {
		t.Fatalf("status err=%v; want the unparseable listing reported", err)
	}
	requireFleetOpen(t, path)
}

// No server answered from this command's environment (another TMUX_TMPDIR,
// user or private /tmp, or a reboot): the CLI cannot tell which, leaves the
// book to the daemon and says so.
func TestStatusLeavesBookWhenNoServerAnswers(t *testing.T) {
	for _, message := range []string{
		"no server running on /tmp/tmux-0/default",
		"error connecting to /tmp/tmux-0/default (No such file or directory)",
	} {
		t.Run(message, func(t *testing.T) {
			dir, path := proofFleet(t)
			a := issueApp(t, path, filepath.Join(dir, "state"), statusListingTmux(t, "", message, 1))
			a.loadCache = func(map[string]string) map[string]cache.State { return nil }
			if err := a.status(nil); err != nil {
				t.Fatal(err)
			}
			requireFleetOpen(t, path)
			if got := readTestOutput(t, a.err); !strings.Contains(got, "agentbook not reconciled: no tmux server answered") {
				t.Fatalf("stderr=%q; want the skipped reconcile explained", got)
			}
		})
	}
}

// A reached server that lists none of the fleet (a foreign server, or a
// listing that lost every row) is not proof either.
func TestStatusLeavesBookWhenServerListsNoneOfTheFleet(t *testing.T) {
	dir, path := proofFleet(t)
	a := issueApp(t, path, filepath.Join(dir, "state"), statusListingTmux(t, "scratch\t1\t$9\t1\t1\tzsh\t900\n", "", 0))
	a.loadCache = func(map[string]string) map[string]cache.State { return nil }
	if err := a.status(nil); err != nil {
		t.Fatal(err)
	}
	requireFleetOpen(t, path)
	if got := readTestOutput(t, a.err); !strings.Contains(got, "lists none of the open agents' sessions") {
		t.Fatalf("stderr=%q; want the skipped reconcile explained", got)
	}
}

// A closed ephemeral is only auto-archived after a reconcile that proved
// something; an unproven listing must not archive it either.
func TestStatusArchivesOnlyAfterProvenListing(t *testing.T) {
	for _, test := range []struct {
		name         string
		listing      string
		stderrText   string
		code         int
		wantArchived bool
	}{
		{"no server", "", "no server running on /tmp/tmux-0/default", 1, false},
		{"fleet's server", "server-main\t0\t$1\t1\t1\tclaude\t101\n", "", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "agentbook.json")
			issueWriteBook(t, path, []map[string]any{
				{"name": "server-main", "folder": dir, "status": "open"},
				{"name": "eph", "folder": dir, "parent": "server-main", "status": "closed", "lifetime": "ephemeral"},
			})
			a := issueApp(t, path, filepath.Join(dir, "state"), statusListingTmux(t, test.listing, test.stderrText, test.code))
			a.config.Lifecycle.ArchiveOnClose = true
			a.loadCache = func(map[string]string) map[string]cache.State { return nil }
			if err := a.status(nil); err != nil {
				t.Fatal(err)
			}
			file, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(file), "archivedAt"); got != test.wantArchived {
				t.Fatalf("archived=%v, want %v: %s", got, test.wantArchived, file)
			}
		})
	}
}
