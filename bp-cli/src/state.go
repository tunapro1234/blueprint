package bp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type DepState struct {
	SnapshotID string
	APIHash    string
}

type State struct {
	SnapshotID    string
	BlueprintHash string
	ImplHash      string
	Files         map[string]string
	Deps          map[string]DepState
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
		b.WriteString(k)
		b.WriteString(":")
		b.WriteString(deps[k].SnapshotID)
		b.WriteString("\n")
	}
	return HashString(b.String())
}

func ComputeSnapshotID(blueprintHash, implHash, depsHash string) string {
	combined := fmt.Sprintf("%s:%s:%s", blueprintHash, implHash, depsHash)
	sum := sha256.Sum256([]byte(combined))
	return hex.EncodeToString(sum[:])[:12]
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
			if dep.SnapshotID != "" {
				b.WriteString("    snapshot_id: ")
				b.WriteString(dep.SnapshotID)
				b.WriteString("\n")
			}
			if dep.APIHash != "" {
				b.WriteString("    api_hash: ")
				b.WriteString(dep.APIHash)
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
		Files: map[string]string{},
		Deps:  map[string]DepState{},
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
				if s, ok := sub["snapshot_id"].(string); ok {
					dep.SnapshotID = s
				}
				if s, ok := sub["api_hash"].(string); ok {
					dep.APIHash = s
				}
				if dep.SnapshotID != "" || dep.APIHash != "" {
					state.Deps[k] = dep
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
	changed := []string{}
	for rel, oldHash := range state.Files {
		abs, ok := currentFiles[rel]
		if !ok {
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
	files := map[string]string{}
	if len(entries) == 0 {
		return files, nil
	}
	visited := map[string]struct{}{}
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
			err = walkFiles(abs, visited, func(path string, info os.FileInfo) error {
				if info.IsDir() {
					return nil
				}
				rel, err := relPath(b.Dir, path)
				if err != nil {
					return err
				}
				if isUnderBlueprintState(rel) {
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
		rel, err := relPath(b.Dir, abs)
		if err != nil {
			return nil, err
		}
		if isUnderBlueprintState(rel) {
			continue
		}
		files[rel] = abs
	}
	return files, nil
}

func walkFiles(root string, visited map[string]struct{}, fn func(path string, info os.FileInfo) error) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		stat, err := os.Stat(root)
		if err != nil {
			return err
		}
		if stat.IsDir() {
			real, err := filepath.EvalSymlinks(root)
			if err != nil {
				return err
			}
			if _, ok := visited[real]; ok {
				return nil
			}
			visited[real] = struct{}{}
			return walkDirEntries(root, visited, fn)
		}
		return fn(root, stat)
	}
	if info.IsDir() {
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}
		if _, ok := visited[real]; ok {
			return nil
		}
		visited[real] = struct{}{}
		return walkDirEntries(root, visited, fn)
	}
	return fn(root, info)
}

func walkDirEntries(dir string, visited map[string]struct{}, fn func(path string, info os.FileInfo) error) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".blueprint" {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			if err := walkFiles(path, visited, fn); err != nil {
				return err
			}
			continue
		}
		if err := walkFiles(path, visited, fn); err != nil {
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
