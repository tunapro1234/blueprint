package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	bp "blueprint"
)

type PurgeResult struct {
	ArchiveFreed   int64
	ArchiveCount   int
	SnapshotsFreed int64
	SnapshotsCount int
	TotalFreed     int64
}

func PurgeCommand(ctx CommandContext) CommandResult {
	archiveOnly, _ := ctx.Args["archive_only"].(bool)
	snapshotsOnly, _ := ctx.Args["snapshots_only"].(bool)
	pretend, _ := ctx.Args["pretend"].(bool)

	rootDir, rootBP, err := resolveNewLangRoot(ctx.Path, "")
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	rootDir = rootBP.Dir

	archiveRoot := filepath.Join(rootBP.StateDir, "archive")
	var archiveEntries []archiveEntry
	var archiveFreed int64
	var archiveCount int
	if !snapshotsOnly {
		archiveEntries, archiveFreed, archiveCount, err = collectArchiveEntries(archiveRoot)
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
	}

	var snapshotEntries []snapshotEntry
	var snapshotsFreed int64
	var snapshotsCount int
	if !archiveOnly {
		snapshotEntries, snapshotsFreed, snapshotsCount, err = collectUnusedSnapshots(rootDir)
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
	}

	totalFreed := archiveFreed + snapshotsFreed
	if archiveCount == 0 && snapshotsCount == 0 {
		return CommandResult{ExitCode: 0, Output: "Nothing to purge", Data: PurgeResult{}}
	}

	if pretend {
		output := buildPurgePreview(archiveEntries, snapshotEntries, totalFreed, archiveOnly, snapshotsOnly)
		return CommandResult{ExitCode: 0, Output: output, Data: PurgeResult{ArchiveFreed: archiveFreed, ArchiveCount: archiveCount, SnapshotsFreed: snapshotsFreed, SnapshotsCount: snapshotsCount, TotalFreed: totalFreed}}
	}

	if !snapshotsOnly && archiveCount > 0 {
		if err := os.RemoveAll(archiveRoot); err != nil && !os.IsNotExist(err) {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
	}
	if !archiveOnly && snapshotsCount > 0 {
		for _, entry := range snapshotEntries {
			for _, dir := range entry.UnusedDirs {
				if err := os.RemoveAll(dir); err != nil {
					return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
				}
			}
		}
	}

	output := buildPurgeSummary(archiveFreed, archiveCount, snapshotsFreed, snapshotsCount, totalFreed, archiveOnly, snapshotsOnly)
	return CommandResult{ExitCode: 0, Output: output, Data: PurgeResult{ArchiveFreed: archiveFreed, ArchiveCount: archiveCount, SnapshotsFreed: snapshotsFreed, SnapshotsCount: snapshotsCount, TotalFreed: totalFreed}}
}

type archiveEntry struct {
	Name       string
	Path       string
	Size       int64
	ArchivedAt string
}

type snapshotEntry struct {
	Package    string
	Size       int64
	Count      int
	KeepCount  int
	UnusedDirs []string
}

func collectArchiveEntries(archiveRoot string) ([]archiveEntry, int64, int, error) {
	entries := []archiveEntry{}
	info, err := os.Stat(archiveRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return entries, 0, 0, nil
		}
		return nil, 0, 0, err
	}
	if !info.IsDir() {
		return entries, 0, 0, nil
	}
	dirEntries, err := os.ReadDir(archiveRoot)
	if err != nil {
		return nil, 0, 0, err
	}
	var total int64
	for _, entry := range dirEntries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(archiveRoot, name)
		size, err := dirSize(path)
		if err != nil {
			return nil, 0, 0, err
		}
		meta, _ := readArchiveMeta(filepath.Join(path, "meta.yaml"))
		entries = append(entries, archiveEntry{Name: name, Path: path, Size: size, ArchivedAt: meta.ArchivedAt})
		total += size
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, total, len(entries), nil
}

