package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/projectschema"
	bptmux "blueprint/internal/tmux"
)

func writePortableSchema(t *testing.T, root, runtime string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, projectschema.Directory), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`version: 1
history: file
agents:
  - name: worker
    folder: .
    runtime: ` + runtime + `
    role: worker
    color: blue
`)
	if err := os.WriteFile(projectschema.Path(root), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeHistoryMovesWithProjectAndImportsToNativePath(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	base := t.TempDir()
	source := filepath.Join(base, "a", "proj")
	destination := filepath.Join(base, "b", "proj")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(destination, projectschema.Directory), 0o755); err != nil {
		t.Fatal(err)
	}
	writePortableSchema(t, source, "claude")
	id := "12345678-1234-4234-8234-123456789abc"
	transcript := filepath.Join(bptmux.ClaudeProjectDir(source), id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("{\"type\":\"user\",\"message\":{\"content\":\"portable turn\"}}\n")
	if err := os.WriteFile(transcript, content, 0o600); err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(t.TempDir(), "agentbook.json")
	writeSchemaBook(t, bookPath, "worker", []book.Agent{{Name: "worker", Folder: source, Status: "closed", Launch: &bptmux.OpenOptions{Resume: true, ResumeID: id}}})
	a := &app{config: bpconfig.Config{Agentbooks: []string{bookPath}}, out: schemaOutput(t)}
	if err := a.exportHistory(historyExportOptions{Project: source, Keep: 1}); err != nil {
		t.Fatal(err)
	}
	portable := filepath.Join(source, projectschema.Directory, "history", "worker", "claude", id+".jsonl")
	if data, err := os.ReadFile(portable); err != nil || !bytes.Equal(data, content) {
		t.Fatalf("portable Claude transcript = %q, %v", data, err)
	}
	if err := os.WriteFile(projectschema.Path(destination), mustReadFile(t, projectschema.Path(source)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(destination, projectschema.Directory, "history", "worker", "claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, projectschema.Directory, "history", "worker", "claude", id+".jsonl"), mustReadFile(t, portable), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := projectschema.Load(destination)
	if err != nil {
		t.Fatal(err)
	}
	resume, err := importProjectHistory(filepath.Join(destination, projectschema.Directory, "history"), loaded)
	if err != nil || resume["worker"] != id {
		t.Fatalf("import result = %v, %v", resume, err)
	}
	nativePath := filepath.Join(bptmux.ClaudeProjectDir(destination), id+".jsonl")
	if data, err := os.ReadFile(nativePath); err != nil || !bytes.Equal(data, content) {
		t.Fatalf("moved Claude transcript = %q, %v; wanted %s", data, err, nativePath)
	}
}

func TestCodexHistoryMovesRolloutAndSessionIndex(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	sourceProject := filepath.Join(t.TempDir(), "a", "proj")
	destination := filepath.Join(filepath.Dir(filepath.Dir(sourceProject)), "b", "proj")
	if err := os.MkdirAll(sourceProject, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(destination, projectschema.Directory), 0o755); err != nil {
		t.Fatal(err)
	}
	writePortableSchema(t, sourceProject, "codex")
	const id = "0190aabb-ccdd-7eef-8123-456789abcdef"
	sourceHome := filepath.Join(home, "codex-source")
	destinationHome := filepath.Join(home, "codex-destination")
	relative := filepath.Join("2026", "09", "24", "rollout-"+id+".jsonl")
	sourceRollout := filepath.Join(sourceHome, "sessions", relative)
	if err := os.MkdirAll(filepath.Dir(sourceRollout), 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"" + id + "\",\"cwd\":\"" + sourceProject + "\"}}\n{\"type\":\"response_item\"}\n")
	if err := os.WriteFile(sourceRollout, rollout, 0o600); err != nil {
		t.Fatal(err)
	}
	indexRow := []byte("{\"id\":\"" + id + "\",\"thread_name\":\"worker\"}\n")
	if err := os.WriteFile(filepath.Join(sourceHome, "session_index.jsonl"), indexRow, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", sourceHome)
	agent := book.Agent{Name: "worker", Folder: sourceProject, Launch: &bptmux.OpenOptions{Codex: true, Resume: true, ResumeID: id}}
	entries, err := codexHistoryEntries(agent, 1)
	if err != nil || len(entries) != 2 {
		t.Fatalf("Codex history entries = %d, %v", len(entries), err)
	}
	portableRoot := filepath.Join(sourceProject, projectschema.Directory, "history")
	for _, entry := range entries {
		path := filepath.Join(portableRoot, filepath.FromSlash(strings.TrimPrefix(entry.Name, "history/")))
		if err := writeNoConflict(path, entry.Data, entry.Mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(projectschema.Path(destination), mustReadFile(t, projectschema.Path(sourceProject)), 0o600); err != nil {
		t.Fatal(err)
	}
	destPortable := filepath.Join(destination, projectschema.Directory, "history")
	if err := copyTestTree(portableRoot, destPortable); err != nil {
		t.Fatal(err)
	}
	loaded, err := projectschema.Load(destination)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", destinationHome)
	resume, err := importProjectHistory(destPortable, loaded)
	if err != nil || resume["worker"] != id {
		t.Fatalf("Codex import result = %v, %v", resume, err)
	}
	destRollout := filepath.Join(destinationHome, "sessions", relative)
	data, err := os.ReadFile(destRollout)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(bytes.SplitN(data, []byte("\n"), 2)[0], &row); err != nil {
		t.Fatal(err)
	}
	payload := row["payload"].(map[string]any)
	if payload["cwd"] != destination {
		t.Fatalf("imported rollout cwd = %v, want %s", payload["cwd"], destination)
	}
	if got, err := os.ReadFile(filepath.Join(destinationHome, "session_index.jsonl")); err != nil || !bytes.Equal(got, indexRow) {
		t.Fatalf("imported session index = %q, %v", got, err)
	}
}

func TestHistoryExportStdoutWritesTarAndNeverOverwritesDifferentContent(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	folder := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "12345678-1234-4234-8234-123456789abc"
	transcript := filepath.Join(bptmux.ClaudeProjectDir(folder), id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("portable\n")
	if err := os.WriteFile(transcript, content, 0o600); err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(t.TempDir(), "agentbook.json")
	writeSchemaBook(t, bookPath, "worker", []book.Agent{{Name: "worker", Folder: folder, Launch: &bptmux.OpenOptions{ResumeID: id}}})
	stdout, stderr := schemaOutput(t), schemaOutput(t)
	a := &app{config: bpconfig.Config{Agentbooks: []string{bookPath}}, out: stdout, err: stderr}
	if err := a.exportHistory(historyExportOptions{Agent: "worker", Keep: 1, Stdout: true}); err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	header, err := reader.Next()
	if err != nil || header.Name != "history/worker/claude/"+id+".jsonl" {
		t.Fatalf("tar header = %#v, %v", header, err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("tar content = %q, %v", got, err)
	}

	conflict := filepath.Join(t.TempDir(), "same.jsonl")
	if err := os.WriteFile(conflict, []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeNoConflict(conflict, []byte("newer"), 0o600); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("different content overwrite error = %v", err)
	}
}

func TestPrunePortableHistoryKeepsNewestSessionsPerRuntime(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	for _, runtime := range []string{"claude", filepath.Join("codex", "sessions")} {
		directory := filepath.Join(root, "worker", runtime)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 3; index++ {
			path := filepath.Join(directory, string(rune('a'+index))+".jsonl")
			if err := os.WriteFile(path, []byte("session"), 0o600); err != nil {
				t.Fatal(err)
			}
			modified := base.Add(time.Duration(index) * time.Hour)
			if err := os.Chtimes(path, modified, modified); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := prunePortableHistory(root, []string{"worker"}, 2); err != nil {
		t.Fatal(err)
	}
	for _, runtime := range []string{"claude", filepath.Join("codex", "sessions")} {
		directory := filepath.Join(root, "worker", runtime)
		if _, err := os.Stat(filepath.Join(directory, "a.jsonl")); !os.IsNotExist(err) {
			t.Errorf("oldest %s session remains: %v", runtime, err)
		}
		for _, name := range []string{"b.jsonl", "c.jsonl"} {
			if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
				t.Errorf("newest %s session %s was pruned: %v", runtime, name, err)
			}
		}
	}
}

func TestRemoteHistoryUsesSSHWithRegistryTransportSettings(t *testing.T) {
	remotes := map[string]bpconfig.RemoteConfig{
		"server": {Host: "history.example", Port: 2222, User: "tuna", Identity: "/keys/server", Transport: "mosh", Elevate: "sudo -i"},
	}
	endpoint, err := resolveHistoryRemote("server", remotes)
	if err != nil {
		t.Fatal(err)
	}
	command, err := remoteHistoryCommand(endpoint, "worker", fixedLookup)
	want := []string{"ssh", "-p", "2222", "-i", "/keys/server", "tuna@history.example", "sudo", "-i", "bp", "history", "export", "--stdout", "--agent", "worker"}
	if err != nil || strings.Join(command.Args, " ") != strings.Join(want, " ") || command.Path != "/usr/bin/ssh" {
		t.Fatalf("remote command = %#v, %v; want %v", command, err, want)
	}
	parsed, err := resolveHistoryRemote("ssh://tuna@history.example:2200", nil)
	if err != nil || parsed.User != "tuna" || parsed.Host != "history.example" || parsed.Port != "2200" {
		t.Fatalf("parsed endpoint = %#v, %v", parsed, err)
	}
	for _, invalid := range []string{"ssh://user:password@host", "ssh://bad;user@host", "ssh://user@-host"} {
		if _, err := resolveHistoryRemote(invalid, nil); err == nil {
			t.Errorf("invalid remote accepted: %q", invalid)
		}
	}
}

func TestExtractHistoryTarRejectsTraversal(t *testing.T) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "history/../../outside", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractHistoryTar(bytes.NewReader(buffer.Bytes()), t.TempDir()); err == nil {
		t.Fatal("path traversal entry was accepted")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func copyTestTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}
