package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/identity"
	bptmux "blueprint/internal/tmux"
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
//
// ORDER MATTERS, and it is the opposite of the obvious one. Four of the five
// steps are local file or tmux edits that either work or fail loudly. The fifth
// — making the agent retitle its own transcript, by typing /rename into its
// composer — is the only one that can fail for reasons bp does not control (a
// composer in vim INSERT mode, a mid-turn pane, a TUI that ate the keystrokes).
// It used to run LAST as a best-effort warning, which is how `bp rename
// worktrack-main worktrack` produced a half-renamed agent: session and
// agentbook on the new name, transcript still on the old one. That is not
// cosmetic — bp finds an agent's transcript by matching the in-file custom
// title against the agent name, so the CACHE column went blank, bp compact
// reported "transcript okunamadi", and `rush worktrack` had nothing to attach.
//
// So the unreliable step goes FIRST and everything else is downstream of it. If
// the pane refuses, nothing else has happened yet: bp exits non-zero, says
// plainly that nothing was renamed and prints the command to run by hand. That
// is all-or-nothing without needing a rollback path that could itself fail.
func (a *app) rename(args []string) error {
	dry := false
	noRetitle := false
	var positional []string
	for _, arg := range args {
		switch arg {
		case "--dry-run", "-n":
			dry = true
		case "--no-retitle":
			// For the one rename this command could not do: an agent renaming
			// ITSELF. Step 1 types /rename into the target pane and refuses a
			// pane that is mid-turn — and an agent asking for its own rename is
			// by definition mid-turn, so it could never get past it (ada,
			// 2026-09-02).
			//
			// It is a real amputation, not a shortcut: the transcript keeps the
			// OLD custom title, and that title is what resolves an agent to its
			// session file. Until the agent types /rename itself, bp cannot read
			// its context or last turn (blank CACHE column), `bp compact` reports
			// "transcript okunamadi", and `bp open --resume` finds no prior
			// conversation to resume. So it is allowed, and it says so loudly.
			noRetitle = true
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("unknown flag: %s", arg)
			}
			positional = append(positional, arg)
		}
	}
	if len(positional) != 2 {
		return fmt.Errorf("usage: bp rename <old-name> <new-name> [--dry-run] [--no-retitle]")
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

	applyClaims, releaseClaims, err := a.prepareResumeRename(old, name, dry)
	if err != nil {
		return err
	}
	defer releaseClaims()

	// 1. Transcript title, via the agent's own pane. First, because it is the
	// step that can fail: everything below is only reached once the agent's own
	// transcript has been observed carrying the new name. Only Claude understands
	// /rename; typing it at a codex or shell pane would just run it as a command,
	// so those panes skip the step entirely (nothing to retitle).
	switch {
	case noRetitle:
		fmt.Fprintf(a.out, "--no-retitle: %s pane'ine /rename YAZILMADI; transcript basligi %q olarak kaliyor\n", old, old)
	case !liveOld:
		fmt.Fprintf(a.out, "no live tmux session for %s (agentbook only)\n", old)
	case !a.paneRunsClaude(old):
		fmt.Fprintf(a.out, "the %s pane is not a Claude agent: no transcript title to change\n", old)
	case dry:
		report("send /rename %s to the %s pane and wait for its transcript title", name, old)
	default:
		if err := a.renamePane(fleet, old, name); err != nil {
			a.reportRenameRefused(old, name, err)
			return errReported
		}
		report("send /rename %s to the %s pane and wait for its transcript title", name, old)
	}

	// 2. tmux session.
	if liveOld {
		if !dry {
			if err := a.tmux.RenameSession(a.ctx, old, name); err != nil {
				return fmt.Errorf("tmux rename-session: %w", err)
			}
		}
		report("rename tmux session %s -> %s", old, name)
	}

	// 3. Agentbooks.
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

	// Local launch claims and the session-local bar hold names, too. Do not
	// touch native input or rewrite conversation history to update these.
	if err := applyClaims(); err != nil {
		return err
	}
	if liveOld && !dry {
		if entry, ok := fleet.Agents[old]; ok && a.ownsLocalSession(name, entry) {
			if err := a.configureLocalBar(name); err != nil {
				return err
			}
		}
	}

	// 4. Usage state.
	for _, change := range a.renameUsage(old, name, dry) {
		report("update %s", change)
	}

	// 5. Live code references, reported but never edited.
	a.reportCodeReferences(fleet, old, name)

	if dry {
		fmt.Fprintln(a.out, "\ndry run: nothing was changed")
	} else {
		fmt.Fprintf(a.out, "\nrenamed %s -> %s. Verify with: bp status && bp tree\n", old, name)
	}
	if noRetitle && !dry {
		// Said at the END, where it is read: the rename LOOKS complete and the one
		// piece that is missing is invisible until something goes quiet.
		fmt.Fprintf(a.err, "\nEKSIK KALAN TEK ADIM — %s kendi pane'inde SU KOMUTU YAZMALI:\n  /rename %s\n", name, name)
		fmt.Fprintf(a.err, "o yazilana kadar transcript basligi %q kalir: bp bu agent'in baglamini/son turunu OKUYAMAZ (bp status'ta CACHE bos), bp compact 'transcript okunamadi' der ve bp open --resume onceki konusmayi bulamaz.\n", old)
	}
	return nil
}

