package commands

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bp "blueprint"
)

var (
	ErrInvalidCurrentID = errors.New("invalid current snapshot id")
)

func formatPath(path string) string {
	if path == "" {
		return "."
	}
	cwd, err := os.Getwd()
	if err != nil {
		return filepath.ToSlash(path)
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "."
	}
	if !strings.HasPrefix(rel, ".") {
		return "./" + rel
	}
	return rel
}

func normalizeYAMLError(err error) string {
	msg := err.Error()
	if strings.HasPrefix(msg, "yaml: ") {
		return strings.TrimPrefix(msg, "yaml: ")
	}
	if strings.HasPrefix(strings.ToLower(msg), "yaml parse error at line") {
		idx := strings.Index(msg, "line")
		if idx >= 0 {
			return msg[idx:]
		}
	}
	return msg
}

func resolveBlueprintPath(path string) (string, error) {
	if path == "" {
		path = "."
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return bp.FindBlueprintFile(path)
	}
	return path, nil
}

func findBlueprintsRecursive(root string) ([]string, error) {
	if root == "" {
		root = "."
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		root = filepath.Dir(root)
	}
	var files []string
	stateDirs := map[string]string{}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			parent := filepath.Dir(path)
			if stateDirRel, ok := stateDirs[parent]; ok {
				if isStateDirName(d.Name(), stateDirRel) {
					return filepath.SkipDir
				}
			} else if d.Name() == ".blueprint" {
				return filepath.SkipDir
			}
			bps, err := bp.FindBlueprintFiles(path)
			if err != nil {
				return err
			}
			if len(bps) > 0 {
				files = append(files, bps[0])
				stateDirs[path] = bp.StateDirRelFromFile(bps[0])
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func isStateDirName(name, stateDirRel string) bool {
	if stateDirRel == "" {
		stateDirRel = ".blueprint"
	}
	if name == stateDirRel {
		return true
	}
	if stateDirRel != ".blueprint" && name == ".blueprint" {
		return true
	}
	return false
}

func readCurrentSnapshotID(stateDir string) (string, error) {
	currentPath := filepath.Join(stateDir, "current")
	data, err := os.ReadFile(currentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", bp.ErrNoSnapshot
		}
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", bp.ErrNoSnapshot
	}
	if !bp.IsSnapshotID(id) {
		return "", ErrInvalidCurrentID
	}
	return id, nil
}

func resolveSnapshotID(stateDir, input string) (string, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return "", fmt.Errorf("Snapshot #%s not found", input)
	}
	if strings.EqualFold(raw, "current") {
		return "current", nil
	}
	id := strings.ToLower(raw)
	if bp.IsSnapshotID(id) {
		hasHistory, err := hasSnapshotHistory(stateDir)
		if err != nil {
			return "", err
		}
		if !hasHistory {
			return "", bp.ErrNoSnapshot
		}
		if _, err := os.Stat(snapshotDir(stateDir, id)); err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("Snapshot #%s not found", raw)
			}
			return "", err
		}
		return id, nil
	}
	if len(id) < len("20060102") {
		return "", fmt.Errorf("Snapshot #%s not found", raw)
	}
	matches, err := matchSnapshotPrefix(stateDir, id)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("Snapshot #%s not found", raw)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("Snapshot #%s is ambiguous", raw)
	}
	return matches[0], nil
}

func matchSnapshotPrefix(stateDir, prefix string) ([]string, error) {
	historyDir := filepath.Join(stateDir, "history")
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, bp.ErrNoSnapshot
		}
		return nil, err
	}
	matches := []string{}
	found := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !bp.IsSnapshotID(name) {
			continue
		}
		found = true
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			matches = append(matches, name)
		}
	}
	if !found {
		return nil, bp.ErrNoSnapshot
	}
	sort.Strings(matches)
	return matches, nil
}

func hasSnapshotHistory(stateDir string) (bool, error) {
	historyDir := filepath.Join(stateDir, "history")
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if bp.IsSnapshotID(entry.Name()) {
			return true, nil
		}
	}
	return false, nil
}

