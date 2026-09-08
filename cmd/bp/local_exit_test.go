package main

import "testing"

func TestResumeHandoffRequiresSpecificSingleWriterError(t *testing.T) {
	id := "01a08084-80c4-75a3-bbfc-3b7ee1645d2a"
	exact := "Error: Failed to resume session from /native/rollout.jsonl: thread/resume failed during TUI bootstrap: thread/resume failed: thread " + id + " already has an active writer (code -32600)"
	if got := failedResumeThread(exact); got != id {
		t.Fatalf("got %q", got)
	}
	for _, screen := range []string{"already has an active writer", "thread " + id + " already has an active writer", exact + "\n" + exact, "Error: Failed to resume session: configuration invalid"} {
		if got := failedResumeThread(screen); got != "" {
			t.Fatalf("ambiguous error routed: %q", got)
		}
	}
}
