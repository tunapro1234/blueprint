// Package projectschema loads and validates portable Blueprint project schemas.
package projectschema

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"blueprint/internal/config"
	"go.yaml.in/yaml/v3"
)

const (
	Directory = ".blueprint"
	Filename  = "schema.yaml"
)

type Schema struct {
	Version int     `yaml:"version"`
	History string  `yaml:"history"`
	Remote  string  `yaml:"remote,omitempty"`
	Agents  []Agent `yaml:"agents"`
}

type Agent struct {
	Name    string `yaml:"name"`
	Folder  string `yaml:"folder"`
	Parent  string `yaml:"parent,omitempty"`
	Runtime string `yaml:"runtime"`
	Role    string `yaml:"role"`
	Color   string `yaml:"color"`
	Model   string `yaml:"model,omitempty"`
	Effort  string `yaml:"effort,omitempty"`
	Launch  string `yaml:"launch,omitempty"`
}

type Loaded struct {
	Schema
	ProjectDir string
	Path       string
	Content    []byte
	Folders    map[string]string
}

var agentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func Path(projectDir string) string {
	return filepath.Join(projectDir, Directory, Filename)
}

// Load treats the directory containing .blueprint as the portable project root.
// Every agent folder must already exist so EvalSymlinks can prove it remains in
// that root rather than following a committed symlink outside the checkout.
func Load(projectDir string) (Loaded, error) {
	absolute, err := filepath.Abs(projectDir)
	if err != nil {
		return Loaded{}, err
	}
	projectDir, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return Loaded{}, fmt.Errorf("resolve project directory: %w", err)
	}
	path := Path(projectDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return Loaded{}, fmt.Errorf("read %s: %w", path, err)
	}
	schema, err := Decode(data)
	if err != nil {
		return Loaded{}, fmt.Errorf("parse %s: %w", path, err)
	}
	folders, err := ResolveFolders(projectDir, schema)
	if err != nil {
		return Loaded{}, fmt.Errorf("validate %s: %w", path, err)
	}
	return Loaded{Schema: schema, ProjectDir: projectDir, Path: path, Content: data, Folders: folders}, nil
}

func Decode(data []byte) (Schema, error) {
	var schema Schema
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&schema); err != nil {
		return Schema{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Schema{}, fmt.Errorf("expected a single YAML document")
		}
		return Schema{}, err
	}
	if err := Validate(schema); err != nil {
		return Schema{}, err
	}
	return schema, nil
}

func Validate(schema Schema) error {
	if schema.Version != 1 {
		return fmt.Errorf("version must be 1")
	}
	switch schema.History {
	case "none", "file":
		if schema.Remote != "" {
			return fmt.Errorf("remote is only valid when history is remote")
		}
	case "remote":
		if strings.TrimSpace(schema.Remote) == "" {
			return fmt.Errorf("remote is required when history is remote")
		}
		if hasControl(schema.Remote) {
			return fmt.Errorf("remote contains control characters")
		}
	default:
		return fmt.Errorf("history must be none, file, or remote")
	}
	if len(schema.Agents) == 0 {
		return fmt.Errorf("agents must not be empty")
	}
	byName := make(map[string]Agent, len(schema.Agents))
	for index, agent := range schema.Agents {
		label := fmt.Sprintf("agents[%d]", index)
		if !agentName.MatchString(agent.Name) {
			return fmt.Errorf("%s.name is invalid", label)
		}
		if _, exists := byName[agent.Name]; exists {
			return fmt.Errorf("duplicate agent name %q", agent.Name)
		}
		if agent.Folder == "" || filepath.IsAbs(agent.Folder) || hasControl(agent.Folder) {
			return fmt.Errorf("%s.folder must be a relative path", label)
		}
		clean := filepath.Clean(agent.Folder)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("%s.folder escapes the project root", label)
		}
		switch agent.Runtime {
		case "claude", "codex", "hermes", "opencode":
		default:
			return fmt.Errorf("%s.runtime must be claude, codex, hermes, or opencode", label)
		}
		if schema.History != "none" && agent.Runtime != "claude" && agent.Runtime != "codex" {
			return fmt.Errorf("%s runtime %s is not supported with history %s; use history none", label, agent.Runtime, schema.History)
		}
		if strings.TrimSpace(agent.Role) == "" || hasControl(agent.Role) {
			return fmt.Errorf("%s.role must be non-empty text without control characters", label)
		}
		if hasControl(agent.Model) || hasControl(agent.Effort) || hasControl(agent.Launch) {
			return fmt.Errorf("%s launch settings contain control characters", label)
		}
		if agent.Color == "" {
			return fmt.Errorf("%s.color is required", label)
		}
		if agent.Color != "auto" {
			if _, err := config.ColorIndex(agent.Color); err != nil {
				return fmt.Errorf("%s.color: %w", label, err)
			}
		}
		if agent.Launch != "" && agent.Launch != "default" && agent.Launch != "no-sandbox" {
			return fmt.Errorf("%s.launch must be default or no-sandbox", label)
		}
		if agent.Launch == "no-sandbox" && agent.Runtime != "codex" {
			return fmt.Errorf("%s.launch no-sandbox requires the codex runtime", label)
		}
		if agent.Effort != "" && agent.Runtime != "claude" && agent.Runtime != "codex" {
			return fmt.Errorf("%s.effort is not supported by the %s runtime", label, agent.Runtime)
		}
		byName[agent.Name] = agent
	}
	for _, agent := range schema.Agents {
		if agent.Parent != "" {
			if agent.Parent == agent.Name {
				return fmt.Errorf("agent %q cannot be its own parent", agent.Name)
			}
			if _, exists := byName[agent.Parent]; !exists {
				return fmt.Errorf("agent %q has unknown parent %q", agent.Name, agent.Parent)
			}
		}
		seen := map[string]bool{agent.Name: true}
		for parent := agent.Parent; parent != ""; parent = byName[parent].Parent {
			if seen[parent] {
				return fmt.Errorf("parent cycle includes %q", parent)
			}
			seen[parent] = true
		}
	}
	return nil
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func ResolveFolders(projectDir string, schema Schema) (map[string]string, error) {
	root, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	result := make(map[string]string, len(schema.Agents))
	for _, agent := range schema.Agents {
		candidate := filepath.Join(root, filepath.Clean(agent.Folder))
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return nil, fmt.Errorf("agent %q folder: %w", agent.Name, err)
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("agent %q folder is not a directory", agent.Name)
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			return nil, fmt.Errorf("agent %q folder escapes the project root through a symlink", agent.Name)
		}
		result[agent.Name] = resolved
	}
	return result, nil
}

func OrderedAgents(schema Schema) []Agent {
	byName := make(map[string]Agent, len(schema.Agents))
	for _, agent := range schema.Agents {
		byName[agent.Name] = agent
	}
	var result []Agent
	done := map[string]bool{}
	for len(result) < len(schema.Agents) {
		var ready []string
		for name, agent := range byName {
			if !done[name] && (agent.Parent == "" || done[agent.Parent]) {
				ready = append(ready, name)
			}
		}
		sort.Strings(ready)
		for _, name := range ready {
			result = append(result, byName[name])
			done[name] = true
		}
	}
	return result
}

func Encode(schema Schema) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(schema); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
