package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	bpconfig "blueprint/internal/config"
)

func fixedLookup(name string) (string, error) { return "/usr/bin/" + name, nil }

func TestRemoteCommandConstruction(t *testing.T) {
	tests := []struct {
		name   string
		remote bpconfig.RemoteConfig
		args   []string
		want   commandSpec
	}{
		{
			name:   "ssh",
			remote: bpconfig.RemoteConfig{Host: "host.example", Port: 2222, User: "tuna", Identity: "/keys/id", Transport: "ssh"},
			args:   []string{"attach", "worker"},
			want:   commandSpec{Path: "/usr/bin/ssh", Args: []string{"ssh", "-t", "-p", "2222", "-i", "/keys/id", "tuna@host.example", "bp", "attach", "worker"}},
		},
		{
			name:   "ssh elevate",
			remote: bpconfig.RemoteConfig{Host: "host.example", Transport: "ssh", Elevate: "sudo -i"},
			args:   []string{"attach", "worker", "--no-revive"},
			want:   commandSpec{Path: "/usr/bin/ssh", Args: []string{"ssh", "-t", "host.example", "sudo", "-i", "bp", "attach", "worker", "--no-revive"}},
		},
		{
			name:   "mosh",
			remote: bpconfig.RemoteConfig{Host: "host.example", Port: 2222, User: "tuna", Identity: "/keys/id file", Transport: "mosh", MoshPorts: "60000:61000", Elevate: "sudo -i"},
			args:   []string{"attach", "worker"},
			want:   commandSpec{Path: "/usr/bin/mosh", Args: []string{"mosh", "--ssh=ssh -p 2222 -i '/keys/id file'", "--port=60000:61000", "tuna@host.example", "sudo", "-i", "bp", "attach", "worker"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := remoteBPCommand(test.remote, test.args, fixedLookup)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("command = %#v, %v; want %#v", got, err, test.want)
			}
		})
	}
}

func TestRemoteShellWithoutSessionDoesNotInventTmuxName(t *testing.T) {
	remote := bpconfig.RemoteConfig{Host: "host.example", Transport: "ssh", Elevate: "sudo -i"}
	got, err := remoteShellCommand(remote, fixedLookup)
	want := []string{"ssh", "-t", "host.example", "sudo", "-i"}
	if err != nil || !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("shell command = %#v, %v", got, err)
	}
}

func TestRemoteRegistryAddListAndRemove(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("localMouse: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(filepath.Join(dir, "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	a := &app{
		config: bpconfig.Config{Path: configPath, Home: dir},
		out:    output,
	}
	resetOutput := func() {
		t.Helper()
		if err := output.Truncate(0); err != nil {
			t.Fatal(err)
		}
		if _, err := output.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
	}
	readOutput := func() string {
		t.Helper()
		if _, err := output.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(output.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	handled, err := a.remoteRegistry([]string{"add", "server", "--host", "server.example", "--port", "2222", "--user", "tuna", "--transport", "mosh"})
	if !handled || err != nil {
		t.Fatalf("remote add = %v, %v", handled, err)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(configData), "server.example") {
		t.Fatalf("saved config = %q, %v", configData, err)
	}
	t.Setenv("BP_HOME", dir)
	loaded, err := bpconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	a.config = loaded

	resetOutput()
	handled, err = a.remoteRegistry([]string{"list", "--json"})
	if !handled || err != nil {
		t.Fatalf("remote list = %v, %v", handled, err)
	}
	var listed map[string]bpconfig.RemoteConfig
	if err := json.Unmarshal([]byte(readOutput()), &listed); err != nil {
		t.Fatalf("remote list JSON = %q: %v", readOutput(), err)
	}
	if got := listed["server"]; got.Host != "server.example" || got.Port != 2222 || got.User != "tuna" || got.Transport != "mosh" {
		t.Fatalf("listed remote = %#v", got)
	}

	a.config.Remotes = listed
	resetOutput()
	handled, err = a.remoteRegistry([]string{"rm", "server"})
	if !handled || err != nil {
		t.Fatalf("remote rm = %v, %v", handled, err)
	}
	configData, err = os.ReadFile(configPath)
	if err != nil || strings.Contains(string(configData), "server.example") {
		t.Fatalf("config after remove = %q, %v", configData, err)
	}
}

func TestRemoteRegistryJSONListsEmptyObject(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	a := &app{out: output}
	handled, err := a.remoteRegistry([]string{"list", "--json"})
	if !handled || err != nil {
		t.Fatalf("remote list = %v, %v", handled, err)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil || strings.TrimSpace(string(data)) != "{}" {
		t.Fatalf("empty remote list = %q, %v", data, err)
	}
}
