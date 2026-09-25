package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorAcceptsBarFallbackAndWarnsOnStaleOption(t *testing.T) {
	format := localBarStatusFormat("bar", "#(bp bar 'agent')")
	if check, wrong := doctorBarTargetCheck("agent", "status-right", "bar", format); wrong {
		t.Fatalf("valid option fallback was reported as a wrong target: %+v", check)
	}
	if check := doctorStaleBarOption("agent", "rendered line", false); check == nil || !check.Warning {
		t.Fatalf("set bar option without a running daemon did not warn: %+v", check)
	}
	if check := doctorStaleBarOption("agent", "rendered line", true); check != nil {
		t.Fatalf("running daemon produced stale-option warning: %+v", check)
	}
}

func TestTmux34BarOptionFallbackDoesNotSpawn(t *testing.T) {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	scriptBin, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script is not installed; cannot attach an isolated client")
	}
	version, err := exec.Command(tmuxBin, "-V").CombinedOutput()
	if err != nil || strings.TrimSpace(string(version)) != "tmux 3.4" {
		t.Skipf("proof requires tmux 3.4, found %q", strings.TrimSpace(string(version)))
	}
	dir, err := os.MkdirTemp("/tmp", "bp-bar-proof-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	spawns := filepath.Join(dir, "spawns")
	fakeBP := filepath.Join(dir, "bp")
	if err := os.WriteFile(fakeBP, []byte("#!/bin/sh\nprintf 'spawn\\n' >> "+quoteShell(spawns)+"\nprintf 'fallback\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runTmux := func(args ...string) ([]byte, error) {
		command := []string{"-u", "TMUX", "-u", "TMUX_PANE", "TMUX_TMPDIR=" + dir, tmuxBin, "-S", socket}
		command = append(command, args...)
		return exec.Command("env", command...).CombinedOutput()
	}
	if out, err := runTmux("new-session", "-d", "-s", "proof", "-x", "80", "-y", "24"); err != nil {
		t.Fatalf("create isolated tmux session: %v: %s", err, out)
	}
	cleanupServer := func() {
		_, _ = runTmux("kill-server") // Always pin cleanup to this test's socket.
	}
	t.Cleanup(cleanupServer)
	if out, err := runTmux("set-option", "-t", "=proof:", "@bp-bar", escapeTmuxBarFormat("#[bg=colour236] rendered # hash #[default]")); err != nil {
		t.Fatalf("set renderer option: %v: %s", err, out)
	}
	format := localBarStatusFormat("bar", "#("+quoteShell(fakeBP)+" bar proof)")
	if out, err := runTmux("set-option", "-t", "=proof:", "status-right", format); err != nil {
		t.Fatalf("set fallback status format %q: %v: %s", format, err, out)
	}
	if out, err := runTmux("set-option", "-t", "=proof:", "status-interval", "1"); err != nil {
		t.Fatalf("set status interval: %v: %s", err, out)
	}

	capture := filepath.Join(dir, "attached-terminal")
	captureFile, err := os.Create(capture)
	if err != nil {
		t.Fatal(err)
	}
	attach := exec.Command("env", "-u", "TMUX", "-u", "TMUX_PANE", "TMUX_TMPDIR="+dir, "TERM=xterm-256color",
		scriptBin, "-q", "-c", tmuxBin+" -S "+quoteShell(socket)+" attach-session -t proof", "/dev/null")
	attach.Stdout, attach.Stderr = captureFile, captureFile
	if err := attach.Start(); err != nil {
		t.Fatalf("attach isolated tmux client: %v", err)
	}
	t.Cleanup(func() {
		if attach.Process != nil {
			_ = attach.Process.Kill()
			_ = attach.Wait()
		}
		_ = captureFile.Close()
	})
	client := ""
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := runTmux("list-clients", "-F", "#{client_name}\t#{session_name}")
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				fields := strings.SplitN(line, "\t", 2)
				if len(fields) == 2 && fields[1] == "proof" {
					client = fields[0]
					break
				}
			}
		}
		if client != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if client == "" {
		_ = captureFile.Sync()
		output, _ := os.ReadFile(capture)
		t.Fatalf("isolated tmux client did not attach: %s", output)
	}
	if out, err := runTmux("refresh-client", "-S", "-t", client); err != nil {
		t.Fatalf("refresh hash-escape proof: %v: %s", err, out)
	}
	time.Sleep(100 * time.Millisecond)
	if err := captureFile.Sync(); err != nil {
		t.Fatal(err)
	}
	terminal, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(terminal), "rendered # hash") || strings.Contains(string(terminal), "rendered ## hash") {
		t.Fatalf("status bar did not render one literal hash: %q", terminal[len(terminal)-min(500, len(terminal)):])
	}
	refresh := func() error {
		out, err := runTmux("refresh-client", "-S", "-t", client)
		if err != nil {
			return fmtProofError(err, out)
		}
		return nil
	}

	started := time.Now()
	for time.Since(started) < 20*time.Second {
		if err := refresh(); err != nil {
			t.Fatalf("refresh isolated client: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if data, err := os.ReadFile(spawns); err == nil && len(data) != 0 {
		t.Fatalf("fallback #() ran while @bp-bar was set: %q", data)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	// Prove the fake command and refresh path work: removing the option should
	// activate the fallback and create at least one counted spawn.
	if out, err := runTmux("set-option", "-u", "-t", "=proof:", "@bp-bar"); err != nil {
		t.Fatalf("unset renderer option for control: %v: %s", err, out)
	}
	for i := 0; i < 20; i++ {
		if err := refresh(); err != nil {
			t.Fatalf("refresh fallback control: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, err := os.ReadFile(spawns)
	if err != nil || len(data) == 0 {
		t.Fatalf("fallback control did not execute fake bp: %q, %v", data, err)
	}
}

func fmtProofError(err error, output []byte) error {
	return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
}
