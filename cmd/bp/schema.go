package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/identity"
	"blueprint/internal/projectschema"
	bptmux "blueprint/internal/tmux"
)

func (a *app) schemaCommand(args []string) error {
	if len(args) == 0 || args[0] != "export" {
		return fmt.Errorf("usage: bp schema export [<project-dir>] [--lead <agent>]")
	}
	project, lead, err := parseSchemaExportArgs(args[1:])
	if err != nil {
		return err
	}
	return a.exportSchema(project, lead)
}

func parseSchemaExportArgs(args []string) (string, string, error) {
	project, lead := ".", ""
	projectSet := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--lead":
			if index+1 >= len(args) {
				return "", "", fmt.Errorf("--lead requires an agent name")
			}
			lead = args[index+1]
			index++
		default:
			if strings.HasPrefix(args[index], "-") || projectSet {
				return "", "", fmt.Errorf("usage: bp schema export [<project-dir>] [--lead <agent>]")
			}
			project, projectSet = args[index], true
		}
	}
	return project, lead, nil
}

func (a *app) exportSchema(projectDir, lead string) error {
	root, err := physicalProjectDir(projectDir)
	if err != nil {
		return err
	}
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	for name, agent := range fleet.Agents {
		folder := book.FirstPath(agent.Folder)
		physical, err := filepath.EvalSymlinks(folder)
		if err != nil || !pathInside(root, physical) {
			continue
		}
		if lead != "" && name != lead && !fleet.IsDescendant(name, lead) {
			continue
		}
		selected[name] = true
	}
	if lead != "" && !selected[lead] {
		return fmt.Errorf("lead %q is not registered inside %s", lead, root)
	}
	if len(selected) == 0 {
		return fmt.Errorf("no registered agents have folders inside %s", root)
	}

	schema := projectschema.Schema{Version: 1, History: "none"}
	path := projectschema.Path(root)
	if data, readErr := os.ReadFile(path); readErr == nil {
		previous, decodeErr := projectschema.Decode(data)
		if decodeErr != nil {
			return fmt.Errorf("refusing to replace invalid %s: %w", path, decodeErr)
		}
		schema.History, schema.Remote = previous.History, previous.Remote
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}

	seen := map[string]bool{}
	for _, name := range fleet.Order {
		if !selected[name] {
			continue
		}
		agent := fleet.Agents[name]
		row, err := exportedSchemaAgent(root, agent)
		if err != nil {
			return err
		}
		if selected[fleet.Parents[name]] {
			row.Parent = fleet.Parents[name]
		}
		schema.Agents = append(schema.Agents, row)
		seen[name] = true
	}
	// Fleet order should contain every loaded agent, but sorted fallback keeps
	// export deterministic for synthetic or old books that do not.
	var missing []string
	for name := range selected {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		row, err := exportedSchemaAgent(root, fleet.Agents[name])
		if err != nil {
			return err
		}
		if selected[fleet.Parents[name]] {
			row.Parent = fleet.Parents[name]
		}
		schema.Agents = append(schema.Agents, row)
	}
	if err := projectschema.Validate(schema); err != nil {
		return fmt.Errorf("generated schema: %w", err)
	}
	data, err := projectschema.Encode(schema)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := writeAtomic(path, data, 0o644); err != nil {
		return err
	}
	gitignore := filepath.Join(filepath.Dir(path), ".gitignore")
	if err := ensureHistoryIgnored(gitignore, a.out); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "wrote %s with %d agent(s)\n", path, len(schema.Agents))
	return nil
}

func ensureHistoryIgnored(path string, output *os.File) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data = []byte{}
	} else if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		rule := strings.TrimSpace(line)
		if rule == "history" || rule == "history/" || rule == "/history/" {
			return nil
		}
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, []byte("# History may contain private conversation data; opt in with git add -f.\nhistory/\n")...)
	if err := writeAtomic(path, data, mode); err != nil {
		return err
	}
	fmt.Fprintf(output, "updated %s (history ignored by default)\n", path)
	return nil
}

