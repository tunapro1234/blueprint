package book

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenBindingNeedsExplicitRebindForChangedFolderOrThread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.json")
	writeBookFile(t, path, map[string]any{"agents": []any{map[string]any{
		"name": "worker", "folder": "/repo/old", "status": "closed",
		"nativeTitle": map[string]any{"threadId": "thread-old"},
	}}})

	err := CheckOpenBinding([]string{path}, "worker", "/repo/new", "thread-new", false, false, false)
	if err == nil || !strings.Contains(err.Error(), "thread-old") || !strings.Contains(err.Error(), "--rebind") {
		t.Fatalf("conflict=%v, want old/new binding and --rebind guidance", err)
	}
	if err := CheckOpenBinding([]string{path}, "worker", "/repo/new", "thread-new", false, false, true); err != nil {
		t.Fatalf("explicit rebind rejected: %v", err)
	}
}

func TestOpenBindingRejectsThreadClaimedByArchivedRecordEvenWithRebind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.json")
	writeBookFile(t, path, map[string]any{"agents": []any{
		map[string]any{"name": "worker", "folder": "/repo", "status": "closed"},
		map[string]any{"name": "retired", "folder": "/old", "archivedAt": "2026-01-01", "nativeTitle": map[string]any{"threadId": "thread-one"}},
	}})
	err := CheckOpenBinding([]string{path}, "worker", "/repo", "thread-one", false, false, true)
	if err == nil || !strings.Contains(err.Error(), "retired (archived)") {
		t.Fatalf("conflict=%v, want archived owner", err)
	}
}

func TestRenameConflictTreatsArchivedRowsAsReserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.json")
	writeBookFile(t, path, map[string]any{"agents": []any{
		map[string]any{"name": "old"},
		map[string]any{"name": "new", "archivedAt": "2026-01-01"},
	}})
	if err := RenameConflict([]string{path}, "old", "new"); err == nil || !strings.Contains(err.Error(), "archived record") {
		t.Fatalf("conflict=%v, want archived namespace refusal", err)
	}
}

func TestReconcileClosedUsesSessionProcessAndOpeningGrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.json")
	now := time.Now().UTC()
	writeBookFile(t, path, map[string]any{"agents": []any{
		map[string]any{"name": "stale-open", "status": "open"},
		map[string]any{"name": "stale-opening", "status": "opening", "statusAt": now.Add(-3 * time.Minute).Format(time.RFC3339Nano)},
		map[string]any{"name": "fresh-opening", "status": "opening", "statusAt": now.Format(time.RFC3339Nano)},
		map[string]any{"name": "live-session", "status": "open"},
		map[string]any{"name": "live-process", "status": "open", "localRuntime": map[string]any{"pid": os.Getpid()}},
	}})
	changes, err := ReconcileClosed([]string{path}, map[string]bool{"live-session": true}, func(pid int) bool { return pid == os.Getpid() }, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes=%v, want only stale open/opening rows", changes)
	}
	file, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, agent := range file.Agents {
		statuses[agent.Name] = agent.Status
	}
	for name, want := range map[string]string{
		"stale-open": "closed", "stale-opening": "closed", "fresh-opening": "opening",
		"live-session": "open", "live-process": "open",
	} {
		if statuses[name] != want {
			t.Errorf("%s status=%q, want %q", name, statuses[name], want)
		}
	}
}
