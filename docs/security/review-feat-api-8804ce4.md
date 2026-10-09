# Review: feat/api 8804ce4 (HTTP API, MCP, gateway, rooms, board)

Reviewer: bp-guard (W6), 2026-10-09. Scope: `internal/api` (http.go, core.go,
safety.go, rooms.go, mcp.go, gateway.go, OAuth), the msgq/p2p DeliveryState
refactor. Read-only review; nothing was run against the live install.

Verdict: the local API is sound to merge behind its default-off switch. The
gateway (remote MCP) must not be enabled until H1-H3 and M1 are fixed and
guard framing is merged and wired.

## High (block gateway enablement)

### H1: nothing frames remote text

`PassthroughFramer` (safety.go:26) is the only Framer and core.go:103 uses it
by default; `guard.NeedsFrame` is not called anywhere. Gateway messages, room
posts and board values from remote clients reach agents as plain text, so an
`[agent] ...` line or a slash command in a body is indistinguishable from a
local message (threat T-E1).

Fix: frame every non-local origin at read time with `guard.Framer` (see the
seam answer below). Until then the gateway must refuse to start, or refuse
remote writes, while the Framer is the passthrough. A nil Framer must fail
closed (error), never pass text through.

### H2: rooms bypass the expose list

- `Post` (rooms.go:235-245) loops over `room.Members` and calls `c.Send` for
  each member with no policy check, so a remote client that joins a room
  reaches every member, including agents never exposed to it.
- `bp_room_leave` (mcp.go:549) calls `Core.Leave(caller, room, agent)` for any
  agent: a remote member can remove any member.
- Room listings (mcp.go:489, filtered by room policy only) return full member
  lists, and `RoomRead` (mcp.go:605) returns history to any member, including
  posts from before the caller joined.

Fix: apply the caller's expose policy (`guard.Policy.CheckSend`) per member at
fan-out time and skip (audit) members outside it; let `Leave` remove only the
caller unless the caller is local; filter member lists by exposure; return
only history after the caller's join time.

### H3: other remote-set text reaches agents unframed

- Room topic: anyone may join an existing room and `topic != ""` overwrites
  `room.Topic` (rooms.go:148).
- Board keys and values.
- The `bp_register` description, shown in `bp_agents` and the A2A card.

Fix: frame these at read time like messages, cap their length, and let only
the room creator (or a local caller) change the topic.

## Medium

- **M1: proxied or tunnelled traffic counts as local.** `serve` (http.go)
  checks only `r.Host` and `Origin`. Behind a reverse proxy or an SSH/Cloudflare
  tunnel on `api.listen`, every request is treated as a local caller. Require
  a loopback `RemoteAddr`; reject requests carrying `Forwarded`,
  `X-Forwarded-*`, `X-Real-IP` or `Via`; document "never proxy api.listen,
  use the gateway".
- **M2: pre-auth audit flood.** Rejections before authentication are audited
  one line each with no budget; a web page in the owner's browser can trigger
  them. Same fix shape as review-cee0889 R1: a bucket per source, then one
  `api.audit.suppressed` line per minute with the count.
- **M3: pairing code not bound to the client.** A code is not tied to a client
  id or redirect host, so an attacker-registered client can be paired with a
  code the owner issued for something else. Bind the code to the client id
  shown to the owner.
- **M4: gateway profile and inbox name collision.** `ensureInbox`
  (gateway.go:439) returns early when any registration with `caller.Name`
  exists, so a gateway profile named like an existing inbox agent shares that
  inbox and reads its unread messages. Namespace gateway inboxes
  (`gateway:<profile>`) or refuse names already registered by another
  transport.
- **M5: unauthenticated client registration exhaustion.** Dynamic registration
  stops at 200 clients and never prunes, so anyone who can reach the gateway
  can lock out new clients. Expire unpaired registrations.
- **M6: write amplification.** `authenticate()` rewrites `state.json` on every
  request; together with unsampled rejection audits this is disk churn on
  demand. Update last-used at most once a minute.
- **M7: loops and amplification.** One room post fans out to up to 63 sends
  against a 120/min limiter; there is no hop marker and no per-room rate. Count
  fan-out against the sender's budget and add a per-room rate.
- **M8: unbounded room and board history.** Cap entries and bytes per room and
  per board.
- **M9: the Framer seam loses provenance and errors.**
  `Frame(source, text string) string` cannot carry the Peer ID or channel, and
  findings cannot reach audit. See the answer below.

## Low

- Revoking a client leaves its codes and pairings.
- Refresh-token reuse does not revoke the token family.
- Per-token limiter buckets never shrink.
- `board_put` reports "refused" without a reason code; `bp_status` shows
  Parent/Role and other senders' ids to remote callers.
- `ContextID` is not validated.
- `Status()` is not scoped to the caller.
- `Inbox` has no upper limit on `limit`.
- The bearer token appears in snippet argv (visible in `ps`); prefer an env
  var or a file.

## Verified good

- API and gateway are off by default and bind loopback.
- OAuth: exact redirect matching, PKCE S256 only, single-use codes, refresh
  rotation.
- Tokens hashed at rest, files 0600 and directories 0700, constant-time
  comparison.
- The unix socket checks `SO_PEERCRED`.
- No slash-command or force-busy path; every message carries a `[from]`
  envelope.
- Labels are validated; MCP server instructions are static.
- The msgq/p2p DeliveryState refactor keeps delivery semantics.

## Answers to bp-api

1. **PeerID for gateway records: yes.** Set `Origin.PeerID` to a stable
   `gateway:<client id>` (the OAuth client id, not the display name). It keys
   the deterministic frame nonce and is the provenance the owner sees.
2. **Widen the seam: yes.** Use
   `Frame(src guard.Source, text string) (guard.Framed, error)`, with `Source`
   built from the same Origin fields (Transport, PeerAlias, PeerID,
   AgentClaim, Room, Channel = stable record id). On error return an error to
   the reader, never the raw body. Write High findings as `guard.finding`
   (`alert`) through `audit.Append`; keep warn-level flags as fields on the
   accepted event. The decision is `guard.NeedsFrame(origin.Transport)`, not
   `PeerAuthenticated`.
