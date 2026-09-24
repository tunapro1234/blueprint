package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/identity"
	"blueprint/internal/projectschema"
	bptmux "blueprint/internal/tmux"
)

type historyExportOptions struct {
	Project string
	Agent   string
	Keep    int
	Stdout  bool
}

type historyEntry struct {
	Name    string
	Data    []byte
	Mode    os.FileMode
	ModTime time.Time
}

func (a *app) historyCommand(args []string) error {
	if len(args) == 0 || args[0] != "export" {
		return fmt.Errorf("usage: bp history export [<project-dir>] [--agent <name>] [--keep N] [--stdout]")
	}
	opts, err := parseHistoryExportArgs(args[1:])
	if err != nil {
		return err
	}
	return a.exportHistory(opts)
}

func parseHistoryExportArgs(args []string) (historyExportOptions, error) {
	opts := historyExportOptions{Project: ".", Keep: 1}
	projectSet := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--agent":
			if index+1 >= len(args) {
				return opts, fmt.Errorf("--agent requires a name")
			}
			opts.Agent = args[index+1]
			index++
		case "--keep":
			if index+1 >= len(args) {
				return opts, fmt.Errorf("--keep requires a positive integer")
			}
			keep, err := parsePositive(args[index+1], "--keep")
			if err != nil {
				return opts, err
			}
			opts.Keep = keep
			index++
		case "--stdout":
			opts.Stdout = true
		default:
			if strings.HasPrefix(args[index], "-") || projectSet {
				return opts, fmt.Errorf("usage: bp history export [<project-dir>] [--agent <name>] [--keep N] [--stdout]")
			}
			opts.Project, projectSet = args[index], true
		}
	}
	if opts.Agent != "" && !validSchemaAgentName(opts.Agent) {
		return opts, fmt.Errorf("invalid agent name: %s", opts.Agent)
	}
	return opts, nil
}

func (a *app) exportHistory(opts historyExportOptions) error {
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return err
	}
	var names []string
	projectRoot := ""
	if opts.Stdout && opts.Agent != "" {
		if _, ok := fleet.Agents[opts.Agent]; !ok {
			return fmt.Errorf("unknown agent: %s", opts.Agent)
		}
		names = []string{opts.Agent}
	} else {
		loaded, err := projectschema.Load(opts.Project)
		if err != nil {
			return err
		}
		projectRoot = loaded.ProjectDir
		for _, agent := range loaded.Agents {
			if opts.Agent == "" || opts.Agent == agent.Name {
				names = append(names, agent.Name)
			}
		}
		if opts.Agent != "" && len(names) == 0 {
			return fmt.Errorf("agent %s is not declared in %s", opts.Agent, loaded.Path)
		}
	}

	var entries []historyEntry
	for _, name := range names {
		agent, ok := fleet.Agents[name]
		if !ok {
			return fmt.Errorf("agent %s is not registered", name)
		}
		found, err := historyEntriesForAgent(agent, opts.Keep)
		if err != nil {
			return fmt.Errorf("export %s history: %w", name, err)
		}
		entries = append(entries, found...)
	}
	if opts.Stdout {
		if err := writeHistoryTar(a.out, entries); err != nil {
			return err
		}
		fmt.Fprintf(a.err, "exported %d history file(s) for %d agent(s)\n", len(entries), len(names))
		return nil
	}
	root := filepath.Join(projectRoot, projectschema.Directory, "history")
	if err := ensureHistoryIgnored(filepath.Join(projectRoot, projectschema.Directory, ".gitignore"), a.out); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	for _, entry := range entries {
		target := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(entry.Name, "history/")))
		if err := writePortableHistoryEntry(target, entry); err != nil {
			return err
		}
		_ = os.Chtimes(target, entry.ModTime, entry.ModTime)
	}
	if err := prunePortableHistory(root, names, opts.Keep); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "exported %d history file(s) to %s (keeping %d session(s) per agent)\n", len(entries), root, opts.Keep)
	return nil
}

