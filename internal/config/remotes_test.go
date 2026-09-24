package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteConfigLoadsAndExpandsIdentity(t *testing.T) {
	c, err := yamlConfig(t, "config.yaml", `remotes:
  server:
    host: server.example
    port: 2222
    user: tuna
    identity: ~/.ssh/server
    transport: mosh
    moshPorts: 60000:61000
    elevate: sudo -i
`)
	if err != nil {
		t.Fatal(err)
	}
	remote := c.Remotes["server"]
	user, _ := os.UserHomeDir()
	if remote.Host != "server.example" || remote.Port != 2222 || remote.Transport != "mosh" || remote.Identity != filepath.Join(user, ".ssh/server") {
		t.Fatalf("remote = %#v", remote)
	}
}

func TestUpdateRemoteYAMLIsAtomicAndPreservesOtherSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	initial := "# keep this comment\nlocalMouse: false\n"
	if err := os.WriteFile(path, []byte(initial), 0o640); err != nil {
		t.Fatal(err)
	}
	remote := RemoteConfig{Host: "server.example", Transport: "ssh", Identity: "~/.ssh/id"}
	if err := UpdateRemote(path, "server", &remote); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# keep this comment") || !strings.Contains(string(data), "localMouse: false") || !strings.Contains(string(data), "server.example") {
		t.Fatalf("updated YAML = %q", data)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if err := UpdateRemote(path, "server", nil); err != nil {
		t.Fatal(err)
	}
	c, err := yamlConfig(t, "config.yaml", string(mustRead(t, path)))
	if err != nil || len(c.Remotes) != 0 || c.LocalMouse {
		t.Fatalf("config after removal = %#v, %v", c, err)
	}
}

func TestUpdateRemoteJSONPreservesOtherSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"localMouse":false,"bar":{"widgets":["model"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	remote := RemoteConfig{Host: "server.example", Transport: "ssh"}
	if err := UpdateRemote(path, "server", &remote); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BP_HOME", dir)
	c, err := Load()
	if err != nil || c.LocalMouse || c.Remotes["server"].Host != "server.example" || len(c.Bar.Widgets) != 1 || c.Bar.Widgets[0] != "model" {
		t.Fatalf("updated JSON = %#v, %v", c, err)
	}
}

func TestRemoteValidationRejectsUnsafeValues(t *testing.T) {
	for _, data := range []string{
		"remotes: {server: {host: '-bad'}}\n",
		"remotes: {server: {host: ok, port: 70000}}\n",
		"remotes: {server: {host: ok, transport: telnet}}\n",
		"remotes: {server: {host: ok, moshPorts: '61000:60000'}}\n",
		"remotes: {server: {host: ok, elevate: 'sudo; reboot'}}\n",
	} {
		if _, err := yamlConfig(t, "config.yaml", data); err == nil {
			t.Fatalf("unsafe remote accepted: %s", data)
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
