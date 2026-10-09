package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0640); err != nil {
		t.Fatal(err)
	}
	return path
}

func readString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSetModulesJSONInsertKeepsEverythingElse(t *testing.T) {
	original := "{\n  \"fed\": {\n    \"mode\": \"hub\"\n  },\n  \"unknownKey\": [1, 2],\n  \"p2p\": {\"enabled\": true}\n}\n"
	path := writeTemp(t, "config.json", original)
	if err := SetModules(path, map[string]bool{"wa": true, "bar": false}); err != nil {
		t.Fatal(err)
	}
	got := readString(t, path)
	want := "{\n  \"fed\": {\n    \"mode\": \"hub\"\n  },\n  \"unknownKey\": [1, 2],\n  \"p2p\": {\"enabled\": true},\n  \"modules\": {\"bar\": false, \"wa\": true}\n}\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode changed: %v", info.Mode())
	}
	// Replacing keeps the position and is idempotent.
	if err := SetModules(path, map[string]bool{"wa": false}); err != nil {
		t.Fatal(err)
	}
	got = readString(t, path)
	if !strings.Contains(got, "\"p2p\": {\"enabled\": true},\n  \"modules\": {\"wa\": false}\n}") {
		t.Fatalf("replace failed:\n%s", got)
	}
}

func TestSetModulesJSONEmptyAndCompact(t *testing.T) {
	path := writeTemp(t, "config.json", "{}")
	if err := SetModules(path, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	if got := readString(t, path); got != `{"modules": {}}` {
		t.Fatalf("got %q", got)
	}
}

func TestSetModulesYAMLKeepsCommentsAndLayout(t *testing.T) {
	original := "# header\nagentbooks: [agentbook.json]\n\n# bar\nbar:\n  widgets: [ctx]\n"
	path := writeTemp(t, "config.yaml", original)
	if err := SetModules(path, map[string]bool{"bar": true}); err != nil {
		t.Fatal(err)
	}
	got := readString(t, path)
	if got != original+"modules: {bar: true}\n" {
		t.Fatalf("got:\n%s", got)
	}
	if err := SetModules(path, map[string]bool{"bar": false, "ui": true}); err != nil {
		t.Fatal(err)
	}
	got = readString(t, path)
	if got != original+"modules: {bar: false, ui: true}\n" {
		t.Fatalf("got:\n%s", got)
	}
}

func TestSetModulesRoundTripsThroughLoad(t *testing.T) {
	for _, name := range []string{"config.yaml", "config.json"} {
		home := t.TempDir()
		content := "{}\n"
		if name == "config.yaml" {
			content = "modules: {} # none yet\n"
		}
		path := filepath.Join(home, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		cfg := loadFor(t, home)
		if name == "config.yaml" && (!cfg.ModulesSet || len(cfg.Modules) != 0) {
			t.Fatalf("%s: empty modules not recorded: %+v", name, cfg.Modules)
		}
		if name == "config.json" && cfg.ModulesSet {
			t.Fatalf("%s: modules reported set", name)
		}
		if err := SetModules(path, map[string]bool{"sessions": true}); err != nil {
			t.Fatal(err)
		}
		cfg = loadFor(t, home)
		if !cfg.ModulesSet || !cfg.Modules["sessions"] {
			t.Fatalf("%s: got %+v (%s)", name, cfg.Modules, readString(t, path))
		}
		if name == "config.yaml" && !strings.Contains(readString(t, path), "# none yet") {
			t.Fatalf("comment lost: %s", readString(t, path))
		}
	}
}

func loadFor(t *testing.T, home string) Config {
	t.Helper()
	cfg, err := loadWithWarning(func(key string) string {
		if key == "BP_HOME" {
			return home
		}
		return ""
	}, os.Stat, func() (string, error) { return home, nil }, os.ReadFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
