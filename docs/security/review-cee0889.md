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

## Follow-up: d8afaf5 (lookup rate limit)

- Closes R3's cost half: the limiter runs before `ResolveLookup`.
- `firstRejection("lookup:"+peer, "rate")` audits only the first throttle per
  peer for the life of the process (until the 4096-entry map resets). A peer
  that floods again hours later leaves no new trace. Re-audit once per
  window with a suppressed count (same fix shape as R1).
- R1 (audit flood from unconfigured identities) is still open.

## Fixed on feat/guard (R1, R2, R3)

- **R1:** every rejection event a remote request causes now goes through
  `Node.auditRemote`. Each source may write `AuditBudgetPerMinute` (30)
  events per minute. A configured peer is charged to its own budget. Every
  unconfigured identity is charged to one shared budget, so a new identity
  does not get a new budget. Events over the budget are counted, and each
  finished window that suppressed events writes one `p2p.audit.suppressed`
  event with `count`, `since` and `worst`. Its severity is `alert` when an
  alert was suppressed. `Step` calls `FlushAudit`, so a flood that stopped
  is still reported. The log stays append-only: nothing is deleted. The
  budget map holds at most one entry per configured peer plus one shared
  entry, so the limiter itself stays bounded.
- **Lookup throttle audit:** the throttle is now audited at most once per
  peer per minute (`onceEvery`), not once per process lifetime.
  `p2p.lookup.hidden` is under the same budget.
- **R2:** `Pause` and `Resume` read, modify and write `paused.json` under an
  exclusive flock on `paused.json.lock`.
- **R3:** lookup from a peer with nothing exposed skips `ResolveLookup`, so it
  does no agentbook or tmux work. The lookup rate limit from d8afaf5 still
  runs first.
- **Tests:** `TestAuditBudgetCountsOverflow`,
  `TestDeniedFloodIsCappedAndReported` (an end-to-end flood from an
  unconfigured identity), `TestOnceEvery`,
  `TestPauseResumeConcurrentNoLostUpdate` and
  `TestLookupWithNoExposeSkipsResolve`.
- **Open note (pre-existing, not changed here):**
  `TestLostReplyConcurrentRetryAndRestart` flakes under load. Retries of the
  same channel ID race the first enqueue: each one sees no queue record and
  spends a rate token, so some get "rate limited". This is benign because
  the client retries, but a per-channel lock around the check-and-admit
  would remove the flake.