func writePortableHistoryEntry(path string, entry historyEntry) error {
	name := strings.TrimPrefix(filepath.ToSlash(entry.Name), "history/")
	parts := strings.Split(name, "/")
	if len(parts) != 3 || parts[1] != "codex" || parts[2] != "session_index.jsonl" {
		return writeNoConflict(path, entry.Data, entry.Mode)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	mode := entry.Mode
	if mode == 0 {
		mode = 0o600
	}
	return writeAtomic(path, entry.Data, mode)
}

func historyEntriesForAgent(agent book.Agent, keep int) ([]historyEntry, error) {
	runtime := runtimeForSchema(agent)
	switch runtime {
	case "claude":
		return claudeHistoryEntries(agent, keep)
	case "codex":
		return codexHistoryEntries(agent, keep)
	default:
		return nil, nil
	}
}

func claudeHistoryEntries(agent book.Agent, keep int) ([]historyEntry, error) {
	directory := bptmux.ClaudeProjectDir(book.FirstPath(agent.Folder))
	files, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var candidates []historyEntry
	pinned := ""
	if agent.Launch != nil {
		pinned = agent.Launch.ResumeID
	}
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(directory, file.Name())
		id := strings.TrimSuffix(file.Name(), ".jsonl")
		title, titled := bptmux.ReadCustomTitle(path)
		if (pinned != "" && id != pinned) || (pinned == "" && (!titled || title != agent.Name)) {
			continue
		}
		entry, err := readHistoryEntry(path, filepath.ToSlash(filepath.Join("history", agent.Name, "claude", file.Name())))
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, entry)
	}
	newestHistory(candidates)
	if len(candidates) > keep {
		candidates = candidates[:keep]
	}
	return candidates, nil
}

func codexHistoryEntries(agent book.Agent, keep int) ([]historyEntry, error) {
	home := bptmux.CodexProcessInfo(0).Home
	paths, err := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "*.jsonl"))
	if err != nil {
		return nil, err
	}
	pinned := ""
	if agent.Launch != nil {
		pinned = agent.Launch.ResumeID
	}
	folder := filepath.Clean(book.FirstPath(agent.Folder))
	var candidates []historyEntry
	selectedIDs := map[string]bool{}
	for _, path := range paths {
		id, cwd, ok := codexMeta(path)
		if !ok || filepath.Clean(cwd) != folder || (pinned != "" && id != pinned) {
			continue
		}
		relative, err := filepath.Rel(filepath.Join(home, "sessions"), path)
		if err != nil || strings.HasPrefix(relative, "..") {
			continue
		}
		entry, err := readHistoryEntry(path, filepath.ToSlash(filepath.Join("history", agent.Name, "codex", "sessions", relative)))
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, entry)
		selectedIDs[id] = true
	}
	newestHistory(candidates)
	if len(candidates) > keep {
		candidates = candidates[:keep]
		selectedIDs = map[string]bool{}
		for _, entry := range candidates {
			selectedIDs[codexIDBytes(entry.Data)] = true
		}
	}
	indexRows, err := codexIndexRows(filepath.Join(home, "session_index.jsonl"), selectedIDs)
	if err != nil {
		return nil, err
	}
	if len(indexRows) > 0 {
		candidates = append(candidates, historyEntry{
			Name: filepath.ToSlash(filepath.Join("history", agent.Name, "codex", "session_index.jsonl")),
			Data: indexRows, Mode: 0o600, ModTime: time.Now(),
		})
	}
	return candidates, nil
}

func codexMeta(path string) (string, string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	var row struct {
		Type    string `json:"type"`
		Payload struct {
			ID  string `json:"id"`
			CWD string `json:"cwd"`
		} `json:"payload"`
	}
	if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &row) != nil || row.Type != "session_meta" || row.Payload.ID == "" || !filepath.IsAbs(row.Payload.CWD) {
		return "", "", false
	}
	return row.Payload.ID, row.Payload.CWD, true
}

func codexIDBytes(data []byte) string {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	var row struct {
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &row) != nil {
		return ""
	}
	return row.Payload.ID
}

func codexIndexRows(path string, ids map[string]bool) ([]byte, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var output bytes.Buffer
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	for scanner.Scan() {
		var row struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) == nil && ids[row.ID] {
			output.Write(scanner.Bytes())
			output.WriteByte('\n')
		}
	}
	return output.Bytes(), scanner.Err()
}

func readHistoryEntry(path, name string) (historyEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return historyEntry{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return historyEntry{}, err
	}
	return historyEntry{Name: name, Data: data, Mode: info.Mode().Perm(), ModTime: info.ModTime()}, nil
}

