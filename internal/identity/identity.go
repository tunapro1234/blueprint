// Package identity answers one question for every outbound message: who is
// sending it. The answer becomes a visible label — the "[sender]" envelope on
// agent-to-agent messages and the "[agent]" prefix on WhatsApp — so a wrong
// answer is not a cosmetic bug: agents act on the name they read.
//
// The rule that matters is that `tmux display-message -p '#S'` with no target
// is only meaningful INSIDE a pane. Called from cron, a systemd unit or any
// other process with no TMUX in its environment, tmux answers for whichever
// client happens to be attached — a spectator, not the sender. That is how four
// cron-sent WhatsApp messages ended up signed by three uninvolved agents on
// 2026-08-09/10. Everything below exists so that mistake has exactly one place
// to live, and so an unattributable message says "unknown" instead of
// borrowing the orchestrator's name.
package identity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Sessioner is the sliver of the tmux client this package needs. It is an
// interface so a test can prove DisplaySession is never consulted with TMUX
// empty — the single fact that caused the incident.
type Sessioner interface {
	DisplaySession(ctx context.Context) (string, error)
}

// Unknown is the label for a sender we cannot name. It is deliberately a word
// no agent answers to: readers must see the gap rather than trust a guess.
const Unknown = "unknown"

// InferMark separates a guessed label from a stated one. It cannot appear in a
// valid agent name (see namePattern), so "cron?:inbox_watcher.py" is
// structurally impossible to mistake for an agent. A stated origin uses the
// same shape without the mark: "cron:/srv/kavram/.../inbox_watcher.py".
const InferMark = "?"

// Identity is a label plus how much the label is worth.
type Identity struct {
	Label    string
	ThreadID string
	Parent   string
	// Certain records attribution confidence, not execution authority.
	// Authoritative separately excludes declarations and CLI subagents.
	Certain bool
	// Source names the signal that won, for diagnostics and warnings.
	Source string
	Reason string
	// Declared is the caller-stated --from value when it shaped Label. It is
	// empty when a stronger signal (a pane, a thread, the daemon) won and the
	// stated value was ignored, so a call site can say so.
	Declared string `json:"Declared,omitempty"`
}

// Inferred reports whether the label confesses a guess.
func (i Identity) Inferred() bool { return strings.Contains(i.Label, InferMark) }

// Options tunes the resolution for a given call site.
type Options struct {
	Origin func(context.Context) Origin
	// Pane supplies a kernel-ancestry-verified context, never thread authority.
	Pane   func(context.Context) (string, error)
	Thread func(context.Context, string) Identity
	// From is a sender stated by the caller (bp wa send --from). It is honored
	// only outside tmux: inside a pane the pane's own session wins, so an agent
	// cannot sign as somebody else. An invalid value is ignored here; call
	// sites reject it loudly with ValidFrom before getting this far.
	From string
	// FromMarked asks for From to be shown as a declaration, never as a bare
	// signature: "declared?:<label>", or "service:<unit>/<label>" when the
	// caller runs in a system service unit. A verified daemon child also
	// outranks it. bp msg sets it; bp wa send keeps the historical bare form.
	FromMarked bool
	// Cgroup returns this process's cgroup path ("/system.slice/x.service").
	// Nil reads /proc/self/cgroup. Tests inject their own so no result depends
	// on where the suite runs.
	Cgroup func() string
	// HostUnit reports whether the system unit at the given cgroup path only
	// hosts other people's processes (cron, ssh, the tmux server or the bp
	// daemon), so being inside it says nothing about who is calling. Nil uses
	// hostsOthers.
	HostUnit func(cgroup, unit string) bool
	// Known reports agentbook membership. A derived name (a login name) that
	// collides with a real agent is downgraded to a guess instead of claiming
	// that agent's identity: a plausible-but-wrong name is more dangerous than
	// "unknown". Nil means "nothing is known".
	Known func(string) bool
	// DaemonChild verifies the exact caller -> parent -> bp daemon -> PID 1
	// chain for pid. Nil uses the local /proc implementation.
	DaemonChild func(int) bool
	// Infer allows explicitly uncertain process-tree attribution for scripts.
	// These labels never establish hierarchy, slash-command or force authority.
	Infer bool
	// Fallback is the label of last resort, used only when every signal above
	// is silent. Empty means Unknown. This is where a call site may keep a
	// historical default; the chain itself never invents one.
	Fallback string
	// Ancestors returns the process chain, nearest first, each entry a command
	// line split into arguments. Nil reads /proc. Tests inject their own.
	Ancestors func() [][]string
}

