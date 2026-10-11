# Fleet Console — a Paperclip-style web UI for bp

Status: design / in progress (branch `feat/fleet-console`). This document is the
coordination contract for the workstreams below. It is generic and public; no
host paths or agent names belong here.

## Goal

A single local web console to run a fleet of bp agents, modeled on
[Paperclip AI](https://github.com/agencyenterprise/paperclip-ai) (Node + React
org-chart/task/cost console). bp already owns the hard parts — liveness, the
turn-safe delivery seam, identity, hierarchy, workflows, usage — so the console
is a thin presentation + a few new data endpoints on top of bp's existing API,
**not** a second control plane.

The owner's explicit asks:

1. **Hierarchy**: view it, edit it (reparent), and arrange agents by their
   physical location (host/machine).
2. **Tasks**: see tasks and their status.
3. **Accounts / usage**: recent-days utilization per account — percent of limit
   and the token count that percentage corresponds to.
4. **Todo** tool (a lightweight personal task list).
5. **Workflow** visibility: see a workflow's shape; workflows must be savable
   (already in bp) and duplicable/reusable.
6. "Drop into a normal chat" with any agent stays first-class (the console links
   out to the agent's pane; direct `rush` attach already works).

Hierarchy *reorganization* happens later, after these features land.

## Principles (from CLAUDE.md)

- The console is part of the **`ui` module** (opt-in, owner-gated). Installing bp
  still changes nothing; the console only runs when `ui` is enabled and the
  daemon serves it on loopback.
- **One source of truth.** The console reads bp's live state (agentbook,
  runtime, usage, workflow store) through the API. It never keeps its own copy of
  fleet state.
- **Status is derived, not babysat.** Task/agent status comes from bp's existing
  runtime + transcript signals wherever possible, so the board is
  exception-based (look when something is blocked), not a second inbox.
- **Honest signals.** Unknown stays unknown (never render a guessed "idle").
  Respect the turn: the console never becomes a way to type into a busy pane.
- English only in code, identifiers, API, docs.

## Architecture

```
browser (127.0.0.1)
   │
   ▼
bp daemon ──► internal/dashboard  (serves the SPA static bundle + proxies /v1)
   │
   ▼
internal/api (bp serve --api)  ──►  /v1/* JSON endpoints
   │
   ├─ internal/book      (hierarchy, runtime status, host)
   ├─ internal/tasks     (NEW: durable task store)
   ├─ internal/usagecli  + internal/claudeacct (account utilization)
   └─ internal/workflow  (definitions, runs, clone)
```

- **Frontend**: a self-contained SPA that builds to static assets served by the
  daemon (no runtime Node). Final stack chosen from the Paperclip UX study
  (`scratchpad/paperclip-ui-spec.md`); default assumption = React + Vite +
  Tailwind, org chart via React Flow (dagre layout). Lives in `ui/console/`
  (source) and builds into `site/console/` (committed static bundle) OR is
  embedded via `go:embed` — decided in WS5 (prefer `go:embed` so the daemon
  needs no external files).
- **Backend**: new handlers in `internal/api/*.go`, registered on the existing
  `/v1` mux in `http.go`. All new read endpoints are GET; mutations (reparent,
  task edits) are POST and audited.

## API contract (v1)

JSON, loopback-auth as the rest of `/v1`. Shapes are the minimum each screen
needs; extend additively.

### Fleet / hierarchy  — WS1
- `GET /v1/fleet/tree` →
  ```json
  { "agents": [
    { "name":"server-main", "parent":"", "role":"coordinator",
      "host":"<machine>", "runtime":"claude", "status":"idle|working|unknown|blocked|dead",
      "statusReason":"", "lastTurnAt":"RFC3339", "folder":"/path", "open":true }
  ] }
  ```
  `host` = the physical machine (local host name, or the P2P/fed peer the agent
  lives on). `status` comes straight from `book.RuntimeFor` — never recomputed.
- `POST /v1/fleet/reparent` `{ "agent":"x", "parent":"y" }` → updates the
  agentbook hierarchy; validates no cycles; audited. (Physical layout / canvas
  positions are a UI concern — persist them in a small `GET/POST
  /v1/fleet/layout` key-value, UI-owned, so agents stay movable on the canvas
  without touching the agentbook.)

### Tasks  — WS2
- `Task` = `{ id, title, owner (agent), parent (task/goal id or ""), state:
  "todo|in_progress|blocked|done|cancelled", body, acceptance, createdAt,
  updatedAt, derivedStatus }`.
