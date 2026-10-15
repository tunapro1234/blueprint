package hop

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// threeAgentTree builds root -> child -> grandchild, each with one work file
// and the root with a transcript, and returns the packed bundle bytes.
func threeAgentTree(t *testing.T) ([]byte, TreeManifest) {
	t.Helper()
	rootDir, childDir, grandDir := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, rootDir, map[string]string{"root.txt": "root files"})
	writeTree(t, childDir, map[string]string{"sub/child.txt": "child files"})
	writeTree(t, grandDir, map[string]string{"g.txt": "grand files"})
	transcript := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	manifest, err := WriteTree(&buf, TreeWriteOptions{
		Root: "lh-hack",
		Agents: []TreeWriteAgent{
			{Spec: AgentSpec{Name: "lh-hack", Harness: "claude", SessionID: "sess", FolderBase: "lh-hack"}, Folder: rootDir, IncludeFiles: true,
				Transcripts: []TranscriptFile{{AbsPath: transcript, Name: "sess.jsonl"}}},
			{Spec: AgentSpec{Name: "lh-hack-qloo", Harness: "claude", FolderBase: "qloo"}, Parent: "lh-hack", Folder: childDir, IncludeFiles: true},
			{Spec: AgentSpec{Name: "lh-hack-qloo-dev1", Harness: "codex", FolderBase: "dev1"}, Parent: "lh-hack-qloo", Folder: grandDir, IncludeFiles: true},
		},
		Source: Origin{Host: "server", Agent: "lh-hack"},
		Clock:  fixedClock(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), manifest
}

func TestWriteReadTreeRoundTrip(t *testing.T) {
	bundle, manifest := threeAgentTree(t)
	if manifest.Format != TreeFormat || manifest.Root != "lh-hack" || len(manifest.Agents) != 3 {
		t.Fatalf("manifest header wrong: %+v", manifest)
	}
	// 3 work files + 1 transcript.
	if len(manifest.Files) != 4 {
		t.Fatalf("expected 4 files, got %d: %+v", len(manifest.Files), manifest.Files)
	}
	// Hierarchy preserved.
	parent := map[string]string{}
	for _, a := range manifest.Agents {
		parent[a.Agent.Name] = a.Parent
	}
	if parent["lh-hack"] != "" || parent["lh-hack-qloo"] != "lh-hack" || parent["lh-hack-qloo-dev1"] != "lh-hack-qloo" {
		t.Fatalf("hierarchy lost: %+v", parent)
	}

	dest := t.TempDir()
	got, err := ReadTree(bytes.NewReader(bundle), dest, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Agents) != 3 {
		t.Fatalf("read agents=%d", len(got.Agents))
	}
	// Files landed under the per-agent namespace.
	for rel, want := range map[string]string{
		"agents/lh-hack/work/root.txt":           "root files",
		"agents/lh-hack/transcript/sess.jsonl":   `{"type":"user"}`,
		"agents/lh-hack-qloo/work/sub/child.txt": "child files",
		"agents/lh-hack-qloo-dev1/work/g.txt":    "grand files",
	} {
		data, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v; want %q", rel, data, err, want)
		}
	}
	// AgentFiles strips the namespace so the caller can place files relative to
	// the agent's target dir.
	qloo := got.AgentFiles("lh-hack-qloo", "work")
	if len(qloo) != 1 || qloo[0].Path != "sub/child.txt" {
		t.Fatalf("AgentFiles(qloo,work) = %+v", qloo)
	}
	if ts := got.AgentFiles("lh-hack", "transcript"); len(ts) != 1 || ts[0].Path != "sess.jsonl" {
		t.Fatalf("AgentFiles(root,transcript) = %+v", ts)
	}
}

