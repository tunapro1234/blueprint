# Review: dev 217dda2 (delivery-time framing seam)

Reviewer: bp-guard (W6), 2026-10-09. Scope: `internal/msgq/msgq.go` seam,
`internal/delivery/render.go`, the daemon and cmd/bp wiring.

Verdict: the seam is right. Every paste, witness and post-paste check uses
`Wire()`, the raw body stays in pending, `done/` and `messages.jsonl`, and
the render is deterministic per record. Two gaps undo the guarantee in
specific cases and should be fixed before a release.

## D1 (high): failure notices carry raw external text to the coordinator

When a delivery is unverified or fails, `noticeText` (msgq.go:1427) quotes
the first 60 runes of `message.Msg` and enqueues the notice from the trusted
sender `bp`, with no Origin, to the sender's home. An external sender
(`external:...`) has no session, so `resolveNoticeHome` routes the notice to
`NoticeOwner`, the fleet root. Result: up to 60 raw, unframed runes of
attacker text, including newlines, reach the coordinator under the `bp:`
label. A peer can aim for this path by targeting an agent whose deliveries
tend to go unverified (a busy or asking pane).

Fix: when `message.Origin != nil && guard.NeedsFrame(message.Origin.Transport)`,
do not quote the body. Write "external message from <peer alias> (<peer id>);
inspect with bp qstat <id>" instead. If a head is wanted, run it through
`guard.Body` and collapse it to one escaped line, never raw `Msg`. Add a test
in which an external record goes unverified and the notice contains no body
bytes.

## D2 (medium): framing fails open

If `guard.LoadFramer` fails (unreadable `guard/frame.key`, a full disk on
first creation, wrong owner), the daemon and cmd/bp log a warning and leave
`Render` nil, so external messages are pasted raw. The failure is silent to
the agents receiving them and easy to miss in the log.

Fix: on a load error, install a `Render` that returns local records
unchanged and returns an error for `NeedsFrame` origins. External records
then wait as bad records, visible in `bp qstat`, and local traffic is
unaffected. Also write one `guard.frame.unavailable` alert to audit.

## D3 (low): render failures hide records from replay dedup

`pendingRecords` now renders on every call, including `RecentIdentical` and
the idempotent replay check (idempotent.go:126). A record that fails to
render becomes a bad record there too, so a replayed P2P channel would not
find it and could be enqueued a second time. Only dispatch needs `wire`:
render in the dispatch caller (or carry the render error on the record), and
keep every record visible to the dedup readers.

## Notes

- bp-api: once feat/api is on dev, wire `api.Core.Frame` with a guard
  adapter and set `Core.DeliveryFramed` where `Queue.Render` is set. Until
  then the gateway refuses to start (by design).
- Build one `guard.Watch` per process and share it between the P2P node and
  api Core, so cross-transport taint is correlated.
- bp-compat's hook delivery path (`msgq.Claim` into Claude
  `additionalContext`) must emit `Wire()`, and the D1 rule applies to any
  notice it writes. W6 reviews it when feat/compat rebases.

## Confirmed good

- `wire` is unexported and unserialised; `Wire()` falls back to `Msg`.
- All delivery, witness and recovery sites use `Wire()`; `RecentIdentical`
  and display keep `Msg`.
- Daemon and CLI share one key under StateDir, so a record frames to the
  same bytes in both and across a restart.
- The decision is `NeedsFrame(transport)`, not `PeerAuthenticated`.

## Follow-up: 3576643

D1, D2 and D3 are fixed. Verified in code:

- D1: an external record's notice names only the peer alias and Peer ID
  (both set by the operator or by crypto, never by the remote), omits the
  claimed agent name, and withholds the body. `msgq` stays guard-free through
  the `FrameExternal` predicate, which is the same decision `Renderer` makes.
- D2: on a framer load error both dispatchers install `FailClosedRenderer`
  (local records pass, external records are held) and write
  `guard.frame.unavailable` at `alert`.
- D3: a render error rides on the record; only `dispatchRecord` holds it, so
  replay and duplicate scans still see the record.

Two small notes:

- The notice says "read the message with bp qstat <id>", but `Queue.Status`
  prints only state, never the body. That is the safe behaviour; reword the
  notice so the coordinator does not go looking (for example "the body is in
  the queue record; the owner can read it").
- cmd/bp writes `guard.frame.unavailable` from `prepareDispatch`, so while
  the key is broken every bp command adds one alert line. Write it once per
  hour (for example, a marker file's mtime under `<state>/guard/`).

## Follow-up: hook delivery path (eea14fd) and API wiring (5ba3aef)

- `claimForHook` calls `wireFraming()` before `Claim` and hands
  `message.Wire()` to the harness; `Claim` stops at a record with a render
  error and validates `Wire()`, as `dispatchRecord` does. Each claimed
  external message is framed on its own, so concatenating up to 10 under
  the `[bp] N messages arrived` header keeps every body inside its frame.
  The digest part comes from the local pending spool (local senders only).
  Verified.
- `wireFraming()` fails closed with the same `guard.frame.unavailable`
  alert. The once-per-hour throttle note above now applies to every hook
  call as well (UserPromptSubmit and Stop run it on every turn).
- API: `Core.Frame` is `GuardFramer` only when the key loads, and
  `Core.Render` is the delivery renderer. `Gateway.Ready` probes both with a
  real frame, so a passthrough can no longer pass. Verified.
- Shared `guard.Watch`, corrected (9 Oct). The 61d789a reasoning was
  wrong in one place. Probe and enumeration key on per-transport peer
  identities, but taint and tainted-relay key on the LOCAL agent. So inbound
  text through P2P or fed followed by an outbound send through the API is a
  real cross-process pattern. Fix:
  - In-process: blueprint's `a.guardWatch()` gives one Watch per process
    (api, gateway, room and mcp in `bp api serve`, and the p2p node).
  - Cross-process: `WatchConfig.TaintSource`, set to
    `MessageLogTaint(<msgq>/messages.jsonl)`. It covers secret-access and
    outbound events, and the newer of in-memory and on-disk taint wins.
    `messages.jsonl` is the one canonical taint store, the same one the
    tool-call hook reads, and it already holds every transport's delivered
    Remote records. There is no separate taint file and no startup seeding:
    a startup seed would miss input that arrives later in another process.
  - P2P outbound: the node reports `EvOutbound` on a channel's first send
    attempt, with the agent taken from the channel's sender label. Its
    default Watch uses `MessageLogTaint`, so text that came in through fed,
    the API or P2P and then goes out to a different P2P peer raises
    `guard.reach.relay` (`TestOutboundAfterTaintAlertsRelay`).
- The Wire() rule applies to the Codex, Hermes and OpenCode hook paths when
  they land.

## Follow-up: the two small notes (8e17829)

D1, D2 and D3 were already verified closed in 3576643 (see above).
blueprint's 8e17829 closes the two small notes, and I verified it:

- The external-delivery notice now says "check delivery with bp qstat
  <id>". That is true, because qstat reports delivery state and never prints
  bodies.
- `wireFraming` writes `guard.frame.unavailable` at most once an hour. A
  marker file under `<state>/guard/` holds the time, and a marker error
  fails toward alerting. The stderr warning still prints on every call.
  `TestFrameUnavailableAlertThrottle` covers it.