func updateSnapshotImports(bpObj *bp.Blueprint, depDir, depStateDirRel, fromID, toID string) (int, string, error) {
	if fromID == "" || toID == "" || fromID == toID {
		return 0, "", nil
	}
	hidden, err := bp.IsImplHidden(bpObj.StateDir)
	if err != nil {
		return 0, "", err
	}
	if hidden {
		return 0, "working tree hidden; run 'bp impl' to update imports", nil
	}
	modRoot, modPath, ok, err := findGoModuleRoot(bpObj.Dir)
	if err != nil {
		return 0, "", err
	}
	if !ok || strings.TrimSpace(modPath) == "" {
		return 0, "go.mod not found; skipping import update", nil
	}
	rel, err := filepath.Rel(modRoot, depDir)
	if err != nil {
		return 0, "dependency path not under module root; skipping import update", nil
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || strings.HasPrefix(rel, "..") {
		return 0, "dependency path not under module root; skipping import update", nil
	}
	if depStateDirRel == "" {
		depStateDirRel = ".blueprint"
	}
	oldImport := path.Join(modPath, rel, depStateDirRel, "history", fromID, "impl")
	newImport := path.Join(modPath, rel, depStateDirRel, "history", toID, "impl")
	if oldImport == newImport {
		return 0, "", nil
	}

	files, err := bpObj.TrackedFiles()
	if err != nil {
		return 0, "", err
	}
	nestedDirs, err := nestedBlueprintDirs(bpObj.Dir)
	if err != nil {
		return 0, "", err
	}
	updated := 0
	for relPath, abs := range files {
		if !strings.HasSuffix(relPath, ".go") {
			continue
		}
		if containsStateDirSegment(relPath, depStateDirRel) {
			continue
		}
		if isUnderAnyDir(abs, nestedDirs) {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return updated, "", err
		}
		if !bytes.Contains(data, []byte(oldImport)) {
			continue
		}
		newData := bytes.ReplaceAll(data, []byte(oldImport), []byte(newImport))
		info, err := os.Stat(abs)
		if err != nil {
			return updated, "", err
		}
		if err := os.WriteFile(abs, newData, info.Mode().Perm()); err != nil {
			return updated, "", err
		}
		updated++
	}
	return updated, "", nil
}

func findGoModuleRoot(start string) (string, string, bool, error) {
	dir := start
	for {
		goModPath := filepath.Join(dir, "go.mod")
		data, err := os.ReadFile(goModPath)
		if err == nil {
			modulePath, err := parseGoModulePath(data)
			if err != nil {
				return "", "", false, err
			}
			if modulePath == "" {
				return dir, "", false, nil
			}
			return dir, modulePath, true, nil
		}
		if !os.IsNotExist(err) {
			return "", "", false, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", "", false, nil
}

func nestedBlueprintDirs(root string) ([]string, error) {
	files, err := findBlueprintsRecursive(root)
	if err != nil {
		return nil, err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dirs := map[string]struct{}{}
	for _, file := range files {
		dir := filepath.Dir(file)
		dirAbs, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		if pathsEqual(dirAbs, rootAbs) {
			continue
		}
		dirs[dirAbs] = struct{}{}
	}
	out := make([]string, 0, len(dirs))
	for dir := range dirs {
		out = append(out, dir)
	}
	return out, nil
}

func isUnderAnyDir(path string, dirs []string) bool {
	for _, dir := range dirs {
		if isUnderDir(path, dir) {
			return true
		}
	}
	return false
}

func isUnderDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." {
		return false
	}
	prefix := ".." + string(os.PathSeparator)
	if strings.HasPrefix(rel, prefix) {
		return false
	}
	return true
}

func containsStateDirSegment(relPath, stateDirRel string) bool {
	if relPath == "" {
		return false
	}
	if stateDirRel == "" {
		stateDirRel = ".blueprint"
	}
	relPath = filepath.ToSlash(relPath)
	parts := strings.Split(relPath, "/")
	for _, part := range parts {
		if part == stateDirRel || part == ".blueprint" {
			return true
		}
	}
	return false
}

func pathsEqual(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func parseGoModulePath(data []byte) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}

func formatTimestamp(ts string) string {
	parsed, err := time.Parse("2006-01-02T15:04:05", ts)
	if err != nil {
		if len(ts) >= 16 {
			return strings.ReplaceAll(ts[:16], "T", " ")
		}
		return ts
	}
	return parsed.Format("2006-01-02 15:04")
}

func snapshotDir(stateDir, id string) string {
	return filepath.Join(stateDir, "history", id)
}

func snapshotBlueprintPath(stateDir, id string) string {
	return filepath.Join(snapshotDir(stateDir, id), "BLUEPRINT.yaml")
}

func compareIDs(a, b string) int {
	if len(a) != len(b) {
		return strings.Compare(a, b)
	}
	return strings.Compare(a, b)
}

func resolveMessage(defaultText string, message any) string {
	if s, ok := message.(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return defaultText
}

func formatErr(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%v", err)
}
