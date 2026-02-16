package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	bp "blueprint"
)

func RemoveLangCommand(ctx CommandContext) CommandResult {
	lang, _ := ctx.Args["lang"].(string)
	lang = strings.TrimSpace(lang)
	if lang == "" {
		msg := "Language is required"
		return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
	}
	rootDir, rootBP, err := resolveNewLangRoot(ctx.Path, "")
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	rootDir = rootBP.Dir

	targetRoot, err := resolveExistingLangRoot(rootDir, rootBP, lang)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if targetRoot.Path == "" {
		msg := fmt.Sprintf("Language root not found: %s", lang)
		return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
	}
	targetRoot.Name = ensureLangName(targetRoot)

	roots, err := loadLanguageRoots(rootDir, rootBP)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	inRoots := false
	for _, root := range roots {
		if filepath.Clean(root.Path) == filepath.Clean(targetRoot.Path) {
			inRoots = true
			break
		}
	}
	if !inRoots {
		roots = append(roots, targetRoot)
	}
	remaining := 0
	for _, root := range roots {
		if filepath.Clean(root.Path) == filepath.Clean(targetRoot.Path) {
			continue
		}
		remaining++
	}
	if remaining == 0 {
		msg := "Cannot remove last language"
		return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
	}

	ssResult := SsCommand(CommandContext{Path: targetRoot.Path, Args: map[string]any{}})
	if ssResult.ExitCode != 0 {
		return CommandResult{ExitCode: 1, Output: ssResult.Output, Errors: ssResult.Errors}
	}
	snapshotID := ""
	if info, ok := ssResult.Data.(SnapshotInfo); ok {
		snapshotID = info.ID
	}
	if snapshotID == "" {
		if bpObj, err := bp.LoadBlueprint(targetRoot.Path); err == nil {
			if id, err := readCurrentSnapshotID(bpObj.StateDir); err == nil {
				snapshotID = id
			}
		}
	}

	archiveRoot := filepath.Join(rootBP.StateDir, "archive")
	if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	archiveName := filepath.Base(targetRoot.Path)
	archivePath := filepath.Join(archiveRoot, archiveName)
	if _, err := os.Stat(archivePath); err == nil {
		msg := fmt.Sprintf("Archive already exists: %s", archiveName)
		return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
	} else if !os.IsNotExist(err) {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := os.Rename(targetRoot.Path, archivePath); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	meta := archiveMeta{
		ArchivedAt:   time.Now().Format("2006-01-02T15:04:05"),
		LastSnapshot: snapshotID,
		Reason:       fmt.Sprintf("bp remove-lang %s", lang),
	}
	if err := writeArchiveMeta(filepath.Join(archivePath, "meta.yaml"), meta); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := updateLanguageLists(rootBP, rootDir, targetRoot.Name, targetRoot.Path, false); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	output := fmt.Sprintf("Archived: %s → %s\nLast snapshot: %s", formatRelPath(rootDir, targetRoot.Path), formatRelPath(rootDir, archivePath), snapshotID)
	return CommandResult{ExitCode: 0, Output: output}
}