const (
	// renamePollInterval and renamePollAttempts bound the wait for the agent to
	// record its new title (~10s in total). The agent writes the custom-title
	// record as it processes the slash command, so this is a short wait on a
	// live pane, not a poll for something that may never come.
	renamePollInterval = 500 * time.Millisecond
	renamePollAttempts = 20
)

// renamePane makes the agent retitle its own transcript and does not return
// until that is a fact on disk.
//
// ClearComposer runs first — that is what it was written for. The reported
// failure was a composer that LOOKED empty but made the pre-send check report
// "composer is not empty", so the /rename never went in. ClearComposer settles
// that state with C-u — never Escape, which would cancel a running turn (see its
// own doc comment for the retraction) — and refuses both a working pane and a
// composer holding real text, which is also this command's refusal: a rename is
// never worth interrupting a turn or overwriting a half-written line for.
//
// Verification reads the TRANSCRIPT, never the screen. The rendered TUI title
// bar is not evidence — some panes do not draw one at all — while the transcript
// is exactly what every other bp command resolves an agent by, so a title bp can
// read is the only outcome that counts as a successful rename.
func (a *app) renamePane(fleet book.Fleet, old, name string) error {
	// Clear and send are one operation on one composer, so one pane lock covers
	// both: another bp pasting in between would be typing into a composer this
	// command has just emptied for its own slash command. The lock is given back
	// before the transcript poll below, which reads a file and touches no pane —
	// holding it there would only make the fleet's queues wait on a rename.
	release, err := a.lockPane(old)
	if err != nil {
		return err
	}
	clearErr := a.tmux.ClearComposer(a.ctx, old)
	var sendErr error
	if clearErr == nil {
		sendErr = a.tmux.Send(a.ctx, old, "/rename "+name)
	}
	release()
	if clearErr != nil {
		return clearErr
	}
	// ErrUnverified means the keystrokes went in but nothing confirmed the
	// submit; it must not be re-sent, and the transcript below is a far better
	// witness than the composer anyway. Every other error is a real failure to
	// deliver, so there is nothing to wait for.
	if sendErr != nil && !errors.Is(sendErr, bptmux.ErrUnverified) {
		return sendErr
	}
	folders := a.agentFolders(fleet, old)
	if len(folders) == 0 {
		// Nothing to read the title out of. Rare (a live session that is in no
		// agentbook and whose pane directory tmux would not report), and refusing
		// here would make such an agent unrenameable, so this continues — loudly.
		fmt.Fprintf(a.err, "warning: no folder known for %s, so its transcript title could not be verified\n", old)
		fmt.Fprintf(a.err, "         check it with: bp status\n")
		return nil
	}
	if !a.awaitTranscriptTitle(folders, old, name) {
		return fmt.Errorf("the agent did not retitle its transcript to %q within %s", name, renamePollInterval*renamePollAttempts)
	}
	return nil
}

// awaitTranscriptTitle polls the agent's own session file until it reports the
// new custom title. It resolves that file under the OLD name first and then
// watches those exact files, because /rename appends a fresh custom-title record
// to the running session rather than rewriting the first one — so the same file
// simply starts answering to the new name.
//
// When the old title resolves to nothing (a session that was never titled, or
// one already broken by a half-finished rename) it falls back to the looser
// question bp status itself asks: does any transcript in the folder now carry
// the new name. The precise check is preferred whenever it is available, because
// a folder can hold a stale transcript from an agent that used to have this name.
func (a *app) awaitTranscriptTitle(folders []string, old, name string) bool {
	root := bptmux.ClaudeProjectsRoot()
	var paths []string
	for _, folder := range folders {
		if path, ok := bptmux.ResumeSessionPath(root, folder, old); ok {
			paths = append(paths, path)
		}
	}
	for attempt := 0; ; attempt++ {
		for _, path := range paths {
			if title, ok := bptmux.ReadCustomTitle(path); ok && title == name {
				return true
			}
		}
		if len(paths) == 0 {
			for _, folder := range folders {
				if _, ok := bptmux.ResumeSessionPath(root, folder, name); ok {
					return true
				}
			}
		}
		if attempt >= renamePollAttempts {
			return false
		}
		a.sleep(renamePollInterval)
	}
}

