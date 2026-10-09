package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	bpconfig "blueprint/internal/config"
)

// TestFrameUnavailableAlertThrottle checks that the guard.frame.unavailable audit
// alert fires at most once per hour: the first call is due, an immediate second is
// throttled, and backdating the marker past the hour makes it due again.
func TestFrameUnavailableAlertThrottle(t *testing.T) {
	dir := t.TempDir()
	a := &app{config: bpconfig.Config{StateDir: dir}}

	if !a.frameUnavailableAlertDue() {
		t.Fatal("first call should be due")
	}
	marker := filepath.Join(dir, "guard", "frame-unavailable.alerted")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker not created: %v", err)
	}
	if a.frameUnavailableAlertDue() {
		t.Fatal("immediate second call should be throttled")
	}

	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatalf("backdate marker: %v", err)
	}
	if !a.frameUnavailableAlertDue() {
		t.Fatal("call after the hour window should be due again")
	}
}
