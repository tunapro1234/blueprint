// Package harness is the one place bp keeps what it knows about each agent
// harness: how to recognise it, how its screen says busy or idle, where its
// transcripts live and what compaction looks like in them, how to resume and
// name it, where its hooks and MCP servers are configured, and which delivery
// paths it offers.
//
// It is data, not behavior. The terminal layer, the transcript readers and the
// queue read their literals from here, so a new TUI build that rewords a
// placeholder or changes a spinner is one edit in this package, and the
// conformance tests (internal/harness/conformance) say which behaviors still
// hold. docs/harnesses.json is generated from Catalog; a test keeps them equal.
//
// The package imports nothing from bp, so every layer may depend on it.
package harness

import (
	"encoding/json"
	"sort"
)

// Kind names a harness. The values are the names bp already uses on disk
// (cache.LocalBinding.Harness, cache.State.Runtime, projectschema), so a Kind
// can be compared with a stored string directly.
type Kind string

const (
	Claude   Kind = "claude"
	Codex    Kind = "codex"
	Hermes   Kind = "hermes"
	OpenCode Kind = "opencode"
)

// Path is one way a message can reach an agent (docs/direction.md, "Delivery
// and the terminal layer").
type Path string

const (
	// PathTerminal pastes the message into the agent's terminal at a turn
	// boundary and never into a busy turn.
	PathTerminal Path = "terminal"
	// PathPush hands the message to an API the harness exposes (Codex
	// app-server, an A2A endpoint, an HTTP server, a webhook).
	PathPush Path = "push"
	// PathHooks answers the harness when it asks at a turn boundary.
	PathHooks Path = "hooks"
	// PathPull lets the agent read its inbox through MCP, HTTP or the CLI.
	PathPull Path = "pull"
)

// Behavior names one thing every adapter must do (docs/behaviors.md).
type Behavior string

const (
	BReceiveAtBoundary  Behavior = "B1-receive-at-turn-boundary"
	BNeverInterrupt     Behavior = "B2-never-interrupt-busy-turn"
	BConfirmDelivery    Behavior = "B3-confirm-delivery"
	BSurviveCompaction  Behavior = "B4-survive-compaction"
	BResume             Behavior = "B5-resume-after-restart"
	BReportUsage        Behavior = "B6-report-context-and-usage"
	BRenameTitle        Behavior = "B7-rename-and-title"
	BIdentityAfterReset Behavior = "B8-identity-after-compaction"
)

// Behaviors lists the shared behaviors in their documented order.
var Behaviors = []Behavior{BReceiveAtBoundary, BNeverInterrupt, BConfirmDelivery, BSurviveCompaction, BResume, BReportUsage, BRenameTitle, BIdentityAfterReset}

// Support says how far bp meets a behavior for one harness today.
type Support string

const (
	// Supported: implemented and covered by a conformance test.
	Supported Support = "supported"
	// Partial: implemented with a known gap the Note names.
	Partial Support = "partial"
	// Planned: the harness offers what is needed; bp does not use it yet.
	Planned Support = "planned"
	// Unsupported: the harness offers no way to do it.
	Unsupported Support = "unsupported"
	// Unknown: not verified.
	Unknown Support = "unknown"
)

// Status is one behavior's support level with the reason.
type Status struct {
	Support Support `json:"support"`
	Via     Path    `json:"via,omitempty"`
	Note    string  `json:"note,omitempty"`
}

// Descriptor is everything bp knows about one harness. Fields that hold a
// value bp has not verified say "unknown" rather than guessing; Sources lists
// where each fact came from.
type Descriptor struct {
	Kind    Kind   `json:"kind"`
	Name    string `json:"name"`
	Vendor  string `json:"vendor,omitempty"`
	Class   Class  `json:"class"`
	Checked string `json:"checked"` // version and date the facts were verified against
	License string `json:"license,omitempty"`

	Install  []string `json:"install,omitempty"`
	Login    string   `json:"login,omitempty"`
	Binaries []string `json:"binaries,omitempty"`
	// Wrappers are process names that only NOMINATE this harness (node, bwrap,
	// python): the screen has to confirm it.
	Wrappers []string `json:"wrappers,omitempty"`

	Interactive bool   `json:"interactive"`
	Headless    string `json:"headless,omitempty"`
	Resume      Resume `json:"resume"`
	Naming      string `json:"naming,omitempty"`

	MCP          MCP         `json:"mcp"`
	Hooks        Hooks       `json:"hooks"`
	Push         string      `json:"push,omitempty"`
	Transcripts  Transcripts `json:"transcripts"`
	Compaction   Compaction  `json:"compaction"`
	Screen       ScreenFacts `json:"screen"`
	Usage        string      `json:"usage,omitempty"`
	Instructions string      `json:"instructions,omitempty"`
	Terms        string      `json:"terms,omitempty"`

	// Delivery lists the paths bp can use for this harness, best first.
	Delivery  []Path              `json:"delivery"`
	Behaviors map[Behavior]Status `json:"behaviors"`
	Notes     []string            `json:"notes,omitempty"`
	Sources   []string            `json:"sources,omitempty"`
}