// namePattern is the shape of an agent name (tmux session names, agentbook
// keys). Single definition on purpose: the inference labels are built to fail
// this pattern.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidName reports whether name has agent-name shape.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// maxLabel keeps an envelope readable and bounded; nothing legitimate is close.
const maxLabel = 120

// ValidFrom accepts the values a caller may state as its own name. A path or a
// "cron:<path>" origin must stay expressible, so this is a rejection list, not
// an allow list: anything that could forge envelope structure ("[", "]"),
// smuggle a second line, or hide as a control byte is refused.
func ValidFrom(value string) error {
	if value == "" {
		return fmt.Errorf("--from requires a label")
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("--from must not start or end with whitespace")
	}
	if len(value) > maxLabel {
		return fmt.Errorf("--from is too long (max %d bytes)", maxLabel)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("--from must be valid UTF-8")
	}
	if strings.ContainsAny(value, "[]") {
		return fmt.Errorf("--from must not contain [ or ]: they are the envelope's own markers")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("--from must not contain control characters")
		}
	}
	return nil
}

// Resolve separates a verified pane/thread from a self-declared label. Codex
// never falls back to inherited tmux, AGENT or login names. Other unverified
// agent claims are visibly marked and cannot carry hierarchy authority.
//
// Order: Codex thread, tmux pane, pane process context, --from, AGENT (daemon
// child or guess), login name, system service unit ("service:<unit>"), the
// process-tree guess, a transient unit's generated name, then Fallback or
// Unknown. A unit label is certain attribution but never Authoritative.
func Resolve(ctx context.Context, client Sessioner, opts Options) Identity {
	probe := opts.Origin
	if probe == nil {
		probe = CodexOrigin
	}
	origin := probe(ctx)
	if origin.ThreadID != "" {
		if opts.Thread != nil {
			if who := opts.Thread(ctx, origin.ThreadID); who.Label != "" {
				if who.Source == "codex-local-hint" {
					if who.Inferred() {
						who.ThreadID, who.Certain = origin.ThreadID, false
						return who
					}
					// Local runtime evidence is intentionally only a hint. Refuse
					// one whose visible label does not confess that uncertainty.
				} else if origin.Verified {
					return who
				} else if who.Certain && who.Source == "codex-thread" {
					if opts.Pane != nil {
						if name, err := opts.Pane(ctx); err == nil && name == who.Label {
							// The pin and pane agree on a label; codex-pane remains
							// excluded from Authoritative's power checks.
							return Identity{Label: who.Label, ThreadID: origin.ThreadID, Certain: true, Source: "codex-pane"}
						}
					}
					if origin.ServerThread {
						return Identity{Label: who.Label, ThreadID: origin.ThreadID, Certain: true, Source: "codex-app-server"}
					}
					who.Label += "?"
					who.ThreadID, who.Certain, who.Source = origin.ThreadID, false, "codex-unverified"
					return who
				} else if who.Certain && who.Source == "codex-subagent" {
					// A registry match can provide a readable hint without proving
					// this caller owns the thread. Keep the UUID in diagnostics and
					// never turn this display label into hierarchy/force authority.
					who.Label += "?"
					who.ThreadID, who.Certain, who.Source = origin.ThreadID, false, "codex-unverified"
					return who
				}
			}
		}
		return Identity{Label: "codex?:" + sanitize(origin.ThreadID), ThreadID: origin.ThreadID, Source: "codex-unverified"}
	}
	if origin.CodexDetected {
		return Identity{Label: Unknown, Source: "codex-unverified", Reason: "Codex caller has no thread evidence"}
	}
	if os.Getenv("TMUX") != "" && client != nil {
		if value, err := client.DisplaySession(ctx); err == nil {
			if value = strings.TrimSpace(value); value != "" {
				return Identity{Label: value, Certain: true, Source: "tmux"}
			}
		}
	}
	reason := "no usable sender evidence"
	if opts.Pane != nil {
		if name, err := opts.Pane(ctx); err == nil && ValidName(name) && opts.Known != nil && opts.Known(name) {
			// CLI subagents can share this process. A readable parent context is
			// useful, but cannot grant that agent's hierarchy or force rights.
			return Identity{Label: name + "?", Parent: name, Source: "pane-process-context"}
		} else if err != nil {
			reason = err.Error()
		}
	}
	agent := os.Getenv("AGENT")
	if agent != "" && !ValidName(agent) {
		agent = ""
	}
	daemonChild := func() bool {
		isDaemonChild := opts.DaemonChild
		if isDaemonChild == nil {
			isDaemonChild = DaemonChild
		}
		return isDaemonChild(os.Getpid())
	}
	if opts.From != "" && ValidFrom(opts.From) == nil {
		if opts.FromMarked {
			// A verified daemon child is the daemon's own process: like a pane,
			// it cannot sign as somebody else.
			if agent != "" && daemonChild() {
				return Identity{Label: agent, Certain: true, Source: "bp-daemon-child"}
			}
			if unit, _, ok := systemUnit(opts); ok {
				// The unit is kernel evidence; the label after it is what the
				// unit says about itself, and it stays inside the unit's name.
				return Identity{Label: unitPrefix + unit + "/" + opts.From, Certain: true, Source: "systemd-unit", Declared: opts.From}
			}
			return Identity{Label: "declared" + InferMark + ":" + opts.From, Source: "--from", Declared: opts.From}
		}
		if ValidName(opts.From) {
			return Identity{Label: "declared?:" + opts.From, Source: "--from", Declared: opts.From}
		}
		return Identity{Label: opts.From, Certain: true, Source: "--from", Declared: opts.From}
	}
	if agent != "" {
		if daemonChild() {
			return Identity{Label: agent, Certain: true, Source: "bp-daemon-child"}
		}
		return Identity{Label: "agent?:" + agent, Source: "AGENT"}
	}
	// Root login is disabled on the box, so people SSH as themselves and reach
	// bp through sudo: these name that human.
	for _, key := range []string{"SUDO_USER", "USER", "LOGNAME"} {
		value := os.Getenv(key)
		if value == "" || value == "root" || !ValidName(value) {
			continue
		}
		if value == "server-main" || opts.Known != nil && opts.Known(value) {
			// A login name that happens to be an agent's name must not inherit
			// that agent's standing.
			return Identity{Label: "user" + InferMark + ":" + value, Source: key + "-collision"}
		}
		return Identity{Label: value, Certain: true, Source: key}
	}
	// A named system unit is kernel evidence of who is calling. A transient
	// unit's generated name (systemd-run without --unit) says less than the
	// script it runs, so a process-tree guess outranks it.
	unit, transient, inUnit := systemUnit(opts)
	if inUnit && !transient {
		return Identity{Label: unitPrefix + unit, Certain: true, Source: "systemd-unit"}
	}
	if opts.Infer {
		if label := inferred(opts.Ancestors); label != "" {
			return Identity{Label: label, Source: "process-tree"}
		}
	}
	if inUnit {
		return Identity{Label: unitPrefix + unit, Certain: true, Source: "systemd-unit"}
	}
	if opts.Fallback != "" {
		return Identity{Label: "fallback?:" + sanitize(opts.Fallback), Source: "fallback"}
	}
	return Identity{Label: Unknown, Source: "none", Reason: reason}
}

