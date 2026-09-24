package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"blueprint/internal/identity"
	"blueprint/internal/messagetext"
	bptmux "blueprint/internal/tmux"
)

type localExitReport struct {
	Harness  string `json:"harness"`
	Status   string `json:"status"`
	Signal   string `json:"signal,omitempty"`
	Screen   string `json:"screen,omitempty"`
	RoutedTo string `json:"routed_to,omitempty"`
	Agent    string `json:"agent,omitempty"`
}

// retainSessionLaunchFailure records errors returned before _session replaces
// itself with the native CLI. Its tmux pane is held open by main until the user
// acknowledges the error.
func retainSessionLaunchFailure(args []string, err error) {
	if err == nil || len(args) < 2 || args[0] != "_session" || !identity.ValidName(args[1]) {
		return
	}
	path := os.Getenv("BP_EXIT_REPORT")
	if !filepath.IsAbs(path) {
		return
	}
	harness := "bp"
	if len(args) > 2 && localHarness(args[2]) {
		harness = args[2]
	}
	report := localExitReport{Agent: args[1], Harness: harness, Status: "1", Screen: err.Error()}
	data, marshalErr := json.Marshal(report)
	if marshalErr == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

func holdFailedSessionPane(args []string) bool {
	return len(args) > 0 && args[0] == "_session" && os.Getenv("BP_EXIT_REPORT") != "" && os.Getenv("TMUX") != "" && terminal(os.Stdin)
}

func localExitFailed(report localExitReport) bool {
	return report.Status != "0" && report.Status != "130" && report.Signal != "2" && report.RoutedTo == ""
}

var activeWriterError = regexp.MustCompile(`thread ([0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}) already has an active writer`)

func failedResumeThread(screen string) string {
	if !strings.Contains(screen, "Error: Failed to resume session") || !strings.Contains(screen, "thread/resume failed") {
		return ""
	}
	matches := activeWriterError.FindAllStringSubmatch(screen, -1)
	if len(matches) != 1 {
		return ""
	}
	return strings.ToLower(matches[0][1])
}

// This runs after our own native process has exited. A failed picker can route
// clients, but never sends input, removes a writer lock or closes a live pane.
func (a *app) finishLocalExit(name string, parent int, harness, reportPath string) {
	if reportPath == "" || !filepath.IsAbs(reportPath) || !identity.ValidName(name) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	run := func(args ...string) ([]byte, error) { return exec.CommandContext(ctx, a.tmux.Bin, args...).Output() }
	// The worker can notice its parent is gone before tmux marks the pane dead,
	// and tmux can mark it dead (pty EOF) before it has reaped the exit status.
	// Reading once left a "Pane is dead" corpse or a status-less false failure (#18).
	var fields []string
	for {
		out, err := run("display-message", "-p", "-t", "="+name+":", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{pane_dead_status}\t#{pane_dead_signal}")
		if err != nil {
			return
		}
		fields = strings.Split(strings.TrimSuffix(string(out), "\n"), "\t")
		if len(fields) != 5 || fields[1] != strconv.Itoa(parent) || !regexp.MustCompile(`^%[0-9]+$`).MatchString(fields[0]) {
			return
		}
		if fields[2] == "1" && (fields[3] != "" || fields[4] != "") {
			break
		}
		select {
		case <-ctx.Done():
			if fields[2] != "1" {
				return
			}
		case <-time.After(100 * time.Millisecond):
			continue
		}
		break
	}
	pane := fields[0]
	report := localExitReport{Agent: name, Harness: harness, Status: fields[3], Signal: fields[4]}
	failed := localExitFailed(report)
	if failed {
		screen, err := run("capture-pane", "-p", "-J", "-S", "-100", "-t", pane)
		if err != nil {
			fmt.Fprintln(a.err, "retain failed pane: capture:", err)
			return
		}
		report.Screen = string(screen)
		if harness == "codex" {
			if thread := failedResumeThread(report.Screen); thread != "" {
				probe := *a
				probe.ctx = ctx
				owners, err := probe.codexResumeOwners(bptmux.CodexProcessInfo(0).Home, thread)
				if err == nil && len(owners) == 1 && owners[0] != name {
					clients, err := run("list-clients", "-t", "="+name, "-F", "#{client_name}")
					names := strings.Fields(string(clients))
					switched := err == nil && len(names) > 0
					for _, client := range names {
						if _, err := run("switch-client", "-c", client, "-t", "="+owners[0]); err != nil {
							switched = false
						}
					}
					if switched {
						report.RoutedTo = owners[0]
					}
				}
			}
		}
	}
	data, err := json.Marshal(report)
	if err == nil {
		err = os.WriteFile(reportPath, data, 0600)
	}
	if err != nil {
		fmt.Fprintln(a.err, "retain failed pane: save exit:", err)
		return
	}
	// tmux evaluates the condition in its command queue, so a user respawning the
	// pane in the meantime cannot have their new process killed by this cleanup.
	condition := fmt.Sprintf("#{&&:#{pane_dead},#{==:#{pane_pid},%d}}", parent)
	if _, err := run("if-shell", "-F", "-t", pane, condition, "kill-pane -t "+pane); err != nil {
		fmt.Fprintln(a.err, "close exited pane:", err)
	}
}

func (a *app) reportLocalExit(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	} // client detached while CLI still runs
	if err != nil {
		return err
	}
	var report localExitReport
	if err := json.Unmarshal(data, &report); err != nil {
		return err
	}
	if !localExitFailed(report) {
		return nil
	}
	if messagetext.Validate(report.Screen) == nil {
		fmt.Fprintln(a.err, strings.TrimSpace(report.Screen))
	}
	return fmt.Errorf("%s exited (status %s, signal %s); details: %s", report.Harness, report.Status, report.Signal, path)
}
