package api

// fleet.go serves the fleet console's hierarchy surface (docs/fleet-console.md,
// "Fleet / hierarchy — WS1"): the live org chart, an audited reparent, and a
// UI-owned canvas layout. It is reads, a hierarchy edit and a small key/value
// store only. It never delivers a message and never types into a pane, so it
// cannot become a way past the turn.
//
// The handlers are registered through RegisterFleet so the integrator wires them
// onto the /v1 mux without this file touching http.go. Mount them behind the
// same loopback + token auth as the rest of /v1.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"blueprint/internal/audit"
	"blueprint/internal/book"
	"blueprint/internal/cache"
	"blueprint/internal/identity"
	bptmux "blueprint/internal/tmux"
)

// FleetDeps are the dependencies the fleet endpoints need. The integrator builds
// it (NewFleetDeps for production); tests inject stubs so no tmux server or real
// BP_HOME is touched.
type FleetDeps struct {
	// StateDir is bp's state directory. The canvas layout lives under
	// <StateDir>/console and reparent decisions append to the audit log there.
	StateDir string
	// LoadFleet returns the current hierarchy.
	LoadFleet func() (book.Fleet, error)
	// Observe returns one agent's runtime state. In production it is
	// book.RuntimeForWithSessions over a tmux client; the handler copies its
	// status verbatim and never recomputes it.
	Observe func(ctx context.Context, fleet book.Fleet, name string, sessions []string) cache.State
	// Sessions lists the live agent sessions; used only to decide `open` and to
	// surface live-but-unregistered agents.
	Sessions func(ctx context.Context) ([]string, error)
	// Reparent moves name under parent ("" detaches); it validates existence and
	// refuses cycles.
	Reparent func(name, parent string) (previous string, err error)
	// Host is this machine's name, reported for agents that record no remote.
	Host string
	// Audit records the reparent decision; defaults to the audit log in StateDir.
	Audit func(audit.Event)
	// Now is the clock; defaults to time.Now.
	Now func() time.Time
}

// NewFleetDeps wires the production fleet dependencies: the hierarchy from the
// configured agentbooks, status strictly from book.RuntimeFor over client, and
// the host from this machine's name.
func NewFleetDeps(stateDir string, agentbooks []string, client *bptmux.Client) FleetDeps {
	host, _ := os.Hostname()
	return FleetDeps{
		StateDir: stateDir,
		Host:     host,
		LoadFleet: func() (book.Fleet, error) {
			return book.LoadFleet(book.Paths(agentbooks))
		},
		Observe: func(ctx context.Context, fleet book.Fleet, name string, sessions []string) cache.State {
			return book.RuntimeForWithSessions(ctx, client, fleet, name, sessions)
		},
		Sessions: func(ctx context.Context) ([]string, error) {
			return client.Sessions(ctx)
		},
		Reparent: func(name, parent string) (string, error) {
			return book.Reparent(agentbooks, name, parent)
		},
		Audit: func(ev audit.Event) { _ = audit.Append(stateDir, ev) },
		Now:   time.Now,
	}
}

func (d FleetDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d FleetDeps) audit(ev audit.Event) {
	if d.Audit == nil {
		return
	}
	if ev.Time.IsZero() {
		ev.Time = d.now().UTC()
	}
	if ev.Severity == "" {
		ev.Severity = audit.Info
	}
	d.Audit(ev)
}

// RegisterFleet mounts the fleet endpoints on mux. The caller supplies an
// already-authenticated mux; these handlers add no auth of their own.
func RegisterFleet(mux *http.ServeMux, deps FleetDeps) {
	mux.HandleFunc("GET /v1/fleet/tree", deps.handleTree)
	mux.HandleFunc("POST /v1/fleet/reparent", deps.handleReparent)
	mux.HandleFunc("GET /v1/fleet/layout", deps.handleLayoutGet)
	mux.HandleFunc("POST /v1/fleet/layout", deps.handleLayoutPost)
}

// --- GET /v1/fleet/tree ---

type fleetAgentJSON struct {
	Name         string `json:"name"`
	Parent       string `json:"parent"`
	Role         string `json:"role,omitempty"`
	Host         string `json:"host,omitempty"`
	Runtime      string `json:"runtime,omitempty"`
	Status       string `json:"status"`
	StatusReason string `json:"statusReason,omitempty"`
	LastTurnAt   string `json:"lastTurnAt,omitempty"`
	Folder       string `json:"folder,omitempty"`
	Open         bool   `json:"open"`
}

func (d FleetDeps) handleTree(w http.ResponseWriter, r *http.Request) {
	if d.LoadFleet == nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "fleet loader not configured", "")
		return
	}
	fleet, err := d.LoadFleet()
	if err != nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "load fleet: "+err.Error(), "")
		return
	}
	ctx := r.Context()
	// A failure to list sessions means tmux is unreachable; the same tmux server
	// backs the per-agent observation, so every agent then reads as unknown and
	// not open. That is honest, so a listing error is treated as no live
	// sessions rather than failing the whole view.
	var sessions []string
	if d.Sessions != nil {
		if live, serr := d.Sessions(ctx); serr == nil {
			sessions = live
		}
	}
	// Surface live-but-unregistered agents too, as the status views do.
	fleet.AddLive(sessions)
	open := make(map[string]bool, len(sessions))
	for _, name := range sessions {
		open[name] = true
	}
	observe := func(name string) cache.State {
		if d.Observe == nil {
			return cache.State{}
		}
		return d.Observe(ctx, fleet, name, sessions)
	}
	tree := book.ConsoleTree(fleet, open, d.Host, observe)
	agents := make([]fleetAgentJSON, 0, len(tree))
	for _, a := range tree {
		entry := fleetAgentJSON{
			Name: a.Name, Parent: a.Parent, Role: a.Role, Host: a.Host,
			Runtime: a.Runtime, Status: a.Status, StatusReason: a.StatusReason,
			Folder: a.Folder, Open: a.Open,
		}
		if a.HasTurn {
			entry.LastTurnAt = a.LastTurnAt.UTC().Format(time.RFC3339)
		}
		agents = append(agents, entry)
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"agents": agents})
}