// Class separates harnesses by where they run, which decides the delivery
// paths that can exist at all.
type Class string

const (
	ClassCLI    Class = "cli"    // a terminal agent on this machine
	ClassIDE    Class = "ide"    // an editor extension or app
	ClassHosted Class = "hosted" // a cloud agent reached over its API
	ClassChat   Class = "chat"   // a consumer chat app
	ClassBot    Class = "bot"    // a messaging-platform bot
)

type Resume struct {
	Continue string `json:"continue,omitempty"` // resume the latest session
	ByID     string `json:"by_id,omitempty"`    // resume a named session
	IDFormat string `json:"id_format,omitempty"`
}

type MCP struct {
	Client     bool     `json:"client"`
	Config     []string `json:"config,omitempty"`
	Format     string   `json:"format,omitempty"`
	Transports []string `json:"transports,omitempty"`
	AddCommand string   `json:"add_command,omitempty"`
}

type Hooks struct {
	Config string   `json:"config,omitempty"`
	Events []string `json:"events,omitempty"`
	// Inject says how a hook adds text to the agent's next turn, or "none".
	Inject string `json:"inject,omitempty"`
	// PerLaunch says how bp can add hooks for one session without touching the
	// user's own configuration, or "" when it cannot.
	PerLaunch string `json:"per_launch,omitempty"`
}

type Transcripts struct {
	Root    string   `json:"root,omitempty"`
	Format  string   `json:"format,omitempty"`
	Records []string `json:"records,omitempty"`
}

type Compaction struct {
	Manual  string   `json:"manual,omitempty"`
	Auto    string   `json:"auto,omitempty"`
	Markers []string `json:"markers,omitempty"`
	Event   string   `json:"event,omitempty"`
}

// ScreenFacts is the human-readable summary of a TUI's screen states. The
// matchers bp actually uses are the typed signature sets in screen.go.
type ScreenFacts struct {
	Busy    string `json:"busy,omitempty"`
	Idle    string `json:"idle,omitempty"`
	Paste   string `json:"paste,omitempty"`
	Submit  string `json:"submit,omitempty"`
	Dialogs string `json:"dialogs,omitempty"`
}

var registry = map[Kind]*Descriptor{}

func register(d *Descriptor) *Descriptor {
	if _, dup := registry[d.Kind]; dup {
		panic("harness: duplicate descriptor " + string(d.Kind))
	}
	registry[d.Kind] = d
	return d
}

// Get returns the descriptor for kind.
func Get(kind Kind) (*Descriptor, bool) {
	d, ok := registry[kind]
	return d, ok
}

// Catalog returns every descriptor, sorted by class then kind, for the
// generated matrix.
func Catalog() []*Descriptor {
	out := make([]*Descriptor, 0, len(registry))
	for _, d := range registry {
		out = append(out, d)
	}
	order := map[Class]int{ClassCLI: 0, ClassIDE: 1, ClassHosted: 2, ClassChat: 3, ClassBot: 4}
	sort.Slice(out, func(i, j int) bool {
		if order[out[i].Class] != order[out[j].Class] {
			return order[out[i].Class] < order[out[j].Class]
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// ForCommand returns the harnesses a pane's foreground command could be. A
// binary name is decisive; a wrapper (node, bwrap, python) only nominates, and
// the caller must confirm with the screen.
func ForCommand(command string) (exact *Descriptor, nominated []*Descriptor) {
	for _, d := range Catalog() {
		for _, b := range d.Binaries {
			if b == command {
				return d, nil
			}
		}
	}
	for _, d := range Catalog() {
		for _, w := range d.Wrappers {
			if w == command {
				nominated = append(nominated, d)
			}
		}
	}
	return nil, nominated
}

// Matrix renders the catalog as the machine-readable matrix
// (docs/harnesses.json): one object per harness plus the behavior and path
// vocabularies, so a reader needs nothing else to interpret it.
func Matrix() ([]byte, error) {
	paths := []Path{PathTerminal, PathPush, PathHooks, PathPull}
	data, err := json.MarshalIndent(struct {
		Schema    string        `json:"schema"`
		Behaviors []Behavior    `json:"behaviors"`
		Paths     []Path        `json:"paths"`
		Harnesses []*Descriptor `json:"harnesses"`
	}{"bp-harness-matrix/1", Behaviors, paths, Catalog()}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
