package book

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	bptmux "blueprint/internal/tmux"
)

func reconcileFixture(t *testing.T) (string, func() string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentbook.json")
	before := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	writeBookFile(t, path, map[string]any{"agents": []any{
		map[string]any{"name": "alpha", "status": "open", "statusAt": before},
		map[string]any{"name": "beta", "status": "open", "statusAt": before},
		map[string]any{"name": "gamma", "status": "opening", "statusAt": before},
		map[string]any{"name": "done", "status": "closed", "statusAt": before},
		map[string]any{"name": "kept", "status": "open", "statusAt": before, "archivedAt": before},
	}})
	statuses := func() string {
		file, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		var rows []string
		for _, agent := range file.Agents {
			rows = append(rows, agent.Name+"="+agent.Status)
		}
		sort.Strings(rows)
		return strings.Join(rows, " ")
	}
	return path, statuses
}

const reconcileUntouched = "alpha=open beta=open done=closed gamma=opening kept=open"

func TestReconcileFromListingRule(t *testing.T) {
	for _, test := range []struct {
		name      string
		listing   bptmux.SessionListing
		authority ReconcileAuthority
		want      string
		skipped   bool
	}{
		// The 2026-11-19 incident: a CLI listing that reached a server but
		// parsed none of the fleet's sessions must not close the fleet.
		{"cli reached server listing none of the fleet", bptmux.SessionListing{Reached: true, Names: []string{"unrelated"}}, ReconcileCLI, reconcileUntouched, true},
		{"cli reached empty server", bptmux.SessionListing{Reached: true}, ReconcileCLI, reconcileUntouched, true},
		{"cli found no server", bptmux.SessionListing{}, ReconcileCLI, reconcileUntouched, true},
		// A server that demonstrably hosts the fleet proves the others gone.
		{"cli reached the fleet's server", bptmux.SessionListing{Reached: true, Names: []string{"alpha"}}, ReconcileCLI, "alpha=open beta=closed done=closed gamma=closed kept=open", false},
		{"cli everything live", bptmux.SessionListing{Reached: true, Names: []string{"alpha", "beta", "gamma"}}, ReconcileCLI, reconcileUntouched, false},
		// Only the daemon records a whole fleet gone: reboot or crashed server.
		{"daemon found no server", bptmux.SessionListing{}, ReconcileDaemon, "alpha=closed beta=closed done=closed gamma=closed kept=open", false},
		{"daemon reached empty server", bptmux.SessionListing{Reached: true}, ReconcileDaemon, "alpha=closed beta=closed done=closed gamma=closed kept=open", false},
		{"daemon reached the fleet's server", bptmux.SessionListing{Reached: true, Names: []string{"beta"}}, ReconcileDaemon, "alpha=closed beta=open done=closed gamma=closed kept=open", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, statuses := reconcileFixture(t)
			changes, skipped, err := ReconcileFromListing([]string{path}, test.listing, test.authority, nil, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if (skipped != "") != test.skipped {
				t.Fatalf("skipped=%q, want skipped=%v", skipped, test.skipped)
			}
			if test.skipped && len(changes) != 0 {
				t.Fatalf("a skipped pass reported changes %v", changes)
			}
			if got := statuses(); got != test.want {
				t.Fatalf("book=%s\nwant %s", got, test.want)
			}
		})
	}
}

// An archived row is not part of the open fleet: its session being listed must
// not count as proof that the reached server is the fleet's.
func TestReconcileFromListingIgnoresArchivedRowsAsProof(t *testing.T) {
	path, statuses := reconcileFixture(t)
	_, skipped, err := ReconcileFromListing([]string{path}, bptmux.SessionListing{Reached: true, Names: []string{"kept", "done"}}, ReconcileCLI, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if skipped == "" || statuses() != reconcileUntouched {
		t.Fatalf("skipped=%q book=%s; want the pass refused", skipped, statuses())
	}
}

// With nothing open there is nothing to prove or close, and no note to print.
func TestReconcileFromListingNothingOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeBookFile(t, path, map[string]any{"agents": []any{map[string]any{"name": "done", "status": "closed"}}})
	changes, skipped, err := ReconcileFromListing([]string{path}, bptmux.SessionListing{}, ReconcileCLI, nil, time.Now())
	if err != nil || skipped != "" || len(changes) != 0 {
		t.Fatalf("changes=%v skipped=%q err=%v; want a silent no-op", changes, skipped, err)
	}
}

// The proof spans every configured book: a live session recorded in one book
// lets the CLI close a vanished agent recorded in another.
func TestReconcileFromListingProofSpansBooks(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	writeBookFile(t, first, map[string]any{"agents": []any{map[string]any{"name": "live", "status": "open"}}})
	writeBookFile(t, second, map[string]any{"agents": []any{map[string]any{"name": "gone", "status": "open"}}})
	changes, skipped, err := ReconcileFromListing([]string{first, second}, bptmux.SessionListing{Reached: true, Names: []string{"live"}}, ReconcileCLI, nil, time.Now())
	if err != nil || skipped != "" {
		t.Fatalf("skipped=%q err=%v", skipped, err)
	}
	if !reflect.DeepEqual(changes, []string{"gone: open -> closed"}) {
		t.Fatalf("changes=%v, want gone closed", changes)
	}
}

// A live recorded process keeps protecting its row under the new entry point.
func TestReconcileFromListingKeepsProcessGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeBookFile(t, path, map[string]any{"agents": []any{
		map[string]any{"name": "local", "status": "open", "localRuntime": map[string]any{"pid": os.Getpid()}},
	}})
	changes, _, err := ReconcileFromListing([]string{path}, bptmux.SessionListing{}, ReconcileDaemon, func(pid int) bool { return pid == os.Getpid() }, time.Now())
	if err != nil || len(changes) != 0 {
		t.Fatalf("changes=%v err=%v; want the live local process kept open", changes, err)
	}
}