// interpreters are programs that say nothing about who is calling: the script
// they are running does.
var interpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
	"fish": true, "env": true, "sudo": true, "nohup": true, "timeout": true,
	"xargs": true, "python": true, "python3": true, "perl": true, "ruby": true,
	"node": true, "deno": true, "bwrap": true,
}

// launchers map a recognizable ancestor to the origin worth reporting.
var launchers = map[string]string{
	"cron": "cron", "crond": "cron", "cronie": "cron", "anacron": "cron",
	"atd": "cron", "systemd": "systemd", "sshd": "ssh",
}

// inferred builds a label from the process tree. The operator's warning stands:
// inferring identity is the disease being cured, so the result must never be
// mistakable for an agent name — every label produced here carries InferMark,
// and a label that somehow would not is dropped.
func inferred(ancestors func() [][]string) string {
	if ancestors == nil {
		ancestors = func() [][]string { return procAncestors(os.Getppid(), 8) }
	}
	chain := ancestors()
	if len(chain) == 0 || len(chain[0]) == 0 {
		return ""
	}
	kind := ""
	for _, frame := range chain {
		if len(frame) == 0 {
			continue
		}
		if mapped, ok := launchers[strings.ToLower(program(frame[0]))]; ok {
			kind = mapped
			break
		}
	}
	if kind == "" {
		kind = strings.ToLower(program(chain[0][0]))
	}
	detail := ""
	for _, frame := range chain {
		for _, arg := range frame[1:] {
			if scriptish(arg) {
				detail = program(arg)
				break
			}
		}
		if detail != "" {
			break
		}
	}
	kind, detail = sanitize(kind), sanitize(detail)
	if kind == "" && detail == "" {
		return ""
	}
	if kind == "" {
		kind = "proc"
	}
	label := kind + InferMark
	if detail != "" && detail != kind {
		label += ":" + detail
	}
	if !strings.Contains(label, InferMark) || ValidName(label) {
		return "" // belt and braces: never hand back an agent-shaped name
	}
	return label
}

