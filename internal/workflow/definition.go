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
	Name        string   `yaml:"name" json:"name"`
	Version     int      `yaml:"version" json:"version"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Units       UnitSpec `yaml:"units" json:"units"`
	// Steps is optional. A definition with steps sends every unit through
	// them in order, each step on an agent of its role; it has no top-level
	// prompt, output or validate. Without steps a unit is one prompt with
	// its follow-up rounds, as before steps existed.
	Steps    []StepSpec   `yaml:"steps,omitempty" json:"steps,omitempty"`
	Prompt   PromptSpec   `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	Output   OutputSpec   `yaml:"output,omitempty" json:"output,omitempty"`
	Validate ValidateSpec `yaml:"validate,omitempty" json:"validate,omitempty"`
	Compact  CompactSpec  `yaml:"compact,omitempty" json:"compact,omitempty"`
	Timeouts TimeoutSpec  `yaml:"timeouts,omitempty" json:"timeouts,omitempty"`
	Notify   string       `yaml:"notify,omitempty" json:"notify,omitempty"`
	// Supervise and Budget are optional. A definition without them behaves
	// exactly as before they existed.
	Supervise *SuperviseSpec `yaml:"supervise,omitempty" json:"supervise,omitempty"`
	Budget    *BudgetSpec    `yaml:"budget,omitempty" json:"budget,omitempty"`
	// Approve is optional. Without it every validated result is final.
	Approve *ApproveSpec `yaml:"approve,omitempty" json:"approve,omitempty"`
	// Guardrails is optional. Without it no tool activity is read.
	Guardrails *GuardrailSpec `yaml:"guardrails,omitempty" json:"guardrails,omitempty"`
}

// ApproveSpec parks validated outcomes for a human decision. On lists the
// results (done, weak, failed) that wait for bp workflow approve or reject
// instead of finishing.
type ApproveSpec struct {
	On []string `yaml:"on" json:"on"`
}

// approvalResults are the validated outcomes approve.on may name.
var approvalResults = []string{"done", "weak", "failed"}

// Approves reports whether a validated result waits for a human decision.
func (d Definition) Approves(result string) bool {
	if d.Approve == nil {
		return false
	}
	for _, value := range d.Approve.On {
		if value == result {
			return true
		}
	}
	return false
}

// SuperviseSpec stops a broken agent or a broken run from failing every
// remaining unit one after another.
type SuperviseSpec struct {
	Breaker  *BreakerSpec  `yaml:"breaker,omitempty" json:"breaker,omitempty"`
	Meltdown *MeltdownSpec `yaml:"meltdown,omitempty" json:"meltdown,omitempty"`
}

// BreakerSpec opens one agent's circuit breaker after Failures consecutive
// failed units; the agent then takes no unit for OpenFor, followed by exactly
// one trial unit.
type BreakerSpec struct {
	Failures int    `yaml:"failures" json:"failures"`
	OpenFor  string `yaml:"open_for" json:"open_for"`
}

// MeltdownSpec pauses the run when Failures units finish failed within the
// sliding Window, across all agents.
type MeltdownSpec struct {
	Failures int    `yaml:"failures" json:"failures"`
	Window   string `yaml:"window" json:"window"`
}

// BudgetSpec caps token spend. Pointers keep an explicit zero distinguishable
// from an absent key so that zero can be rejected.
type BudgetSpec struct {
	UnitTokens       *int64 `yaml:"unit_tokens,omitempty" json:"unit_tokens,omitempty"`
	RunTokens        *int64 `yaml:"run_tokens,omitempty" json:"run_tokens,omitempty"`
	CacheReadPercent *int   `yaml:"cache_read_percent,omitempty" json:"cache_read_percent,omitempty"`
}

// DefaultCacheReadPercent is the weight of cache-read tokens in the budget
// measure when budget.cache_read_percent is not set.
const DefaultCacheReadPercent = 10

