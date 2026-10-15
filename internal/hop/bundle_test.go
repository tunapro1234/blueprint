package hop

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func fixedClock() func() time.Time {
	t := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWriteReadRoundTripWithFiles(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"main.go":        "package main",
		"sub/util.go":    "package sub",
		"data/notes.txt": "hello",
	})
	transcript := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	manifest, err := Write(&buf, WriteOptions{
		Agent:        AgentSpec{Name: "worker-1", Harness: "claude", ThreadID: "t1", SessionID: "sess", FolderBase: "proj"},
		Folder:       src,
		IncludeFiles: true,
		Transcripts:  []TranscriptFile{{AbsPath: transcript, Name: "sess.jsonl"}},
		Source:       Origin{Host: "server", Agent: "worker-1"},
		Clock:        fixedClock(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.TotalBytes == 0 || len(manifest.Files) != 4 {
		t.Fatalf("manifest: total=%d files=%d", manifest.TotalBytes, len(manifest.Files))
	}

	dest := t.TempDir()
	got, err := Read(bytes.NewReader(buf.Bytes()), dest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent.Name != "worker-1" || got.Agent.Harness != "claude" || got.Agent.ThreadID != "t1" {
		t.Fatalf("agent spec lost: %+v", got.Agent)
	}
	for rel, want := range map[string]string{
		"work/main.go":        "package main",
		"work/sub/util.go":    "package sub",
		"work/data/notes.txt": "hello",
		"transcript/sess.jsonl": `{"type":"user"}`,
	} {
		b, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
		if string(b) != want {
			t.Fatalf("%s = %q want %q", rel, b, want)
		}
	}
}

func TestWriteNoFilesCarriesOnlyTranscript(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{"main.go": "x"})
	transcript := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(transcript, []byte("{}"), 0o600)

	var buf bytes.Buffer
	m, err := Write(&buf, WriteOptions{
		Agent:        AgentSpec{Name: "a", Harness: "claude"},
		Folder:       src,
		IncludeFiles: false,
		Transcripts:  []TranscriptFile{{AbsPath: transcript, Name: "s.jsonl"}},
		Clock:        fixedClock(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 1 || m.Files[0].Path != "transcript/s.jsonl" {
		t.Fatalf("expected only transcript, got %+v", m.Files)
	}
	if m.IncludeFiles {
		t.Fatal("IncludeFiles should be false")
	}
}

func TestGitEnumerationExcludesIgnoredAndGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	src := t.TempDir()
	writeTree(t, src, map[string]string{
		"keep.go":     "a",
		"build/out.o": "binary",
		".gitignore":  "build/\nsecret.txt\n",
		"secret.txt":  "nope",
		"untracked.md": "new",
	})
	for _, args := range [][]string{{"init", "-q"}, {"add", "keep.go", ".gitignore"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", src}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}

	rels, err := enumerate(src, false)
	if err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, r := range rels {
		has[r] = true
	}
	if !has["keep.go"] || !has["untracked.md"] || !has[".gitignore"] {
		t.Fatalf("expected tracked+untracked-not-ignored, got %v", rels)
	}
	if has["build/out.o"] || has["secret.txt"] {
		t.Fatalf("ignored files leaked: %v", rels)
	}
	for _, r := range rels {
		if r == ".git" || len(r) >= 5 && r[:5] == ".git/" {
			t.Fatalf(".git leaked: %v", rels)
		}
	}

	raw, err := enumerate(src, true)
	if err != nil {
		t.Fatal(err)
	}
	rawHas := map[string]bool{}
	for _, r := range raw {
		rawHas[r] = true
	}
	if !rawHas["build/out.o"] || !rawHas["secret.txt"] {
		t.Fatalf("raw should include ignored files, got %v", raw)
	}
}

func TestReadRejectsSizeCap(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{"big.bin": "0123456789"})
	var buf bytes.Buffer
	if _, err := Write(&buf, WriteOptions{Agent: AgentSpec{Name: "a", Harness: "claude"}, Folder: src, IncludeFiles: true, Clock: fixedClock()}); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(bytes.NewReader(buf.Bytes()), t.TempDir(), 5); err == nil {
		t.Fatal("expected size-cap rejection")
	}
}

func TestReadRejectsTraversalAndUndeclared(t *testing.T) {
	// A hand-crafted hostile bundle: manifest is clean but a tar entry tries to
	// escape, and a second one is absent from the manifest.
	mk := func(entries []tar.Header, bodies []string, manifest Manifest) []byte {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		mb, _ := json.Marshal(manifest)
		tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(mb)), Typeflag: tar.TypeReg})
		tw.Write(mb)
		for i, h := range entries {
			tw.WriteHeader(&h)
			tw.Write([]byte(bodies[i]))
		}
		tw.Close()
		gz.Close()
		return buf.Bytes()
	}

	// Entry name not under a payload root / contains .. — Read must refuse.
	bundle := mk(
		[]tar.Header{{Name: "work/../../etc/x", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg}},
		[]string{"y"},
		Manifest{Format: Format, Agent: AgentSpec{Name: "a", Harness: "claude"}, TotalBytes: 0, Files: nil},
	)
	if _, err := Read(bytes.NewReader(bundle), t.TempDir(), 0); err == nil {
		t.Fatal("expected traversal rejection")
	}

	// Symlink entry must be refused as not-a-regular-file.
	sym := mk(
		[]tar.Header{{Name: "work/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}},
		[]string{""},
		Manifest{Format: Format, Agent: AgentSpec{Name: "a", Harness: "claude"}},
	)
	if _, err := Read(bytes.NewReader(sym), t.TempDir(), 0); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestManifestValidate(t *testing.T) {
	cases := []struct {
		name string
		m    Manifest
		ok   bool
	}{
		{"good", Manifest{Format: Format, Agent: AgentSpec{Name: "a", Harness: "claude"}, TotalBytes: 3, Files: []FileEntry{{Path: "work/a", Size: 3}}}, true},
		{"bad-format", Manifest{Format: 99, Agent: AgentSpec{Name: "a", Harness: "claude"}}, false},
		{"bad-name", Manifest{Format: Format, Agent: AgentSpec{Name: "a/b", Harness: "claude"}}, false},
		{"no-harness", Manifest{Format: Format, Agent: AgentSpec{Name: "a"}}, false},
		{"sum-mismatch", Manifest{Format: Format, Agent: AgentSpec{Name: "a", Harness: "claude"}, TotalBytes: 2, Files: []FileEntry{{Path: "work/a", Size: 3}}}, false},
		{"bad-path", Manifest{Format: Format, Agent: AgentSpec{Name: "a", Harness: "claude"}, Files: []FileEntry{{Path: "../x"}}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.m.Validate()
			if (err == nil) != c.ok {
				t.Fatalf("Validate=%v want ok=%v", err, c.ok)
			}
		})
	}
}

func TestWriteSkipsSymlinks(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, map[string]string{"real.txt": "data"})
	if err := os.Symlink("real.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Skip("symlinks unsupported")
	}
	var buf bytes.Buffer
	m, err := Write(&buf, WriteOptions{Agent: AgentSpec{Name: "a", Harness: "claude"}, Folder: src, IncludeFiles: true, Raw: true, Clock: fixedClock()})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range m.Files {
		if f.Path == "work/link.txt" {
			t.Fatal("symlink should not be packed")
		}
	}
}