- `GET /v1/tasks?owner=&state=&parent=` → list.
- `GET /v1/tasks/{id}` → one task + child tasks.
- `POST /v1/tasks` → create. `PATCH /v1/tasks/{id}` → update state/fields.
- Storage: new `internal/tasks` package, durable under `state/tasks/` (atomic
  write/rename, readable by the previous release — principle 12). `derivedStatus`
  may be filled from the owner agent's runtime (e.g. owner working ⇒ task likely
  in_progress) but the stored `state` is authoritative; never overwrite a
  human-set state silently.
- New CLI `bp task` (list/add/done/assign) so the model is usable without the UI.
- "Todo" = the tasks owned by the current identity, a filtered view — no separate
  store.

### Accounts / usage  — WS3
- `GET /v1/usage/accounts` →
  ```json
  { "asOf":"RFC3339", "accounts":[
    { "name":"azra", "slot":2, "percentOfLimit":0.37, "limit":0.80,
      "estTokens":1234567, "fiveHourResetAt":"RFC3339", "window":"5h",
      "active":true } ],
    "recentDays":[ { "date":"2026-10-10", "percent":0.42, "estTokens":... } ] }
  ```
- Reuses `internal/usagecli` / `internal/claudeacct` (the usage endpoints and the
  accounts module). `estTokens` = percent × the window's token budget; document
  the budget source and mark it an estimate (honest labeling). Read-only; no
  account switching from the console in v1 (accounts module stays owner-gated).

### Workflows  — WS4
- `GET /v1/workflows` → saved definitions (from `workflow.Store.List`).
- `GET /v1/workflows/{name}` → definition + its units (the multi-agent graph) +
  recent runs (`Store` runs/events) for the visualization.
- `POST /v1/workflows/{name}/clone` `{ "to":"newname" }` → duplicate a saved
  definition (via `Store.Add` of the copied source). Enables "reusable".
- Run control (start/stop) is out of scope for v1 view-only; link to existing
  `bp workflow` CLI.

## Workstreams & file ownership (avoid collisions)

| WS | Scope | Owns (writes) | Shares |
|----|-------|---------------|--------|
| WS1 | Fleet/hierarchy API | `internal/api/fleet.go`, `internal/book/console.go` | `internal/api/http.go` (route reg) |
| WS2 | Tasks store+API+CLI | `internal/tasks/**`, `internal/api/tasks.go`, `cmd/bp/task.go` | `http.go`, `cmd/bp/main.go` (dispatch) |
| WS3 | Usage API | `internal/api/usage.go` | `http.go` |
| WS4 | Workflow API | `internal/api/workflow_console.go` | `http.go` |
| WS5 | Frontend SPA | `ui/console/**`, `internal/dashboard/console.go` | `internal/dashboard/dashboard.go` |
| WS6 | Paperclip UX study | `scratchpad/paperclip-ui-spec.md` (research only) | — |

**Shared-file rule:** `http.go` route registration is the one hot spot. Each WS
adds its handlers in its own file and exposes a single `registerXxx(mux)` func;
the integrator (me) wires them in `http.go` to keep merges clean. Do NOT each
edit the same block of `http.go` in parallel.

## Build discipline

- Branch per WS off `feat/fleet-console`; PR back into it; I integrate and we
  merge the umbrella to `dev` when a coherent slice works.
- Every Go build/test under the shared lock, heat-gated:
  `env -u TMUX -u TMUX_PANE GOFLAGS=-buildvcs=false flock /run/lock/bp-gobuild.lock nice -n 10 env GOMAXPROCS=2 go test -p 2 <pkgs>` ; wait for `sensors` Package id 0 < 76°C. Max 3 concurrent Go builders.
- Frontend build (npm/vite) does not take the Go lock but is disk-heavy
  (node_modules) — build in one place, commit only the static bundle, keep
  `node_modules` out of git.
- Every behavior change ships a test. New endpoints get `internal/api` handler
  tests; delivery/turn safety is untouched by this work.

## Phasing

1. **Skeleton** (proves the direction): WS1 fleet tree + WS5 shell rendering the
   live org chart with real status. This is the first thing to show the owner.
2. Tasks (WS2) + the task board; todo view.
3. Usage (WS3) panel; workflow (WS4) viewer.
4. Editable hierarchy (reparent + host layout) and "features to steal" from the
   Paperclip study.
5. Hierarchy reorg (owner-led), after the above.
