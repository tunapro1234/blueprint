package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bp "blueprint"
)

func RestoreLangCommand(ctx CommandContext) CommandResult {
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
	archiveRoot := filepath.Join(rootBP.StateDir, "archive")
	if _, err := os.Stat(archiveRoot); err != nil {
		if os.IsNotExist(err) {
			msg := fmt.Sprintf("No archived language found: %s", lang)
			return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	archivePath, targetRoot, langName, err := resolveArchivePaths(rootDir, rootBP, archiveRoot, lang)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if _, err := os.Stat(targetRoot); err == nil {
		msg := fmt.Sprintf("Language root already exists: %s", formatRelPath(rootDir, targetRoot))
		return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
	} else if !os.IsNotExist(err) {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	meta, _ := readArchiveMeta(filepath.Join(archivePath, "meta.yaml"))

	if err := os.Rename(archivePath, targetRoot); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	if meta.LastSnapshot != "" {
		if bpObj, err := bp.LoadBlueprint(targetRoot); err == nil {
			_ = os.WriteFile(filepath.Join(bpObj.StateDir, "current"), []byte(meta.LastSnapshot), 0o644)
		} else {
			_ = os.WriteFile(filepath.Join(targetRoot, ".bp", "current"), []byte(meta.LastSnapshot), 0o644)
		}
	} else {
		if bpObj, err := bp.LoadBlueprint(targetRoot); err == nil {
			if current, err := readCurrentSnapshotID(bpObj.StateDir); err == nil {
				meta.LastSnapshot = current
			}
		}
	}
	_ = os.Remove(filepath.Join(targetRoot, "meta.yaml"))

	if err := updateLanguageLists(rootBP, rootDir, langName, targetRoot, true); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	output := fmt.Sprintf("Restored: %s → %s\nCurrent snapshot: %s", formatRelPath(rootDir, archivePath), formatRelPath(rootDir, targetRoot), meta.LastSnapshot)
	return CommandResult{ExitCode: 0, Output: output}
}

func resolveArchivePaths(rootDir string, rootBP *bp.Blueprint, archiveRoot, lang string) (string, string, string, error) {
	if lang == "" {
		return "", "", "", fmt.Errorf("No archived language found: %s", lang)
	}
	candidate := filepath.Join(archiveRoot, lang)
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		target := filepath.Join(rootDir, lang)
		name := deriveLanguageName(filepath.Base(target), "src-*")
		return candidate, target, name, nil
	}
	if rootBP != nil {
		if target, _ := resolveTargetLangRoot(rootDir, rootBP, lang, ""); target != "" {
			candidate = filepath.Join(archiveRoot, filepath.Base(target))
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				name := deriveLanguageName(filepath.Base(target), "src-*")
				return candidate, target, name, nil
			}
		}
	}
	if looksLikePath(lang) {
		target := lang
		if !filepath.IsAbs(target) {
			target = filepath.Join(rootDir, target)
		}
		candidate = filepath.Join(archiveRoot, filepath.Base(target))
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			name := deriveLanguageName(filepath.Base(target), "src-*")
			return candidate, target, name, nil
		}
	}
	return "", "", "", fmt.Errorf("No archived language found: %s", lang)
}
