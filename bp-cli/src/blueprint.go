package bp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Blueprint struct {
	Path        string
	Dir         string
	Data        map[string]interface{}
	StateDir    string
	StateDirRel string
}

type ValidationResult struct {
	Valid    bool
	Errors   []string
	Warnings []string
}

type StructureEntry struct {
	Path         string
	HasBlueprint *bool
	DirHint      bool
}

func LoadBlueprint(path string) (*Blueprint, error) {
	if path == "" {
		path = "."
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		bpPath, err := FindBlueprintFile(path)
		if err != nil {
			return nil, err
		}
		path = bpPath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dataBytes, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	data, err := ParseYAML(dataBytes)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)
	stateDirRel := ".blueprint"
	if metaRaw, ok := data["_meta"]; ok {
		if meta, ok := convertYAML(metaRaw).(map[string]interface{}); ok {
			if raw, ok := meta["state_dir"].(string); ok {
				trimmed := strings.TrimSpace(raw)
				if trimmed != "" {
					if err := validateStateDirRel(trimmed); err != nil {
						return nil, err
					}
					stateDirRel = trimmed
				}
			}
		}
	}
	return &Blueprint{
		Path:        abs,
		Dir:         dir,
		Data:        data,
		StateDir:    filepath.Join(dir, stateDirRel),
		StateDirRel: stateDirRel,
	}, nil
}

func (b *Blueprint) Validate() ValidationResult {
	result := ValidationResult{}
	meta, _ := b.Data["_meta"].(map[string]interface{})
	if meta == nil {
		result.Errors = append(result.Errors, "missing _meta.version")
	} else {
		if _, ok := meta["version"]; !ok {
			result.Errors = append(result.Errors, "missing _meta.version")
		}
	}
	// Spec dosyaları için (type: spec) intent zorunlu değil
	isSpec := false
	if meta != nil {
		if t, ok := meta["type"].(string); ok && t == "spec" {
			isSpec = true
		}
	}
	if !isSpec {
		if _, ok := b.Data["intent"]; !ok {
			result.Warnings = append(result.Warnings, "missing intent")
		}
	}
	result.Valid = len(result.Errors) == 0
	return result
}

func (b *Blueprint) GetSection(section string) (map[string]interface{}, error) {
	val, ok := b.Data[section]
	if !ok || val == nil {
		return nil, nil
	}
	switch t := val.(type) {
	case map[string]interface{}:
		return t, nil
	case map[interface{}]interface{}:
		converted := convertYAML(t)
		if convertedMap, ok := converted.(map[string]interface{}); ok {
			return convertedMap, nil
		}
		return nil, ErrInvalidSection
	case string:
		return b.loadSectionFromRef(section, t)
	default:
		return nil, ErrInvalidSection
	}
}

func (b *Blueprint) loadSectionFromRef(section, ref string) (map[string]interface{}, error) {
	path := ref
	anchor := ""
	if strings.Contains(ref, "#") {
		parts := strings.SplitN(ref, "#", 2)
		path = parts[0]
		anchor = parts[1]
	}
	if path == "" {
		return nil, fmt.Errorf("invalid section reference")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(b.Dir, path)
	}
	dataBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data, err := ParseYAML(dataBytes)
	if err != nil {
		return nil, err
	}
	var selected interface{}
	if anchor != "" {
		selected = data[anchor]
		if selected == nil {
			return nil, fmt.Errorf("Section '#%s' not found in file", anchor)
		}
	} else {
		if v, ok := data[section]; ok {
			selected = v
		} else {
			selected = data
		}
	}
	switch s := selected.(type) {
	case map[string]interface{}:
		return s, nil
	case map[interface{}]interface{}:
		converted := convertYAML(s)
		if convertedMap, ok := converted.(map[string]interface{}); ok {
			return convertedMap, nil
		}
		return nil, ErrInvalidSection
	default:
		return nil, ErrInvalidSection
	}
}

func (b *Blueprint) ImplementationStructure() ([]StructureEntry, error) {
	section, err := b.GetSection("implementation")
	if err != nil {
		return nil, err
	}
	if section == nil {
		return nil, nil
	}
	structureVal, ok := section["structure"]
	if !ok || structureVal == nil {
		return nil, nil
	}
	items, ok := structureVal.([]interface{})
	if !ok {
		return nil, fmt.Errorf("implementation.structure is not a list")
	}
	entries := make([]StructureEntry, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case string:
			entries = append(entries, StructureEntry{Path: v, DirHint: strings.HasSuffix(v, "/")})
		case map[string]interface{}:
			for k, raw := range v {
				entry := StructureEntry{Path: k, DirHint: strings.HasSuffix(k, "/")}
				if attrs, ok := raw.(map[string]interface{}); ok {
					if hb, ok := attrs["has_blueprint"]; ok {
						if bval, ok := hb.(bool); ok {
							entry.HasBlueprint = &bval
						}
					}
				}
				entries = append(entries, entry)
			}
		case map[interface{}]interface{}:
			converted := convertYAML(v)
			if cm, ok := converted.(map[string]interface{}); ok {
				for k, raw := range cm {
					entry := StructureEntry{Path: k, DirHint: strings.HasSuffix(k, "/")}
					if attrs, ok := raw.(map[string]interface{}); ok {
						if hb, ok := attrs["has_blueprint"]; ok {
							if bval, ok := hb.(bool); ok {
								entry.HasBlueprint = &bval
							}
						}
					}
					entries = append(entries, entry)
				}
			}
		default:
			return nil, fmt.Errorf("unsupported structure entry")
		}
	}
	return entries, nil
}

func (b *Blueprint) Dependencies() ([]string, error) {
	val, ok := b.Data["dependencies"]
	if !ok || val == nil {
		return nil, nil
	}
	switch t := val.(type) {
	case []interface{}:
		return collectDepList(t), nil
	case map[string]interface{}:
		return collectDepMap(t), nil
	case map[interface{}]interface{}:
		if converted, ok := convertYAML(t).(map[string]interface{}); ok {
			return collectDepMap(converted), nil
		}
	}
	return nil, nil
}

func collectDepList(list []interface{}) []string {
	deps := make([]string, 0, len(list))
	for _, item := range list {
		switch v := item.(type) {
		case string:
			deps = append(deps, v)
		case map[string]interface{}:
			if path, ok := v["path"].(string); ok {
				deps = append(deps, path)
			}
		case map[interface{}]interface{}:
			if converted, ok := convertYAML(v).(map[string]interface{}); ok {
				if path, ok := converted["path"].(string); ok {
					deps = append(deps, path)
				}
			}
		}
	}
	return deps
}

func collectDepMap(depMap map[string]interface{}) []string {
	deps := []string{}
	for key, value := range depMap {
		if key == "from_parent" {
			continue
		}
		list, ok := value.([]interface{})
		if !ok {
			continue
		}
		deps = append(deps, collectDepList(list)...)
	}
	return deps
}

func (b *Blueprint) TrackedFiles() (map[string]string, error) {
	return b.collectTrackedFiles()
}

func (b *Blueprint) IsStale() (*bool, error) {
	info, err := b.StalenessInfo()
	if err != nil {
		return nil, err
	}
	if info.State == "no_snapshot" {
		return nil, ErrNoSnapshot
	}
	stale := info.State == "stale"
	return &stale, nil
}
