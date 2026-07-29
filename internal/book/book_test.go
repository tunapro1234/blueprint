package book

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSetStatusNoopAndPreservesFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbook.json")
	original := []byte("{\n  \"updated\": \"hand-edited\",\n  \"agents\": [\n    {\"name\":\"ada\",\"folder\":\"/srv/ada\",\"status\":\"open\",\"parent\":\"manual-parent\",\"role\":\"manual role\",\"extra\":\"keep\"}\n  ]\n}\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	if err := SetStatus([]string{path}, "ada", "open", "/srv/ada", "ignored"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("no-op changed file bytes:\n%s", after)
	}

	if err := SetStatus([]string{path}, "ada", "closed", "", "ignored"); err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Agents []map[string]any `json:"agents"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	agent := raw.Agents[0]
	if agent["status"] != "closed" || agent["parent"] != "manual-parent" || agent["role"] != "manual role" || agent["extra"] != "keep" {
		t.Fatalf("updated agent lost fields: %#v", agent)
	}
}

func TestSetStatusNewRecordGetsParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbook.json")
	if err := os.WriteFile(path, []byte("{\"agents\":[]}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SetStatus([]string{path}, "new-agent", "open", "/srv/new", "ada"); err != nil {
		t.Fatal(err)
	}
	file, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Agents) != 1 || file.Agents[0].Parent != "ada" || file.Agents[0].Class != "other" {
		t.Fatalf("agents=%+v, want fallback parent ada and class other", file.Agents)
	}
}

func TestSetStatusInfersFromLongestFolderAndWritesMatchingBook(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.json")
	projectPath := filepath.Join(dir, "project.json")
	if err := os.WriteFile(mainPath, []byte(`{"agents":[{"name":"server-main","folder":"/srv","class":"server"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, []byte(`{"agents":[{"name":"probot-main","folder":"/srv/probot (home: /srv/probot/main)","class":"probot"}]}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SetStatus([]string{mainPath, projectPath}, "probot-outreach", "open", "/srv/probot/outreach", "caller"); err != nil {
		t.Fatal(err)
	}
	main, err := Load(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	project, err := Load(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(main.Agents) != 1 {
		t.Fatalf("main book got new record: %+v", main.Agents)
	}
	if len(project.Agents) != 2 {
		t.Fatalf("project agents=%+v, want new record", project.Agents)
	}
	got := project.Agents[1]
	if got.Parent != "probot-main" || got.Class != "probot" {
		t.Fatalf("new agent=%+v, want parent probot-main and class probot", got)
	}
}

func TestSetStatusFolderMatchStopsAtPathBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbook.json")
	data := []byte(`{"agents":[{"name":"kavram-main","folder":"/srv/kavram","class":"kavram"}]}`)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := SetStatus([]string{path}, "unrelated", "open", "/srv/kavram-old/work", "caller"); err != nil {
		t.Fatal(err)
	}
	file, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := file.Agents[1]
	if got.Parent != "caller" || got.Class != "other" {
		t.Fatalf("new agent=%+v, want fallback across path boundary", got)
	}
}

func TestAddLiveUsesNameInference(t *testing.T) {
	fleet := Fleet{
		Agents: map[string]Agent{
			"server-main": {Name: "server-main"},
			"kavram-main": {Name: "kavram-main", Nickname: "kavram", Class: "kavram"},
		},
		Order:   []string{"server-main", "kavram-main"},
		Parents: map[string]string{},
		Root:    "server-main",
	}
	fleet.AddLive([]string{"kavram-worker"})
	if fleet.Parents["kavram-worker"] != "kavram-main" || fleet.Agents["kavram-worker"].Class != "kavram" {
		t.Fatalf("agent=%+v parent=%q", fleet.Agents["kavram-worker"], fleet.Parents["kavram-worker"])
	}
}
