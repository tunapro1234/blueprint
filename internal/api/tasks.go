package api

// tasks.go serves the fleet console's task surface (docs/fleet-console.md,
// "Tasks — WS2"): list, read, create and patch tasks backed by the durable
// internal/tasks store. It is reads and task edits only; it never delivers a
// message and never types into a pane, so it cannot become a way past the turn.
//
// The handlers are registered through RegisterTasks so the integrator wires them
// onto the /v1 mux without this file touching http.go. Mount them behind the
// same loopback + token auth as the rest of /v1.
//
// derivedStatus is the owner agent's live runtime status, copied verbatim from
// the injected ObserveStatus (the same observation fleet.go uses). The stored
// State is authoritative and is never changed by it: derivedStatus is a display
// hint beside the state, not a replacement for it. When ObserveStatus is nil, or
// a task has no owner, derivedStatus is "" (unknown — honest, never guessed).

import (
	"context"
	"errors"
	"net/http"
	"time"

	"blueprint/internal/identity"
	"blueprint/internal/tasks"
)

// TasksDeps are the dependencies the task endpoints need. The integrator builds
// it (NewTasksDeps for production); tests inject a store over a temp dir and
// stub the clock, id source and observation so no tmux or network is touched.
type TasksDeps struct {
	// Store is the durable task store under <StateDir>/tasks.
	Store *tasks.Store
	// ObserveStatus returns one owner agent's live runtime status for
	// derivedStatus. In production the integrator wires it to the same runtime
	// observation fleet.go uses. Nil means derivedStatus is always "" (unknown).
	ObserveStatus func(ctx context.Context, owner string) string
	// Now is the clock for createdAt/updatedAt; defaults to time.Now.
	Now func() time.Time
	// NewID mints a task id on create; defaults to tasks.NewID.
	NewID func() string
}

// NewTasksDeps wires the production task dependencies over stateDir. The
// integrator sets ObserveStatus afterwards to the runtime observation fleet.go
// uses; left nil, derivedStatus stays empty (honest unknown).
func NewTasksDeps(stateDir string) TasksDeps {
	return TasksDeps{
		Store: tasks.NewStore(stateDir),
		Now:   time.Now,
		NewID: tasks.NewID,
	}
}

func (d TasksDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d TasksDeps) newID() string {
	if d.NewID != nil {
		return d.NewID()
	}
	return tasks.NewID()
}

// RegisterTasks mounts the task endpoints on mux. The caller supplies an
// already-authenticated mux; these handlers add no auth of their own.
func RegisterTasks(mux *http.ServeMux, deps TasksDeps) {
	mux.HandleFunc("GET /v1/tasks", deps.handleList)
	mux.HandleFunc("POST /v1/tasks", deps.handleCreate)
	mux.HandleFunc("GET /v1/tasks/{id}", deps.handleGet)
	mux.HandleFunc("PATCH /v1/tasks/{id}", deps.handleUpdate)
	mux.HandleFunc("DELETE /v1/tasks/{id}", deps.handleDelete)
}

// taskJSON is one task as the API returns it: the stored task plus the derived,
// non-authoritative runtime hint. The embedded tasks.Task flattens its own JSON
// fields (id, title, owner, parent, state, body, acceptance, createdAt,
// updatedAt); DerivedStatus is added beside them.
type taskJSON struct {
	tasks.Task
	DerivedStatus string `json:"derivedStatus"`
}

// taskObserver memoizes ObserveStatus per owner within one request, so listing
// many tasks owned by the same agent observes that agent once.
type taskObserver struct {
	ctx     context.Context
	observe func(context.Context, string) string
	cache   map[string]string
}

func (d TasksDeps) observer(ctx context.Context) *taskObserver {
	return &taskObserver{ctx: ctx, observe: d.ObserveStatus, cache: map[string]string{}}
}

