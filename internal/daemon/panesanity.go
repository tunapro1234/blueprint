package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// paneSanity is the watchdog for the failure that has now happened twice: bp
// stops recognising a live agent because the NAME it keys on changed under it.
//
//	2026-08-22  a Hermes pane reports "python", bp reads it as dead.
//	2026-08-25  the sandbox comes off Codex, the pane reports "node" instead of
//	            "bwrap", bp reads probot-out-codex and probot-egitim-cx as dead
//	            and closes inbound delivery to both.
//
// Both times a human noticed. The point of this sweep is that the next one is
// noticed by bp, and it works by asking a question that does NOT go through the
// pane command or the TUI screen — the two things that drift:
//
//	CLASS A  the agentbook says this agent is open, bp's own agent test says the
//	         pane is not an agent, and yet an agent BINARY is running inside it
//	         (/proc, by executable path). That contradiction is the drift itself.
//	CLASS B  the pane is a live Claude agent, but bp cannot resolve a session
//	         file for it, so every context/model/turn reading for that agent is
//	         silently blank. This caught server-main, whose agentbook folder
//	         carries an annotation ("/srv (home: /srv/server-main)").
//
// Neither class fails the sweep it shares a beat with, and each agent is
// reported at most once per paneSanityCooldown: a watchdog that repeats itself
// is one the fleet learns to skip.
const paneSanityCooldown = 24 * time.Hour

// agentBinaries are the executables an agent pane runs. The match is on the
// executable PATH, not on the pane command, which is exactly the point: the
// unsandboxed Codex pane calls itself "node" while its argv[0] still reads
// .../@openai/codex/.../bin/codex.
var agentBinaries = []string{"claude", "codex", "hermes"}

// paneObservation is one session as a sweep saw it. Keeping the decision away
// from tmux and /proc is what makes the rules testable.
type paneObservation struct {
	Session string
	// Open is the agentbook's word: an agent nobody claims is open proves
	// nothing when bp declines to talk to it.
	Open bool
	// IsAgent is bp's own verdict — the one that gates inbound delivery.
	IsAgent bool
	// Binary is the agent executable found in the pane's process tree, empty
	// when there is none.
	Binary string
	// ClaudePane distinguishes class B's subject: only a Claude session has a
	// transcript to resolve. Codex keeps rollouts elsewhere and Hermes keeps
	// none, so their blank state is not evidence of anything.
	ClaudePane bool
	// SessionFound is whether bp resolved a session file for this agent.
	SessionFound bool
	// Folder is reported with class B so the reader can see the string bp
	// actually looked up.
	Folder string
}

// paneSanityFindings returns one human-readable line per contradiction, in
// session order so a sweep's output is stable.
func paneSanityFindings(observations []paneObservation) map[string]string {
	findings := make(map[string]string)
	sorted := append([]paneObservation(nil), observations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Session < sorted[j].Session })
	for _, o := range sorted {
		if !o.Open {
			continue
		}
		if !o.IsAgent {
			if o.Binary != "" {
				findings[o.Session] = fmt.Sprintf(
					"bp: %s pane'ini AGENT SAYMIYOR ama icinde %s kosuyor — inbound teslim bu agent'a kapali. bp'nin TUI imzasi kaymis olabilir (kurulum/surum degisikligi); internal/tmux IsAgentPane'e bak, bp peek %s ile ekrani gor.",
					o.Session, o.Binary, o.Session)
			}
			continue
		}
		if o.ClaudePane && !o.SessionFound {
			findings[o.Session] = fmt.Sprintf(
				"bp: %s canli bir Claude pane'i ama bp oturum dosyasini bulamiyor (folder=%q) — bu agent'in baglam/model/tur okumalari BOS gorunuyor, agent sessiz sanilabilir. agentbook folder alanini ve ~/.claude/projects karsiligini kontrol et.",
				o.Session, o.Folder)
		}
	}
	return findings
}

// dueFindings drops the findings already reported inside the cooldown, and
// stamps the ones it lets through.
func dueFindings(findings map[string]string, reported map[string]string, now time.Time) []string {
	var due []string
	for session, message := range findings {
		if stamp, ok := reported[session]; ok {
			if when, err := time.Parse(time.RFC3339, stamp); err == nil && now.Sub(when) < paneSanityCooldown {
				continue
			}
		}
		reported[session] = now.UTC().Format(time.RFC3339)
		due = append(due, message)
	}
	sort.Strings(due)
	// A session that has recovered must be able to alarm again later, so its
	// stamp is dropped once it stops being a finding.
	for session := range reported {
		if _, still := findings[session]; !still {
			delete(reported, session)
		}
	}
	return due
}

// paneAgentBinary walks the pane's process tree and returns the first agent
// executable it finds. Reading /proc rather than asking tmux is deliberate:
// this watchdog exists because the tmux-visible name lies.
func paneAgentBinary(pid int) string {
	if pid <= 0 {
		return ""
	}
	children := procChildren()
	seen := make(map[int]bool)
	queue := []int{pid}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if seen[current] {
			continue
		}
		seen[current] = true
		if name := agentBinaryIn(procCmdline(current)); name != "" {
			return name
		}
		queue = append(queue, children[current]...)
	}
	return ""
}

// agentBinaryIn looks at the executable only — argv[0] — so a shell command
// that merely MENTIONS an agent ("grep claude", "bp msg codex-agent") is not
// mistaken for one running.
func agentBinaryIn(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	base := strings.ToLower(filepath.Base(strings.TrimSpace(argv[0])))
	for _, name := range agentBinaries {
		if base == name {
			return name
		}
	}
	// A packaged agent may be launched through its runtime, in which case argv[0]
	// is the runtime and the script path carries the name. Only the SECOND
	// argument is consulted, and only as a path, which is what the unsandboxed
	// Codex pane looks like.
	if len(argv) > 1 && strings.HasPrefix(argv[1], "/") {
		lower := strings.ToLower(argv[1])
		for _, name := range agentBinaries {
			if strings.Contains(lower, "/"+name+"/") || filepath.Base(lower) == name {
				return name
			}
		}
	}
	return ""
}

func procChildren() map[int][]int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	children := make(map[int][]int, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if parent := procParent(pid); parent > 0 {
			children[parent] = append(children[parent], pid)
		}
	}
	return children
}

func procParent(pid int) int {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	// The comm field is parenthesised and may itself contain spaces, so the
	// fields after it are counted from the LAST ')'.
	close := strings.LastIndexByte(string(data), ')')
	if close < 0 {
		return 0
	}
	fields := strings.Fields(string(data)[close+1:])
	if len(fields) < 2 {
		return 0
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0
	}
	return parent
}

func procCmdline(pid int) []string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil
	}
	var argv []string
	for _, field := range strings.Split(string(data), "\x00") {
		if field != "" {
			argv = append(argv, field)
		}
	}
	return argv
}

// paneSanityScan turns one sweep's observations into alarms. Like the other two
// watchdogs on this beat it swallows nothing but its own noise: findings go to
// the log and to server-main, and the state it keeps is only the cooldown.
func (s *Service) paneSanityScan(observations []paneObservation, state *busySanityState, now time.Time) {
	if state.PaneSanityReported == nil {
		state.PaneSanityReported = make(map[string]string)
	}
	for _, message := range dueFindings(paneSanityFindings(observations), state.PaneSanityReported, now) {
		s.log.Print(message)
		if s.queue != nil {
			if _, err := s.queue.Enqueue("server-main", "bp", message); err != nil {
				s.log.Printf("pane-sanity: alarm could not be queued: %v", err)
			}
		}
	}
}
