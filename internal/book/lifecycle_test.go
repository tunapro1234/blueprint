package book

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/cache"
)

func TestLifetimeDefaultsPreserveLegacyAndExplicitRunIntent(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "book.json")
	if err := os.WriteFile(path, []byte(`{"orchestrator":"main","agents":[{"name":"main"},{"name":"legacy","status":"closed"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetStatus([]string{path}, "legacy", "open", "/work", Registration{Lifetime: LifetimeEphemeral}); err != nil {
		t.Fatal(err)
	}
	file, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Agents[1].Lifetime != "" || file.Agents[1].IsEphemeral() {
		t.Fatalf("ordinary relaunch changed legacy lifetime: %+v", file.Agents[1])
	}
	if err := SetStatus([]string{path}, "fresh", "open", "/work", Registration{Lifetime: LifetimeEphemeral}); err != nil {
		t.Fatal(err)
	}
	if err := SetStatus([]string{path}, "legacy", "open", "/work", Registration{Lifetime: LifetimeEphemeral, UpdateLifetime: true}); err != nil {
		t.Fatal(err)
	}
	file, _ = Load(path)
	if file.Agents[1].Lifetime != LifetimeEphemeral || file.Agents[2].Lifetime != LifetimeEphemeral {
		t.Fatalf("explicit/new lifetimes not recorded: %+v", file.Agents)
	}
}

func TestSetLifetimeMutatesActiveRegistrationWhenArchivedDuplicateAppearsFirst(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	root := t.TempDir()
	archivedPath := filepath.Join(root, "archived.json")
	activePath := filepath.Join(root, "active.json")
	for path, data := range map[string]string{
		archivedPath: `{"orchestrator":"main","agents":[{"name":"main"},{"name":"worker","archivedAt":"2026-09-01T00:00:00Z","lifetime":"ephemeral"}]}`,
		activePath:   `{"orchestrator":"main","agents":[{"name":"main"},{"name":"worker","status":"open"}]}`,
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetLifetime([]string{archivedPath, activePath}, "worker", LifetimePersistent); err != nil {
		t.Fatal(err)
	}
	archived, err := Load(archivedPath)
	if err != nil {
		t.Fatal(err)
	}
	active, err := Load(activePath)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Agents[1].Lifetime != LifetimeEphemeral {
		t.Fatalf("archived registration was mutated: %+v", archived.Agents[1])
	}
	if active.Agents[1].Lifetime != LifetimePersistent {
		t.Fatalf("active registration was not mutated: %+v", active.Agents[1])
	}
}

func TestArchiveClosedEphemeralsUsesChildRefusalAndRecordsReason(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "book.json")
	data := `{"orchestrator":"main","agents":[{"name":"main"},{"name":"free","status":"closed","lifetime":"ephemeral"},{"name":"parent","status":"closed","lifetime":"ephemeral"},{"name":"child","parent":"parent","status":"open","lifetime":"persistent"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := ArchiveClosedEphemerals([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(results, "\n"); !strings.Contains(got, "free: archived") || !strings.Contains(got, "parent: automatic archive skipped") || !strings.Contains(got, "still has child child") {
		t.Fatalf("results=%q", got)
	}
	file, _ := Load(path)
	byName := map[string]Agent{}
	for _, agent := range file.Agents {
		byName[agent.Name] = agent
	}
	if byName["free"].ArchivedAt == "" {
		t.Fatal("unblocked ephemeral record was not archived")
	}
	if byName["parent"].ArchivedAt != "" || !strings.Contains(byName["parent"].LifecycleNote, "still has child child") {
		t.Fatalf("blocked parent=%+v", byName["parent"])
	}
}

func TestTranscriptMissingRequiresEvidence(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing.jsonl")
	agent := Agent{Name: "old", NativeTitle: &NativeTitle{ThreadID: "thread", Path: missing}}
	if !TranscriptMissing(agent) {
		t.Fatal("missing bound transcript was not detected")
	}
	if TranscriptMissing(Agent{Name: "unknown"}) {
		t.Fatal("unknown transcript binding was treated as missing")
	}
	if TranscriptMissing(Agent{Name: "empty", NativeTitle: &NativeTitle{}}) {
		t.Fatal("empty native-title metadata was treated as a missing transcript")
	}
	if err := os.WriteFile(missing, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if TranscriptMissing(agent) {
		t.Fatal("existing transcript was treated as missing")
	}

	observation := filepath.Join(root, "observation.json")
	data, _ := json.Marshal(map[string]any{"transcript_path": filepath.Join(root, "gone.jsonl")})
	if err := os.WriteFile(observation, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if !TranscriptMissing(Agent{Local: &cache.LocalBinding{Path: observation}}) {
		t.Fatal("missing transcript from local observation was not detected")
	}

	codexHome := filepath.Join(root, "codex")
	codexIndex := filepath.Join(codexHome, "session_index.jsonl")
	codexAgent := Agent{Name: "codex", NativeTitle: &NativeTitle{ThreadID: "thread-id", Path: codexIndex}}
	if !TranscriptMissing(codexAgent) {
		t.Fatal("Codex binding with no rollout was not detected as missing")
	}
	rolloutDir := filepath.Join(codexHome, "sessions", "2026", "09", "24")
	if err := os.MkdirAll(rolloutDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rolloutDir, "rollout-thread-id.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if TranscriptMissing(codexAgent) {
		t.Fatal("existing Codex rollout was treated as missing")
	}
}