// agentFolders lists the working directories whose Claude project directory may
// hold the agent's transcripts, best first: the agentbook folder (annotations
// like "/srv (home: /srv/server-main)" stripped), then the directory tmux
// started the session in and the pane's current one. Claude munges the cwd it
// was launched in into its projects path, and a book entry can name a different
// directory than the session was actually opened in, so both are worth asking.
func (a *app) agentFolders(fleet book.Fleet, session string) []string {
	var folders []string
	add := func(folder string) {
		if folder == "" {
			return
		}
		for _, existing := range folders {
			if existing == folder {
				return
			}
		}
		folders = append(folders, folder)
	}
	if entry, ok := fleet.Agents[session]; ok {
		add(book.FirstPath(entry.Folder))
	}
	locations, err := a.tmux.Locations(a.ctx)
	if err != nil {
		return folders
	}
	for _, location := range locations {
		if location.Session != session {
			continue
		}
		add(location.StartDir)
		add(location.CurrentDir)
	}
	return folders
}

// reportRenameRefused explains a refused rename. The point of the message is
// that the operator is NOT left guessing which half of the rename happened:
// nothing did, and the way forward is one command.
func (a *app) reportRenameRefused(old, name string, err error) {
	fmt.Fprintf(a.err, "could not make %s retitle its own transcript: %v\n", old, err)
	fmt.Fprintf(a.err, "nothing was renamed: tmux session, agentbooks and usage state are all still on %s.\n", old)
	if errors.Is(err, bptmux.ErrBusy) {
		fmt.Fprintf(a.err, "the pane is mid-turn and must not be interrupted; wait for it to finish (bp peek %s), then run: bp rename %s %s\n", old, old, name)
		return
	}
	fmt.Fprintf(a.err, "type this inside the pane by hand (rush %s; Ctrl-u a few times first if the composer is stuck):\n", old)
	fmt.Fprintf(a.err, "  /rename %s\n", name)
	fmt.Fprintf(a.err, "then run: bp rename %s %s\n", old, name)
}

// sleep waits through the client's clock, so tests that stub it out do not spend
// real seconds waiting for a transcript a fake tmux will never write.
func (a *app) sleep(d time.Duration) {
	if a.tmux != nil && a.tmux.Sleep != nil {
		a.tmux.Sleep(d)
		return
	}
	time.Sleep(d)
}

// paneRunsClaude reports whether the session's pane is a Claude agent. A pane
// whose command cannot be read is assumed not to be Claude: sending a slash
// command to a shell would type it as a shell command.
func (a *app) paneRunsClaude(session string) bool {
	commands, err := a.tmux.Commands(a.ctx)
	if err != nil {
		return false
	}
	return strings.Contains(commands[session], "claude")
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

// validAgentName defers to the identity package: the name shape and the sender
// labels built to fail it must never drift apart.
func validAgentName(name string) bool {
	return identity.ValidName(name)
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
	folder := book.FirstPath(entry.Folder)
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

func (a *app) prepareResumeRename(old, name string, dry bool) (func() error, func(), error) {
	noop := func() error { return nil }
	release := func() {}
	root := filepath.Join(a.config.StateDir, "local-resume")
	paths, err := filepath.Glob(filepath.Join(root, "*.json"))
	if err != nil || len(paths) == 0 {
		return noop, release, err
	}
	// Hold the native launch guard across the rename, including the claim update.
	if !dry {
		lock, err := os.OpenFile(filepath.Join(root, "launch.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return noop, release, err
		}
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			lock.Close()
			return noop, release, fmt.Errorf("another BP launch is in progress; retry rename: %w", err)
		}
		release = func() { syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); lock.Close() }
	}

	changes := map[string]map[string]any{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			release()
			return noop, func() {}, err
		}
		var raw map[string]any
		if json.Unmarshal(data, &raw) != nil {
			release()
			return noop, func() {}, fmt.Errorf("invalid resume claim: %s", path)
		}
		if raw["Name"] == old {
			raw["Name"] = name
			changes[path] = raw
		}
	}
	return func() error {
		for _, path := range paths {
			raw, ok := changes[path]
			if !ok {
				continue
			}
			fmt.Fprintf(a.out, "update resume claim %s: %s -> %s\n", path, old, name)
			if !dry {
				if err := writeJSONFile(path, raw); err != nil {
					return err
				}
			}
		}
		return nil
	}, release, nil
}
