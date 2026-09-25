package workflow

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"
	"unicode"

	"go.yaml.in/yaml/v3"
)

const SchemaVersion = 1

var workflowNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Definition struct {
	Name        string       `yaml:"name" json:"name"`
	Version     int          `yaml:"version" json:"version"`
	Description string       `yaml:"description,omitempty" json:"description,omitempty"`
	Units       UnitSpec     `yaml:"units" json:"units"`
	Prompt      PromptSpec   `yaml:"prompt" json:"prompt"`
	Output      OutputSpec   `yaml:"output,omitempty" json:"output,omitempty"`
	Validate    ValidateSpec `yaml:"validate,omitempty" json:"validate,omitempty"`
	Compact     CompactSpec  `yaml:"compact,omitempty" json:"compact,omitempty"`
	Timeouts    TimeoutSpec  `yaml:"timeouts,omitempty" json:"timeouts,omitempty"`
	Notify      string       `yaml:"notify,omitempty" json:"notify,omitempty"`
}

type UnitSpec struct {
	File string `yaml:"file" json:"file"`
	Key  string `yaml:"key,omitempty" json:"key,omitempty"`
}

type PromptSpec struct {
	Template string `yaml:"template" json:"template"`
	Followup string `yaml:"followup,omitempty" json:"followup,omitempty"`
}

type OutputSpec struct {
	Path         string `yaml:"path,omitempty" json:"path,omitempty"`
	Format       string `yaml:"format,omitempty" json:"format,omitempty"`
	RequireFresh bool   `yaml:"require_fresh,omitempty" json:"require_fresh,omitempty"`
}

type ValidateSpec struct {
	Command   []string `yaml:"command,omitempty" json:"command,omitempty"`
	Timeout   string   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Rounds    int      `yaml:"rounds,omitempty" json:"rounds,omitempty"`
	WeakAfter int      `yaml:"weak_after,omitempty" json:"weak_after,omitempty"`
}

type CompactSpec struct {
	ContextAbove int `yaml:"ctx_above,omitempty" json:"ctx_above,omitempty"`
	EveryUnits   int `yaml:"every_units,omitempty" json:"every_units,omitempty"`
}

type TimeoutSpec struct {
	Start string `yaml:"start,omitempty" json:"start,omitempty"`
	Unit  string `yaml:"unit,omitempty" json:"unit,omitempty"`
}

type Unit struct {
	Key  string         `json:"key"`
	Data map[string]any `json:"data"`
}

type TemplateData struct {
	Unit     map[string]any
	Key      string
	Round    int
	Problems string
	Output   string
	Started  string
	Workdir  string
	Run      string
}