func physicalProjectDir(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	info, err := os.Stat(physical)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("project directory does not exist: %s", path)
	}
	return filepath.Clean(physical), nil
}

func pathInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func exportedSchemaAgent(root string, agent book.Agent) (projectschema.Agent, error) {
	folder, err := filepath.EvalSymlinks(book.FirstPath(agent.Folder))
	if err != nil {
		return projectschema.Agent{}, fmt.Errorf("resolve folder for %s: %w", agent.Name, err)
	}
	relative, err := filepath.Rel(root, folder)
	if err != nil || !pathInside(root, folder) {
		return projectschema.Agent{}, fmt.Errorf("agent %s is outside project root", agent.Name)
	}
	if relative == "" {
		relative = "."
	}
	row := projectschema.Agent{Name: agent.Name, Folder: filepath.ToSlash(relative), Runtime: runtimeForSchema(agent), Role: agent.Role, Color: agent.ColorOverride}
	if row.Role == "" {
		row.Role = "(new - add role)"
	}
	if row.Color == "" {
		row.Color = agent.Color
	}
	if row.Color == "" {
		row.Color = "auto"
	}
	row.Model, row.Effort = launchModelEffort(row.Runtime, agent.Launch)
	if agent.Launch != nil && agent.Launch.NoSandbox {
		row.Launch = "no-sandbox"
	}
	return row, nil
}

func runtimeForSchema(agent book.Agent) string {
	if agent.Launch != nil {
		switch {
		case agent.Launch.Codex:
			return "codex"
		case agent.Launch.Hermes:
			return "hermes"
		case agent.Launch.OpenCode:
			return "opencode"
		default:
			return "claude"
		}
	}
	if agent.Local != nil {
		switch agent.Local.Harness {
		case "claude", "codex", "hermes", "opencode":
			return agent.Local.Harness
		}
	}
	return "claude"
}

func launchModelEffort(runtime string, launch *bptmux.OpenOptions) (string, string) {
	if launch == nil {
		return "", ""
	}
	if runtime == "codex" {
		return codexArgsModel(append([]string{"codex"}, launch.Args...))
	}
	model, effort := "", ""
	for index := 0; index < len(launch.Args); index++ {
		name, value, inline := strings.Cut(launch.Args[index], "=")
		if (name == "--model" || name == "--effort") && !inline && index+1 < len(launch.Args) {
			index++
			value = launch.Args[index]
		}
		switch name {
		case "--model", "-m":
			model = value
		case "--effort":
			effort = value
		}
	}
	return model, effort
}

type continueOptions struct {
	Project string
	DryRun  bool
	Yes     bool
}

func parseContinueArgs(args []string) (continueOptions, error) {
	opts := continueOptions{Project: "."}
	projectSet := false
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			opts.DryRun = true
		case "--yes":
			opts.Yes = true
		default:
			if strings.HasPrefix(arg, "-") || projectSet {
				return opts, fmt.Errorf("usage: bp continue [<project-dir>] [--dry-run] [--yes]")
			}
			opts.Project, projectSet = arg, true
		}
	}
	return opts, nil
}

type continuePlan struct {
	Agent projectschema.Agent
	Path  string
	State string
}

