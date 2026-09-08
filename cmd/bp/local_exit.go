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
	out, err := run("display-message", "-p", "-t", "="+name+":", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{pane_dead_status}\t#{pane_dead_signal}")
	if err != nil {
		return
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\n"), "\t")
	if len(fields) != 5 || fields[1] != strconv.Itoa(parent) || fields[2] != "1" || !regexp.MustCompile(`^%[0-9]+$`).MatchString(fields[0]) {
		return
	}
	pane := fields[0]
	report := localExitReport{Harness: harness, Status: fields[3], Signal: fields[4]}
	failed := report.Status != "0" && report.Status != "130" && report.Signal != "2"
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
	if report.RoutedTo != "" || report.Status == "0" || report.Status == "130" || report.Signal == "2" {
		return nil
	}
	if messagetext.Validate(report.Screen) == nil {
		fmt.Fprintln(a.err, strings.TrimSpace(report.Screen))
	}
	return fmt.Errorf("%s exited (status %s, signal %s); details: %s", report.Harness, report.Status, report.Signal, path)
}
