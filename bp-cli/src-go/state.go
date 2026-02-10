package bp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type DepState struct {
	Pinned        string
	Latest        string
	APIHash       string
	LatestAPIHash string
	APIChanged    bool
	Rotten        bool
}

type DepRef struct {
	Using string
}

type State struct {
	SnapshotID    string
	BlueprintHash string
	ImplHash      string
	Files         map[string]string
	Deps          map[string]DepState
	Dependents    map[string]DepRef
}

func (b *Blueprint) ComputeFileHashes() (map[string]string, error) {
	files, err := b.collectTrackedFiles()
	if err != nil {
		return nil, err
	}
	hashes := map[string]string{}
	for rel, abs := range files {
		h, err := HashFile(abs)
		if err != nil {
			return nil, err
		}
		hashes[rel] = h
	}
	return hashes, nil
}

func ComputeImplHash(files map[string]string) string {
	if len(files) == 0 {
		return HashString("")
	}
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(":")
		b.WriteString(files[k])
		b.WriteString("\n")
	}
	return HashString(b.String())
}

func ComputeDepsHash(deps map[string]DepState) string {
	if len(deps) == 0 {
		return HashString("")
	}
	keys := make([]string, 0, len(deps))
	for k := range deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		dep := deps[k]
		id := dep.Pinned
		if id == "" {
			id = dep.Latest
		}
		if id == "" {
			continue
		}
		b.WriteString(k)
		b.WriteString(":")
		b.WriteString(id)
		b.WriteString("\n")
	}
	return HashString(b.String())
}

func ComputeContentHash(blueprintHash, implHash, depsHash string) string {
	combined := fmt.Sprintf("%s:%s:%s", blueprintHash, implHash, depsHash)
	return HashString(combined)
}

func BuildSnapshotID(contentHash, message string) string {
	trimmed := strings.TrimSpace(message)
	hash := strings.TrimPrefix(contentHash, "sha256:")
	if trimmed == "" {
		return fmt.Sprintf("ss-%s", shortHashN(hash, 8))
	}
	return fmt.Sprintf("%s-%s", slugify(trimmed), shortHashN(hash, 4))
}

func shortHashN(hash string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(hash) >= n {
		return hash[:n]
	}
	return hash
}

