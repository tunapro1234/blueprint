package tokens

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type agentEntry struct {
	Name   string `json:"name"`
	Folder string `json:"folder"`
}

type resolver struct {
	byFolder map[string]string
	agents   []string
}

func loadResolver(paths []string) resolver {
	r := resolver{byFolder: map[string]string{}}
	seenNames := map[string]bool{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var envelope struct {
			Agents json.RawMessage `json:"agents"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			continue
		}
		var entries []agentEntry
		if err := json.Unmarshal(envelope.Agents, &entries); err != nil {
			var mapped map[string]agentEntry
			if json.Unmarshal(envelope.Agents, &mapped) != nil {
				continue
			}
			for name, entry := range mapped {
				if entry.Name == "" {
					entry.Name = name
				}
				entries = append(entries, entry)
			}
		}
		for _, entry := range entries {
			folder := firstPath(entry.Folder)
			if entry.Name == "" || folder == "" {
				continue
			}
			folder = filepath.Clean(folder)
			if _, exists := r.byFolder[folder]; !exists {
				r.byFolder[folder] = entry.Name
			}
			if !seenNames[entry.Name] {
				seenNames[entry.Name] = true
				r.agents = append(r.agents, entry.Name)
			}
		}
	}
	sort.Slice(r.agents, func(i, j int) bool {
		if len(r.agents[i]) == len(r.agents[j]) {
			return r.agents[i] < r.agents[j]
		}
		return len(r.agents[i]) > len(r.agents[j])
	})
	return r
}

func firstPath(folder string) string {
	folder = strings.TrimSpace(folder)
	if !strings.HasPrefix(folder, "/") {
		return ""
	}
	if index := strings.IndexAny(folder, " \t\r\n"); index >= 0 {
		folder = folder[:index]
	}
	return folder
}

func mungedPath(path string) string {
	clean := filepath.Clean(path)
	var result strings.Builder
	result.Grow(len(clean))
	for _, char := range clean {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			result.WriteRune(char)
		} else {
			result.WriteByte('-')
		}
	}
	return result.String()
}

func (r resolver) claudeAgent(path, title string) (agent, owner string) {
	if title != "" {
		return title, ""
	}
	munged := filepath.Base(filepath.Dir(path))
	for folder, name := range r.byFolder {
		if mungedPath(folder) == munged {
			if label, parent := r.worktreeAgent(folder); label != "" {
				return label, parent
			}
			return name, ""
		}
	}
	reversed := reverseMungedPath(munged)
	if label, parent := r.worktreeAgent(reversed); label != "" {
		return label, parent
	}
	return reversed, ""
}

func (r resolver) codexAgent(cwd string) (agent, owner string) {
	if cwd == "" {
		return "?", ""
	}
	clean := filepath.Clean(cwd)
	if label, parent := r.worktreeAgent(clean); label != "" {
		return label, parent
	}
	if name := r.byFolder[clean]; name != "" {
		return name, ""
	}
	if name := r.containingAgent(clean); name != "" {
		return name, ""
	}
	if project := scratchpadProject(clean); project != "" {
		for folder, name := range r.byFolder {
			if mungedPath(folder) == project {
				return name, name
			}
		}
		candidate := strings.TrimPrefix(project, "-")
		candidate = strings.TrimPrefix(candidate, "srv-")
		for _, name := range r.agents {
			if name == candidate {
				return name, name
			}
		}
		for _, name := range r.agents {
			if strings.HasPrefix(candidate, name+"-") || strings.HasPrefix(name, candidate+"-") {
				return name, name
			}
		}
	}
	return clean, ""
}

func (r resolver) containingAgent(path string) string {
	bestFolder := ""
	bestName := ""
	for folder, name := range r.byFolder {
		// /srv is the orchestrator's broad scope, not evidence that every
		// otherwise unresolved project belongs to it.
		if folder == "/" || folder == "/srv" {
			continue
		}
		if strings.HasPrefix(path, folder+string(filepath.Separator)) && len(folder) > len(bestFolder) {
			bestFolder = folder
			bestName = name
		}
	}
	return bestName
}

func (r resolver) normalizeRawAgent(row RawRecord) RawRecord {
	switch {
	case row.Src == "claude" && strings.HasPrefix(row.Agent, "-"):
		row.Agent, row.Owner = r.claudeAgent(filepath.Join("/stored", row.Agent, "session.jsonl"), "")
	case row.Src == "codex" && strings.HasPrefix(row.Agent, "/"):
		agent, owner := r.codexAgent(row.Agent)
		row.Agent = agent
		if owner != "" || row.Owner == "" {
			row.Owner = owner
		}
	}
	return row
}

func (r resolver) worktreeAgent(path string) (label, owner string) {
	clean := filepath.ToSlash(filepath.Clean(path))
	marker := "/.worktrees/"
	index := strings.Index(clean, marker)
	if index < 1 {
		return "", ""
	}
	rest := clean[index+len(marker):]
	topic := strings.SplitN(rest, "/", 2)[0]
	if topic == "" {
		return "", ""
	}
	parent := filepath.Clean(clean[:index])
	name := r.byFolder[parent]
	if name == "" {
		return "", ""
	}
	return name + "@" + topic, name
}

func reverseMungedPath(value string) string {
	const marker = "\x00WORKTREE\x00"
	value = strings.ReplaceAll(value, "--worktrees-", marker)
	value = strings.ReplaceAll(value, "-", "/")
	value = strings.ReplaceAll(value, marker, "/.worktrees/")
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return filepath.Clean(value)
}

func scratchpadProject(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) < 5 || parts[1] != "tmp" || !strings.HasPrefix(parts[2], "claude-") {
		return ""
	}
	scratch := false
	for _, part := range parts[4:] {
		if part == "scratchpad" {
			scratch = true
			break
		}
	}
	if !scratch {
		return ""
	}
	return parts[3]
}