// --- POST /v1/fleet/reparent ---

func (d FleetDeps) handleReparent(w http.ResponseWriter, r *http.Request) {
	if d.Reparent == nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "reparent not configured", "")
		return
	}
	var body struct {
		Agent  string `json:"agent"`
		Parent string `json:"parent"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), "")
		return
	}
	actor := r.Header.Get("X-BP-Agent")
	if actor == "" {
		actor = "console"
	}
	if !identity.ValidName(body.Agent) {
		d.auditReparent(actor, body.Agent, body.Parent, "", errors.New("invalid agent name"))
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "agent must be a valid agent name", "")
		return
	}
	if body.Parent != "" && !identity.ValidName(body.Parent) {
		d.auditReparent(actor, body.Agent, body.Parent, "", errors.New("invalid parent name"))
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "parent must be empty (detach) or a valid agent name", "")
		return
	}
	previous, err := d.Reparent(body.Agent, body.Parent)
	d.auditReparent(actor, body.Agent, body.Parent, previous, err)
	if err != nil {
		code, status := reparentErrorCode(err)
		writeStatusError(w, code, status, err.Error(), "")
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"agent": body.Agent, "parent": body.Parent, "previous": previous})
}

func reparentErrorCode(err error) (int, string) {
	switch {
	case errors.Is(err, book.ErrUnknownAgent), errors.Is(err, book.ErrUnknownParent):
		return http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, book.ErrReparentRoot):
		return http.StatusBadRequest, "INVALID_ARGUMENT"
	case errors.Is(err, book.ErrReparentCycle):
		return http.StatusConflict, "ABORTED"
	default:
		return http.StatusInternalServerError, "INTERNAL"
	}
}

func (d FleetDeps) auditReparent(actor, agent, parent, previous string, err error) {
	ev := audit.Event{Kind: "api.fleet.reparent", Actor: actor, Target: agent,
		Fields: map[string]string{"parent": parent, "previous": previous}}
	if parent == "" {
		ev.Fields["parent"] = "(detach)"
	}
	if err != nil {
		ev.Kind, ev.Severity, ev.Reason = "api.fleet.reparent.rejected", audit.Warn, err.Error()
	}
	d.audit(ev)
}

// --- GET/POST /v1/fleet/layout ---

// Layout positions are a UI concern, persisted separately from the agentbook so
// agents stay movable on the canvas without a hierarchy edit.

type layoutPos struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type fleetLayout struct {
	Positions map[string]layoutPos `json:"positions"`
}

const (
	maxLayoutAgents = 4000
	maxLayoutCoord  = 1e7
)

func (d FleetDeps) layoutPath() string {
	return filepath.Join(d.StateDir, "console", "fleet-layout.json")
}

func (d FleetDeps) handleLayoutGet(w http.ResponseWriter, r *http.Request) {
	layout := fleetLayout{Positions: map[string]layoutPos{}}
	if err := readJSON(d.layoutPath(), &layout); err != nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "read layout: "+err.Error(), "")
		return
	}
	if layout.Positions == nil {
		layout.Positions = map[string]layoutPos{}
	}
	writeJSONResponse(w, http.StatusOK, layout)
}

func (d FleetDeps) handleLayoutPost(w http.ResponseWriter, r *http.Request) {
	if d.StateDir == "" {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "state directory not configured", "")
		return
	}
	var layout fleetLayout
	if err := decodeJSONBody(r, &layout); err != nil {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), "")
		return
	}
	if layout.Positions == nil {
		layout.Positions = map[string]layoutPos{}
	}
	if err := validateLayout(layout); err != nil {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), "")
		return
	}
	path := d.layoutPath()
	err := withLock(path, func() error { return writeJSON(path, layout) })
	if err != nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "write layout: "+err.Error(), "")
		return
	}
	writeJSONResponse(w, http.StatusOK, layout)
}

func validateLayout(layout fleetLayout) error {
	if len(layout.Positions) > maxLayoutAgents {
		return invalid("layout has %d agents, over the %d limit", len(layout.Positions), maxLayoutAgents)
	}
	for name, pos := range layout.Positions {
		if !identity.ValidName(name) || len(name) > 64 {
			return invalid("%q is not a valid agent name", name)
		}
		if !finiteCoord(pos.X) || !finiteCoord(pos.Y) {
			return invalid("position of %q must be finite coordinates within +/-%g", name, maxLayoutCoord)
		}
	}
	return nil
}

func finiteCoord(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= -maxLayoutCoord && v <= maxLayoutCoord
}

// decodeJSONBody reads one JSON object from r. An empty body is accepted as the
// zero value, matching the rest of the REST surface.
func decodeJSONBody(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON body: " + err.Error())
	}
	return nil
}