func collectUnusedSnapshots(rootDir string) ([]snapshotEntry, int64, int, error) {
	tree := &bp.BlueprintTree{Root: rootDir}
	bps, err := tree.Walk()
	if err != nil {
		return nil, 0, 0, err
	}
	var entries []snapshotEntry
	var totalSize int64
	var totalCount int
	for _, bpObj := range bps {
		entry, err := collectUnusedForBlueprint(rootDir, bpObj)
		if err != nil {
			return nil, 0, 0, err
		}
		if entry.Count == 0 {
			continue
		}
		entries = append(entries, entry)
		totalSize += entry.Size
		totalCount += entry.Count
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Package < entries[j].Package })
	return entries, totalSize, totalCount, nil
}

func collectUnusedForBlueprint(rootDir string, bpObj *bp.Blueprint) (snapshotEntry, error) {
	entry := snapshotEntry{Package: formatPackagePath(rootDir, bpObj.Dir)}
	historyDir := filepath.Join(bpObj.StateDir, "history")
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return entry, nil
		}
		return entry, err
	}
	currentID, err := readCurrentSnapshotID(bpObj.StateDir)
	if err != nil && !errors.Is(err, bp.ErrNoSnapshot) {
		return entry, err
	}
	keep := 0
	if err == nil {
		keep = 1
	}
	for _, item := range entries {
		if !item.IsDir() {
			continue
		}
		name := item.Name()
		if name == currentID {
			continue
		}
		dirPath := filepath.Join(historyDir, name)
		size, err := dirSize(dirPath)
		if err != nil {
			return entry, err
		}
		entry.Size += size
		entry.Count++
		entry.UnusedDirs = append(entry.UnusedDirs, dirPath)
	}
	entry.KeepCount = keep
	return entry, nil
}

func dirSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

func formatPackagePath(rootDir, dir string) string {
	rel, err := filepath.Rel(rootDir, dir)
	if err != nil {
		rel = dir
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "./"
	}
	if !strings.HasSuffix(rel, "/") {
		rel += "/"
	}
	return rel
}

func buildPurgePreview(archives []archiveEntry, snapshots []snapshotEntry, total int64, archiveOnly, snapshotsOnly bool) string {
	var lines []string
	if !snapshotsOnly && len(archives) > 0 {
		lines = append(lines, "Archive:")
		for _, entry := range archives {
			name := entry.Name
			if !strings.HasSuffix(name, "/") {
				name += "/"
			}
			archivedAt := entry.ArchivedAt
			if archivedAt == "" {
				archivedAt = "unknown"
			}
			lines = append(lines, fmt.Sprintf("  %-10s %6s   (archived: %s)", name, formatMB(entry.Size), archivedAt))
		}
		lines = append(lines, "")
	}
	if !archiveOnly && len(snapshots) > 0 {
		lines = append(lines, "Unused snapshots:")
		for _, entry := range snapshots {
			lines = append(lines, fmt.Sprintf("  %-10s %6s   (%d snapshots, keeping %d current)", entry.Package, formatMB(entry.Size), entry.Count, entry.KeepCount))
		}
		lines = append(lines, "")
	}
	lines = append(lines, fmt.Sprintf("Total: %s would be freed", formatMB(total)))
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func buildPurgeSummary(archiveFreed int64, archiveCount int, snapshotsFreed int64, snapshotsCount int, total int64, archiveOnly, snapshotsOnly bool) string {
	var lines []string
	lines = append(lines, "Purged:")
	if !snapshotsOnly {
		lines = append(lines, fmt.Sprintf("  Archive: %s (%d languages)", formatMB(archiveFreed), archiveCount))
	}
	if !archiveOnly {
		lines = append(lines, fmt.Sprintf("  Snapshots: %s (%d snapshots)", formatMB(snapshotsFreed), snapshotsCount))
	}
	lines = append(lines, fmt.Sprintf("Total freed: %s", formatMB(total)))
	return strings.Join(lines, "\n")
}

func formatMB(bytes int64) string {
	const mb = 1024 * 1024
	if bytes <= 0 {
		return "0 MB"
	}
	val := (bytes + mb/2) / mb
	if val < 0 {
		val = 0
	}
	return fmt.Sprintf("%d MB", val)
}
