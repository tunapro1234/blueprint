package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"blueprint/internal/book"
)

// rename renames an agent everywhere bp knows about it: the tmux session, the
// agentbooks, the usage state files, and the agent's own transcript title.
//
// Two things it deliberately does NOT do. It never edits session .jsonl files —
// the title is changed by sending /rename to the pane, and the reader already
// takes the newest custom-title record. And it never edits the agent's source
// tree; a live reference like a hook's TARGET_AGENT is reported for a human to
// fix, because silently rewriting someone's code is not a rename's job.
//
// Historical records (timeline.md, policy.log, msgq/done, daily notes) are left
// alone on purpose: they correctly hold the name the agent had that day.
func (a *app) rename(args []string) error {
	dry := false
	var positional []string
	for _, arg := range args {
		switch arg {
		case "--dry-run", "-n":
			dry = true
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("unknown flag: %s", arg)
			}
			positional = append(positional, arg)
		}
	}
	if len(positional) != 2 {
		return fmt.Errorf("usage: bp rename <old-name> <new-name> [--dry-run]")
	}
	old, name := positional[0], positional[1]
	if old == name {
		return fmt.Errorf("%s is already the name", name)
	}
	if !validAgentName(name) {
		return fmt.Errorf("invalid agent name: %s (allowed: letters, digits, . _ -)", name)
	}

	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return err
	}
	_, knownOld := fleet.Agents[old]
	_, knownNew := fleet.Agents[name]
	liveOld := a.tmux.HasSession(a.ctx, old)
	liveNew := a.tmux.HasSession(a.ctx, name)

	if !knownOld && !liveOld {
		return fmt.Errorf("no agent named %s (not in the agentbook, no tmux session)", old)
	}
	if knownNew || liveNew {
		where := "the agentbook"
		if liveNew {
			where = "a tmux session"
		}
		return fmt.Errorf("%s is already taken by %s", name, where)
	}

	prefix := ""
	if dry {
		prefix = "would "
	}
	report := func(format string, args ...any) {
		fmt.Fprintf(a.out, prefix+format+"\n", args...)
	}

	// 1. tmux session.
	if liveOld {
		if !dry {
			if err := a.tmux.RenameSession(a.ctx, old, name); err != nil {
				return fmt.Errorf("tmux rename-session: %w", err)
			}
		}
		report("rename tmux session %s -> %s", old, name)
	} else {
		fmt.Fprintf(a.out, "no live tmux session for %s (agentbook only)\n", old)
	}

	// 2. Agentbooks.
	if dry {
		for _, change := range a.renamePreview(old, name) {
			report("update %s", change)
		}
	} else {
		changes, err := book.Rename(a.config.Agentbooks, old, name)
		if err != nil {
			return fmt.Errorf("agentbook: %w", err)
		}
		for _, change := range changes {
			report("update %s", change)
		}
	}

	// 3. Usage state.
	for _, change := range a.renameUsage(old, name, dry) {
		report("update %s", change)
	}

	// 4. Transcript title, via the agent's own pane. Only Claude understands
	// /rename; typing it at a codex or shell pane would just run it as a command.
	if liveOld && a.paneRunsClaude(name, old) {
		if dry {
			report("send /rename %s to the pane", name)
		} else if err := a.tmux.Send(a.ctx, name, "/rename "+name); err != nil {
			fmt.Fprintf(a.err, "warning: could not send /rename to the pane: %v\n", err)
			fmt.Fprintf(a.err, "         run `/rename %s` inside %s yourself, or bp status will keep the old title\n", name, name)
		} else {
			report("send /rename %s to the pane", name)
		}
	}

	// 5. Live code references, reported but never edited.
	a.reportCodeReferences(fleet, old, name)

	if dry {
		fmt.Fprintln(a.out, "\ndry run: nothing was changed")
	} else {
		fmt.Fprintf(a.out, "\nrenamed %s -> %s. Verify with: bp status && bp tree\n", old, name)
	}
	return nil
}

var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// paneRunsClaude reports whether the session's pane is a Claude agent, checking
// under both names since the tmux rename may already have happened. A pane whose
// command cannot be read is assumed not to be Claude: sending a slash command to
// a shell would type it as a shell command.
func (a *app) paneRunsClaude(names ...string) bool {
	commands, err := a.tmux.Commands(a.ctx)
	if err != nil {
		return false
	}
	for _, name := range names {
		if strings.Contains(commands[name], "claude") {
			return true
		}
	}
	return false
}

