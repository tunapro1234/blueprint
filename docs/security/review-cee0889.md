# Review: cee0889 (P2P audit, rate limit, loop cap, exposed-only lookup)

Reviewer: bp-guard (W6), 2026-10-09. Scope: `internal/p2p/limits.go`,
`internal/p2p/node.go` changes, `internal/audit`, `cmd/bp/p2p.go` resume/pauses.

## R1 (high): unauthenticated audit flood fills the disk

`handle` audits "peer is not allowed" for any libp2p identity that opens a
`/bp/msg` or `/bp/qstat` stream (no connection gater). `reject` deduplicates
only by `req.ID`, which is not validated before the peer check, so every new
ID is a new line. `audit.Append` rotates at 32 MiB and never deletes.

Measured on feat/guard: an unconfigured node sending 200 requests with
distinct IDs produced 200 audit lines. A relay node (`relay: true`) is
reachable by anyone, so this is an internet-facing disk-fill path on the
relay server. Configured peers can do the same with non-exposed targets
(audited at `alert` before `admit` runs, so the rate limit does not apply).

Fix: a per-source audit budget in front of `reject`, e.g. a token bucket
keyed by Peer ID for configured peers and one shared bucket for all
unconfigured peers (plus a small LRU of their IDs). When a bucket is empty,
count instead of writing, and write one `p2p.audit.suppressed` event per
minute with the count. Also apply the peer rate limit to rejected messages,
not only to new admissions.

## R2 (medium): pause file read-modify-write race

`Pause` (service, on loop cap) and `Resume` (CLI) both read `paused.json`,
edit, and rewrite it with no lock. A resume that races a new pause can lose
either change. Take a flock on `paused.json.lock` around both.

## R3 (low): lookup still costs work and is not throttled

Exposed-only lookup is correct, and the hidden answer is identical to
"unknown" on the wire. But `ResolveLookup` (agentbook read and a tmux check)
still runs before the expose check, and lookups do not pass the limiter.
Check `exposed(policy, req.Find)` first when the query is a canonical name,
and run lookups through a separate per-peer bucket. guard.Watch now alerts on
enumeration (8 distinct names in 10 minutes); it does not throttle.

## R4 (low): admission accounting

- A token and a loop slot are spent before `EnqueueOnceOrigin`; if enqueue
  fails (conflicting replay), the slot is still spent.
- `fresh` is decided by `Queue.Record` outside the enqueue lock, so two
  concurrent first deliveries of one channel are both counted. Harmless
  today, but it skews the loop cap under retries.

## R5 (note): loop cap scope

The cap counts inbound messages per peer and local agent. Loops between two
local agents, and A to B to C chains across peers, are not covered. Fine for
this step; record it in the threat model as T-L2 partial.

## Confirmed good

- Replays of an accepted channel bypass the limiter.
- Pauses survive a service restart and are owner-visible (`bp p2p pauses`).
- Hidden lookup answers exactly like an unknown name.
- Audit fields are clipped; the file and directory are 0600 and 0700.