func (o *taskObserver) status(owner string) string {
	if o.observe == nil || owner == "" {
		return ""
	}
	if s, ok := o.cache[owner]; ok {
		return s
	}
	s := o.observe(o.ctx, owner)
	o.cache[owner] = s
	return s
}

func (o *taskObserver) view(task tasks.Task) taskJSON {
	return taskJSON{Task: task, DerivedStatus: o.status(task.Owner)}
}

// --- GET /v1/tasks?owner=&state=&parent= ---

func (d TasksDeps) handleList(w http.ResponseWriter, r *http.Request) {
	if d.Store == nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "task store not configured", "")
		return
	}
	query := r.URL.Query()
	filter := tasks.Filter{Owner: query.Get("owner"), State: query.Get("state"), Parent: query.Get("parent")}
	if filter.Owner != "" && !identity.ValidName(filter.Owner) {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "owner must be a valid agent name", "")
		return
	}
	if filter.Parent != "" && !identity.ValidName(filter.Parent) {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "parent must be a valid task/agent id", "")
		return
	}
	if filter.State != "" && !tasks.ValidState(filter.State) {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "state must be one of todo|in_progress|blocked|done|cancelled", "")
		return
	}
	list, err := d.Store.List(filter)
	if err != nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "list tasks: "+err.Error(), "")
		return
	}
	obs := d.observer(r.Context())
	views := make([]taskJSON, 0, len(list))
	for _, task := range list {
		views = append(views, obs.view(task))
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"tasks": views})
}

// --- GET /v1/tasks/{id} ---

func (d TasksDeps) handleGet(w http.ResponseWriter, r *http.Request) {
	if d.Store == nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "task store not configured", "")
		return
	}
	id := r.PathValue("id")
	task, err := d.Store.Get(id)
	if err != nil {
		taskWriteError(w, err)
		return
	}
	// Child tasks are those that name this task as their parent.
	children, err := d.Store.List(tasks.Filter{Parent: id})
	if err != nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "list child tasks: "+err.Error(), "")
		return
	}
	obs := d.observer(r.Context())
	childViews := make([]taskJSON, 0, len(children))
	for _, child := range children {
		childViews = append(childViews, obs.view(child))
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"task": obs.view(task), "children": childViews})
}

// --- POST /v1/tasks ---

