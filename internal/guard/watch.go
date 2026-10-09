package guard

import (
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

// EventKind is an observation fed to the Watch.
type EventKind string

const (
	// EvDenied: a peer request was refused by policy (Detail = decision code).
	EvDenied EventKind = "denied"
	// EvLookup: a peer looked up a name (Target), found or not.
	EvLookup EventKind = "lookup"
	// EvInbound: untrusted text was delivered to a local agent (Agent).
	EvInbound EventKind = "inbound"
	// EvSensitive: a local agent touched a secret (tool use, file read).
	EvSensitive EventKind = "sensitive-access"
	// EvOutbound: a local agent sent text across a trust boundary.
	EvOutbound EventKind = "outbound"
)

// Event is one observation. Peer is the authenticated peer alias; Agent is a
// local agent name.
type Event struct {
	Time     time.Time `json:"time"`
	Kind     EventKind `json:"kind"`
	Peer     string    `json:"peer,omitempty"`
	Agent    string    `json:"agent,omitempty"`
	Target   string    `json:"target,omitempty"`
	Channel  string    `json:"channel,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	Severity Severity  `json:"severity,omitempty"` // of the inbound findings
}

// Alert is a pattern the owner must see.
type Alert struct {
	Time     time.Time `json:"time"`
	Rule     string    `json:"rule"`
	Severity Severity  `json:"severity"`
	Peer     string    `json:"peer,omitempty"`
	Agent    string    `json:"agent,omitempty"`
	Channel  string    `json:"channel,omitempty"`
	Summary  string    `json:"summary"`
}

// Sink receives every event and alert. The audit adapter (W1 internal/audit)
// writes both to audit.jsonl; the alert path notifies the owner. Sinks must
// not block for long; Watch calls them with its lock released.
type Sink interface {
	Event(Event)
	Alert(Alert)
}

// WatchConfig tunes detection windows. Zero values use the defaults.
type WatchConfig struct {
	// Window for probe and enumeration counts (default 10m).
	Window time.Duration
	// ProbeThreshold distinct non-exposed targets in Window (default 3).
	ProbeThreshold int
	// LookupThreshold distinct lookup names in Window (default 8).
	LookupThreshold int
	// Taint is how long an agent stays tainted after untrusted input (default 30m).
	Taint time.Duration
	// Cooldown suppresses a repeated alert for the same rule and subject (default 10m).
	Cooldown time.Duration
	// TaintSource, when set, reports outside text that reached an agent through
	// another process (P2P, fed and API inbound run in different processes).
	// It is consulted only for secret-access and outbound events, and the newer
	// of it and this Watch's own taint wins. MessageLogTaint is the standard
	// source: the delivered Remote records in messages.jsonl.
	TaintSource func(agent string, since time.Time) (Taint, bool)
}

// Watch correlates events into alerts:
//
//   - probe: a peer addressed non-exposed agents (first one warns, ProbeThreshold
//     distinct ones is high);
//   - enumeration: a peer looked up many distinct names;
//   - tainted-secret-access: an agent touched a secret while tainted by
//     untrusted input;
//   - tainted-relay: a tainted agent sent text across a boundary to a
//     different peer than the one that tainted it, or sent redacted secrets.
type Watch struct {
	cfg  WatchConfig
	sink Sink
	now  func() time.Time

	mu      sync.Mutex
	probes  map[string][]stamp // peer -> denied targets
	lookups map[string][]stamp // peer -> looked-up names
	taint   map[string]taint   // agent -> latest untrusted input
	last    map[string]time.Time
}

type stamp struct {
	t    time.Time
	name string
}

type taint struct {
	t        time.Time
	peer     string
	channel  string
	severity Severity
}

func NewWatch(cfg WatchConfig, sink Sink) *Watch {
	if cfg.Window <= 0 {
		cfg.Window = 10 * time.Minute
	}
	if cfg.ProbeThreshold <= 0 {
		cfg.ProbeThreshold = 3
	}
	if cfg.LookupThreshold <= 0 {
		cfg.LookupThreshold = 8
	}
	if cfg.Taint <= 0 {
		cfg.Taint = 30 * time.Minute
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 10 * time.Minute
	}
	return &Watch{cfg: cfg, sink: sink, now: time.Now,
		probes: map[string][]stamp{}, lookups: map[string][]stamp{},
		taint: map[string]taint{}, last: map[string]time.Time{}}
}

// Observe records ev and emits any alerts it completes.
func (w *Watch) Observe(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = w.now()
	}
	w.mu.Lock()
	alerts := w.observe(ev)
	w.mu.Unlock()
	if w.sink == nil {
		return
	}
	w.sink.Event(ev)
	for _, a := range alerts {
		w.sink.Alert(a)
	}
}

func (w *Watch) observe(ev Event) []Alert {
	var out []Alert
	emit := func(a Alert, subject string) {
		key := a.Rule + "\x00" + string(a.Severity) + "\x00" + subject
		if t, ok := w.last[key]; ok && ev.Time.Sub(t) < w.cfg.Cooldown {
			return
		}
		w.last[key] = ev.Time
		a.Time = ev.Time
		out = append(out, a)
	}
	switch ev.Kind {
	case EvDenied:
		if ev.Detail != "not-exposed" && ev.Detail != "room-not-granted" {
			return nil
		}
		w.probes[ev.Peer] = w.push(w.probes[ev.Peer], ev.Time, ev.Target)
		n := distinct(w.probes[ev.Peer])
		sev := Warn
		if n >= w.cfg.ProbeThreshold {
			sev = High
		}
		emit(Alert{Rule: "probe", Severity: sev, Peer: ev.Peer, Channel: ev.Channel,
			Summary: "peer addressed " + strconv.Itoa(n) + " non-exposed target(s), latest " + quote(ev.Target)}, ev.Peer)
	case EvLookup:
		w.lookups[ev.Peer] = w.push(w.lookups[ev.Peer], ev.Time, ev.Target)
		if n := distinct(w.lookups[ev.Peer]); n >= w.cfg.LookupThreshold {
			emit(Alert{Rule: "enumeration", Severity: High, Peer: ev.Peer,
				Summary: "peer looked up " + strconv.Itoa(n) + " distinct names in " + w.cfg.Window.String()}, ev.Peer)
		}
	case EvInbound:
		// The newest input names the source; the worst severity in the
		// taint window is kept so a harmless follow-up cannot launder it.
		next := taint{ev.Time, ev.Peer, ev.Channel, ev.Severity}
		if prev, ok := w.tainted(ev.Agent, ev.Time); ok && prev.severity == High {
			next.severity = High
		}
		w.taint[ev.Agent] = next
	case EvSensitive:
		if t, ok := w.taintedAnywhere(ev.Agent, ev.Time); ok {
			sev := Warn
			if t.severity == High || ev.Time.Sub(t.t) < 5*time.Minute {
				sev = High
			}
			emit(Alert{Rule: "tainted-secret-access", Severity: sev, Peer: t.peer, Agent: ev.Agent, Channel: t.channel,
				Summary: "agent accessed " + quote(ev.Target) + " " + ev.Time.Sub(t.t).Round(time.Second).String() + " after untrusted input from " + quote(t.peer)}, ev.Agent)
		}
	case EvOutbound:
		if t, ok := w.taintedAnywhere(ev.Agent, ev.Time); ok && (ev.Peer != t.peer || ev.Detail == "redacted") {
			emit(Alert{Rule: "tainted-relay", Severity: High, Peer: t.peer, Agent: ev.Agent, Channel: t.channel,
				Summary: "agent sent to " + quote(ev.Peer) + " after untrusted input from " + quote(t.peer) + detailSuffix(ev.Detail)}, ev.Agent)
		}
	}
	return out
}

func (w *Watch) tainted(agent string, now time.Time) (taint, bool) {
	t, ok := w.taint[agent]
	if !ok || now.Sub(t.t) > w.cfg.Taint {
		return taint{}, false
	}
	return t, true
}

// taintedAnywhere is tainted plus the cross-process TaintSource. A record
// from the source carries no scan severity; it counts as Warn.
func (w *Watch) taintedAnywhere(agent string, now time.Time) (taint, bool) {
	t, ok := w.tainted(agent, now)
	if w.cfg.TaintSource == nil || agent == "" {
		return t, ok
	}
	if d, found := w.cfg.TaintSource(agent, now.Add(-w.cfg.Taint)); found && !d.At.After(now) && (!ok || d.At.After(t.t)) {
		severity := Warn
		if ok && t.severity == High {
			severity = High // a worse in-process finding is not laundered
		}
		return taint{d.At, d.Alias, d.ID, severity}, true
	}
	return t, ok
}

// MessageLogTaint is the standard Watch.TaintSource: the newest delivered
// outside message to the agent in the message log at logPath. A read error
// counts as no taint.
func MessageLogTaint(logPath string) func(agent string, since time.Time) (Taint, bool) {
	return func(agent string, since time.Time) (Taint, bool) {
		t, found, err := TaintFrom(logPath, agent, since)
		return t, found && err == nil
	}
}

// push appends and drops entries older than the window; the slice is capped
// so a flood cannot grow memory.
func (w *Watch) push(s []stamp, now time.Time, name string) []stamp {
	keep := s[:0]
	for _, x := range s {
		if now.Sub(x.t) <= w.cfg.Window {
			keep = append(keep, x)
		}
	}
	keep = append(keep, stamp{now, name})
	if len(keep) > 256 {
		keep = keep[len(keep)-256:]
	}
	return keep
}

func distinct(s []stamp) int {
	m := map[string]bool{}
	for _, x := range s {
		m[x.name] = true
	}
	return len(m)
}

func detailSuffix(d string) string {
	if d == "" {
		return ""
	}
	return " (" + d + ")"
}

func quote(s string) string { return "\"" + field(s, "") + "\"" }

var sensitivePaths = regexp.MustCompile(`(?i)` +
	`(^|[\s"'=/])(\.ssh|\.gnupg|\.aws|\.kube|\.docker|\.netrc|\.npmrc|\.pypirc|\.git-credentials)(/|\b)` +
	`|\.credentials\.json|\.claude\.json\b|/\.codex/auth\.json|\bauth\.json\b` +
	`|/state/p2p/identity\.key|\bidentity\.key\b|/state/fed/|\bpeers\.json\b` +
	`|(^|[\s"'=/])\.env(\.[a-z]+)?\b|/etc/(shadow|gshadow|sudoers)` +
	`|\bid_(rsa|ed25519|ecdsa|dsa)\b|\.pem\b|\.p12\b|\.pfx\b|\.kdbx\b` +
	`|\b(printenv|env)\s*($|\|)|/proc/[0-9a-z]+/environ` +
	`|\bsecurity\s+find-generic-password|\bsecret-tool\s+lookup`)

// Sensitive reports whether a tool call (name and its raw input, such as a
// file path or shell command) touches a secret. Hooks call it to produce
// EvSensitive events; it is a tripwire, not a sandbox.
func Sensitive(tool, input string) (string, bool) {
	if m := sensitivePaths.FindString(input); m != "" {
		return tool + ": " + trimmed(m), true
	}
	return "", false
}

func trimmed(s string) string {
	b := []rune(s)
	for len(b) > 0 && (b[0] == ' ' || b[0] == '"' || b[0] == '\'' || b[0] == '=' || b[0] == '/') {
		b = b[1:]
	}
	return string(b)
}

// Snapshot is the per-peer view the owner UI shows.
type Snapshot struct {
	Peer    string   `json:"peer"`
	Probes  []string `json:"probes,omitempty"`
	Lookups int      `json:"lookups"`
}

// Snapshot returns current per-peer probe and lookup windows, sorted by peer.
func (w *Watch) Snapshot() []Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	peers := map[string]*Snapshot{}
	get := func(p string) *Snapshot {
		if peers[p] == nil {
			peers[p] = &Snapshot{Peer: p}
		}
		return peers[p]
	}
	for p, s := range w.probes {
		seen := map[string]bool{}
		for _, x := range s {
			if !seen[x.name] {
				seen[x.name] = true
				get(p).Probes = append(get(p).Probes, x.name)
			}
		}
	}
	for p, s := range w.lookups {
		get(p).Lookups = distinct(s)
	}
	var out []Snapshot
	for _, s := range peers {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Peer < out[j].Peer })
	return out
}
