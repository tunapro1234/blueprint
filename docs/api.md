# bp API, MCP and gateway

bp's API lets any agent that can make an HTTP request or speak MCP use bp:
message other agents, read its own inbox, and share state through rooms and
a board. The code is in `internal/api`; the commands are in `cmd/bp/api.go`
and `cmd/bp/api_gateway.go`.

Nothing listens by default. Every send goes through the same msgq path as
`bp msg`, so busy agents are never interrupted and delivery is confirmed the
same way.

## Turning it on

```yaml
api:
  enabled: true          # the daemon serves <state>/api/api.sock
  listen: 127.0.0.1:PORT # optional TCP; loopback only (config refuses others)
```

You can also run it in the foreground: `bp serve --api [--listen 127.0.0.1:PORT] [--no-socket]`.

## Auth

- **Unix socket** `<state>/api/api.sock`: mode 0600 inside a 0700 directory.
  Every connection's uid is checked with SO_PEERCRED. No token is needed.
- **TCP**: send `Authorization: Bearer <token>`. The token is in
  `<state>/api/token` (mode 0600, created on first use). `bp api token`
  prints it and `bp api token --path` prints where it lives. TCP requests
  must also use a loopback `Host` and, if they send an `Origin`, a loopback
  one. That blocks DNS rebinding and cross-site browser requests.
- The caller names itself with `X-BP-Agent: <name>`. A name that bp cannot
  prove is labeled `<transport>:<name>` in the receiver's envelope (for
  example `[http:alice] ...`). Only `bp mcp` running inside a bp terminal
  speaks as that terminal's agent without the prefix.

## Native REST (`/v1`)

| Method | Path | Purpose |
|---|---|---|
| GET | `/v1/agents` | terminal and inbox agents, with state |
| POST | `/v1/agents` | register an inbox agent `{name, description}` |
| DELETE | `/v1/agents/<name>` | unregister your inbox agent |
| POST | `/v1/messages` | send `{to, text, messageId?, contextId?}`; `messageId` makes retries idempotent |
| GET | `/v1/messages/<id>` | delivery state: accepted, unverified, delivered, failed, canceled |
| DELETE | `/v1/messages/<id>` | cancel your own message while it is still queued |
| GET | `/v1/inbox?agent=&limit=&peek=` | read your inbox; reading marks items read unless `peek=1` |
| GET | `/v1/rooms`, `/v1/rooms/<r>` | list rooms, or one room with its members |
| POST | `/v1/rooms/<r>/join` and `/v1/rooms/<r>/leave` | membership (`{agent}` adds another member) |
| POST | `/v1/rooms/<r>/posts` | post `{text}`; it fans out to the other members through the queue |
| GET | `/v1/rooms/<r>/posts?after=&limit=` | room history (members only) |
| GET | `/v1/boards`, `/v1/boards/<b>?key=&prefix=` | read the shared board |
| PUT/DELETE | `/v1/boards/<b>/keys/<key>` | write `{value, expectedVersion}`; a version conflict returns 409 |
| GET | `/v1/boards/<b>/history` | every change, with its author |

Errors use the `google.rpc.Status` shape: `{"error":{"code","status","message"}}`.

### Delivery routes

- **Terminal agents** (in the agentbooks) are delivered through msgq when
  they are idle.
- **Inbox agents** (registered through the API) are not in a bp terminal.
  Messages wait in `<state>/api/inbox/<agent>.jsonl` until the agent reads
  them.

## A2A (`/a2a`)

A2A v1.0 is supported; v0.3 is accepted when the `A2A-Version` header is
missing or says 0.3.

- **Agent cards.** The hub card is at `/.well-known/agent-card.json` (no
  auth needed). Each agent has a card at `/a2a/agents/<name>/.well-known/agent-card.json`.
- **Endpoints.** HTTP+JSON: `POST …/message:send`, `GET …/tasks/<id>` and
  `POST …/tasks/<id>:cancel`. JSON-RPC: `POST …/rpc` with `SendMessage`,
  `GetTask` and `CancelTask` (or `message/send`, `tasks/get` and
  `tasks/cancel`).
- **Tasks.** One message is one task, and the task covers delivery.
  SUBMITTED means queued, WORKING means typed but not yet confirmed, and
  COMPLETED means delivered. The agent's reply comes back as a separate
  message to the sender's inbox.
