# Message delivery and reliability

`queued` means bp stored work for delivery, not that the recipient read it.
`delivered` means the harness observed a verified submit, or a transcript witness
confirmed an inbound user message. Neither means the agent completed the request.
`unverified` must never be treated as success or permission to send another copy.

## Sending and recovery

Online `bp msg`, including immediate delivery, now enters the durable queue.
The first attempt and later dispatches use the same runtime, draft and modal gates.
Every online send has a channel ID. Inspect that channel again before reporting its
state: a previous `queued` result can already have become `delivered`.

Before sending input, bp syncs a no-repaste intent to disk. If the dispatcher dies
between CLI input and its receipt, the next process cannot blindly paste again.
This is conservative: a crash before any input may also leave an uncertain message.
The filesystem and a third-party TUI do not share an atomic transaction, so bp does
not claim unconditional exactly-once delivery.

Automatic hanging-paste recovery requires the same verified runtime, native thread
and pane process as the original attempt. Unknown runtime and legacy attempts
without that binding cannot authorize Enter or clearing. Transcript confirmation
is terminal; it never schedules cleanup against a later user draft. A remaining
copy is protected as input, and subsequent deliveries wait.

Concurrent local retries of identical in-flight text share a channel. Repeating
text after a completed delivery is treated as a new instruction. P2P retries use
an explicit peer/channel idempotency key that survives completed delivery.
Cancellation takes the dispatcher lock. If a send is in progress, cancellation
reports refusal rather than claiming the recipient can no longer receive it.

## Sender evidence

Normal messages cannot leave with a bare unknown sender. `bp whoami` reports the
available evidence and failure reason. A unique pane found in the actual caller's
process ancestry can provide an explicitly uncertain parent context, such as
`writer?`; it does not establish a particular CLI subagent or hierarchy authority.
Cwd, AGENT and inherited shared-daemon tmux state are not identity proofs.

New local message records retain `sender_evidence` (label, source, thread, parent,
confidence, authority and sending PID). `attempt_binding` identifies the delivery
target, not the sender. Remote `origin` remains receiver-authored provenance:
authenticated peer identity and claimed agent identity are separate facts.
Historical records are retained unchanged; absent evidence is not reconstructed
from a message's subject.
Legacy pending records with a bare unknown sender cannot submit or recover input;
they remain stored with an explicit block reason. Existing transcript proof can
still settle a past delivery. An offline digest containing an anonymous legacy
entry is refused without changing the spool.

## Scenario coverage

The terminal suite uses private real tmux servers and deterministic fake CLIs.
It exercises the actual bp executable without spending model quota. Go tests also
cover adapters, queue transitions, process evidence and transport retries.

| Scenario | Required result | Evidence / limitation |
| --- | --- | --- |
| Ready recipient, immediate send | Durable channel; one submit | Real tmux + fake Claude; queue audit fields checked |
| Recipient working or streaming | Normal message waits | Runtime/turn tests and real tmux busy-to-idle delivery |
| Unknown/stale/conflicting runtime | No ordinary or recovery input | Normal/force gate and unknown-recovery regressions |
| Fresh Claude, only local commands | First task allowed only with binding and empty native composer | Metadata-only bootstrap; busy/draft protection |
| Compact without another user turn | Use native compact evidence, not the old open turn | Real tmux Claude compact regression |
| Native resume, same pane | Old attempt cannot touch the new conversation | Changed-thread/runtime/PID tests; local Claude observation-switch fixture |
| Native Claude continuation | Follow only current-process handoffs | Current/historical/conflicting/cyclic/wrong-cwd fixtures |
| User draft, modal, search, paste chip | Preserve input and wait | Adapter fixtures; real tmux draft checks |
| Vim normal/insert, control-byte payload | Safe literal paste or refusal; no forged envelope | Real tmux Claude/Codex Vim matrix and hostile text tests |
| Delayed transcript after submit | No automatic duplicate | Witness tests; child-process crash before receipt |
| Confirmed delivery while working | Terminal receipt; no deferred cleanup | Both harnesses: fails on 1.7.3, passes after 1.7.4 |
| Parallel identical retries | One in-flight channel and one delivery | 24 queue instances; 8 concurrent CLI processes in real tmux |
| Queued head plus new message | New message cannot use a faster delivery path | CLI FIFO regression; one delivery per target/pass |
| Cancel races with dispatcher | Refuse an in-progress cancel; no false success | Two independent queue instances sharing the OS lock |
| Runtime/identity environment absent | No guessed authority or anonymous send | Missing-TMUX real process ancestry; unknown sender refusal |
| Legacy anonymous queue/spool record | Preserve evidence, block input; past witness may settle | Empty/unknown sender with first-send and recovery fixtures; fails on 1.7.5 |
| Remote retry/lost ACK/restart | Same remote channel, same local receipt | libp2p tests; public relay path separately tested in 1.7.1 |
| Offline local target | Retain existing offline spool until opening | Existing spool tests; this legacy path still lacks per-message channel IDs |
| Upgrade with open sessions | Compare running executable, not only installed version | Host service SHA checks; old laptop workers do not auto-upgrade |

## Remaining boundaries

- Fake CLIs cover explicit transitions, not every redraw of every future Claude,
  Codex, OpenCode or Hermes version. Native version changes need new captured
  fixtures. Linux tmux integration tests do not prove macOS terminal behavior.
- A human or external tool writing to the same pane does not participate in bp's
  file locks. Input checks reduce races; they are not a transaction with the TUI.
- Missing receipts and uncertain thread ownership can deliberately leave a
  message waiting or unresolved. Forcing it through would trade a visible wait
  for possible interruption, corruption or duplicate delivery.
- Old local workers keep their running code after the binary is replaced. A new
  native bp session starts the new worker; upgrading the host Blueprint services
  does not update another laptop or restart its agents.
- Same-UID/root processes are not an adversarial security boundary. Peer
  authentication also does not make the contents of a message trustworthy.