func ReadDefinition(dir string) (Definition, error) {
	path := filepath.Join(dir, "workflow.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, fmt.Errorf("read %s: %w", path, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var definition Definition
	if err := decoder.Decode(&definition); err != nil {
		return Definition{}, fmt.Errorf("parse workflow.yaml: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Definition{}, errors.New("workflow.yaml must contain one document")
		}
		return Definition{}, fmt.Errorf("parse trailing workflow document: %w", err)
	}
	if err := ValidateDefinition(definition); err != nil {
		return Definition{}, err
	}
	if err := CheckReferences(dir, definition); err != nil {
		return Definition{}, err
	}
	return definition, nil
}

func ValidateDefinition(d Definition) error {
	if !workflowNamePattern.MatchString(d.Name) {
		return fmt.Errorf("invalid workflow name %q; use lowercase letters, digits, and interior hyphens", d.Name)
	}
	if d.Version != SchemaVersion {
		return fmt.Errorf("unsupported workflow version %d (supported: %d)", d.Version, SchemaVersion)
	}
	if strings.TrimSpace(d.Units.File) == "" {
		return errors.New("units.file is required")
	}
	if err := validateRelativePath("units.file", d.Units.File); err != nil {
		return err
	}
	ext := strings.ToLower(filepath.Ext(d.Units.File))
	if ext := strings.ToLower(filepath.Ext(d.Units.File)); ext != ".json" && ext != ".jsonl" && ext != ".txt" {
		return fmt.Errorf("units.file must end in .json, .jsonl, or .txt (got %q)", ext)
	}
	if ext != ".txt" && strings.TrimSpace(d.Units.Key) == "" {
		return errors.New("units.key is required for JSON and JSONL unit files")
	}
	if strings.TrimSpace(d.Prompt.Template) == "" {
		return errors.New("prompt.template is required")
	}
	if err := validateRelativePath("prompt.template", d.Prompt.Template); err != nil {
		return err
	}
	if d.Prompt.Followup != "" {
		if err := validateRelativePath("prompt.followup", d.Prompt.Followup); err != nil {
			return err
		}
	}
	if d.Output.Format == "" {
		d.Output.Format = "any"
	}
	if d.Output.Format != "json" && d.Output.Format != "any" {
		return fmt.Errorf("output.format must be json or any, got %q", d.Output.Format)
	}
	if d.Output.Path != "" {
		if err := validateRelativePath("output.path", d.Output.Path); err != nil {
			return fmt.Errorf("output.path must be relative to the run workdir: %w", err)
		}
		if _, err := template.New("output.path").Option("missingkey=error").Parse(d.Output.Path); err != nil {
			return fmt.Errorf("parse output.path template: %w", err)
		}
	}
	if len(d.Validate.Command) > 0 {
		if strings.TrimSpace(d.Validate.Command[0]) == "" {
			return errors.New("validate.command[0] must name an executable")
		}
		for i, arg := range d.Validate.Command {
			if _, err := template.New(fmt.Sprintf("validate.command[%d]", i)).Option("missingkey=error").Parse(arg); err != nil {
				return fmt.Errorf("parse validate.command[%d]: %w", i, err)
			}
		}
	}
	if d.Validate.Timeout != "" {
		if timeout, err := time.ParseDuration(d.Validate.Timeout); err != nil || timeout <= 0 {
			return fmt.Errorf("validate.timeout must be a positive duration: %q", d.Validate.Timeout)
		}
	}
	if d.Validate.Rounds < 0 || d.Validate.WeakAfter < 0 {
		return errors.New("validate.rounds and validate.weak_after cannot be negative")
	}
	if d.Compact.ContextAbove < 0 || d.Compact.EveryUnits < 0 {
		return errors.New("compact thresholds cannot be negative")
	}
	for field, value := range map[string]string{"timeouts.start": d.Timeouts.Start, "timeouts.unit": d.Timeouts.Unit} {
		if value == "" {
			continue
		}
		if timeout, err := time.ParseDuration(value); err != nil || timeout <= 0 {
			return fmt.Errorf("%s must be a positive duration: %q", field, value)
		}
	}
	if d.Notify != "" && d.Notify != "owner" && d.Notify != "none" {
		return fmt.Errorf("notify must be owner or none, got %q", d.Notify)
	}
	return nil
}

func validateRelativePath(field, value string) error {
	if filepath.IsAbs(value) {
		return fmt.Errorf("%s must be relative", field)
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s escapes its directory: %q", field, value)
	}
	if strings.ContainsRune(value, '\x00') || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s contains a control character", field)
	}
	return nil
}

func CheckReferences(dir string, d Definition) error {
	for field, relative := range map[string]string{"prompt.template": d.Prompt.Template, "prompt.followup": d.Prompt.Followup} {
		if relative == "" {
			continue
		}
		path, err := containedFile(dir, relative)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s %q: %w", field, relative, err)
		}
		if _, err := readTemplate(path); err != nil {
			return fmt.Errorf("%s %q: %w", field, relative, err)
		}
	}
	for index, argument := range d.Validate.Command {
		if filepath.IsAbs(argument) {
			return fmt.Errorf("validate.command[%d] must not contain an absolute path", index)
		}
		if strings.HasPrefix(argument, "-") || strings.Contains(argument, "{{") {
			continue
		}
		if index == 0 && !strings.ContainsAny(argument, "/\\") && filepath.Ext(argument) == "" {
			continue // Resolve a bare interpreter (python3, node, sh) through PATH.
		}
		if !strings.ContainsAny(argument, "/\\") && filepath.Ext(argument) == "" {
			continue
		}
		path, err := containedFile(dir, argument)
		if err != nil {
			return fmt.Errorf("validate.command[%d]: %w", index, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("validate.command[%d] referenced file %q: %w", index, argument, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("validate.command[%d] referenced path %q is not a file", index, argument)
		}
	}
	return nil
}

func containedFile(root, relative string) (string, error) {
	if err := validateRelativePath("path", relative); err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(rootAbs, filepath.Clean(relative))
	rel, err := filepath.Rel(rootAbs, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes directory: %q", relative)
	}
	// Refuse symlink components so a workflow cannot smuggle in an outside file.
	current := rootAbs
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				break
			}
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink path is not allowed: %q", relative)
		}
	}
	return path, nil
}

func readTemplate(path string) (*template.Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return template.New(filepath.Base(path)).Option("missingkey=error").Parse(string(data))
}

