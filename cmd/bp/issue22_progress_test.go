package main

import (
	"strings"
	"testing"
)

// Issue #22: reviving printed one line and then waited silently; a launch
// blocked on the harness trust prompt looked exactly like a slow one.
func TestIssue22_OpenReportsStepsAndWhatItWaitsOn(t *testing.T) {
	dir := t.TempDir()
	bookPath := openTestBook(t, dir, "closed")
	client, _ := launchLogTmux(t, false, "Accessing workspace:\\n"+dir+"-elsewhere\\nQuick safety check: Is this a project you created or one you trust?\\n❯ No, exit\\n  Yes, I trust this folder\\nEnter to confirm · Esc to cancel")
	a := openTestApp(t, bookPath, client)
	err := a.open([]string{"ghost", dir, "--claude", "--no-prompt"})
	if err == nil {
		t.Fatal("open succeeded behind a trust prompt")
	}
	progress := readTestOutput(t, a.err) + readTestOutput(t, a.out)
	for _, want := range []string{"record resolved", "harness started", "waiting: harness trust prompt"} {
		if !strings.Contains(progress, want) {
			t.Fatalf("progress missing %q:\n%s", want, progress)
		}
	}
	if !strings.Contains(err.Error(), "trust prompt") {
		t.Fatalf("failure does not say which step blocked: %v", err)
	}
}