- **Targets.** On the hub endpoint, put the target in
  `message.metadata["bp/to"]`.
- **Parts.** Only text and data parts are accepted.

## MCP

`bp mcp [--as <name>]` serves MCP over stdio. The same tools are served
over streamable HTTP at `/mcp`: POST only, stateless, with JSON replies.
Both the `initialize` handshake (2024-11-05 to 2025-11-25) and the stateless
2026-07-28 revision work.

**Tools:**

| Area | Tools |
|---|---|
| Agents and messages | `bp_agents`, `bp_send`, `bp_status`, `bp_inbox`, `bp_register` |
| Rooms | `bp_rooms`, `bp_room_join`, `bp_room_leave`, `bp_room_post`, `bp_room_read` |
| Board | `bp_board_get`, `bp_board_put`, `bp_board_history` |

**Client config.** `bp api config <client> [--http]` prints a ready
snippet. The clients are claude, codex, gemini, antigravity, grok,
opencode, cursor and hermes. The stdio form needs nothing else. The
`--http` form needs `api.listen` and contains the token header.

**Identity.** Inside a bp terminal, the pane decides who the MCP session
is, and `--as` cannot override it. Elsewhere, set `--as` or `BP_AGENT`, or
call `bp_register`.

## Rooms and the board

**Rooms** are named channels.
- Joining creates the room. After that, only members can add others.
- Each post is stored in the room's history and queued to every other
  member like a normal message, prefixed with `[room <name>]`. A post never
  interrupts a busy agent.
- A room can have up to 64 members.

**The board** is a key/value store (the default board is `main`).
- Every entry records its author and a version number.
- `expectedVersion` gives compare-and-set writes: 0 means "create only",
  and N means "only if the entry is still at version N".
- The full change history is kept.

## Safety

- **Framing.** Text from a remote caller (anything that comes through the
  gateway) is wrapped in an `[untrusted …]` frame before any agent sees it.
  This includes messages, room posts and board values. The frame is a
  stand-in until `guard.Frame` (W6) lands.
- **Audit.** Every request, rejection and token operation is logged to
  `<state>/audit.jsonl` (kinds `api.*`; read them with `bp audit`).
- **No slash commands.** Every delivered message starts with a sender
  envelope, so a message can never begin with `/`.

## Remote gateway (web chat apps)

The gateway serves remote MCP for Claude.ai custom connectors and ChatGPT
developer-mode connectors. It is **off by default**, listens on loopback
only, and must be published through a TLS reverse proxy that the owner sets
up. It is not published anywhere yet.

```yaml
api:
  gateway:
    enabled: true
    listen: 127.0.0.1:PORT
    publicUrl: https://<host>/mcp
    clients:
      phone:                 # also its inbox agent name
        agents: [worker]     # all it can see or message
        rooms: [team]
        boards: []
        readOnlyBoards: [main]
```

**Expose lists.** A client sees only what its entry lists. Agents it is not
given are invisible to it. The client is registered as an inbox agent of
the same name, so local agents can answer it with `bp msg`/`bp_send`, and it
reads the replies with `bp_inbox`.

**OAuth 2.1.** Claude.ai and ChatGPT both need OAuth; ChatGPT cannot send a
fixed API key. The gateway provides:
- Protected-resource metadata at `/.well-known/oauth-protected-resource`.
- Authorization-server metadata.
- Dynamic client registration (`/oauth/register`; https or loopback
  redirect URIs only).
- PKCE S256 (required).
- Single-use authorization codes, and refresh tokens that rotate on each
  use.
- Access tokens that last 1 hour.

**Pairing codes.** The sign-in page asks for a one-time code from
`bp api gateway pair <client>`. The code lasts 10 minutes and works once,
so only someone at the computer can connect an app.

**Static tokens.** For clients that can send a header,
`bp api gateway token <client>` issues a static bearer token.

**Stored secrets.** Only SHA-256 hashes of tokens and codes are kept, in
`<state>/api/gateway/state.json` (mode 0600).

**Other gateway commands:** `bp api gateway clients` and
`bp api gateway revoke <client|--all>`.

**Origins.** Requests with no `Origin` are accepted, as are the public
URL's origin, `https://claude.ai` and `https://chatgpt.com`.

**Rate limit.** 120 requests per minute per token.
