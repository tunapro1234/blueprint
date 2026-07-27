package tmux

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMungeProjectPath(t *testing.T) {
	cases := map[string]string{
		"/srv/probot-business":       "-srv-probot-business",
		"/srv/kitap/.worktrees/x":    "-srv-kitap--worktrees-x",
		"/srv/some_dir":              "-srv-some-dir",
		"/srv":                       "-srv",
		"/tmp/claude-0/-srv-x/scrat": "-tmp-claude-0--srv-x-scrat",
	}
	for in, want := range cases {
		if got := mungeProjectPath(in); got != want {
			t.Errorf("mungeProjectPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// writeSession creates a Claude-style session file with a line-1 custom-title
// record and stamps its mtime, returning the session id (file base name).
func writeSession(t *testing.T, projectDir, id, title string, mod time.Time) {
	t.Helper()
	path := filepath.Join(projectDir, id+".jsonl")
	body := `{"type":"custom-title","customTitle":"` + title + `","sessionId":"` + id + `"}` + "\n" +
		`{"type":"user","message":"hello"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestResumeSessionIDPicksNewestMatchingAgent(t *testing.T) {
	root := t.TempDir()
	dir := "/srv/shared-cwd"
	projectDir := filepath.Join(root, mungeProjectPath(dir))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	// A co-located agent's newer session must NOT win — the bug `claude -c` hit.
	writeSession(t, projectDir, "aaaa-other-newer", "op-main", base.Add(2*time.Hour))
	writeSession(t, projectDir, "bbbb-mine-old", "worker-1", base.Add(1*time.Hour))
	writeSession(t, projectDir, "cccc-mine-new", "worker-1", base.Add(90*time.Minute))

	id, ok := ResumeSessionID(root, dir, "worker-1")
	if !ok {
		t.Fatal("expected a match for worker-1")
	}
	if id != "cccc-mine-new" {
		t.Fatalf("resumeSessionID = %q, want cccc-mine-new (newest with our customTitle)", id)
	}
}

func TestResumeSessionIDNoMatchFallsThrough(t *testing.T) {
	root := t.TempDir()
	dir := "/srv/lonely"
	projectDir := filepath.Join(root, mungeProjectPath(dir))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSession(t, projectDir, "zzzz-someone-else", "different-agent", time.Now())

	if _, ok := ResumeSessionID(root, dir, "worker-1"); ok {
		t.Fatal("expected no match when no session carries our customTitle")
	}
	// Missing project dir entirely must also report no match, not error.
	if _, ok := ResumeSessionID(root, "/srv/does-not-exist", "worker-1"); ok {
		t.Fatal("expected no match for a cwd with no project dir")
	}
}

func TestReadCustomTitle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	body := `{"type":"summary","summary":"x"}` + "\n" +
		`{"type":"custom-title","customTitle":"probot-business","sessionId":"s"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	title, ok := ReadCustomTitle(path)
	if !ok || title != "probot-business" {
		t.Fatalf("readCustomTitle = (%q, %v), want (probot-business, true)", title, ok)
	}

	// A file without a custom-title record yields no title.
	plain := filepath.Join(dir, "p.jsonl")
	if err := os.WriteFile(plain, []byte(`{"type":"user","message":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadCustomTitle(plain); ok {
		t.Fatal("expected no title for a file without a custom-title record")
	}
}
