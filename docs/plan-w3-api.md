# W3 plan: HTTP API, MCP, inbox, rooms, board, remote gateway

Owner: bp-api. Branch: feat/api. Base: dev de916f9.

## Shape

One new package, `internal/api`, holds the logic; the CLI and the daemon only
wire it up. Everything reads and writes files under the state dir with the
same uid, so `bp mcp` works without a running daemon.

```
internal/api/
  a2a.go       A2A types (Message, Part, Task, TaskState, AgentCard) + mapping
  core.go      Core: Agents, Send, Inbox, MessageStatus, rooms, board (no transport)
  inbox.go     per-agent inbox store for agents bp has no terminal for
  rooms.go     rooms: members, history, fan-out through Send
  board.go     shared board: keys with author, version and history
  token.go     per-user token file (0600), constant-time check
  http.go      local HTTP server (A2A JSON-RPC + small REST surface)
  mcp.go       MCP server core (tools/list, tools/call), transport-agnostic
  mcpstdio.go  `bp mcp` stdio transport
  gateway.go   remote streamable-HTTP MCP gateway (expose list, bearer/OAuth)
cmd/bp/api.go  `bp serve --api`, `bp mcp`, `bp api token`, config snippets
```

## Decisions

- **Sending uses msgq, not a second delivery path.** A send for a terminal
  agent is `Queue.EnqueueOnceOrigin` keyed by the A2A `messageId`, with an
  `Origin{Transport: "http"|"mcp"}`. The daemon's dispatch pass delivers it
  with the same busy, composer and witness gates as `bp msg`; status comes
  from `Queue.Record` and maps to A2A task states exactly as P2P does
  (accepted → `submitted`/`working`, delivered → `completed`, unverified →
  `unknown`... see a2a.go). Retrying the same messageId is idempotent.
- **Inbox agents.** An agent that bp has no terminal for registers a name
  (`bp_register` / `POST /v1/agents`). Sends to it go to
  `<state>/inbox/<agent>.jsonl` instead of msgq. Reading the inbox (GET or
  MCP) returns unread items and marks them read; the msgq-style record of
  each item moves to `delivered` on read, so status works the same way.
- **Sender identity.** `bp mcp` resolves the caller with
  `identity.Resolve` (pane ancestry) when it runs inside a bp terminal;
  otherwise it uses `--as <name>` / `BP_AGENT`, marked unverified. HTTP
  callers name themselves; the token proves same-user, not which agent.
  Unverified senders get an envelope that says so and never get hierarchy
  authority (no slash commands, no force-busy).
- **Rooms.** `<state>/rooms/<room>/{members.json,history.jsonl}`. A post is
  appended to history, then sent to every member except the author through
  the normal Send (queue or inbox), so busy agents are never interrupted.
  A room post reaches a member as one message with a `[room <name>]`
  envelope. Room history is readable by members.
- **Board.** `<state>/board/<board>.json` plus `history.jsonl`: key → value,
  author, version, updated time. `put` takes an optional expected version
  (compare-and-swap) so two agents cannot overwrite each other silently.
- **Auth.** HTTP requires `Authorization: Bearer <token>` from
  `<state>/api/token` (0600, created on first use, 32 random bytes). The
  alternative is a unix socket `<state>/api/api.sock` (0600 in a 0700 dir)
  that also checks `SO_PEERCRED` uid. Loopback-only bind; Host and Origin
  checks against DNS rebinding.
- **Listening.** Nothing listens by default. `bp serve --api` runs in the
  foreground on the socket and, with `--listen 127.0.0.1:<port>`, on TCP.
  The daemon serves it only when `api.enabled` is set in config. Tests bind
  `127.0.0.1:0`.
- **Remote gateway.** Off unless `api.gateway.enabled`. Streamable HTTP MCP
  on one path; bearer tokens listed in config (OAuth 2.1 metadata stub so
  Claude.ai / ChatGPT connectors can discover it, implemented after bearer
  works). It exposes only `api.gateway.expose` agents, rooms and boards;
  every inbound text goes through the framing function (`guard.Frame` once
  W6 lands; a local interim implementation with the same signature until
  then); every call is audited (`audit.Append` once W1 lands; an interim
  writer with the same event shape until then). Never published on a public
  host here.
- **Audit.** Every accept, reject, send, read, room post, board write and
  auth failure produces an audit event.

## Order of work (each step: tests, commit, bp msg blueprint)

1. A2A types + core Send/Status/Agents over msgq; HTTP server with token and
   socket auth; `bp serve --api`; agent card. Tests: httptest on
   127.0.0.1:0, temp state roots, fake queue target.
2. MCP core + stdio transport (`bp mcp`); tools bp_agents, bp_send,
   bp_inbox, bp_status; config snippets doc for Claude Code, Codex, Gemini
   CLI, Antigravity, Grok CLI, OpenCode, Cursor, Hermes.
3. Inbox agents (register, inbox read marks read, status).
4. Rooms and board (core, HTTP routes, MCP tools).
5. Remote gateway (streamable HTTP, bearer, expose list, framing, audit);
   OAuth discovery stub; local tests only.

## Open points (to blueprint, not blocking)

- `guard.Frame` and `audit.Append` signatures: coded against the contract
  text in workplan-2026-10.md behind small adapter functions, swapped when
  W1/W6 land.
- Delivery to terminal agents stays in msgq's dispatch; when `internal/term`
  lands it changes under msgq, not here. W3 never calls tmux.
- Port and hostname for the gateway: blueprint asks the owner later.
