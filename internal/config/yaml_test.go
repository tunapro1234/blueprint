package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func yamlConfig(t *testing.T, name, data string) (Config, error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BP_HOME", home)
	if err := os.WriteFile(filepath.Join(home, name), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return Load()
}

func TestLocalObservationYAMLDefaultsAndOverrides(t *testing.T) {
	c, err := yamlConfig(t, "config.yaml", "{}")
	if err != nil || !c.LocalObservation || !c.LocalMouse || c.Bar.Context != "used" {
		t.Fatal(c, err)
	}
	c, err = yamlConfig(t, "config.yaml", "localObservation: false\nlocalMouse: false\nstateDir: custom-metrics\nbar:\n  context: used\n")
	if err != nil || c.LocalObservation || c.LocalMouse || c.Bar.Context != "used" || c.StateDir != filepath.Join(c.Home, "custom-metrics") {
		t.Fatal(c, err)
	}
	if _, err = yamlConfig(t, "config.yaml", "bar:\n  context: imaginary\n"); err == nil {
		t.Fatal("invalid context display accepted")
	}
	if defaults("/srv/blueprint", true).Bar.Context != "used" {
		t.Fatal("changed server bar default")
	}
}

func TestYAMLConfigPathsAndDisabledValues(t *testing.T) {
	c, err := yamlConfig(t, "config.yaml", `# portable paths
agentbooks: [agents/book.json]
stateDir: state-custom
msgqRoot: queues
usageHistory: ~/usage.jsonl
waBridge: false
bar:
  widgets: []
codex:
  sockets: [sockets/codex.sock]
ntfy:
  url: https://example.com
  topic: bp
  token: "test-only"
`)
	if err != nil {
		t.Fatal(err)
	}
	user, _ := os.UserHomeDir()
	if c.StateDir != filepath.Join(c.Home, "state-custom") || c.MsgqRoot != filepath.Join(c.Home, "queues") || c.UsageHistory != filepath.Join(user, "usage.jsonl") || c.WABridge || len(c.Bar.Widgets) != 0 {
		t.Fatal("paths or explicit empty/false lost")
	}
	if !reflect.DeepEqual(c.Agentbooks, []string{filepath.Join(c.Home, "agents/book.json")}) || !reflect.DeepEqual(c.Agentbooks, c.TokenAgentbooks) {
		t.Fatal(c.Agentbooks, c.TokenAgentbooks)
	}
	if c.Codex.Sockets[0] != filepath.Join(c.Home, "sockets/codex.sock") || c.Ntfy.Topic != "bp" || c.Ntfy.Token != "test-only" {
		t.Fatal("nested configuration lost")
	}
}

func TestInvalidYAMLNeverFallsBackToDefaults(t *testing.T) {
	for _, data := range []string{
		"bar: [", "stateDri: state", "bar: {widgtes: [ctx]}", "waBridge: definitely-not-a-bool",
		"stateDir: first\nstateDir: second\n", "bar: {widgets: [typo]}", "bar: {}\n---\nbar: {}\n",
		"fed: {mode: hub, listen: '0.0.0.0:7877', peerName: laptop}",
	} {
		if _, err := yamlConfig(t, "config.yaml", data); err == nil {
			t.Fatalf("invalid config accepted: %q", data)
		}
	}
}

func TestYMLAndJSONCompatibilityAndConflict(t *testing.T) {
	for _, name := range []string{"config.yml", "config.json"} {
		c, err := yamlConfig(t, name, `{"bar":{"widgets":["model","quota"]}}`)
		if err != nil || !reflect.DeepEqual(c.Bar.Widgets, []string{"model", "quota"}) {
			t.Fatal(name, err)
		}
		if filepath.Base(c.Path) != name {
			t.Fatal(c.Path)
		}
		if err := os.WriteFile(filepath.Join(c.Home, "config.yaml"), []byte("bar: {}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "multiple bp configs") {
			t.Fatal(err)
		}
	}
}

func TestInitYAMLCreatesValidPrivateConfigAndPreservesEdits(t *testing.T) {
	home := filepath.Join(t.TempDir(), "bp")
	t.Setenv("BP_HOME", home)
	path, err := InitYAML(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	edited := []byte("bar:\n  widgets: [model]\n")
	if err := os.WriteFile(path, edited, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := InitYAML(home); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(edited) {
		t.Fatal("setup overwrote user settings")
	}
	for _, name := range []string{"config.json", "config.yml"} {
		dir := t.TempDir()
		existing := filepath.Join(dir, name)
		if err := os.WriteFile(existing, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if got, err := InitYAML(dir); err != nil || got != existing {
			t.Fatal(got, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "config.yaml")); !os.IsNotExist(err) {
			t.Fatal("existing format shadowed")
		}
	}
}

func TestServerAndLaptopBarDefaultsMatch(t *testing.T) {
	server, laptop := defaults("/srv/blueprint", true), defaults("/tmp/laptop", false)
	if server.Bar.Context != laptop.Bar.Context {
		t.Fatal(server.Bar, laptop.Bar)
	}
	if strings.Join(server.Bar.Widgets, ",") != strings.Join(laptop.Bar.Widgets, ",") {
		t.Fatal(server.Bar, laptop.Bar)
	}
	c, err := yamlConfig(t, "config.yaml", "bar:\n  context: remaining\n")
	if err != nil || c.Bar.Context != "remaining" {
		t.Fatal(c, err)
	}
}

func TestDefaultColorConfig(t *testing.T) {
	for _, value := range []string{"purple", "33", "''"} {
		if _, err := yamlConfig(t, "config.yaml", "bar:\n  defaultColor: "+value+"\n"); err != nil {
			t.Fatal(value, err)
		}
	}
	for _, value := range []string{"256", "blurple", "-1"} {
		if _, err := yamlConfig(t, "config.yaml", "bar:\n  defaultColor: "+value+"\n"); err == nil {
			t.Fatal("accepted", value)
		}
	}
}
