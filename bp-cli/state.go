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

type State struct {
	BlueprintHash string
	Files         map[string]string
}

func (b *Blueprint) SaveState() error {
	files, err := b.collectTrackedFiles()
	if err != nil {
		return err
	}
	state := State{Files: map[string]string{}}
	bpHash, err := HashFile(b.Path)
	if err != nil {
		return err
	}
	state.BlueprintHash = bpHash
	for rel, abs := range files {
		h, err := HashFile(abs)
		if err != nil {
			return err
		}
		state.Files[rel] = h
	}
	if err := os.MkdirAll(b.StateDir, 0o755); err != nil {
		return err
	}
	content := marshalState(&state)
	return os.WriteFile(filepath.Join(b.StateDir, "state.yaml"), []byte(content), 0o644)
}

func marshalState(state *State) string {
	var b strings.Builder
	b.WriteString("blueprint_hash: ")
	b.WriteString(state.BlueprintHash)
	b.WriteString("\nfiles:\n")
	// Sort keys for deterministic output
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
	state := &State{Files: map[string]string{}}
	if hash, ok := parsed["blueprint_hash"].(string); ok {
		state.BlueprintHash = hash
	}
	if files, ok := parsed["files"].(map[string]interface{}); ok {
		for k, v := range files {
			if s, ok := v.(string); ok {
				state.Files[k] = s
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
	bpHash, err := HashFile(b.Path)
	if err != nil {
		return nil, err
	}
	if bpHash != state.BlueprintHash {
		changed = append(changed, "BLUEPRINT.yaml")
	}
	sort.Strings(changed)
	return changed, nil
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