func (d TasksDeps) handleCreate(w http.ResponseWriter, r *http.Request) {
	if d.Store == nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "task store not configured", "")
		return
	}
	var body struct {
		Title      string `json:"title"`
		Owner      string `json:"owner"`
		Parent     string `json:"parent"`
		State      string `json:"state"`
		Priority   string `json:"priority"`
		Due        string `json:"due"`
		Body       string `json:"body"`
		Acceptance string `json:"acceptance"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), "")
		return
	}
	if body.State == "" {
		body.State = tasks.StateTodo
	}
	if msg, ok := taskValidateFields(body.Owner, body.Parent, body.State); !ok {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", msg, "")
		return
	}
	if !tasks.ValidPriority(body.Priority) {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "priority must be empty or one of low|medium|high|urgent", "")
		return
	}
	if !tasks.ValidDue(body.Due) {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "due must be empty or a YYYY-MM-DD date", "")
		return
	}
	now := d.now().UTC()
	task := tasks.Task{
		ID: d.newID(), Title: body.Title, Owner: body.Owner, Parent: body.Parent,
		State: body.State, Priority: body.Priority, Due: body.Due,
		Body: body.Body, Acceptance: body.Acceptance,
		CreatedAt: now, UpdatedAt: now,
	}
	created, err := d.Store.Create(task)
	if err != nil {
		taskWriteError(w, err)
		return
	}
	obs := d.observer(r.Context())
	writeJSONResponse(w, http.StatusOK, map[string]any{"task": obs.view(created)})
}

// --- PATCH /v1/tasks/{id} ---

func (d TasksDeps) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if d.Store == nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "task store not configured", "")
		return
	}
	id := r.PathValue("id")
	// Pointers distinguish an omitted field from one set to its zero value, so a
	// patch never silently clears a field the caller did not mention.
	var body struct {
		Title      *string `json:"title"`
		Owner      *string `json:"owner"`
		Parent     *string `json:"parent"`
		State      *string `json:"state"`
		Priority   *string `json:"priority"`
		Due        *string `json:"due"`
		Body       *string `json:"body"`
		Acceptance *string `json:"acceptance"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), "")
		return
	}
	// Validate every supplied field up front (fail closed) before touching disk.
	if body.State != nil && !tasks.ValidState(*body.State) {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "state must be one of todo|in_progress|blocked|done|cancelled", "")
		return
	}
	if body.Priority != nil && !tasks.ValidPriority(*body.Priority) {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "priority must be empty or one of low|medium|high|urgent", "")
		return
	}
	if body.Due != nil && !tasks.ValidDue(*body.Due) {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "due must be empty or a YYYY-MM-DD date", "")
		return
	}
	if body.Owner != nil && *body.Owner != "" && !identity.ValidName(*body.Owner) {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "owner must be empty or a valid agent name", "")
		return
	}
	if body.Parent != nil && *body.Parent != "" && !identity.ValidName(*body.Parent) {
		writeStatusError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "parent must be empty or a valid task/agent id", "")
		return
	}
	updated, err := d.Store.Update(id, d.now().UTC(), func(task *tasks.Task) error {
		if body.Title != nil {
			task.Title = *body.Title
		}
		if body.Owner != nil {
			task.Owner = *body.Owner
		}
		if body.Parent != nil {
			task.Parent = *body.Parent
		}
		if body.State != nil {
			task.State = *body.State
		}
		if body.Priority != nil {
			task.Priority = *body.Priority
		}
		if body.Due != nil {
			task.Due = *body.Due
		}
		if body.Body != nil {
			task.Body = *body.Body
		}
		if body.Acceptance != nil {
			task.Acceptance = *body.Acceptance
		}
		return nil
	})
	if err != nil {
		taskWriteError(w, err)
		return
	}
	obs := d.observer(r.Context())
	writeJSONResponse(w, http.StatusOK, map[string]any{"task": obs.view(updated)})
}

// --- DELETE /v1/tasks/{id} ---

func (d TasksDeps) handleDelete(w http.ResponseWriter, r *http.Request) {
	if d.Store == nil {
		writeStatusError(w, http.StatusInternalServerError, "INTERNAL", "task store not configured", "")
		return
	}
	id := r.PathValue("id")
	if err := d.Store.Delete(id); err != nil {
		taskWriteError(w, err)
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]any{"deleted": id})
}

// taskValidateFields checks the name-shaped and state fields of a create. It
// returns a message and false on the first problem, mirroring fleet.go's
// name validation.
func taskValidateFields(owner, parent, state string) (string, bool) {
	if owner != "" && !identity.ValidName(owner) {
		return "owner must be empty or a valid agent name", false
	}
	if parent != "" && !identity.ValidName(parent) {
		return "parent must be empty or a valid task/agent id", false
	}
	if !tasks.ValidState(state) {
		return "state must be one of todo|in_progress|blocked|done|cancelled", false
	}
	return "", true
}

// taskWriteError maps a store error to the google.rpc.Status shape the rest of
// the REST surface uses.
func taskWriteError(w http.ResponseWriter, err error) {
	code, status := taskErrorCode(err)
	writeStatusError(w, code, status, err.Error(), "")
}

func taskErrorCode(err error) (int, string) {
	switch {
	case errors.Is(err, tasks.ErrNotFound):
		return http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, tasks.ErrInvalid):
		return http.StatusBadRequest, "INVALID_ARGUMENT"
	case errors.Is(err, tasks.ErrExists):
		return http.StatusConflict, "ABORTED"
	default:
		return http.StatusInternalServerError, "INTERNAL"
	}
}
