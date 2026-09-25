package cache

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCodexRolloutPathsFollowsMovedRollout(t *testing.T) {
	home := t.TempDir()
	id := "019a0000-0000-7000-8000-00000000cafe"
	write := func(day string) string {
		path := filepath.Join(home, "sessions", "2026", "09", day, "rollout-x-"+id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	first := write("24")
	if got := RolloutPathsByID(home, id); len(got) != 1 || got[0] != first {
		t.Fatalf("first lookup = %v", got)
	}
	if got := RolloutPathsByID(home, id); len(got) != 1 || got[0] != first {
		t.Fatalf("cached lookup = %v", got)
	}
	second := write("25")
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	if got := RolloutPathsByID(home, id); len(got) != 1 || got[0] != second {
		t.Fatalf("lookup after move = %v", got)
	}
	if got := RolloutPathsByID(home, id); len(got) != 1 || got[0] != second {
		t.Fatalf("cached lookup after move = %v", got)
	}
	write("26")
	if got := RolloutPathsByID(home, id); len(got) != 2 {
		t.Fatalf("second copy hidden by the cache: %v", got)
	}
}

func TestRolloutPathsByIDSeesNewFilesAtOnce(t *testing.T) {
	home := t.TempDir()
	id := "019a0000-0000-7000-8000-00000000beef"
	day := filepath.Join(home, "sessions", "2026", "09", "25")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := RolloutPathsByID(home, id); len(got) != 0 {
		t.Fatalf("miss = %v", got)
	}
	path := filepath.Join(day, "rollout-y-"+id+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := RolloutPathsByID(home, id); len(got) != 1 || got[0] != path {
		t.Fatalf("new rollout = %v", got)
	}
	copyPath := filepath.Join(day, "copy-"+id+".jsonl")
	if err := os.WriteFile(copyPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := RolloutPathsByID(home, id); len(got) != 2 {
		t.Fatalf("duplicate hidden by the cache: %v", got)
	}
}

// Settled directories are served from the index; a directory modified within
// the racy window of the lookup is globbed again.
func TestRolloutPathsByIDReusesOnlySettledDirectories(t *testing.T) {
	home := t.TempDir()
	id := "019a0000-0000-7000-8000-00000000f00d"
	day := filepath.Join(home, "sessions", "2026", "09", "25")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(day, "rollout-z-"+id+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	settle := func() {
		t.Helper()
		for _, dir := range []string{day, filepath.Dir(day), filepath.Dir(filepath.Dir(day)), filepath.Join(home, "sessions")} {
			if err := os.Chtimes(dir, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	settle()
	if got := RolloutPathsByID(home, id); len(got) != 1 {
		t.Fatalf("settled lookup = %v", got)
	}
	// A copy whose directory mtime is forced back to the cached value is
	// invisible exactly when the answer is being reused.
	copyPath := filepath.Join(day, "copy-"+id+".jsonl")
	if err := os.WriteFile(copyPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settle()
	if got := RolloutPathsByID(home, id); len(got) != 1 {
		t.Fatalf("settled answer was not reused: %v", got)
	}
	// Same trick with a fresh mtime that is still equal between the two
	// calls: the racy window forces a new glob.
	fresh := time.Now()
	if err := os.Chtimes(day, fresh, fresh); err != nil {
		t.Fatal(err)
	}
	if got := RolloutPathsByID(home, id); len(got) != 2 {
		t.Fatalf("fresh lookup = %v", got)
	}
	if err := os.Remove(copyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(day, fresh, fresh); err != nil {
		t.Fatal(err)
	}
	if got := RolloutPathsByID(home, id); len(got) != 1 {
		t.Fatalf("racy answer was reused: %v", got)
	}
}

func TestRolloutPathsByIDMatchesGlobAcrossGeneratedTree(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "sessions")
	ids := []string{"019a0000-0000-7000-8000-00000000cafe", "thread-with-hyphens", "missing", "wild*card"}
	entries := []struct {
		day  string
		name string
		dir  bool
	}{
		{"2024/12/31", "rollout-a-" + ids[0] + ".jsonl", false},
		{"2025/01/01", "copy-" + ids[0] + ".jsonl", false}, // Duplicate id in another day.
		{"2025/01/02", "rollout-" + ids[1] + ".jsonl", false},
		{"2025/01/02", "rollout-" + ids[0] + ".txt", false},    // Non-jsonl suffix.
		{"2025/01/02", "directory-" + ids[0] + ".jsonl", true}, // Glob includes matching directories.
		{"2025/01/02", "rollout-wild-X-card.jsonl", false},
		{"2026/02/03", "unrelated.jsonl", false},
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.day, entry.name)
		if entry.dir {
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("not opened by the index\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		want, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "*-"+id+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if got := RolloutPathsByID(home, id); !reflect.DeepEqual(got, want) {
			t.Fatalf("id %q index=%v glob=%v", id, got, want)
		}
	}
}