func slugify(message string) string {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return "snapshot"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if r == ' ' || r == '-' || r == '_' || r == '\t' || r == '\n' || r == '\r' {
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		} else {
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
		if b.Len() >= 20 {
			break
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "snapshot"
	}
	if len(slug) > 20 {
		slug = strings.Trim(slug[:20], "-")
	}
	if slug == "" {
		return "snapshot"
	}
	return slug
}

func HashString(data string) string {
	sum := sha256.Sum256([]byte(data))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (b *Blueprint) SaveState(state *State) error {
	if state == nil {
		return fmt.Errorf("state is nil")
	}
	if state.Files == nil {
		state.Files = map[string]string{}
	}
	if state.Deps == nil {
		state.Deps = map[string]DepState{}
	}
	if state.Dependents == nil {
		state.Dependents = map[string]DepRef{}
	}
	if err := os.MkdirAll(b.StateDir, 0o755); err != nil {
		return err
	}
	content := marshalState(state)
	return os.WriteFile(filepath.Join(b.StateDir, "state.yaml"), []byte(content), 0o644)
}

func marshalState(state *State) string {
	var b strings.Builder
	if state.SnapshotID != "" {
		b.WriteString("snapshot_id: ")
		b.WriteString(state.SnapshotID)
		b.WriteString("\n")
	}
	if state.BlueprintHash != "" {
		b.WriteString("blueprint_hash: ")
		b.WriteString(state.BlueprintHash)
		b.WriteString("\n")
	}
	if state.ImplHash != "" {
		b.WriteString("impl_hash: ")
		b.WriteString(state.ImplHash)
		b.WriteString("\n")
	}
	b.WriteString("files:\n")
	keys := make([]string, 0, len(state.Files))
	for k := range state.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("  \"")
		b.WriteString(k)
		b.WriteString("\": ")
		b.WriteString(state.Files[k])
		b.WriteString("\n")
	}
	if len(state.Deps) > 0 {
		b.WriteString("deps:\n")
		depKeys := make([]string, 0, len(state.Deps))
		for k := range state.Deps {
			depKeys = append(depKeys, k)
		}
		sort.Strings(depKeys)
		for _, k := range depKeys {
			dep := state.Deps[k]
			b.WriteString("  \"")
			b.WriteString(k)
			b.WriteString("\":\n")
			if dep.Pinned != "" {
				b.WriteString("    pinned: ")
				b.WriteString(dep.Pinned)
				b.WriteString("\n")
			}
			if dep.Latest != "" {
				b.WriteString("    latest: ")
				b.WriteString(dep.Latest)
				b.WriteString("\n")
			}
			if dep.APIHash != "" {
				b.WriteString("    api_hash: ")
				b.WriteString(dep.APIHash)
				b.WriteString("\n")
			}
			if dep.LatestAPIHash != "" {
				b.WriteString("    latest_api_hash: ")
				b.WriteString(dep.LatestAPIHash)
				b.WriteString("\n")
			}
			b.WriteString("    api_changed: ")
			b.WriteString(strconv.FormatBool(dep.APIChanged))
			b.WriteString("\n")
			b.WriteString("    rotten: ")
			b.WriteString(strconv.FormatBool(dep.Rotten))
			b.WriteString("\n")
		}
	}
	if len(state.Dependents) > 0 {
		b.WriteString("dependents:\n")
		depKeys := make([]string, 0, len(state.Dependents))
		for k := range state.Dependents {
			depKeys = append(depKeys, k)
		}
		sort.Strings(depKeys)
		for _, k := range depKeys {
			ref := state.Dependents[k]
			b.WriteString("  \"")
			b.WriteString(k)
			b.WriteString("\":\n")
			if ref.Using != "" {
				b.WriteString("    using: ")
				b.WriteString(ref.Using)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

func LoadState(statePath string) (*State, error) {
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoSnapshot
		}
		return nil, err
	}
	parsed, err := ParseYAML(data)
	if err != nil {
		return nil, err
	}
	state := &State{
		Files:      map[string]string{},
		Deps:       map[string]DepState{},
		Dependents: map[string]DepRef{},
	}
	if hash, ok := parsed["snapshot_id"].(string); ok {
		state.SnapshotID = hash
	}
	if hash, ok := parsed["blueprint_hash"].(string); ok {
		state.BlueprintHash = hash
	}
	if hash, ok := parsed["impl_hash"].(string); ok {
		state.ImplHash = hash
	}
	if filesRaw, ok := parsed["files"]; ok {
		if filesMap, ok := convertYAML(filesRaw).(map[string]interface{}); ok {
			for k, v := range filesMap {
				if s, ok := v.(string); ok {
					state.Files[k] = s
				}
			}
		}
	}
	if depsRaw, ok := parsed["deps"]; ok {
		if depsMap, ok := convertYAML(depsRaw).(map[string]interface{}); ok {
			for k, v := range depsMap {
				sub, ok := convertYAML(v).(map[string]interface{})
				if !ok {
					continue
				}
				dep := DepState{}
				if s, ok := sub["pinned"].(string); ok {
					dep.Pinned = s
				}
				if s, ok := sub["snapshot_id"].(string); ok && dep.Pinned == "" {
					dep.Pinned = s
				}
				if s, ok := sub["latest"].(string); ok {
					dep.Latest = s
				}
				if s, ok := sub["api_hash"].(string); ok {
					dep.APIHash = s
				}
				if s, ok := sub["latest_api_hash"].(string); ok {
					dep.LatestAPIHash = s
				}
				if dep.Latest == "" {
					dep.Latest = dep.Pinned
				}
				if dep.LatestAPIHash == "" {
					dep.LatestAPIHash = dep.APIHash
				}
				if bval, ok := sub["api_changed"].(bool); ok {
					dep.APIChanged = bval
				} else if dep.APIHash != "" && dep.LatestAPIHash != "" && dep.APIHash != dep.LatestAPIHash {
					dep.APIChanged = true
				}
				if bval, ok := sub["rotten"].(bool); ok {
					dep.Rotten = bval
				}
				if dep.Pinned != "" || dep.Latest != "" || dep.APIHash != "" || dep.LatestAPIHash != "" {
					state.Deps[k] = dep
				}
			}
		}
	}
	if depsRaw, ok := parsed["dependents"]; ok {
		if depsMap, ok := convertYAML(depsRaw).(map[string]interface{}); ok {
			for k, v := range depsMap {
				sub, ok := convertYAML(v).(map[string]interface{})
				if !ok {
					continue
				}
				ref := DepRef{}
				if s, ok := sub["using"].(string); ok {
					ref.Using = s
				}
				if ref.Using != "" {
					state.Dependents[k] = ref
				}
			}
		}
	}
	return state, nil
}

func (b *Blueprint) GetChangedFiles() ([]string, error) {
	statePath := filepath.Join(b.StateDir, "state.yaml")
	state, err := LoadState(statePath)
	if err != nil {
		return nil, err
	}
	return changedFilesFromState(b, state)
}

func HashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func changedFilesFromState(b *Blueprint, state *State) ([]string, error) {
	currentFiles, err := b.collectTrackedFiles()
	if err != nil {
		return nil, err
	}
	ignoreDeleted := false
	if b.Mode() == "hide" {
		locked, err := HasImplLock(b.StateDir)
		if err != nil {
			return nil, err
		}
		if !locked {
			ignoreDeleted = true
		}
	}
	changed := []string{}
	for rel, oldHash := range state.Files {
		abs, ok := currentFiles[rel]
		if !ok {
			if ignoreDeleted {
				continue
			}
			changed = append(changed, "deleted: "+rel)
			continue
		}
		newHash, err := HashFile(abs)
		if err != nil {
			return nil, err
		}
		if newHash != oldHash {
			changed = append(changed, rel)
		}
	}
	for rel := range currentFiles {
		if _, ok := state.Files[rel]; !ok {
			changed = append(changed, "new: "+rel)
		}
	}
	if state.BlueprintHash != "" {
		bpHash, err := HashFile(b.Path)
		if err != nil {
			return nil, err
		}
		if bpHash != state.BlueprintHash {
			changed = append(changed, "BLUEPRINT.yaml")
		}
	}
	sort.Strings(changed)
	return changed, nil
}

func (b *Blueprint) collectTrackedFiles() (map[string]string, error) {
	entries, err := b.ImplementationStructure()
	if err != nil {
		return nil, err
	}
	stateDirRel := b.StateDirRel
	if stateDirRel == "" {
		stateDirRel = ".bp"
	}
	files := map[string]string{}
	visited := map[string]struct{}{}
	seenExisting := false
	if len(entries) == 0 {
		return collectFallbackFiles(b.Dir, stateDirRel, visited)
	}
	for _, entry := range entries {
		if entry.Path == "" {
			continue
		}
		if filepath.IsAbs(entry.Path) {
			return nil, fmt.Errorf("absolute paths are not allowed: %s", entry.Path)
		}
		clean := filepath.Clean(entry.Path)
		abs := filepath.Join(b.Dir, clean)
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		seenExisting = true
		isDir := entry.DirHint || info.IsDir()
		if isDir {
			hasBlueprint := entry.HasBlueprint
			if hasBlueprint == nil {
				if _, err := FindBlueprintFile(abs); err == nil {
					val := true
					hasBlueprint = &val
				}
			}
			if hasBlueprint != nil && *hasBlueprint {
				continue
			}
			err = walkFiles(abs, stateDirRel, visited, func(path string, info os.FileInfo) error {
				if info.IsDir() {
					return nil
				}
				name := info.Name()
				if shouldSkipFileName(name) {
					return nil
				}
				rel, err := relPath(b.Dir, path)
				if err != nil {
					return err
				}
				if isUnderDepsDir(rel) {
					return nil
				}
				if isUnderBlueprintState(rel, stateDirRel) {
					return nil
				}
				files[rel] = path
				return nil
			})
			if err != nil {
				return nil, err
			}
			continue
		}
		name := info.Name()
		if shouldSkipFileName(name) {
			continue
		}
		rel, err := relPath(b.Dir, abs)
		if err != nil {
			return nil, err
		}
		if isUnderDepsDir(rel) {
			continue
		}
		if isUnderBlueprintState(rel, stateDirRel) {
			continue
		}
		files[rel] = abs
	}
	if len(files) == 0 && !seenExisting {
		return collectFallbackFiles(b.Dir, stateDirRel, visited)
	}
	return files, nil
}

func collectFallbackFiles(root, stateDirRel string, visited map[string]struct{}) (map[string]string, error) {
	files := map[string]string{}
	err := walkFiles(root, stateDirRel, visited, func(path string, info os.FileInfo) error {
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if shouldSkipFileName(name) {
			return nil
		}
		rel, err := relPath(root, path)
		if err != nil {
			return err
		}
		if isUnderDepsDir(rel) {
			return nil
		}
		if isUnderBlueprintState(rel, stateDirRel) {
			return nil
		}
		files[rel] = path
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func shouldSkipDir(name, stateDirRel string) bool {
	if isStateDirName(name, stateDirRel) {
		return true
	}
	switch name {
	case ".git", "node_modules", "__pycache__", "deps":
		return true
	default:
		return false
	}
}

func shouldSkipFileName(name string) bool {
	if name == "" {
		return false
	}
	lower := strings.ToLower(name)
	if IsBlueprintFile(name) {
		return true
	}
	if strings.HasSuffix(lower, "_test.go") {
		return true
	}
	return false
}

func walkFiles(root, stateDirRel string, visited map[string]struct{}, fn func(path string, info os.FileInfo) error) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(root); err == nil {
			if isDependencySnapshotPath(target) {
				return nil
			}
		}
		stat, err := os.Stat(root)
		if err != nil {
			return err
		}
		if stat.IsDir() {
			real, err := filepath.EvalSymlinks(root)
			if err != nil {
				return err
			}
			if isDependencySnapshotPath(real) {
				return nil
			}
			if _, ok := visited[real]; ok {
				return nil
			}
			visited[real] = struct{}{}
			return walkDirEntries(root, stateDirRel, visited, fn)
		}
		if real, err := filepath.EvalSymlinks(root); err == nil {
			if isDependencySnapshotPath(real) {
				return nil
			}
		}
		if shouldSkipFileName(stat.Name()) {
			return nil
		}
		return fn(root, stat)
	}
	if info.IsDir() {
		if shouldSkipDir(info.Name(), stateDirRel) {
			return nil
		}
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}
		if _, ok := visited[real]; ok {
			return nil
		}
		visited[real] = struct{}{}
		return walkDirEntries(root, stateDirRel, visited, fn)
	}
	if shouldSkipFileName(info.Name()) {
		return nil
	}
	return fn(root, info)
}

func walkDirEntries(dir, stateDirRel string, visited map[string]struct{}, fn func(path string, info os.FileInfo) error) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if shouldSkipDir(name, stateDirRel) {
			continue
		}
		if !entry.IsDir() && shouldSkipFileName(name) {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			if err := walkFiles(path, stateDirRel, visited, fn); err != nil {
				return err
			}
			continue
		}
		if err := walkFiles(path, stateDirRel, visited, fn); err != nil {
			return err
		}
	}
	return nil
}

func detectHasBlueprintOverride(entries []StructureEntry) map[string]*bool {
	out := map[string]*bool{}
	for _, entry := range entries {
		if entry.Path == "" {
			continue
		}
		if entry.HasBlueprint == nil {
			continue
		}
		clean := filepath.Clean(entry.Path)
		if strings.HasSuffix(entry.Path, "/") || strings.HasSuffix(entry.Path, string(os.PathSeparator)) {
			out[clean] = entry.HasBlueprint
		}
	}
	return out
}

func (b *Blueprint) resolveHasBlueprintOverrides() (map[string]*bool, error) {
	entries, err := b.ImplementationStructure()
	if err != nil {
		return nil, err
	}
	return detectHasBlueprintOverride(entries), nil
}