func newestHistory(entries []historyEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ModTime.Equal(entries[j].ModTime) {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].ModTime.After(entries[j].ModTime)
	})
}

func writeHistoryTar(writer io.Writer, entries []historyEntry) error {
	tw := tar.NewWriter(writer)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.Name, Mode: int64(entry.Mode.Perm()), Size: int64(len(entry.Data)), ModTime: entry.ModTime, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tw.Write(entry.Data); err != nil {
			return err
		}
	}
	return tw.Close()
}

func writeNoConflict(path string, data []byte, mode os.FileMode) error {
	return writeNoConflictWithHook(path, data, mode, nil)
}

func writeNoConflictWithHook(path string, data []byte, mode os.FileMode, beforeCreate func() error) error {
	existing, err := os.ReadFile(path)
	if err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return fmt.Errorf("refusing to overwrite different history file: %s", path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o600
	}
	if beforeCreate != nil {
		if err := beforeCreate(); err != nil {
			return err
		}
	}
	return writeAtomicNoReplace(path, data, mode)
}

func writeAtomicNoReplace(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".bp-history-*")
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
	if err := os.Link(name, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(path)
			if readErr == nil && bytes.Equal(existing, data) {
				return nil
			}
			if readErr == nil {
				return fmt.Errorf("refusing to overwrite different history file: %s", path)
			}
		}
		return err
	}
	return nil
}

