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
	if err := SetStatus([]string{path}, "ada", "open", "/srv/ada", Registration{Sender: "ignored"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("no-op changed file bytes:\n%s", after)
	}

	if err := SetStatus([]string{path}, "ada", "closed", "", Registration{Sender: "ignored"}); err != nil {
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

// The status vocabulary is open: "opening" (a bp open in flight) has to round
// trip like any other, and a status this binary has never heard of must survive
// a write untouched — that is the same guarantee read from the other side, and it
// is what keeps a book written by a newer bp readable by an older one.
func TestSetStatusCarriesOpeningAndUnknownStatuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeBookFile(t, path, map[string]any{"agents": []any{
		map[string]any{"name": "ada", "folder": "/srv/ada", "status": "closed"},
		map[string]any{"name": "future", "folder": "/srv/future", "status": "hibernating"},
	}})

	if err := SetStatus([]string{path}, "ada", "opening", "/srv/ada", Registration{Sender: "server-main"}); err != nil {
		t.Fatal(err)
	}
	if got := findAgent(t, path, "ada"); got.Status != "opening" {
		t.Fatalf("ada=%+v, want status opening", got)
	}
	if got := findAgent(t, path, "future"); got.Status != "hibernating" {
		t.Fatalf("unknown status was rewritten: %+v", got)
	}

	// A second write settles it, so "opening" is a passing state and not a trap.
	if err := SetStatus([]string{path}, "ada", "open", "/srv/ada", Registration{Sender: "server-main"}); err != nil {
		t.Fatal(err)
	}
	fleet, err := LoadFleet([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if fleet.Agents["ada"].Status != "open" || fleet.Agents["future"].Status != "hibernating" {
		t.Fatalf("fleet=%+v, want ada open and future hibernating", fleet.Agents)
	}
}

func TestSetStatusNewRecordGetsParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbook.json")
	if err := os.WriteFile(path, []byte("{\"agents\":[]}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := SetStatus([]string{path}, "new-agent", "open", "/srv/new", Registration{Sender: "ada"}); err != nil {
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

	if err := SetStatus([]string{mainPath, projectPath}, "probot-outreach", "open", "/srv/probot/outreach", Registration{Sender: "caller"}); err != nil {
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
	if err := SetStatus([]string{path}, "unrelated", "open", "/srv/kavram-old/work", Registration{Sender: "caller"}); err != nil {
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

// setupBooks lays out the real fleet's shape inside a temp tree: a main book at
// <root>/server-main/agentbook.json and a project book at
// <root>/probot/.orchestration/agentbook.json that both list probot-main.
func setupBooks(t *testing.T) (root, mainPath, probotPath string) {
	t.Helper()
	root = t.TempDir()
	mainPath = filepath.Join(root, "server-main", "agentbook.json")
	probotPath = filepath.Join(root, "probot", ".orchestration", "agentbook.json")
	for _, path := range []string{mainPath, probotPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeBookFile(t, mainPath, map[string]any{"agents": []any{
		map[string]any{"name": "server-main", "folder": root, "class": "server"},
		map[string]any{"name": "probot-main", "folder": filepath.Join(root, "probot"), "class": "probot"},
	}})
	writeBookFile(t, probotPath, map[string]any{"agents": []any{
		map[string]any{"name": "probot-main", "folder": filepath.Join(root, "probot"), "class": "probot"},
		map[string]any{"name": "probot-business", "folder": filepath.Join(root, "probot", "business"), "class": "probot"},
	}})
	return root, mainPath, probotPath
}

func agentNames(t *testing.T, path string) []string {
	t.Helper()
	file, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(file.Agents))
	for _, agent := range file.Agents {
		names = append(names, agent.Name)
	}
	return names
}

func findAgent(t *testing.T, path, name string) Agent {
	t.Helper()
	file, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range file.Agents {
		if agent.Name == name {
			return agent
		}
	}
	t.Fatalf("%s holds no entry for %s (has %v)", path, name, agentNames(t, path))
	return Agent{}
}

// The true parent is not a path ancestor of the new folder, so only an explicit
// flag can express it: the pin beats the folder-inferred parent, while class and
// book placement still come from inference.
func TestSetStatusExplicitParentAndRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentbook.json")
	writeBookFile(t, path, map[string]any{"agents": []any{
		map[string]any{"name": "server-main", "folder": "/srv", "class": "server"},
		map[string]any{"name": "probot-main", "folder": "/srv/probot", "class": "probot"},
		map[string]any{"name": "probot-business", "folder": "/srv/probot/business", "class": "probot"},
	}})
	reg := Registration{Sender: "server-main", Parent: "probot-business", Role: "fon ekibi"}

	if err := SetStatus([]string{path}, "probot-fon", "open", "/srv/probot/fon", reg); err != nil {
		t.Fatal(err)
	}
	got := findAgent(t, path, "probot-fon")
	if got.Parent != "probot-business" || got.Role != "fon ekibi" || got.Class != "probot" {
		t.Fatalf("new agent=%+v, want pinned parent/role and inherited class", got)
	}
}

// An agent already registered in one book is updated there, never copied into
// another configured book — pins included.
func TestSetStatusExistingAgentStaysInItsOwnBook(t *testing.T) {
	root, mainPath, probotPath := setupBooks(t)
	folder := filepath.Join(root, "probot", "fon")
	writeBookFile(t, mainPath, map[string]any{"agents": []any{
		map[string]any{"name": "server-main", "folder": root, "class": "server"},
		map[string]any{"name": "probot-fon", "folder": folder, "class": "probot", "parent": "probot-main", "role": "eski rol", "status": "closed"},
	}})

	if err := SetStatus([]string{mainPath, probotPath}, "probot-fon", "open", folder, Registration{Sender: "server-main"}); err != nil {
		t.Fatal(err)
	}
	if names := agentNames(t, probotPath); len(names) != 2 {
		t.Fatalf("project book agents=%v, want no duplicate entry", names)
	}
	if got := findAgent(t, mainPath, "probot-fon"); got.Status != "open" || got.Parent != "probot-main" {
		t.Fatalf("existing agent=%+v, want status open in its own book", got)
	}

	// Pins correct that same entry in place rather than creating a second one.
	reg := Registration{Sender: "server-main", Parent: "probot-business", Role: "yeni rol"}
	if err := SetStatus([]string{mainPath, probotPath}, "probot-fon", "open", folder, reg); err != nil {
		t.Fatal(err)
	}
	if names := agentNames(t, probotPath); len(names) != 2 {
		t.Fatalf("project book agents=%v, want no duplicate entry", names)
	}
	got := findAgent(t, mainPath, "probot-fon")
	if got.Parent != "probot-business" || got.Role != "yeni rol" {
		t.Fatalf("existing agent=%+v, want pinned parent/role applied in place", got)
	}
}

func TestFolderHint(t *testing.T) {
	const root = "server-main"
	cases := []struct {
		what         string
		name         string
		folder       string
		parent       string
		parentFolder string
		want         string
	}{
		{
			what: "folder outside the parent's tree",
			name: "probot-fon", folder: "/srv/kitap/fon",
			parent: "probot-main", parentFolder: "/srv/probot",
			want: "oneri: probot-fon klasoru ebeveyni probot-main altinda degil (/srv/kitap/fon vs /srv/probot) — hiyerarsi klasor yapisinda da gorunsun",
		},
		{
			// A sibling name is not a path component: /srv/probot-old is not
			// inside /srv/probot, so this is a real violation.
			what: "sibling directory sharing a name prefix",
			name: "probot-fon", folder: "/srv/probot-old",
			parent: "probot-main", parentFolder: "/srv/probot",
			want: "oneri: probot-fon klasoru ebeveyni probot-main altinda degil (/srv/probot-old vs /srv/probot) — hiyerarsi klasor yapisinda da gorunsun",
		},
		{
			what: "annotated parent folder still resolves",
			name: "probot-fon", folder: "/srv/kitap/fon",
			parent: "probot-main", parentFolder: "/srv/probot (home: /srv/probot/main)",
			want: "oneri: probot-fon klasoru ebeveyni probot-main altinda degil (/srv/kitap/fon vs /srv/probot) — hiyerarsi klasor yapisinda da gorunsun",
		},
		{
			what: "folder under the parent's",
			name: "probot-fon", folder: "/srv/probot/fon",
			parent: "probot-main", parentFolder: "/srv/probot",
		},
		{
			what: "same folder as the parent (worker sharing the repo)",
			name: "blueprint-worker", folder: "/srv/blueprint",
			parent: "blueprint-main", parentFolder: "/srv/blueprint",
		},
		{
			what: "git worktree of another repo",
			name: "probot-blog", folder: "/srv/kitap/.worktrees/blog",
			parent: "probot-main", parentFolder: "/srv/probot",
		},
		{
			what: "unknown agent folder",
			name: "probot-fon", folder: "",
			parent: "probot-main", parentFolder: "/srv/probot",
		},
		{
			what: "unknown parent folder",
			name: "probot-fon", folder: "/srv/kitap/fon",
			parent: "probot-main", parentFolder: "",
		},
		{
			what: "parent is the fleet root",
			name: "probot-fon", folder: "/srv/kitap/fon",
			parent: root, parentFolder: "/srv/server-main",
		},
		{
			what: "no parent at all",
			name: "probot-fon", folder: "/srv/kitap/fon",
			parent: "", parentFolder: "/srv/probot",
		},
	}
	for _, tc := range cases {
		got := FolderHint(tc.name, tc.folder, tc.parent, tc.parentFolder, root)
		if got != tc.want {
			t.Errorf("%s: FolderHint = %q, want %q", tc.what, got, tc.want)
		}
	}
}