// isProse reports whether a hit is documentation rather than something that
// executes, so the listing can put the dangerous files first.
func isProse(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".txt", ".rst", ".csv":
		return true
	}
	return false
}

func validAgentName(name string) bool {
	return agentNamePattern.MatchString(name)
}

// renamePreview reports what a rename would change in the books without taking
// the lock or writing anything.
func (a *app) renamePreview(old, name string) []string {
	var changes []string
	for _, path := range book.Paths(a.config.Agentbooks) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var raw map[string]any
		if json.Unmarshal(data, &raw) != nil {
			continue
		}
		changes = append(changes, book.PreviewRename(raw, old, name, path)...)
	}
	return changes
}

// usageStateTargets lists the live usage files keyed by agent name, with the
// JSON objects inside each that carry those keys. History files are absent by
// design — they record the name the agent had at the time.
var usageStateTargets = map[string][]string{
	"fleet-models.json": {""},
	"policy-state.json": {"applied", "pending"},
	"state.json":        {"limited", "spend"},
}

func (a *app) renameUsage(old, name string, dry bool) []string {
	dir := filepath.Dir(a.config.UsageHistory)
	if dir == "" || dir == "." {
		return nil
	}
	var changes []string
	files := make([]string, 0, len(usageStateTargets))
	for file := range usageStateTargets {
		files = append(files, file)
	}
	sort.Strings(files)

	for _, file := range files {
		path := filepath.Join(dir, file)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var raw map[string]any
		if json.Unmarshal(data, &raw) != nil {
			continue
		}
		moved := false
		for _, section := range usageStateTargets[file] {
			target := raw
			if section != "" {
				nested, ok := raw[section].(map[string]any)
				if !ok {
					continue
				}
				target = nested
			}
			value, ok := target[old]
			if !ok {
				continue
			}
			delete(target, old)
			target[name] = value
			moved = true
			label := file
			if section != "" {
				label = file + "." + section
			}
			changes = append(changes, label)
		}
		if !moved || dry {
			continue
		}
		if err := writeJSONFile(path, raw); err != nil {
			fmt.Fprintf(a.err, "warning: could not rewrite %s: %v\n", path, err)
		}
	}
	return changes
}

func writeJSONFile(path string, raw map[string]any) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rename-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = tmp.Write(encoded); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

// reportCodeReferences greps the agent's own folder for the old name. A live
// reference such as a hook's TARGET_AGENT breaks the moment the session is
// renamed, and only a human can judge which hits are code and which are prose,
// so this prints and never edits.
func (a *app) reportCodeReferences(fleet book.Fleet, old, name string) {
	entry, ok := fleet.Agents[old]
	if !ok || entry.Folder == "" {
		return
	}
	folder := firstPath(entry.Folder)
	if folder == "" {
		return
	}
	// Transcripts and logs are history: they correctly hold the name the agent
	// had that day, and leaving them in drowns the one hit that matters.
	args := []string{"-rIl"}
	for _, skip := range []string{".git", "node_modules", ".codex-home", "sessions", "done"} {
		args = append(args, "--exclude-dir="+skip)
	}
	for _, skip := range []string{"*.jsonl", "*.log", "*.log.*"} {
		args = append(args, "--exclude="+skip)
	}
	cmd := exec.CommandContext(a.ctx, "grep", append(args, "--", old, folder)...)
	out, err := cmd.Output()
	hits := strings.Fields(strings.TrimSpace(string(out)))
	if err != nil && len(hits) == 0 {
		return // grep exits 1 when it finds nothing
	}
	if len(hits) == 0 {
		return
	}
	// Code first: a stale name in a hook breaks the agent, the same name in a
	// note is just prose.
	sort.SliceStable(hits, func(i, j int) bool {
		return !isProse(hits[i]) && isProse(hits[j])
	})
	fmt.Fprintf(a.out, "\n%d file(s) under %s still mention %q — NOT changed, check them:\n", len(hits), folder, old)
	for i, hit := range hits {
		if i == 20 {
			fmt.Fprintf(a.out, "  ... and %d more\n", len(hits)-20)
			break
		}
		fmt.Fprintf(a.out, "  %s\n", hit)
	}
	fmt.Fprintf(a.out, "A live reference (a hook's TARGET_AGENT, a systemd unit, a cron line) will break until it says %q.\n", name)
}