// program strips a path and a login shell's leading dash.
func program(arg string) string {
	return strings.TrimLeft(filepath.Base(arg), "-")
}

// scriptish reports whether an argument names the work being done rather than
// how it is being run: a path, or a file with an extension.
func scriptish(arg string) bool {
	if arg == "" || strings.HasPrefix(arg, "-") {
		return false
	}
	// A shell's -c command string is not a script: its last path is whatever
	// the command happens to mention. Claude Code runs every tool command as
	// `zsh -c '… pwd -P >| /tmp/claude-<id>-cwd'`, and taking that path's base
	// labelled messages "zsh?:claude-e558-cwd" — a session-shaped name that
	// was not the sender (issue #9).
	if strings.ContainsAny(arg, " \t\n") {
		return false
	}
	base := program(arg)
	if base == "" || base == "bp" || interpreters[strings.ToLower(base)] {
		return false
	}
	return strings.Contains(arg, "/") || strings.Contains(base, ".")
}

// sanitize keeps a label's pieces to characters that cannot disturb an envelope
// or a log line, and bounded in length.
func sanitize(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.Trim(b.String(), ".-_")
}

// procAncestors reads command lines up the process tree, nearest first.
func procAncestors(pid, limit int) [][]string {
	var chain [][]string
	for hop := 0; hop < limit && pid > 1; hop++ {
		if args := procCmdline(pid); len(args) > 0 {
			chain = append(chain, args)
		}
		parent, ok := procParent(pid)
		if !ok || parent == pid {
			break
		}
		pid = parent
	}
	return chain
}

func procCmdline(pid int) []string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil
	}
	var args []string
	for _, part := range strings.Split(string(data), "\x00") {
		if part != "" {
			args = append(args, part)
		}
	}
	return args
}

// procParent reads PPid from /proc/<pid>/status. status is used instead of stat
// because a comm containing spaces or parentheses makes stat's fields
// ambiguous.
func procParent(pid int) (int, bool) {
	return procParentAt("/proc", pid)
}

func procParentAt(procRoot string, pid int) (int, bool) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "PPid:")
		if !ok {
			continue
		}
		parent, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			return 0, false
		}
		return parent, true
	}
	return 0, false
}

// DaemonChild reports whether pid's exact parent chain is caller -> parent ->
// bp daemon -> PID 1. The daemon executable is checked by basename because
// the service and its CLI children may use different bp installations.
func DaemonChild(pid int) bool { return daemonChildAt("/proc", pid) }

func daemonChildAt(procRoot string, pid int) bool {
	if pid <= 1 {
		return false
	}
	bridge, ok := procParentAt(procRoot, pid)
	if !ok || bridge <= 1 {
		return false
	}
	daemon, ok := procParentAt(procRoot, bridge)
	if !ok || daemon <= 1 {
		return false
	}
	init, ok := procParentAt(procRoot, daemon)
	if !ok || init != 1 {
		return false
	}

	daemonPath, err := os.Readlink(filepath.Join(procRoot, strconv.Itoa(daemon), "exe"))
	if err != nil || filepath.Base(strings.TrimSuffix(daemonPath, " (deleted)")) != "bp" {
		return false
	}
	command, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(daemon), "cmdline"))
	if err != nil {
		return false
	}
	args := strings.Split(string(command), "\x00")
	return len(args) > 1 && args[1] == "daemon"
}

// Authoritative permits existing hierarchy/force gates only for verified main
// agents. A subagent can be attributed without inheriting its parent's powers.
func (i Identity) Authoritative() bool {
	return i.Certain && i.Parent == "" && (i.Source == "tmux" || i.Source == "codex-thread")
}

// unitPrefix starts every label built from a systemd unit. Its ":" keeps the
// label from ever having agent-name shape.
const unitPrefix = "service:"

// maxUnit bounds the unit part of a label; real unit names are far shorter.
const maxUnit = 64

// hostUnits are system units whose processes belong to whoever logged in or
// scheduled the job, not to the unit: cron and at run other people's jobs, ssh
// hosts their shells, and blueprint is the conventional unit of the bp daemon,
// whose tmux server parents every agent pane. hostsOthers also detects the
// last case live, so a renamed daemon unit is covered too.
var hostUnits = map[string]bool{
	"cron": true, "crond": true, "cronie": true, "anacron": true, "atd": true,
	"ssh": true, "sshd": true, "blueprint": true,
}

