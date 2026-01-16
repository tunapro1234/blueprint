package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bp "blueprint"
)

func WorkCommand(ctx CommandContext) CommandResult {
	path := ctx.Path
	if path == "" {
		path = "."
	}
	sessionID, _ := ctx.Args["session"].(string)
	if strings.TrimSpace(sessionID) == "" {
		return CommandResult{ExitCode: 1, Output: "Session not found", Errors: []string{"Session not found"}}
	}
	showBlueprints, _ := ctx.Args["blueprints"].(bool)
	showStatus, _ := ctx.Args["status"].(bool)

	rootBP, err := bp.LoadBlueprint(path)
	if err != nil {
		if errors.Is(err, bp.ErrNotBlueprint) {
			out := fmt.Sprintf("✗ %s: not a blueprint package", formatPath(path))
			return CommandResult{ExitCode: 1, Output: out, Errors: []string{"not a blueprint package"}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	manifestPath := filepath.Join(rootBP.StateDir, "work", sessionID, "manifest.yaml")
	if _, err := os.Stat(manifestPath); err != nil {
		if os.IsNotExist(err) {
			return CommandResult{ExitCode: 1, Output: "Session not found", Errors: []string{"Session not found"}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	session, err := readWorkManifest(manifestPath)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	if showStatus && !showBlueprints {
		return CommandResult{ExitCode: 0, Output: fmt.Sprintf("Status: %s", session.Status), Data: session}
	}
	if showBlueprints && !showStatus {
		lines := []string{}
		for _, pkg := range session.Packages {
			lines = append(lines, pkg.Path)
		}
		return CommandResult{ExitCode: 0, Output: strings.Join(lines, "\n"), Data: session}
	}

	lines := []string{
		fmt.Sprintf("Session: %s", session.ID),
		fmt.Sprintf("Created: %s", formatTimestamp(session.Created)),
		fmt.Sprintf("Status: %s", session.Status),
		"Packages:",
	}
	for _, pkg := range session.Packages {
		lines = append(lines, fmt.Sprintf("  %s (%s)", pkg.Path, pkg.Reason))
	}
	return CommandResult{ExitCode: 0, Output: strings.Join(lines, "\n"), Data: session}
}
