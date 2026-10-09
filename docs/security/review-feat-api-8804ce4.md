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

## Follow-up: fcac8ef and 32effcb

Resolved:

- M9 and both answers: the seam is now
  `Frame(src FrameSource, text string) (string, error)`; `FrameSource` has
  guard.Source's fields in the same order, so the adapter is a plain
  conversion (`guard.Source(src)`). Gateway origins carry
  `PeerID: gateway/<profile>`; the profile name is set by the owner at
  pairing, so it is stable.
- Fail closed: a nil Framer is an error, an inbox render error fails the
  whole read before anything is marked read, and room and board reads return
  the error instead of raw text. Channels are stable (item id, post id,
  `board/key@version`), so frames are deterministic.
- Gateway origin transport is `mcp`, which `guard.NeedsFrame` frames.
- M1 half: TCP callers whose `RemoteAddr` is not loopback are refused.

Still open:

- **H1:** `NewCore` still defaults to `PassthroughFramer` (core.go:103), so
  until blueprint wires `guard.LoadFramer` the stores return raw remote text.
  Make the gateway refuse to start while `Core.Frame` is a
  `PassthroughFramer`, so enabling it before the wiring is impossible rather
  than a convention.
- **M1 other half:** a reverse proxy or tunnel on the same host connects from
  127.0.0.1 and passes the new check. The local listener should still refuse
  requests that carry `Forwarded`, `X-Forwarded-*`, `X-Real-IP` or `Via`.
- **H2, H3, M2-M8:** unchanged. Room topic and the `bp_register`
  description are still unframed.
- Read-time framing drops `Framed.Findings`; this is fine as long as
  `guard.Scan` runs at intake as planned, once guard is on dev.

Adapter for blueprint (cmd/bp):

```go
type guardFramer struct{ f *guard.Framer }

func (g guardFramer) Frame(src api.FrameSource, text string) (string, error) {
    framed, err := g.f.Frame(guard.Source(src), text)
    return framed.Text, err
}
```

## Follow-up: 3d37c99

All High items and M1, M3-M6 are fixed; nothing below blocks merging.
The gateway stays off by construction until both framing paths are wired.

Verified:

- H1: `Gateway.Ready()` needs a real Framer and `Core.DeliveryFramed`;
  `startGateway` refuses and `/mcp` answers 503 until then. Nothing in
  cmd/bp sets `DeliveryFramed` yet, so it cannot be enabled early.
- H2: room fan-out skips members outside the caller's expose list; member
  lists are filtered; joining unexposed agents reads as not-found; remote
  callers may only remove themselves; remote readers see only posts from
  after their own join (rooms without `Joined` read as empty, fail closed).
- H3: remote callers cannot set a topic or register agents; remote board
  keys are plain identifiers; posts and values are framed at read time.
- M1: non-loopback `RemoteAddr` and proxy headers are refused.
- M3: pairing codes are bound to a client id, and the page shows the id and
  redirect host; a mismatched code is spent.
- M4: gateway inboxes are owned by `bp-gateway`; a collision is refused.
- M6: `authenticate` is read-only.
- Lows: revoke clears codes and pairings, refresh reuse revokes the family
  with an alert, `bp_status` is scoped to the sender, `contextId` is
  validated, inbox reads are capped at 200, idle limiter buckets are swept.

New findings:

- **F1 (medium): skipped-delivery audit lines bypass the room rate limit.**
  `Post` writes one `api.room.deliver.skipped` line per unexposed member
  *before* the rate check, so a rate-limited remote post into a 63-member
  room still writes 63 lines, at the token's 120 requests/min. Check the
  rate first, and write one event per post with the skipped count and names.
- **F2 (medium): legacy refresh reuse revokes every static token.** Static
  tokens and refresh tokens issued before 3d37c99 have `Family: ""`. A
  rotated legacy refresh token is recorded in `Spent` with family `""`;
  presenting it again (a client retry is enough) deletes every token with
  `Family == ""`, including all profiles' static tokens. Skip family
  revocation when the family is empty (revoke by client id instead), and
  never touch `Kind == "static"`.
- **F3 (low): `DeliveryFramed` is a free bool.** Derive it from the queue
  (`c.Queue != nil && c.Queue.Render != nil`) so it cannot be set while
  Render is not.
- **F4 (low): `after` across a history rotation.** When the `after` post id
  is in a rotated file, `RoomRead` returns nothing forever. Fall back to the
  join time (or the file start) when `after` is not found.
- **F5 (low): registration lockout is shorter, not gone.** 200 unpaired
  clients per hour still lock out new clients. Evict the oldest unpaired
  client instead of refusing.
- **F6 (low): a suppressed count is reported only when the same source
  sends again after the window.** A flood that stops leaves no count. Flush
  pending counts on the next event from any source, or on a timer.
- **F7 (low): proxy header list.** Also refuse any `X-Forwarded-*`, and
  `Cf-Connecting-Ip` and `True-Client-Ip` (cloudflared and CDNs).
- History is rotated, never deleted, so the disk bound comes from the post
  rates only; deletion is an owner decision.

Pending on bp-api's side now that guard is on dev (76c2769): `guard.Scan` at
intake, `guard.Redact` outbound, the `guard.Policy`/`Limiter`/`Watch` swap,
and the M7 hop marker.

## Follow-up: c373649 (guard wiring, F1-F7)

F1, F2, F4, F5, F6 and F7 are fixed as described (spot-checked F2: family
revocation skips an empty family and static tokens; legacy reuse still
alerts). F3 is deferred until `Queue.Render` lands; agreed.

Guard wiring in `internal/api/guard.go` follows the locked API:
`CheckSend`/`CheckRoom` and the `Limiter` per Peer ID before acceptance,
denials read as not-found and feed `EvDenied`, lookups feed `EvLookup`,
accepted remote text is scanned once at intake (High -> `guard.finding`
alert), every recipient gets `EvInbound`, and local text read by a remote
client (inbox, room posts, board values and history) goes through
`guard.Redact` with `EvOutbound`.

Remaining notes, none blocking:

- **One Watch per process.** `NewCore` builds its own Watch and the P2P node
  has another, so an agent tainted over P2P and then read by a gateway client
  (or the reverse) does not trip `tainted-relay`. blueprint should build one
  Watch in cmd/bp and hand it to both.
- **Unredacted local metadata.** Room topics and agent descriptions written
  locally are shown to remote clients without `guard.Redact`. Low risk (short,
  owner-written), but run them through `outbound` too.
- **Hop marker (M7):** not needed now. bp never relays by itself, and the
  per-client limiter and room budgets bound agent-driven loops. Tracked as
  T-L2 partial in the threat model.
