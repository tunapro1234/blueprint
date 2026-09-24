package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blueprint/internal/book"
)

// #17: an opencode agent registered in a huge folder made bp rename's
// reference grep run without end. The scan must give up after
// referenceScanTimeout, say so, and skip $HOME and / outright.
func TestIssue17_RenameReferenceScanIsBounded(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "grep"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	saved := referenceScanTimeout
	referenceScanTimeout = 200 * time.Millisecond
	t.Cleanup(func() { referenceScanTimeout = saved })

	a := &app{ctx: context.Background(), out: testOutput(t)}
	fleet := book.Fleet{Agents: map[string]book.Agent{"slow": {Name: "slow", Folder: t.TempDir()}}}
	start := time.Now()
	a.reportCodeReferences(fleet, "slow", "fast")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("reference scan ran %s, want it stopped near %s", elapsed, referenceScanTimeout)
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "stopped after") {
		t.Fatalf("output=%q, want the scan to say it stopped", got)
	}
}

func TestIssue17_RenameReferenceScanSkipsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin, ran := t.TempDir(), filepath.Join(t.TempDir(), "grep-ran")
	if err := os.WriteFile(filepath.Join(bin, "grep"), []byte("#!/bin/sh\ntouch "+quoteShell(ran)+"\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &app{ctx: context.Background(), out: testOutput(t)}
	fleet := book.Fleet{Agents: map[string]book.Agent{"homebody": {Name: "homebody", Folder: home}}}
	a.reportCodeReferences(fleet, "homebody", "renamed")
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("grep ran over $HOME")
	}
	if got := readTestOutput(t, a.out); !strings.Contains(got, "reference scan skipped") {
		t.Fatalf("output=%q, want skip notice", got)
	}
}
