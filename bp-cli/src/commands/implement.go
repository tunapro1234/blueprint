package commands

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	mathrand "math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bp "blueprint"
)

func ImplementCommand(ctx CommandContext) CommandResult {
	path := ctx.Path
	if path == "" {
		path = "."
	}
	recursive, _ := ctx.Args["recursive"].(bool)
	listSessions, _ := ctx.Args["list"].(bool)
	doneSession, _ := ctx.Args["done"].(string)

	rootBP, err := bp.LoadBlueprint(path)
	if err != nil {
		if errors.Is(err, bp.ErrNotBlueprint) {
			out := fmt.Sprintf("✗ %s: not a blueprint package", formatPath(path))
			return CommandResult{ExitCode: 1, Output: out, Errors: []string{"not a blueprint package"}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	workDir := filepath.Join(rootBP.StateDir, "work")

	if listSessions {
		return listWorkSessions(workDir)
	}
	if strings.TrimSpace(doneSession) != "" {
		return markWorkSessionDone(workDir, doneSession)
	}

	stalePackages, err := collectStalePackages(path, recursive)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if len(stalePackages) == 0 {
		return CommandResult{ExitCode: 2, Output: "Nothing to implement", Errors: []string{"Nothing to implement"}}
	}

	sessionID := newSessionID()
	sessionDir := filepath.Join(workDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	packages := make([]WorkPackage, 0, len(stalePackages))
	for _, item := range stalePackages {
		label := formatDepLabel(rootBP.Dir, item.Blueprint.Dir)
		rel, err := filepath.Rel(rootBP.Dir, item.Blueprint.Dir)
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		workPkgDir := sessionDir
		if rel != "." {
			workPkgDir = filepath.Join(sessionDir, rel)
		}
		if err := os.MkdirAll(workPkgDir, 0o755); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		if err := copyFile(item.Blueprint.Path, filepath.Join(workPkgDir, "BLUEPRINT.yaml")); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		if err := copyImplementation(item.Blueprint, workPkgDir); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		blueprintHash, err := bp.HashFile(item.Blueprint.Path)
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		workRel := filepath.Join(".blueprint", "work", sessionID)
		if rel != "." {
			workRel = filepath.Join(workRel, rel)
		}
		workRel = filepath.ToSlash(workRel)
		if !strings.HasPrefix(workRel, ".") {
			workRel = "./" + workRel
		}
		packages = append(packages, WorkPackage{
			Path:          label,
			WorkPath:      workRel,
			Reason:        formatStaleReason(item.Info),
			BlueprintHash: blueprintHash,
		})
	}

	session := WorkSession{
		ID:       sessionID,
		Created:  time.Now().Format("2006-01-02T15:04:05"),
		Status:   "active",
		Packages: packages,
	}
	manifestPath := filepath.Join(sessionDir, "manifest.yaml")
	if err := writeWorkManifest(manifestPath, session); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	return CommandResult{
		ExitCode: 0,
		Output:   fmt.Sprintf("Work session started: %s", sessionID),
		Data:     session,
	}
}

type stalePackage struct {
	Blueprint *bp.Blueprint
	Info      bp.StalenessInfo
}

func collectStalePackages(path string, recursive bool) ([]stalePackage, error) {
	targets := []string{}
	if recursive {
		files, err := findBlueprintsRecursive(path)
		if err != nil {
			return nil, err
		}
		targets = files
	} else {
		blueprintPath, err := resolveBlueprintPath(path)
		if err != nil {
			return nil, err
		}
		targets = []string{blueprintPath}
	}
	stale := []stalePackage{}
	for _, target := range targets {
		bpObj, err := bp.LoadBlueprint(target)
		if err != nil {
			return nil, err
		}
		info, err := bpObj.StalenessInfo()
		if err != nil {
			return nil, err
		}
		if info.State == "stale" {
			stale = append(stale, stalePackage{Blueprint: bpObj, Info: info})
		}
	}
	sort.Slice(stale, func(i, j int) bool {
		return stale[i].Blueprint.Dir < stale[j].Blueprint.Dir
	})
	return stale, nil
}

func copyImplementation(bpObj *bp.Blueprint, workPkgDir string) error {
	files, err := bpObj.TrackedFiles()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	for rel, src := range files {
		dest := filepath.Join(workPkgDir, "impl", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := copyFile(src, dest); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func formatStaleReason(info bp.StalenessInfo) string {
	switch info.Reason {
	case "files_changed":
		count := countNonBlueprintChanges(info.ChangedFiles)
		if count == 0 {
			count = len(info.ChangedFiles)
		}
		if count == 1 {
			return "1 file changed"
		}
		return fmt.Sprintf("%d files changed", count)
	case "blueprint_changed":
		return "BLUEPRINT.yaml changed"
	case "deps_api_changed":
		if len(info.ChangedDeps) == 0 {
			return "api changed in dependency"
		}
		return fmt.Sprintf("api changed in dependency: %s", strings.Join(info.ChangedDeps, ", "))
	case "deps_changed":
		if len(info.ChangedDeps) == 0 {
			return "deps changed"
		}
		return fmt.Sprintf("deps changed: %s", strings.Join(info.ChangedDeps, ", "))
	default:
		return "stale"
	}
}

func countNonBlueprintChanges(changed []string) int {
	count := 0
	for _, item := range changed {
		if item == "BLUEPRINT.yaml" {
			continue
		}
		count++
	}
	return count
}

func newSessionID() string {
	date := time.Now().Format("20060102")
	random := randomHex(4)
	return fmt.Sprintf("%s-%s", date, random)
}

func randomHex(length int) string {
	if length <= 0 {
		return ""
	}
	byteLen := int(math.Ceil(float64(length) / 2))
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)[:length]
	}
	src := mathrand.New(mathrand.NewSource(time.Now().UnixNano()))
	for i := range buf {
		buf[i] = byte(src.Intn(256))
	}
	return hex.EncodeToString(buf)[:length]
}

func listWorkSessions(workDir string) CommandResult {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		if os.IsNotExist(err) {
			return CommandResult{ExitCode: 0, Output: "No active sessions", Data: []WorkSession{}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	sessions := []WorkSession{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		manifestPath := filepath.Join(workDir, entry.Name(), "manifest.yaml")
		session, err := readWorkManifest(manifestPath)
		if err != nil {
			continue
		}
		if session.Status == "" || session.Status == "active" {
			sessions = append(sessions, session)
		}
	}
	if len(sessions) == 0 {
		return CommandResult{ExitCode: 0, Output: "No active sessions", Data: sessions}
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Created > sessions[j].Created
	})
	lines := []string{"Active sessions:"}
	for _, session := range sessions {
		age := humanizeAge(session.Created)
		lines = append(lines, fmt.Sprintf("%s  %d packages  %s ago", session.ID, len(session.Packages), age))
	}
	return CommandResult{ExitCode: 0, Output: strings.Join(lines, "\n"), Data: sessions}
}

func markWorkSessionDone(workDir, sessionID string) CommandResult {
	manifestPath := filepath.Join(workDir, sessionID, "manifest.yaml")
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
	session.Status = "done"
	if err := writeWorkManifest(manifestPath, session); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	return CommandResult{ExitCode: 0, Output: fmt.Sprintf("Session %s marked as done", sessionID), Data: session}
}

func humanizeAge(timestamp string) string {
	parsed, err := time.Parse("2006-01-02T15:04:05", timestamp)
	if err != nil {
		return "unknown"
	}
	delta := time.Since(parsed)
	if delta < 0 {
		delta = -delta
	}
	hours := int(delta.Hours())
	if hours >= 24 {
		days := hours / 24
		return fmt.Sprintf("%dd", days)
	}
	if hours >= 1 {
		return fmt.Sprintf("%dh", hours)
	}
	mins := int(delta.Minutes())
	if mins >= 1 {
		return fmt.Sprintf("%dm", mins)
	}
	return "0m"
}
