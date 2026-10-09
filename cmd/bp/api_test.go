package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/api"
	bpconfig "blueprint/internal/config"
)

func apiTestApp(t *testing.T, cfg *bpconfig.APIConfig) (*app, func() string) {
	t.Helper()
	root := t.TempDir()
	out, err := os.Create(filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close() })
	a := &app{ctx: context.Background(), out: out, err: out,
		config: bpconfig.Config{StateDir: filepath.Join(root, "state"), Path: filepath.Join(root, "config.yaml"), API: cfg}}
	read := func() string {
		data, _ := os.ReadFile(out.Name())
		out.Truncate(0)
		out.Seek(0, 0)
		return string(data)
	}
	return a, read
}

func TestAPICommandTokenAndConfigSnippets(t *testing.T) {
	a, read := apiTestApp(t, &bpconfig.APIConfig{Listen: "127.0.0.1:8765"})
	if err := a.apiCommand([]string{"token"}); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(read())
	if !strings.HasPrefix(token, "bpt_") {
		t.Fatalf("token %q", token)
	}
	if info, err := os.Stat(api.TokenPath(a.config.StateDir)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file %v %v", info, err)
	}
	for _, client := range []string{"claude", "codex", "gemini", "antigravity", "grok", "opencode", "cursor", "hermes"} {
		if err := a.apiCommand([]string{"config", client}); err != nil {
			t.Fatalf("%s stdio: %v", client, err)
		}
		if out := read(); !strings.Contains(out, "mcp") {
			t.Fatalf("%s stdio snippet: %q", client, out)
		}
		if err := a.apiCommand([]string{"config", client, "--http"}); err != nil {
			t.Fatalf("%s http: %v", client, err)
		}
		if out := read(); !strings.Contains(out, "http://127.0.0.1:8765/mcp") {
			t.Fatalf("%s http snippet: %q", client, out)
		}
	}
	if err := a.apiCommand([]string{"config", "notepad"}); err == nil {
		t.Fatal("unknown client accepted")
	}
}

func TestServeRefusesNonLoopbackListen(t *testing.T) {
	a, _ := apiTestApp(t, nil)
	if err := a.serve([]string{"--api", "--no-socket", "--listen", "0.0.0.0:0"}); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("got %v", err)
	}
	if err := a.serve([]string{"--api", "--no-socket"}); err == nil {
		t.Fatal("served nothing without an error")
	}
	if err := a.serve([]string{"--gateway"}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("gateway without config: %v", err)
	}
}

func TestGatewayCommandPairTokenRevoke(t *testing.T) {
	a, read := apiTestApp(t, &bpconfig.APIConfig{Gateway: &bpconfig.GatewayConfig{
		Listen: "127.0.0.1:0", PublicURL: "https://bp.example/mcp",
		Clients: map[string]bpconfig.GatewayClient{"phone": {Agents: []string{"worker"}}}}})
	if err := a.gatewayCommand([]string{"clients"}); err != nil || !strings.Contains(read(), "phone\tagents=[worker]") {
		t.Fatalf("clients: %v", err)
	}
	if err := a.gatewayCommand([]string{"pair", "phone"}); err != nil || !strings.Contains(read(), "-") {
		t.Fatalf("pair: %v", err)
	}
	if err := a.gatewayCommand([]string{"token", "nobody"}); err == nil {
		t.Fatal("token for an unconfigured client")
	}
	if err := a.gatewayCommand([]string{"token", "phone"}); err != nil || !strings.HasPrefix(read(), "bpg_") {
		t.Fatalf("token: %v", err)
	}
	if err := a.gatewayCommand([]string{"revoke", "--all"}); err != nil || !strings.Contains(read(), "revoked 1 tokens") {
		t.Fatalf("revoke: %v", err)
	}
}
