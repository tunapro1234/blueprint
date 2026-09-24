package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Issue #25: a revive/resume must come back in the recorded launch mode
// (model, effort, native flags), not the harness default.
func TestIssue25_ReopenReplaysRecordedNativeFlags(t *testing.T) {
	dir := t.TempDir()
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	id := "4bd47c4f-e853-4eff-bdd0-f3b8444531f8"
	project := filepath.Join(config, "projects", regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(dir, "-"))
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, id+".jsonl"), []byte("{\"type\":\"custom-title\",\"customTitle\":\"ghost\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bookPath := filepath.Join(dir, "agentbook.json")
	t.Setenv("AGENTBOOK", "")
	content := `{"agents":[{"name":"server-main","folder":"` + dir + `"},` +
		`{"name":"ghost","folder":"` + dir + `","parent":"server-main","status":"closed",` +
		`"launch":{"resume":true,"resumeId":"` + id + `","args":["--model","opus-x","--effort","high"]}}]}`
	if err := os.WriteFile(bookPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	client, calls := launchLogTmux(t, false, "bypass permissions")
	a := openTestApp(t, bookPath, client)
	if err := a.open([]string{"ghost", dir, "--resume"}); err != nil {
		t.Fatal(err)
	}
	var launch string
	for _, line := range strings.Split(calls(), "\n") {
		if strings.HasPrefix(line, "send-keys") && strings.Contains(line, "--resume") {
			launch = line
		}
	}
	for _, want := range []string{"--model", "opus-x", "--effort", "high", id} {
		if !strings.Contains(launch, want) {
			t.Fatalf("revive dropped recorded launch mode %q: %q", want, launch)
		}
	}
}