func (a *app) continueProject(args []string) error {
	opts, err := parseContinueArgs(args)
	if err != nil {
		return err
	}
	loaded, err := projectschema.Load(opts.Project)
	if err != nil {
		return err
	}
	plans, err := a.buildContinuePlan(loaded)
	if err != nil {
		return err
	}
	a.printContinuePlan(loaded, plans)
	if opts.DryRun {
		fmt.Fprintln(a.out, "dry run: nothing was changed")
		return nil
	}
	hash := sha256.Sum256(loaded.Content)
	hashText := hex.EncodeToString(hash[:])
	trusted, err := a.schemaTrusted(loaded.ProjectDir, hashText)
	if err != nil {
		return err
	}
	if !trusted {
		if !opts.Yes {
			if !a.interactiveTerminal() {
				return fmt.Errorf("schema is not trusted; review the plan and rerun with --yes")
			}
			fmt.Fprint(a.out, "Trust this schema and continue? [y/N] ")
			line, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
			if readErr != nil && len(line) == 0 {
				return readErr
			}
			answer := strings.ToLower(strings.TrimSpace(line))
			if answer != "y" && answer != "yes" {
				return fmt.Errorf("schema was not trusted")
			}
		}
		if err := a.recordSchemaTrust(loaded.ProjectDir, hashText); err != nil {
			return err
		}
	}

	resume := map[string]string{}
	switch loaded.History {
	case "file":
		resume, err = importProjectHistory(filepath.Join(loaded.ProjectDir, projectschema.Directory, "history"), loaded)
	case "remote":
		resume, err = a.importRemoteHistory(loaded)
	}
	if err != nil {
		return err
	}
	openAgent := a.open
	if a.openForContinue != nil {
		openAgent = a.openForContinue
	}
	for _, plan := range plans {
		args := []string{plan.Agent.Name, plan.Path, "--" + plan.Agent.Runtime, "--parent", plan.Agent.Parent, "--role", plan.Agent.Role}
		if plan.Agent.Runtime != "codex" {
			args = append(args, "--no-prompt")
		}
		if loaded.History == "none" && plan.State != "leave live agent open" {
			args = append(args, "--fresh")
			// Replacing a closed registration's previous conversation is an
			// explicit rebind. Without this, bp open correctly refuses to drop
			// the stored thread identity.
			args = append(args, "--rebind")
		}
		if plan.Agent.Parent == "" {
			args = removeOptionPair(args, "--parent")
		}
		if plan.Agent.Launch == "no-sandbox" {
			args = append(args, "--no-sandbox")
		}
		if id := resume[plan.Agent.Name]; id != "" {
			args = append(args, "--thread", id, "--rebind")
			if plan.Agent.Runtime == "codex" {
				args = append(args, "--no-prompt")
			}
		}
		native := schemaNativeArgs(plan.Agent)
		if len(native) > 0 {
			args = append(args, "--")
			args = append(args, native...)
		}
		if err := openAgent(args); err != nil {
			return fmt.Errorf("continue %s: %w", plan.Agent.Name, err)
		}
		colour := ""
		if plan.Agent.Color != "auto" {
			colour, err = bpconfig.ColorIndex(plan.Agent.Color)
			if err != nil {
				return err
			}
		}
		if err := book.SetColorOverride(a.config.Agentbooks, plan.Agent.Name, colour); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.out, "continued %d agent(s) from %s\n", len(plans), loaded.Path)
	return nil
}

func removeOptionPair(args []string, option string) []string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == option {
			return append(args[:index], args[index+2:]...)
		}
	}
	return args
}

func schemaNativeArgs(agent projectschema.Agent) []string {
	var args []string
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}
	if agent.Effort != "" {
		if agent.Runtime == "codex" {
			args = append(args, "-c", "model_reasoning_effort="+agent.Effort)
		} else {
			args = append(args, "--effort", agent.Effort)
		}
	}
	return args
}

func (a *app) buildContinuePlan(loaded projectschema.Loaded) ([]continuePlan, error) {
	records, err := book.Records(a.config.Agentbooks)
	if err != nil {
		return nil, err
	}
	registered := map[string]book.Agent{}
	for _, record := range records {
		if existing, found := registered[record.Agent.Name]; found && filepath.Clean(book.FirstPath(existing.Folder)) != filepath.Clean(book.FirstPath(record.Agent.Folder)) {
			return nil, fmt.Errorf("agent name %q has multiple registered folders", record.Agent.Name)
		}
		registered[record.Agent.Name] = record.Agent
	}
	var plans []continuePlan
	for _, agent := range projectschema.OrderedAgents(loaded.Schema) {
		path := loaded.Folders[agent.Name]
		state := "open fresh"
		if existing, found := registered[agent.Name]; found {
			existingPath := filepath.Clean(book.FirstPath(existing.Folder))
			physical, evalErr := filepath.EvalSymlinks(existingPath)
			if evalErr == nil {
				existingPath = physical
			}
			if existingPath != filepath.Clean(path) {
				return nil, fmt.Errorf("agent name collision: %s is registered for %s, schema requires %s", agent.Name, existing.Folder, path)
			}
			if existing.ArchivedAt != "" {
				return nil, fmt.Errorf("agent %s is archived; restore it before continuing", agent.Name)
			}
			state = "resume closed"
			if a.tmux != nil && a.tmux.HasSession(a.ctx, agent.Name) {
				state = "leave live agent open"
			}
		}
		plans = append(plans, continuePlan{Agent: agent, Path: path, State: state})
	}
	return plans, nil
}