func prunePortableHistory(root string, agents []string, keep int) error {
	for _, name := range agents {
		for _, runtime := range []string{"claude", "codex/sessions"} {
			base := filepath.Join(root, name, filepath.FromSlash(runtime))
			var entries []historyEntry
			err := filepath.WalkDir(base, func(path string, item os.DirEntry, walkErr error) error {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				if walkErr != nil || item.IsDir() || filepath.Ext(item.Name()) != ".jsonl" {
					return walkErr
				}
				info, err := item.Info()
				if err != nil {
					return err
				}
				entries = append(entries, historyEntry{Name: path, ModTime: info.ModTime()})
				return nil
			})
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			newestHistory(entries)
			for _, entry := range entries[minimum(keep, len(entries)):] {
				if err := os.Remove(entry.Name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func minimum(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func importProjectHistory(root string, loaded projectschema.Loaded) (map[string]string, error) {
	result := map[string]string{}
	for _, agent := range loaded.Agents {
		var id string
		var err error
		switch agent.Runtime {
		case "claude":
			id, err = importClaudeHistory(filepath.Join(root, agent.Name, "claude"), loaded.Folders[agent.Name])
		case "codex":
			id, err = importCodexHistory(filepath.Join(root, agent.Name, "codex"), loaded.Folders[agent.Name])
		}
		if err != nil {
			return nil, fmt.Errorf("import %s history: %w", agent.Name, err)
		}
		if id != "" {
			result[agent.Name] = id
		}
	}
	return result, nil
}

func newestJSONL(root string, exclude string) (string, error) {
	var files []historyEntry
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil || entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" || entry.Name() == exclude {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("portable history entry %s is not a regular file", path)
		}
		files = append(files, historyEntry{Name: path, ModTime: info.ModTime()})
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	newestHistory(files)
	if len(files) == 0 {
		return "", nil
	}
	return files[0].Name, nil
}

func importClaudeHistory(root, folder string) (string, error) {
	source, err := newestJSONL(root, "")
	if err != nil || source == "" {
		return "", err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	id := strings.TrimSuffix(filepath.Base(source), ".jsonl")
	if err := writeNoConflict(filepath.Join(bptmux.ClaudeProjectDir(folder), id+".jsonl"), data, 0o600); err != nil {
		return "", err
	}
	return id, nil
}

func importCodexHistory(root, folder string) (string, error) {
	source, err := newestJSONL(filepath.Join(root, "sessions"), "session_index.jsonl")
	if err != nil || source == "" {
		return "", err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	rewritten, id, err := rewriteCodexCWD(data, folder)
	if err != nil {
		return "", err
	}
	home := bptmux.CodexProcessInfo(0).Home
	relative, err := filepath.Rel(filepath.Join(root, "sessions"), source)
	if err != nil || strings.HasPrefix(relative, "..") {
		return "", fmt.Errorf("invalid portable Codex history path")
	}
	destination := filepath.Join(home, "sessions", relative)
	if err := writeNoConflict(destination, rewritten, 0o600); err != nil {
		return "", err
	}
	rows, err := os.ReadFile(filepath.Join(root, "session_index.jsonl"))
	if err == nil && len(rows) > 0 {
		if err := appendCodexIndexRows(filepath.Join(home, "session_index.jsonl"), rows, id); err != nil {
			return "", err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return id, nil
}

func rewriteCodexCWD(data []byte, folder string) ([]byte, string, error) {
	line, rest, found := bytes.Cut(data, []byte("\n"))
	if !found {
		return nil, "", fmt.Errorf("Codex rollout has no complete metadata row")
	}
	var row map[string]any
	if json.Unmarshal(line, &row) != nil || row["type"] != "session_meta" {
		return nil, "", fmt.Errorf("Codex rollout has invalid session metadata")
	}
	payload, ok := row["payload"].(map[string]any)
	if !ok {
		return nil, "", fmt.Errorf("Codex rollout has invalid session payload")
	}
	id, _ := payload["id"].(string)
	if id == "" {
		return nil, "", fmt.Errorf("Codex rollout has no thread id")
	}
	payload["cwd"] = folder
	encoded, err := json.Marshal(row)
	if err != nil {
		return nil, "", err
	}
	encoded = append(encoded, '\n')
	encoded = append(encoded, rest...)
	return encoded, id, nil
}

func appendCodexIndexRows(path string, rows []byte, id string) error {
	return appendCodexIndexRowsWithHook(path, rows, id, nil)
}

func appendCodexIndexRowsWithHook(path string, rows []byte, id string, beforeAppend func() error) error {
	var selected [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(rows))
	for scanner.Scan() {
		var row struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(scanner.Bytes(), &row) == nil && row.ID == id {
			selected = append(selected, append([]byte(nil), scanner.Bytes()...))
		}
	}
	if err := scanner.Err(); err != nil || len(selected) == 0 {
		return err
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		return fmt.Errorf("Codex session index has an incomplete final record")
	}
	var pending bytes.Buffer
	seen := make(map[string]bool, len(selected))
	for _, row := range selected {
		key := string(row)
		if seen[key] || codexIndexHasRow(existing, row) {
			continue
		}
		seen[key] = true
		pending.Write(row)
		pending.WriteByte('\n')
	}
	if pending.Len() == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if beforeAppend != nil {
		if err := beforeAppend(); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	written, err := file.Write(pending.Bytes())
	if err != nil {
		return err
	}
	if written != pending.Len() {
		return io.ErrShortWrite
	}
	return file.Sync()
}

func codexIndexHasRow(index, row []byte) bool {
	for _, existing := range bytes.Split(index, []byte{'\n'}) {
		if bytes.Equal(existing, row) {
			return true
		}
	}
	return false
}

func (a *app) importRemoteHistory(loaded projectschema.Loaded) (map[string]string, error) {
	endpoint, err := resolveHistoryRemote(loaded.Remote, a.config.Remotes)
	if err != nil {
		return nil, err
	}
	temporary, err := os.MkdirTemp("", "bp-remote-history-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	for _, agent := range loaded.Agents {
		data, err := fetchRemoteHistory(a.ctx, endpoint, agent.Name)
		if err != nil {
			return nil, fmt.Errorf("fetch %s history: %w", agent.Name, err)
		}
		if err := extractHistoryTar(bytes.NewReader(data), temporary); err != nil {
			return nil, err
		}
	}
	return importProjectHistory(filepath.Join(temporary, "history"), loaded)
}

type sshHistoryCommand struct {
	Path string
	Args []string
}

type historyEndpoint struct {
	User     string
	Host     string
	Port     string
	Identity string
	Elevate  string
}

func resolveHistoryRemote(value string, remotes map[string]bpconfig.RemoteConfig) (historyEndpoint, error) {
	if remote, ok := remotes[value]; ok {
		return historyEndpoint{
			User:     remote.User,
			Host:     remote.Host,
			Port:     strconv.Itoa(remote.Port),
			Identity: remote.Identity,
			Elevate:  remote.Elevate,
		}, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "ssh" || parsed.Hostname() == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User == nil {
		return historyEndpoint{}, fmt.Errorf("remote must be ssh://user@host[:port] or a configured remote name")
	}
	if _, set := parsed.User.Password(); set {
		return historyEndpoint{}, fmt.Errorf("remote URLs must not contain credentials")
	}
	user, host := parsed.User.Username(), parsed.Hostname()
	if !identity.ValidName(user) || !validSSHHistoryHost(host) {
		return historyEndpoint{}, fmt.Errorf("remote URL has an invalid user or host")
	}
	port := ""
	if parsed.Port() != "" {
		parsedPort, portErr := strconv.Atoi(parsed.Port())
		if portErr != nil || parsedPort < 1 || parsedPort > 65535 {
			return historyEndpoint{}, fmt.Errorf("invalid SSH port")
		}
		port = parsed.Port()
	}
	return historyEndpoint{User: user, Host: host, Port: port}, nil
}

func validSSHHistoryHost(host string) bool {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if host == "" || strings.HasPrefix(host, "-") {
		return false
	}
	for _, char := range host {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '-') {
			return false
		}
	}
	return true
}

func remoteHistoryCommand(endpoint historyEndpoint, agent string, lookup pathLookup) (sshHistoryCommand, error) {
	if !validSchemaAgentName(agent) {
		return sshHistoryCommand{}, fmt.Errorf("invalid agent name: %s", agent)
	}
	if !validSSHHistoryHost(endpoint.Host) || (endpoint.User != "" && !identity.ValidName(endpoint.User)) {
		return sshHistoryCommand{}, fmt.Errorf("remote has an invalid user or host")
	}
	for _, word := range strings.Fields(endpoint.Elevate) {
		if word == "" {
			continue
		}
		for _, char := range word {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._/+:-=@", char)) {
				return sshHistoryCommand{}, fmt.Errorf("remote elevation command contains unsupported shell characters")
			}
		}
	}
	args := []string{"ssh"}
	if endpoint.Port != "" && endpoint.Port != "0" {
		port, err := strconv.Atoi(endpoint.Port)
		if err != nil || port < 1 || port > 65535 {
			return sshHistoryCommand{}, fmt.Errorf("invalid SSH port")
		}
		args = append(args, "-p", endpoint.Port)
	}
	if endpoint.Identity != "" {
		if strings.ContainsAny(endpoint.Identity, "\x00\r\n") {
			return sshHistoryCommand{}, fmt.Errorf("remote identity path contains invalid characters")
		}
		args = append(args, "-i", endpoint.Identity)
	}
	targetHost := remoteHostTarget(endpoint.Host)
	target := targetHost
	if endpoint.User != "" {
		target = endpoint.User + "@" + targetHost
	}
	args = append(args, target)
	args = append(args, strings.Fields(endpoint.Elevate)...)
	args = append(args, "bp", "history", "export", "--stdout", "--agent", agent)
	if lookup == nil {
		lookup = exec.LookPath
	}
	path, err := lookup("ssh")
	if err != nil {
		return sshHistoryCommand{}, fmt.Errorf("ssh is not installed")
	}
	return sshHistoryCommand{Path: path, Args: args}, nil
}

func fetchRemoteHistory(ctx context.Context, endpoint historyEndpoint, agent string) ([]byte, error) {
	spec, err := remoteHistoryCommand(endpoint, agent, execLookPath)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, spec.Path, spec.Args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	data, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("ssh: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return data, nil
}

func extractHistoryTar(reader io.Reader, root string) error {
	tr := tar.NewReader(reader)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(filepath.FromSlash(header.Name))
		if header.Typeflag != tar.TypeReg || clean == "." || filepath.IsAbs(clean) || (clean != "history" && !strings.HasPrefix(clean, "history"+string(os.PathSeparator))) {
			return fmt.Errorf("unsafe history archive entry: %s", header.Name)
		}
		target := filepath.Join(root, clean)
		data, err := io.ReadAll(io.LimitReader(tr, header.Size+1))
		if err != nil || int64(len(data)) != header.Size {
			return fmt.Errorf("read history archive entry %s", header.Name)
		}
		if err := writeNoConflict(target, data, os.FileMode(header.Mode)); err != nil {
			return err
		}
		_ = os.Chtimes(target, header.ModTime, header.ModTime)
	}
}
