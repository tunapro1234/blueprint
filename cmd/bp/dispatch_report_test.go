package main

import (
	"os"
	"path/filepath"
	"testing"
)

// bp msg drains the whole queue; lines about other agents' records must not
// reach the caller once it has enqueued its own (server-main, 22 Oct).
func TestDispatchReportScopedToOwnChannels(t *testing.T) {
	out := func() (*os.File, func() string) {
		f, err := os.Create(filepath.Join(t.TempDir(), "err"))
		if err != nil {
			t.Fatal(err)
		}
		return f, func() string { b, _ := os.ReadFile(f.Name()); return string(b) }
	}
	f, read := out()
	a := &app{err: f, ownChannels: map[string]bool{"q123456789": true}}
	for _, line := range []string{
		"delivered: q987654321 -> server-necati-doruk",
		"delivered: q123456789 -> probot-lms",
		"msgq: p0e4443efd062fafcc5b73993ea0684ea -> pcb-main@aws waiting: busy; retrying in 1m",
		"msgq: could not read /x: denied",
	} {
		a.reportDispatchLine(line)
	}
	want := "delivered: q123456789 -> probot-lms\nmsgq: could not read /x: denied\n"
	if got := read(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// Commands that enqueue nothing (bp q, bp dispatch) keep the full report.
	f, read = out()
	(&app{err: f}).reportDispatchLine("delivered: q987654321 -> server-necati-doruk")
	if read() == "" {
		t.Fatal("unscoped pass dropped a line")
	}
}