// CacheReadWeight returns the configured cache-read percent or its default.
func (d Definition) CacheReadWeight() int {
	if d.Budget != nil && d.Budget.CacheReadPercent != nil {
		return *d.Budget.CacheReadPercent
	}
	return DefaultCacheReadPercent
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
	// Step is the current step's name; empty in a definition without
	// steps.
	Step string
	// Steps maps the names of the steps already finished for this unit to
	// their results. It is empty (never nil) in a definition without steps.
	Steps map[string]StepResult
	// Attempt is the current step attempt for this unit, from 1. It is
	// always 1 in a definition without steps.
	Attempt int
	// Previous is the previous attempt of the current step for this unit;
	// zero for a first attempt and in a definition without steps.
	Previous StepAttempt
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
	if err := checkStepKeys(data); err != nil {
		return Definition{}, err
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
	if len(d.Steps) > 0 {
		if err := validateSteps(d); err != nil {
			return err
		}
	} else {
		if err := validateStage("", d.Prompt, d.Output, d.Validate); err != nil {
			return err
		}
		if err := checkStageReferences("", nil, -1, d.Output, d.Validate); err != nil {
			return err
		}
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
	if err := validateSupervise(d.Supervise); err != nil {
		return err
	}
	if err := validateApprove(d.Approve); err != nil {
		return err
	}
	if err := validateGuardrails(d.Guardrails); err != nil {
		return err
	}
	return validateBudget(d.Budget)
}

// validateStage checks one prompt/output/validate group: the top-level keys
// of a definition without steps, or one step. prefix names the step in
// errors ("steps.review."); it is empty for the top-level keys.
func validateStage(prefix string, prompt PromptSpec, output OutputSpec, validate ValidateSpec) error {
	if strings.TrimSpace(prompt.Template) == "" {
		return fmt.Errorf("%sprompt.template is required", prefix)
	}
	if err := validateRelativePath(prefix+"prompt.template", prompt.Template); err != nil {
		return err
	}
	if prompt.Followup != "" {
		if err := validateRelativePath(prefix+"prompt.followup", prompt.Followup); err != nil {
			return err
		}
	}
	if output.Format != "" && output.Format != "json" && output.Format != "any" {
		return fmt.Errorf("%soutput.format must be json or any, got %q", prefix, output.Format)
	}
	if output.Path != "" {
		if err := validateRelativePath(prefix+"output.path", output.Path); err != nil {
			return fmt.Errorf("%soutput.path must be relative to the run workdir: %w", prefix, err)
		}
		if _, err := template.New("output.path").Option("missingkey=error").Parse(output.Path); err != nil {
			return fmt.Errorf("parse %soutput.path template: %w", prefix, err)
		}
	}
	if len(validate.Command) > 0 {
		if strings.TrimSpace(validate.Command[0]) == "" {
			return fmt.Errorf("%svalidate.command[0] must name an executable", prefix)
		}
		for i, arg := range validate.Command {
			if _, err := template.New(fmt.Sprintf("validate.command[%d]", i)).Option("missingkey=error").Parse(arg); err != nil {
				return fmt.Errorf("parse %svalidate.command[%d]: %w", prefix, i, err)
			}
		}
	}
	if validate.Timeout != "" {
		if timeout, err := time.ParseDuration(validate.Timeout); err != nil || timeout <= 0 {
			return fmt.Errorf("%svalidate.timeout must be a positive duration: %q", prefix, validate.Timeout)
		}
	}
	if validate.Rounds < 0 || validate.WeakAfter < 0 {
		return fmt.Errorf("%svalidate.rounds and %svalidate.weak_after cannot be negative", prefix, prefix)
	}
	return nil
}

func validateApprove(spec *ApproveSpec) error {
	if spec == nil {
		return nil
	}
	if len(spec.On) == 0 {
		return errors.New("approve.on must list at least one of done, weak, failed")
	}
	seen := map[string]bool{}
	for _, value := range spec.On {
		known := false
		for _, result := range approvalResults {
			known = known || value == result
		}
		if !known {
			return fmt.Errorf("approve.on: unknown result %q (use done, weak, failed)", value)
		}
		if seen[value] {
			return fmt.Errorf("approve.on: duplicate result %q", value)
		}
		seen[value] = true
	}
	return nil
}

func validateSupervise(spec *SuperviseSpec) error {
	if spec == nil {
		return nil
	}
	if spec.Breaker == nil && spec.Meltdown == nil {
		return errors.New("supervise needs a breaker or meltdown block")
	}
	if b := spec.Breaker; b != nil {
		if b.Failures <= 0 {
			return fmt.Errorf("supervise.breaker.failures is required and must be a positive integer, got %d", b.Failures)
		}
		if err := positiveDuration("supervise.breaker.open_for", b.OpenFor); err != nil {
			return err
		}
	}
	if m := spec.Meltdown; m != nil {
		if m.Failures <= 0 {
			return fmt.Errorf("supervise.meltdown.failures is required and must be a positive integer, got %d", m.Failures)
		}
		if err := positiveDuration("supervise.meltdown.window", m.Window); err != nil {
			return err
		}
	}
	return nil
}

func validateBudget(spec *BudgetSpec) error {
	if spec == nil {
		return nil
	}
	if spec.UnitTokens == nil && spec.RunTokens == nil {
		return errors.New("budget needs unit_tokens or run_tokens")
	}
	if spec.UnitTokens != nil && *spec.UnitTokens <= 0 {
		return fmt.Errorf("budget.unit_tokens must be a positive integer, got %d", *spec.UnitTokens)
	}
	if spec.RunTokens != nil && *spec.RunTokens <= 0 {
		return fmt.Errorf("budget.run_tokens must be a positive integer, got %d", *spec.RunTokens)
	}
	if spec.CacheReadPercent != nil && (*spec.CacheReadPercent < 0 || *spec.CacheReadPercent > 100) {
		return fmt.Errorf("budget.cache_read_percent must be between 0 and 100, got %d", *spec.CacheReadPercent)
	}
	return nil
}

func positiveDuration(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if parsed, err := time.ParseDuration(value); err != nil || parsed <= 0 {
		return fmt.Errorf("%s must be a positive duration: %q", field, value)
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
	names := d.stepNames()
	for index, step := range d.Pipeline() {
		prefix, position := "", -1
		if len(d.Steps) > 0 {
			prefix, position = stepPrefix(step.Name), index
		}
		if err := checkStageFiles(dir, prefix, names, position, step.Prompt, step.Validate); err != nil {
			return err
		}
	}
	return nil
}

// checkStageFiles checks the files one prompt/validate group names: the
// prompt templates parse and refer only to earlier steps, and validator
// files exist inside the workflow directory.
func checkStageFiles(dir, prefix string, names []string, position int, prompt PromptSpec, validate ValidateSpec) error {
	for _, item := range []struct{ field, relative string }{{prefix + "prompt.template", prompt.Template}, {prefix + "prompt.followup", prompt.Followup}} {
		field, relative := item.field, item.relative
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
		tmpl, err := readTemplate(path)
		if err != nil {
			return fmt.Errorf("%s %q: %w", field, relative, err)
		}
		if err := checkTemplateSteps(field, tmpl, names, position); err != nil {
			return err
		}
	}
	for index, argument := range validate.Command {
		if filepath.IsAbs(argument) {
			return fmt.Errorf("%svalidate.command[%d] must not contain an absolute path", prefix, index)
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
			return fmt.Errorf("%svalidate.command[%d]: %w", prefix, index, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%svalidate.command[%d] referenced file %q: %w", prefix, index, argument, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%svalidate.command[%d] referenced path %q is not a file", prefix, index, argument)
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