func (a *app) printContinuePlan(loaded projectschema.Loaded, plans []continuePlan) {
	fmt.Fprintf(a.out, "Schema plan: %s\n", loaded.Path)
	fmt.Fprintf(a.out, "History: %s\n", loaded.History)
	if loaded.History == "remote" {
		fmt.Fprintf(a.out, "History remote: %s\n", loaded.Remote)
	}
	for _, plan := range plans {
		parent := plan.Agent.Parent
		if parent == "" {
			parent = "-"
		}
		var launch []string
		if plan.Agent.Launch != "" {
			launch = append(launch, "launch="+plan.Agent.Launch)
		}
		if plan.Agent.Model != "" {
			launch = append(launch, "model="+plan.Agent.Model)
		}
		if plan.Agent.Effort != "" {
			launch = append(launch, "effort="+plan.Agent.Effort)
		}
		details := ""
		if len(launch) > 0 {
			details = ", " + strings.Join(launch, ", ")
		}
		fmt.Fprintf(a.out, "  %-20s %-10s %-18s %s (parent: %s, role: %s, color: %s%s)\n", plan.Agent.Name, plan.Agent.Runtime, plan.State, plan.Path, parent, plan.Agent.Role, plan.Agent.Color, details)
	}
}

type schemaTrust struct {
	Project string `json:"project"`
	Hash    string `json:"hash"`
}

func (a *app) schemaTrustPath(project string) string {
	sum := sha256.Sum256([]byte(project))
	return filepath.Join(a.config.StateDir, "schema-trust", hex.EncodeToString(sum[:])+".json")
}

func (a *app) schemaTrusted(project, hash string) (bool, error) {
	data, err := os.ReadFile(a.schemaTrustPath(project))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var record schemaTrust
	if json.Unmarshal(data, &record) != nil {
		return false, nil
	}
	return record.Project == project && record.Hash == hash, nil
}

func (a *app) recordSchemaTrust(project, hash string) error {
	path := a.schemaTrustPath(project)
	data, err := json.MarshalIndent(schemaTrust{Project: project, Hash: hash}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, data, 0o600)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".bp-write-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func validSchemaAgentName(name string) bool {
	return identity.ValidName(name) && len(name) <= 64
}

func parsePositive(value, flag string) (int, error) {
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 {
		return 0, fmt.Errorf("%s requires a positive integer", flag)
	}
	return number, nil
}

func schemaRenameReferences(agent book.Agent, old string) []string {
	folder := book.FirstPath(agent.Folder)
	if folder == "" {
		return nil
	}
	if physical, err := filepath.EvalSymlinks(folder); err == nil {
		folder = physical
	}
	absolute, err := filepath.Abs(folder)
	if err != nil {
		return nil
	}
	folder = filepath.Clean(absolute)
	var paths []string
	for dir := folder; ; dir = filepath.Dir(dir) {
		path := projectschema.Path(dir)
		data, err := os.ReadFile(path)
		if err == nil {
			if schema, decodeErr := projectschema.Decode(data); decodeErr == nil {
				for _, declared := range schema.Agents {
					if declared.Name == old {
						paths = append(paths, path)
						break
					}
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	sort.Strings(paths)
	return paths
}

func (a *app) reportSchemaRenameHints(agent book.Agent, old, name string) {
	for _, path := range schemaRenameReferences(agent, old) {
		fmt.Fprintf(a.out, "schema still declares %s in %s; update the committed schema manually to %s\n", old, path, name)
	}
}
