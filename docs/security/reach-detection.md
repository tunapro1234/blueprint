# Detecting unauthorized reach

Status: design by W6 (bp-guard), October 2026. Code: `internal/guard`
(`Policy`, `Limiter`, `Watch`, `Sensitive`). Wiring belongs to the owners
named below.

bp cannot stop a same-uid agent from opening a file. It can notice when an
agent or a peer reaches for something the owner did not give it, write that to
`audit.jsonl`, and tell the owner. Prevention lives in policy checks; this page
is about what happens when they fire, and about the cases they cannot see.

## Signals

| Signal | Where it is produced | Guard input |
|---|---|---|
| Peer asks for a non-exposed agent | p2p `handle` (`Policy.CheckSend`), fed hub `handleSend` | `Event{Kind: denied, Detail: "not-exposed", Peer, Target}` |
| Peer uses an ungranted room or capability | rooms/board (W3) via `Policy.CheckRoom` | `Event{Kind: denied, Detail: code}` |
| Peer looks up a name | p2p `handleLookup` (`Policy.CheckLookup`) | `Event{Kind: lookup, Peer, Target}` for every query, found or not |
| Untrusted text delivered to an agent | msgq when a record with `Origin` is delivered (verified or unverified) | `Event{Kind: inbound, Agent, Peer, Channel, Severity: MaxSeverity(findings)}` |
| Agent touches a secret | `bp guard hook` (below), called by a harness pre-tool hook | `Event{Kind: sensitive-access, Agent, Target}` when `guard.Sensitive(tool, input)` matches |
| Agent sends across a boundary | p2p/fed/API outbound path after `Redact` | `Event{Kind: outbound, Agent, Peer, Detail: "redacted" if Redact changed text}` |

## Rules (`guard.Watch`)

| Rule | Fires when | Severity |
|---|---|---|
| `probe` | a peer addresses a non-exposed agent or ungranted room | warn on the first, high at 3 distinct targets in 10 min |
| `enumeration` | a peer looks up 8 distinct names in 10 min | high |
| `tainted-secret-access` | an agent that received untrusted text in the last 30 min touches a secret path | high if the input had a high finding or the access is within 5 min, else warn |
| `tainted-relay` | a tainted agent sends to a different peer than the one that tainted it, or sends text that Redact had to change | high |

The worst severity inside the taint window is kept, so a harmless follow-up
message cannot launder an earlier hostile one. Repeated alerts for the same
rule, severity and subject are collapsed for 10 minutes. Every event reaches
the sink, alert or not, so the audit log is complete even when alerts are
suppressed. Windows hold at most 256 entries per peer.

A denied lookup answers exactly like "not found" on the wire. The peer cannot
tell a hidden agent from a missing one; only the local audit records the
difference.

## Audit records

Guard does not write files itself. A `guard.Sink` adapter in W1's
`internal/audit` maps:

- `Sink.Event(ev)` to `audit.Event{Type: "guard." + ev.Kind, ...}`;
- `Sink.Alert(a)` to `audit.Event{Type: "guard.alert", Severity: "alert", Rule: a.Rule, ...}`.

Records carry names, rule, decision code, channel and finding kinds. They
never carry message bodies or matched secret values; `Finding.Excerpt` is
already redacted and escaped.

## Owner alert path

1. Every alert is in `audit.jsonl` (source of truth).
2. High alerts are also queued as a bp notice to the owner's coordinator
   agent (the `Coordinator` from `bp book`) through the ordinary msgq path,
   so a busy coordinator is never interrupted. The notice carries the rule,
   peer, agent and audit line id, never the text that triggered it.
3. The local UI (module `ui`) shows open alerts and the `Watch.Snapshot`
   per-peer view.
4. Guard never sends WhatsApp or mail itself. An owner who wants phone alerts
   enables that explicitly in the `wa` module, which then reads guard alerts
   from the audit log.

## The secret-access tripwire (`bp guard hook`)

Installing a harness hook changes the user's environment, so it is part of
an opt-in module (W2 registry; proposed name `guard-hooks`), not core.

- Claude Code: a `PreToolUse` hook runs `bp guard hook claude`, reads the tool
  call JSON on stdin, runs `guard.Sensitive(tool_name, input)`, and on a match
  records `sensitive-access` for the calling agent (identity from bp's normal
  sender probes). It returns "allow" in observe mode; in enforce mode it
  returns "ask" so the human confirms the access while the agent is tainted.
  It never blocks an untainted agent.
- Codex and others: the same check runs over transcript tool calls when bp
  reads transcripts for delivery evidence; this detects after the fact.
- Canary files (W7) are planted paths that no legitimate task touches;
  any read is a high alert even without taint.

`Sensitive` matches credential stores (`~/.ssh`, `.credentials.json`,
`.codex/auth.json`, `identity.key`, `state/fed/`, `peers.json`, `.env`,
`/etc/shadow`, key files, `printenv`, `/proc/*/environ`). It is a tripwire,
not a sandbox: a determined same-uid process can always read the file in a way
the hook does not see.

## Integration checklist (W1, P2P)

In `internal/p2p/node.go`:

1. `handle`, MessageProtocol: replace the inline expose loop with
   `policy.CheckSend(req.To, len(req.Text))` and `limiter.Allow(alias, policy)`;
   on deny, `watch.Observe(Event{Kind: EvDenied, Detail: d.Code, ...})` and audit.
2. Build the body as `"["+from+"] " + guard.Frame(src, req.Text).Text`, with
   `src` from the authenticated peer (alias, Peer ID, channel) and the claim.
   Store `Framed.Findings` kinds in the origin record and audit them.
3. `handleLookup`: `policy.CheckLookup(req.Find)` before resolving; a deny
   returns an empty `LookupResponse`; observe `EvLookup` for every query.
4. Outbound (`Service` send path): `guard.Redact(text, policy.Redact)` before
   the channel is created, so the stored channel never holds the secret;
   observe `EvOutbound`.
5. msgq delivery of a record with `Origin`: observe `EvInbound` with the
   stored finding severity.

The same five points apply to the fed hub and client (T-E2, T-F1) and to
W3's HTTP/MCP/rooms entry points.