// transientUnit matches the names systemd-run generates when no --unit is
// given: run-u<n> (older systemd), run-r<hex>, run-p<pid>-i<n>.
var transientUnit = regexp.MustCompile(`^run-(u[0-9]+|r[0-9a-f]+|p[0-9]+-i[0-9]+)$`)

// systemUnit names the system service the caller runs in, if that is
// evidence of who is calling. It reports false for scopes, for user@N.service
// subtrees and user.slice, for a delegated sub-cgroup of a service, and for
// units that only host other people's processes (see HostUnit).
func systemUnit(opts Options) (unit string, transient, ok bool) {
	read := opts.Cgroup
	if read == nil {
		read = selfCgroup
	}
	path := strings.TrimSpace(read())
	unit, ok = parseSystemUnit(path)
	if !ok {
		return "", false, false
	}
	host := opts.HostUnit
	if host == nil {
		host = hostsOthers
	}
	if host(path, unit) {
		return "", false, false
	}
	return unit, transientUnit.MatchString(unit), true
}

// parseSystemUnit accepts "/system.slice/[<sub>.slice/...]<name>.service"
// and returns the sanitized <name>. Anything else is not a system service.
func parseSystemUnit(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] != "system.slice" {
		return "", false
	}
	for _, part := range parts[1 : len(parts)-1] {
		if !strings.HasSuffix(part, ".slice") {
			return "", false // a sub-cgroup inside a service is not the service
		}
	}
	name, ok := strings.CutSuffix(parts[len(parts)-1], ".service")
	if !ok || name == "" || strings.HasPrefix(name, "user@") {
		return "", false
	}
	unit := sanitizeUnit(name)
	if unit == "" || ValidName(unitPrefix+unit) {
		return "", false // belt and braces: never an agent-shaped label
	}
	return unit, true
}

// sanitizeUnit keeps a unit name to label-safe characters and a bounded
// length. systemd's escapes ("\x2d") are decoded first; "@" (template
// instances) becomes "-" because "@" separates agent from peer in federated
// addresses.
func sanitizeUnit(name string) string {
	name = strings.ReplaceAll(name, `\x2d`, "-")
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		case r == '@':
			b.WriteByte('-')
		}
		if b.Len() >= maxUnit {
			break
		}
	}
	return strings.Trim(b.String(), ".-_")
}

// selfCgroup is the default Options.Cgroup. It is a variable so a test binary
// can make sure no case reads the cgroup of whatever runs the suite.
var selfCgroup = readSelfCgroup

// readSelfCgroup reads this process's cgroup v2 path, or the systemd
// hierarchy's path on a hybrid v1 host. Empty when neither is present.
func readSelfCgroup() string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	return parseCgroupFile(string(data))
}

func parseCgroupFile(data string) string {
	fallback := ""
	for _, line := range strings.Split(data, "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok {
			return path
		}
		if _, rest, ok := strings.Cut(line, ":name=systemd:"); ok && fallback == "" {
			fallback = rest
		}
	}
	return fallback
}

// hostsOthers reports whether a system unit hosts processes that belong to
// somebody else: a known host unit by name, or a unit that holds a tmux server
// or a bp daemon (every agent pane, and every background job an agent leaves
// behind, sits in that unit's cgroup).
func hostsOthers(cgroup, unit string) bool {
	return hostsOthersAt("/sys/fs/cgroup", "/proc", cgroup, unit)
}

func hostsOthersAt(cgroupRoot, procRoot, cgroup, unit string) bool {
	if hostUnits[unit] {
		return true
	}
	data, err := os.ReadFile(filepath.Join(cgroupRoot, filepath.Clean("/"+cgroup), "cgroup.procs"))
	if err != nil {
		return false
	}
	self := strconv.Itoa(os.Getpid())
	for _, pid := range strings.Fields(string(data)) {
		if pid == self {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(procRoot, pid, "comm"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(comm)) == "tmux: server" {
			return true
		}
		exe, err := os.Readlink(filepath.Join(procRoot, pid, "exe"))
		if err != nil || filepath.Base(strings.TrimSuffix(exe, " (deleted)")) != "bp" {
			continue
		}
		command, err := os.ReadFile(filepath.Join(procRoot, pid, "cmdline"))
		if err != nil {
			continue
		}
		if args := strings.Split(string(command), "\x00"); len(args) > 1 && args[1] == "daemon" {
			return true
		}
	}
	return false
}
