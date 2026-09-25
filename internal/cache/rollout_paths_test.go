package cache

import (
	"os"
	"path/filepath"
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