func RenderFile(root, relative string, data TemplateData) (string, error) {
	path, err := containedFile(root, relative)
	if err != nil {
		return "", err
	}
	tmpl, err := readTemplate(path)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

func RenderText(name, source string, data TemplateData) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(source)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

func LoadUnits(definition Definition, workdir string) ([]Unit, error) {
	path, err := containedFile(workdir, definition.Units.File)
	if err != nil {
		return nil, fmt.Errorf("units.file: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read units %s: %w", path, err)
	}
	ext := strings.ToLower(filepath.Ext(path))
	var units []Unit
	switch ext {
	case ".txt":
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			line := strings.TrimSuffix(scanner.Text(), "\r")
			if strings.TrimSpace(line) == "" {
				continue
			}
			units = append(units, Unit{Key: line, Data: map[string]any{"key": line}})
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	case ".json":
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var records []map[string]any
		if err := decoder.Decode(&records); err != nil {
			return nil, fmt.Errorf("decode units JSON: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, errors.New("units JSON must contain one array")
		}
		for _, record := range records {
			unit, err := unitFromRecord(record, definition.Units.Key)
			if err != nil {
				return nil, err
			}
			units = append(units, unit)
		}
	case ".jsonl":
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
		for line := 1; scanner.Scan(); line++ {
			if strings.TrimSpace(scanner.Text()) == "" {
				continue
			}
			var record map[string]any
			decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
			decoder.UseNumber()
			if err := decoder.Decode(&record); err != nil {
				return nil, fmt.Errorf("units JSONL line %d: %w", line, err)
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				return nil, fmt.Errorf("units JSONL line %d must contain one JSON object", line)
			}
			unit, err := unitFromRecord(record, definition.Units.Key)
			if err != nil {
				return nil, fmt.Errorf("units JSONL line %d: %w", line, err)
			}
			units = append(units, unit)
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported units file extension: %s", ext)
	}
	seen := make(map[string]struct{}, len(units))
	for _, unit := range units {
		if unit.Key == "" {
			return nil, errors.New("unit key cannot be empty")
		}
		if _, exists := seen[unit.Key]; exists {
			return nil, fmt.Errorf("duplicate unit key %q", unit.Key)
		}
		seen[unit.Key] = struct{}{}
	}
	return units, nil
}

func unitFromRecord(record map[string]any, keyField string) (Unit, error) {
	if record == nil {
		return Unit{}, errors.New("unit must be a JSON object")
	}
	value, ok := record[keyField]
	if !ok {
		return Unit{}, fmt.Errorf("unit is missing key field %q", keyField)
	}
	key, err := scalarKey(value)
	if err != nil {
		return Unit{}, fmt.Errorf("unit key field %q: %w", keyField, err)
	}
	return Unit{Key: key, Data: record}, nil
}

func scalarKey(value any) (string, error) {
	switch item := value.(type) {
	case string:
		if strings.TrimSpace(item) == "" {
			return "", errors.New("key is empty")
		}
		return item, nil
	case json.Number:
		return item.String(), nil
	case bool:
		return fmt.Sprint(item), nil
	default:
		return "", fmt.Errorf("key must be a scalar, got %T", value)
	}
}

func CheckSampleTemplates(workflowDir, workdir string, d Definition) error {
	units, err := LoadUnits(d, workdir)
	if err != nil {
		return err
	}
	if len(units) == 0 {
		return errors.New("units file contains no units")
	}
	unit := units[0]
	data := TemplateData{Unit: unit.Data, Key: unit.Key, Round: 1, Workdir: workdir, Started: time.Now().UTC().Format(time.RFC3339)}
	if _, err := RenderFile(workflowDir, d.Prompt.Template, data); err != nil {
		return fmt.Errorf("render prompt with first unit: %w", err)
	}
	if d.Output.Path != "" {
		out, err := RenderText("output.path", d.Output.Path, data)
		if err != nil {
			return fmt.Errorf("render output.path with first unit: %w", err)
		}
		data.Output = out
	}
	if d.Prompt.Followup != "" {
		if _, err := RenderFile(workflowDir, d.Prompt.Followup, data); err != nil {
			return fmt.Errorf("render followup with first unit: %w", err)
		}
	}
	for index, arg := range d.Validate.Command {
		if _, err := RenderText(fmt.Sprintf("validate.command[%d]", index), arg, data); err != nil {
			return fmt.Errorf("render validate.command[%d] with first unit: %w", index, err)
		}
	}
	return nil
}
