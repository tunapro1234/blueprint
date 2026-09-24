package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/book"
	"blueprint/internal/config"
	"blueprint/internal/release"
)

func TestDoctorDeniedSocketDoesNotReportClosedWriter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\necho 'error connecting to /tmp/tmux-1000/default (Operation not permitted)' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	fleet := book.Fleet{Agents: map[string]book.Agent{"hypr-codex": {Name: "hypr-codex"}}}
	checks := doctorRuntimeChecks(config.Config{}, fleet, "hypr-codex")
	if len(checks) != 1 || checks[0].OK || checks[0].Name != "tmux_access" || !strings.Contains(checks[0].Detail, "Operation not permitted") {
		t.Fatalf("denied socket misdiagnosed: %+v", checks)
	}
}

func TestDoctorNativeTitleMismatchExplainsRetitleAndAdoption(t *testing.T) {
	doctorTestTmux(t, false)
	t.Setenv("AGENTBOOK", "")
	for _, test := range []struct {
		name            string
		title           string
		reservation     *book.Agent
		wantAdopt       bool
		wantUnavailable string
	}{
		{name: "available native title", title: "native-alias", wantAdopt: true},
		{name: "title used by closed registration", title: "native-alias", reservation: &book.Agent{Name: "native-alias", Status: "closed"}, wantUnavailable: `adopt unavailable: "native-alias" is used by agent native-alias (closed)`},
		{name: "title used by archived registration", title: "native-alias", reservation: &book.Agent{Name: "native-alias", Status: "closed", ArchivedAt: "2026-09-01T00:00:00Z"}, wantUnavailable: `adopt unavailable: "native-alias" is used by agent native-alias (archived)`},
		{name: "invalid free-form title", title: "native alias", wantUnavailable: `adopt unavailable: "native alias" is not a valid agent name`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			transcript := filepath.Join(root, "session.jsonl")
			title := test.title
			if title == "" {
				title = "native-alias"
			}
			line, err := json.Marshal(map[string]string{"type": "custom-title", "customTitle": title, "sessionId": "thread"})
			if err != nil {
				t.Fatal(err)
			}
			originalTranscript := append(line, '\n')
			if err := os.WriteFile(transcript, originalTranscript, 0o600); err != nil {
				t.Fatal(err)
			}
			bookPath := filepath.Join(root, "agentbook.json")
			entries := []book.Agent{{Name: "canonical-name"}}
			if test.reservation != nil {
				entries = append(entries, *test.reservation)
			}
			bookData, err := json.Marshal(book.File{Agents: entries})
			if err != nil {
				t.Fatal(err)
			}
			bookData = append(bookData, '\n')
			if err := os.WriteFile(bookPath, bookData, 0o600); err != nil {
				t.Fatal(err)
			}
			entry := book.Agent{
				Name:        "canonical-name",
				NativeTitle: &book.NativeTitle{ThreadID: "thread", Path: transcript, Text: title},
			}
			fleet := book.Fleet{Agents: map[string]book.Agent{"canonical-name": entry}}
			if test.reservation != nil {
				fleet.Agents[title] = *test.reservation
			}
			cfg := config.Config{StateDir: filepath.Join(root, "state"), Agentbooks: []string{bookPath}}
			checks := doctorRuntimeChecks(cfg, fleet, "canonical-name")
			var found *doctorCheck
			for i := range checks {
				if checks[i].Name == "native_title/canonical-name" {
					found = &checks[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("doctor omitted the title mismatch: %+v", checks)
			}
			for _, want := range []string{`bp rename canonical-name canonical-name (retitle the native session to "canonical-name")`} {
				if !strings.Contains(found.Next, want) {
					t.Errorf("next hint %q does not explain retitling", found.Next)
				}
			}
			adoption := "bp rename canonical-name " + title + " (adopt the native title as the bp name)"
			if strings.Contains(found.Next, adoption) != test.wantAdopt {
				t.Fatalf("next hint %q adoption-present=%t want %t", found.Next, strings.Contains(found.Next, adoption), test.wantAdopt)
			}
			if test.wantUnavailable != "" && !strings.Contains(found.Next, test.wantUnavailable) {
				t.Errorf("next hint %q does not explain why adoption is unavailable; want %q", found.Next, test.wantUnavailable)
			}
			if strings.Contains(found.Next, root) {
				t.Errorf("next hint contains an agentbook/transcript path: %q", found.Next)
			}
			afterBook, err := os.ReadFile(bookPath)
			if err != nil || string(afterBook) != string(bookData) {
				t.Fatalf("doctor changed the agentbook: %v", err)
			}
			afterTranscript, err := os.ReadFile(transcript)
			if err != nil || string(afterTranscript) != string(originalTranscript) {
				t.Fatalf("doctor changed the native transcript: %v", err)
			}
		})
	}
}

func TestCheckShellIntegrationDistinguishesActiveDisabledAndBroken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shell.sh")
	tests := []struct {
		name    string
		content string
		detail  string
		wantErr bool
	}{
		{name: "active", content: localShell, detail: "active: " + path},
		{name: "disabled", content: "# Generated by bp setup. Disabled; native aliases and records are preserved.\n", detail: "disabled: " + path},
		{name: "incomplete", content: "# Generated by bp setup.\n", wantErr: true},
		{name: "unrecognized", content: "user-owned shell code\n", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.content), 0600); err != nil {
				t.Fatal(err)
			}
			detail, err := checkShellIntegration(path)
			if (err != nil) != test.wantErr {
				t.Fatalf("detail=%q error=%v", detail, err)
			}
			if !test.wantErr && detail != test.detail {
				t.Fatalf("detail=%q, want %q", detail, test.detail)
			}
		})
	}
}

func TestCheckExecutablePathAcceptsDirectAndNPMLauncherPaths(t *testing.T) {
	dir := t.TempDir()
	running := filepath.Join(dir, release.Platform())
	launcher := filepath.Join(dir, "bp.js")
	other := filepath.Join(dir, "other-bp")
	for _, path := range []string{running, launcher, other} {
		if err := os.WriteFile(path, []byte(path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	direct := filepath.Join(dir, "bp-direct")
	if err := os.Symlink(running, direct); err != nil {
		t.Fatal(err)
	}
	if detail, err := checkExecutablePath(direct, running, ""); err != nil || !strings.Contains(detail, "running") {
		t.Fatalf("direct detail=%q error=%v", detail, err)
	}
	npm := filepath.Join(dir, "bp-npm")
	if err := os.Symlink(launcher, npm); err != nil {
		t.Fatal(err)
	}
	if detail, err := checkExecutablePath(npm, running, launcher); err != nil || !strings.Contains(detail, "launcher") {
		t.Fatalf("npm detail=%q error=%v", detail, err)
	}
	if _, err := checkExecutablePath(other, running, ""); err == nil {
		t.Fatal("mismatched bp executable passed PATH check")
	}
}
