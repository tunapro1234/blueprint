package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bpconfig "blueprint/internal/config"
)

func TestOpenRejectsFolderRebindBeforeTouchingTmux(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "book.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"root"},{"name":"worker","folder":"/old/path","status":"closed","nativeTitle":{"threadId":"thread-old"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &app{ctx: context.Background(), config: bpconfig.Config{Agentbooks: []string{path}}}
	err := a.open([]string{"worker", dir})
	if err == nil || !strings.Contains(err.Error(), "refusing to rebind") || !strings.Contains(err.Error(), "--rebind") {
		t.Fatalf("open error=%v, want explicit rebind refusal", err)
	}
}
