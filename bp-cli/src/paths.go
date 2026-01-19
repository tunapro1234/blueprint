package bp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func IsBlueprintFile(name string) bool {
	lower := strings.ToLower(name)
	if lower == "blueprint.yaml" || lower == ".blueprint.yaml" {
		return true
	}
	if strings.HasPrefix(lower, "blueprint.") && strings.HasSuffix(lower, ".yaml") {
		return true
	}
	if strings.HasPrefix(lower, ".blueprint.") && strings.HasSuffix(lower, ".yaml") {
		return true
	}
	if strings.HasSuffix(lower, ".bp.yaml") {
		return true
	}
	return false
}

func FindBlueprintFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !IsBlueprintFile(name) {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Slice(files, func(i, j int) bool {
		pi := blueprintPriority(filepath.Base(files[i]))
		pj := blueprintPriority(filepath.Base(files[j]))
		if pi != pj {
			return pi < pj
		}
		return files[i] < files[j]
	})
	return files, nil
}

func blueprintPriority(name string) int {
	lower := strings.ToLower(name)
	switch {
	case lower == "blueprint.yaml":
		return 0
	case lower == ".blueprint.yaml":
		return 1
	case strings.HasPrefix(lower, "blueprint.") && strings.HasSuffix(lower, ".yaml"):
		return 2
	case strings.HasPrefix(lower, ".blueprint.") && strings.HasSuffix(lower, ".yaml"):
		return 3
	case strings.HasSuffix(lower, ".bp.yaml"):
		return 4
	default:
		return 5
	}
}

func FindBlueprintFile(dir string) (string, error) {
	files, err := FindBlueprintFiles(dir)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", ErrNotBlueprint
	}
	return files[0], nil
}

func relPath(base, path string) (string, error) {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func isUnderBlueprintState(rel, stateDirRel string) bool {
	if rel == "" {
		return false
	}
	rel = filepath.ToSlash(rel)
	if stateDirRel == "" {
		stateDirRel = ".blueprint"
	}
	if stateDirRel != "" {
		stateDirRel = filepath.ToSlash(stateDirRel)
		if rel == stateDirRel || strings.HasPrefix(rel, stateDirRel+"/") {
			return true
		}
	}
	if stateDirRel != ".blueprint" {
		if rel == ".blueprint" || strings.HasPrefix(rel, ".blueprint/") {
			return true
		}
	}
	return false
}

func validateStateDirRel(rel string) error {
	trimmed := strings.TrimSpace(rel)
	if trimmed == "" {
		return fmt.Errorf("state_dir is empty")
	}
	if trimmed == "." || trimmed == ".." {
		return fmt.Errorf("state_dir must be a single directory name")
	}
	if filepath.IsAbs(trimmed) {
		return fmt.Errorf("state_dir must be a relative directory name")
	}
	if strings.ContainsAny(trimmed, `/\\`) {
		return fmt.Errorf("state_dir must be a single directory name")
	}
	return nil
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

func isDepsDirName(name string) bool {
	return name == "deps"
}

func isUnderDepsDir(rel string) bool {
	if rel == "" {
		return false
	}
	rel = filepath.ToSlash(rel)
	return rel == "deps" || strings.HasPrefix(rel, "deps/")
}

// StateDirRelFromFile reads _meta.state_dir from a blueprint file.
// It returns ".blueprint" on any error or invalid value.
func StateDirRelFromFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ".blueprint"
	}
	parsed, err := ParseYAML(data)
	if err == nil {
		if meta, ok := convertYAML(parsed["_meta"]).(map[string]interface{}); ok {
			if raw, ok := meta["state_dir"].(string); ok {
				trimmed := strings.TrimSpace(raw)
				if trimmed != "" && validateStateDirRel(trimmed) == nil {
					return trimmed
				}
			}
		}
	}
	if fallback := scanStateDirRel(data); fallback != "" {
		return fallback
	}
	return ".blueprint"
}

func scanStateDirRel(data []byte) string {
	lines := strings.Split(string(data), "\n")
	metaIndent := -1
	inMeta := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := indentLevel(line)
		if strings.HasPrefix(trimmed, "_meta:") && indent == 0 {
			inMeta = true
			metaIndent = indent
			continue
		}
		if inMeta {
			if indent <= metaIndent {
				inMeta = false
				metaIndent = -1
				continue
			}
			if strings.HasPrefix(trimmed, "state_dir:") {
				raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "state_dir:"))
				raw = strings.Trim(raw, "\"'")
				if raw != "" && validateStateDirRel(raw) == nil {
					return raw
				}
			}
		}
	}
	return ""
}

func indentLevel(line string) int {
	count := 0
	for _, r := range line {
		if r == ' ' {
			count++
			continue
		}
		if r == '\t' {
			count += 2
			continue
		}
		break
	}
	return count
}
