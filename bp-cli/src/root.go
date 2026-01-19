package bp

import (
	"os"
	"path/filepath"
	"strings"
)

// ResolveRootDir walks up from start and returns the root blueprint directory.
// If no _meta.root is found, the topmost blueprint directory is returned.
// If no blueprint is found at all, the start directory is returned.
func ResolveRootDir(start string) (string, error) {
	if start == "" {
		start = "."
	}
	info, err := os.Stat(start)
	if err != nil {
		return "", err
	}
	dir := start
	if !info.IsDir() {
		dir = filepath.Dir(start)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	topmost := ""
	current := abs
	for {
		bpPath, err := FindBlueprintFile(current)
		if err == nil {
			topmost = current
			rootFlag, _ := readRootFlag(bpPath)
			if rootFlag {
				return current, nil
			}
		} else if err != ErrNotBlueprint {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	if topmost != "" {
		return topmost, nil
	}
	return abs, nil
}

func readRootFlag(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	parsed, err := ParseYAML(data)
	if err != nil {
		// Ignore parse errors for root resolution.
		return false, nil
	}
	metaRaw, ok := parsed["_meta"]
	if !ok || metaRaw == nil {
		return false, nil
	}
	meta, ok := convertYAML(metaRaw).(map[string]interface{})
	if !ok {
		return false, nil
	}
	if rootVal, ok := meta["root"]; ok {
		if b, ok := rootVal.(bool); ok {
			return b, nil
		}
		if s, ok := rootVal.(string); ok {
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "true", "yes", "1":
				return true, nil
			}
		}
	}
	return false, nil
}