func TestTreeManifestValidate(t *testing.T) {
	good := func() TreeManifest {
		return TreeManifest{
			Format: TreeFormat, Root: "r",
			Agents: []TreeAgent{
				{Agent: AgentSpec{Name: "r", Harness: "claude"}},
				{Agent: AgentSpec{Name: "c", Harness: "claude"}, Parent: "r"},
			},
			TotalBytes: 3,
			Files:      []FileEntry{{Path: "agents/c/work/a", Size: 3}},
		}
	}
	cases := []struct {
		name   string
		mutate func(*TreeManifest)
		ok     bool
	}{
		{"good", func(*TreeManifest) {}, true},
		{"bad-format", func(m *TreeManifest) { m.Format = 1 }, false},
		{"root-has-parent", func(m *TreeManifest) { m.Agents[0].Parent = "c" }, false},
		{"root-missing", func(m *TreeManifest) { m.Root = "x" }, false},
		{"parent-outside", func(m *TreeManifest) { m.Agents[1].Parent = "ghost" }, false},
		{"duplicate", func(m *TreeManifest) {
			m.Agents = append(m.Agents, TreeAgent{Agent: AgentSpec{Name: "c", Harness: "claude"}, Parent: "r"})
		}, false},
		{"self-parent", func(m *TreeManifest) { m.Agents[1].Parent = "c" }, false},
		{"file-undeclared-agent", func(m *TreeManifest) { m.Files[0].Path = "agents/ghost/work/a" }, false},
		{"sum-mismatch", func(m *TreeManifest) { m.TotalBytes = 99 }, false},
		{"bad-path", func(m *TreeManifest) { m.Files = []FileEntry{{Path: "agents/c/../x", Size: 3}} }, false},
		{"no-harness", func(m *TreeManifest) { m.Agents[1].Agent.Harness = "" }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := good()
			c.mutate(&m)
			err := m.Validate()
			if (err == nil) != c.ok {
				t.Fatalf("Validate=%v want ok=%v", err, c.ok)
			}
		})
	}
}

func TestTreeCycleRejected(t *testing.T) {
	// r -> a -> b -> a would never reach the root.
	m := TreeManifest{
		Format: TreeFormat, Root: "r",
		Agents: []TreeAgent{
			{Agent: AgentSpec{Name: "r", Harness: "claude"}},
			{Agent: AgentSpec{Name: "a", Harness: "claude"}, Parent: "b"},
			{Agent: AgentSpec{Name: "b", Harness: "claude"}, Parent: "a"},
		},
	}
	if err := m.Validate(); err == nil {
		t.Fatal("expected a cycle to be rejected")
	}
}

func TestReadTreeRejectsHostileEntries(t *testing.T) {
	mk := func(entries []tar.Header, bodies []string, manifest TreeManifest) []byte {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		mb, _ := json.Marshal(manifest)
		_ = tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(mb)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(mb)
		for i, h := range entries {
			h := h
			_ = tw.WriteHeader(&h)
			_, _ = tw.Write([]byte(bodies[i]))
		}
		_ = tw.Close()
		_ = gz.Close()
		return buf.Bytes()
	}
	base := TreeManifest{Format: TreeFormat, Root: "a", Agents: []TreeAgent{{Agent: AgentSpec{Name: "a", Harness: "claude"}}}}

	// Path escaping the agent namespace.
	esc := mk([]tar.Header{{Name: "agents/a/work/../../etc/x", Size: 1, Typeflag: tar.TypeReg}}, []string{"y"}, base)
	if _, err := ReadTree(bytes.NewReader(esc), t.TempDir(), 0); err == nil {
		t.Fatal("expected traversal rejection")
	}
	// Entry for an agent not declared in the manifest.
	undecl := mk([]tar.Header{{Name: "agents/ghost/work/x", Size: 1, Typeflag: tar.TypeReg}}, []string{"y"}, base)
	if _, err := ReadTree(bytes.NewReader(undecl), t.TempDir(), 0); err == nil {
		t.Fatal("expected undeclared-agent rejection")
	}
	// Symlink entry.
	sym := mk([]tar.Header{{Name: "agents/a/work/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}, []string{""}, base)
	if _, err := ReadTree(bytes.NewReader(sym), t.TempDir(), 0); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestReadTreeSizeCap(t *testing.T) {
	bundle, _ := threeAgentTree(t)
	if _, err := ReadTree(bytes.NewReader(bundle), t.TempDir(), 5); err == nil {
		t.Fatal("expected size-cap rejection")
	}
}
