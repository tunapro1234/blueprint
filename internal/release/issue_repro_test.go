package release

import "testing"

// GitHub asset bodies can be large while bytes continue to arrive. A client-wide
// Timeout is a total request deadline and cancels ReadAll even when the server is
// making progress; use transport/stall limits for the connection instead.
func TestIssue04ReleaseDownloadHasNoWholeBodyDeadline(t *testing.T) {
	if got := Default().HTTP.Timeout; got != 0 {
		t.Fatalf("release HTTP client imposes a %s total deadline across the asset body", got)
	}
}
