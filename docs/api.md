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
  one. That blocks DNS rebinding and cross-site browser requests. The peer
  address must be loopback too, and a request carrying any proxy header
  (`Forwarded`, any `X-Forwarded-*`, `X-Real-IP`, `Via`, `Cf-Connecting-Ip`,
  `True-Client-Ip` and similar) is refused: a reverse
  proxy in front of the local API would turn remote callers into local ones.
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
| GET | `/v1/messages/<id>` | delivery state: accepted, unverified, delivered, failed (a canceled message is failed) |
| DELETE | `/v1/messages/<id>` | cancel your own message while it is still queued |
| GET | `/v1/inbox?agent=&limit=&peek=` | read your inbox; reading marks items read unless `peek=1` |
| GET | `/v1/rooms`, `/v1/rooms/<r>` | list rooms, or one room with its members |
| POST | `/v1/rooms/<r>/join` and `/v1/rooms/<r>/leave` | membership (`{agent}` adds another member) |
| POST | `/v1/rooms/<r>/posts` | post `{text}`; it fans out to the other members through the queue |
| GET | `/v1/rooms/<r>/posts?after=&limit=` | room history (members only); an `after` id no longer in the live history resumes from the oldest kept post |
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
- The full change history is kept. Room and board history files rotate
  at 4 MiB by rename to `<name>.<timestamp>.jsonl.old`; nothing is deleted.

## Safety

- **Framing.** Text is always stored raw and never framed before
  `EnqueueOnceOrigin`; a frame differs on every call and would break the
  queue's replay check for a retried `messageId`.
  - A remote (gateway) caller is external, using the P2P inbound pattern.
    Its sender is `external:<client>@gateway`, and the queue Origin is
    `{Transport: "mcp", PeerAlias: "gateway", PeerID: "gateway:<oauth client id>",
    AgentClaim: <client>, AgentVerified: false}`. A static token's PeerID is
    `gateway:static:<client>`.
  - Local callers get `Transport: "bp-api/http"`, `"bp-api/socket"` or
    `"bp-api/mcp"`, which are `guard.LocalTransports`. msgq frames every
    other origin at delivery (`guard.NeedsFrame`; see
    docs/security/guard-api.md on feat/guard).
  - Inbox items, room posts and board values are read directly, not
    delivered, so they are framed when read: `Framer.Frame(FrameSource,
    text) (string, error)`. `FrameSource` mirrors `guard.Source`, and its
    Channel is the stable record id (the item id, the post id, or
    `board/key@version`).
  - A framing error fails the read, and inbox items stay unread. Raw
    external text is never returned.
  - Until guard's Framer is wired in, the framer is a passthrough (TODO W6),
    which keeps the gateway off.
- **States.** Message states come from `msgq.DeliveryState`, the same
  mapping P2P uses: accepted, unverified, delivered or failed. A canceled
  message is failed with a `canceled …` reason, and A2A shows it as
  CANCELED.
- **Guard.** For remote (gateway) callers, `internal/guard` runs on both
  directions:
  - *Policy and rate.* The client's expose list is a `guard.Policy`
    (`CheckSend`, `CheckLookup`, `CheckRoom`), and sends and room posts
    spend a `guard.Limiter` token (default 120 per hour, burst 20, 16 KiB
    per text). A denial reads as not-found to the client and is audited
    locally as `api.guard.denied` with the decision code.
  - *Scan at intake.* Accepted remote text (messages, room posts, board
    values) is scanned with `guard.Scan`. Flags never block; they become
    `guard.flags` fields on the accepted event, and high flags also write a
    `guard.finding` alert.
  - *Redact outbound.* Local text a remote client reads (its inbox, room
    posts, room topics, board values, agent descriptions) passes
    `guard.Redact` with the client's `redact`
    policy; each redaction is audited as `api.guard.redacted`.
  - *Watch.* Denials, lookups, untrusted input reaching an agent and local
    text leaving to a client feed a `guard.Watch`, which raises
    `guard.reach.*` alerts for probing, enumeration and tainted relays.
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
        ratePerHour: 120     # sends and room posts; 0 = guard default
        burst: 20
        maxBytes: 16384
        redact:              # secrets are always redacted
          emails: true
          patterns: ["ACME-[0-9]{6}"]
```

**Expose lists.** A client sees only what its entry lists. Agents it is not
given are invisible to it, and a denial looks exactly like not-found. Inside
a room it shares, a remote client:
- sees only exposed members, and its posts fan out only to them;
- cannot set the topic, add unexposed agents, or remove anyone but itself;
- reads only posts made after it joined;
- pays a room budget (20 posts per minute per room and per client, 120
  deliveries per minute);
- can see the status only of messages it sent;
- writes board keys only in a plain form (`[A-Za-z0-9][A-Za-z0-9._/-]{0,99}`);
- cannot register agents. The client is registered as an inbox agent of
the same name, so local agents can answer it with `bp msg`/`bp_send`, and it
reads the replies with `bp_inbox`. The gateway creates that inbox itself
and refuses to start if a local inbox agent already has the name.

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

**Pairing codes.** The sign-in page shows the registered client id and its
redirect host, and asks for a one-time code from
`bp api gateway pair <client> <client-id>`. `bp api gateway pending` lists
registered OAuth clients waiting to be paired. The code is bound to that
client id, lasts 10 minutes and works once, so only someone at the computer
can connect an app, and a code made for one app cannot be used by another.
Registered clients that are never paired expire after 1 hour. When the
registry is full (200 clients), a new registration evicts the oldest client
that has no token, code or pairing.

**Refresh reuse.** Presenting a refresh token that was already rotated
revokes the whole token family and logs an `api.gateway.refresh.reused`
alert. Static tokens are never part of a family, and a token from before
families is only alerted, never used to revoke others.

**Static tokens.** For clients that can send a header,
`bp api gateway token <client>` issues a static bearer token.

**Stored secrets.** Only SHA-256 hashes of tokens and codes are kept, in
`<state>/api/gateway/state.json` (mode 0600).

**Other gateway commands:** `bp api gateway clients` and
`bp api gateway revoke <client|--all>`.

**Fails closed.** The gateway will not start, and `/mcp` answers 503, until
a real guard Framer is wired into the Core and msgq frames non-local origins
at delivery (`Core.DeliveryFramed`). The passthrough framer is never enough.

**Audit budget.** Rejections before authentication are logged at most 20
per minute per source; the rest are counted in one `api.audit.suppressed`
event per source, written by the next request from any source.

**Origins.** Requests with no `Origin` are accepted, as are the public
URL's origin, `https://claude.ai` and `https://chatgpt.com`.

**Rate limit.** 120 requests per minute per token, plus the per-client
send and post rate above. Room posts from a client also pay a room budget
(20 per minute) and a fan-out budget (120 deliveries per minute).

**Board writes.** A write to a read-only board is refused with the reason;
a write to a board the client cannot see is not found.
