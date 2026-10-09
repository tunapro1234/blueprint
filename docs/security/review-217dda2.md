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
