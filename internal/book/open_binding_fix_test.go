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

	err := CheckOpenBinding([]string{path}, "worker", "/repo/new", "thread-new", false, false, false, false)
	if err == nil || !strings.Contains(err.Error(), "thread-old") || !strings.Contains(err.Error(), "--rebind") {
		t.Fatalf("conflict=%v, want old/new binding and --rebind guidance", err)
	}
	if err := CheckOpenBinding([]string{path}, "worker", "/repo/new", "thread-new", false, false, true, false); err != nil {
		t.Fatalf("explicit rebind rejected: %v", err)
	}
}

func TestOpenBindingIgnoresArchivedThreadClaimEvenWithRebind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.json")
	writeBookFile(t, path, map[string]any{"agents": []any{
		map[string]any{"name": "worker", "folder": "/repo", "status": "closed"},
		map[string]any{"name": "retired", "folder": "/old", "archivedAt": "2026-01-01", "nativeTitle": map[string]any{"threadId": "thread-one"}},
	}})
	err := CheckOpenBinding([]string{path}, "worker", "/repo", "thread-one", false, false, true, false)
	if err != nil {
		t.Fatalf("archived owner should not block: %v", err)
	}
}

func TestOpenBindingArchivedThreadDoesNotBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.json")
	writeBookFile(t, path, map[string]any{"agents": []any{
		map[string]any{"name": "airpods", "folder": "/repo", "status": "closed"},
		map[string]any{"name": "retired", "folder": "/old", "status": "closed", "archivedAt": "2026-01-01", "nativeTitle": map[string]any{"threadId": "thread-one"}},
	}})
	if err := CheckOpenBinding([]string{path}, "airpods", "/repo", "thread-one", false, false, false, false); err != nil {
		t.Fatalf("archived thread owner blocked a new binding: %v", err)
	}
}

func TestOpenBindingClosedThreadListsEveryHolderAndAdoptHint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.json")
	writeBookFile(t, path, map[string]any{"agents": []any{
		map[string]any{"name": "airpods", "folder": "/repo", "status": "closed"},
		map[string]any{"name": "claude-tuna-8da88b", "folder": "/repo", "status": "closed", "identityThreadId": "thread-one"},
		map[string]any{"name": "claude-tuna-a360d0", "folder": "/old", "status": "closed", "archivedAt": "2026-01-01", "launch": map[string]any{"resumeId": "thread-one"}},
	}})
	err := CheckOpenBinding([]string{path}, "airpods", "/repo", "thread-one", false, false, false, false)
	if err == nil {
		t.Fatal("closed thread holder was ignored")
	}
	for _, want := range []string{"claude-tuna-8da88b", "closed", path, "claude-tuna-a360d0", "archived", "--adopt"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("conflict %q does not contain %q", err, want)
		}
	}
}

func TestAdoptThreadMovesClosedAndArchivedReferencesWithoutDeletingRows(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "main.json")
	second := filepath.Join(root, "workers.json")
	writeBookFile(t, first, map[string]any{"agents": []any{
		map[string]any{"name": "airpods", "folder": "/repo", "status": "closed"},
		map[string]any{"name": "claude-tuna-8da88b", "folder": "/repo", "status": "closed", "identityThreadId": "thread-one"},
	}})
	writeBookFile(t, second, map[string]any{"agents": []any{
		map[string]any{"name": "claude-tuna-a360d0", "folder": "/old", "status": "closed", "archivedAt": "2026-01-01", "launch": map[string]any{"resume": true, "resumeId": "thread-one"}},
	}})
	moved, err := AdoptThread([]string{first, second}, "airpods", "thread-one", func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 2 || moved[0].Name != "claude-tuna-8da88b" || moved[1].Name != "claude-tuna-a360d0" {
		t.Fatalf("moved bindings = %+v", moved)
	}
	for _, path := range []string{first, second} {
		file, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if (path == first && len(file.Agents) != 2) || (path == second && len(file.Agents) != 1) {
			t.Fatalf("agent rows were deleted from %s: %+v", path, file.Agents)
		}
	}
	bindings, err := ThreadBindings([]string{first, second}, "thread-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 0 {
		t.Fatalf("old thread references remain: %+v", bindings)
	}
	file, err := Load(second)
	if err != nil {
		t.Fatal(err)
	}
	if file.Agents[0].ArchivedAt == "" || file.Agents[0].Status != "closed" {
		t.Fatalf("adoption changed archived/status state: %+v", file.Agents[0])
	}
}

func TestAdoptThreadRefusesLiveHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.json")
	writeBookFile(t, path, map[string]any{"agents": []any{
		map[string]any{"name": "airpods", "folder": "/repo", "status": "closed"},
		map[string]any{"name": "live-holder", "folder": "/repo", "status": "open", "identityThreadId": "thread-one"},
	}})
	_, err := AdoptThread([]string{path}, "airpods", "thread-one", func(name string) bool { return name == "live-holder" })
	if err == nil || !strings.Contains(err.Error(), "bp attach live-holder") {
		t.Fatalf("live thread conflict = %v", err)
	}
	bindings, err := ThreadBindings([]string{path}, "thread-one")
	if err != nil || len(bindings) != 1 || bindings[0].Name != "live-holder" {
		t.Fatalf("live holder changed after failed adoption: %+v, %v", bindings, err)
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
