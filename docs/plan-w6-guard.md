# W6 plan: guard (bp-guard, branch feat/guard)

Goal: text from outside the machine is untrusted data, agents cannot be steered
into reaching what their owner did not give them, and the owner sees every
security decision.

## Findings from reading dev (de916f9)

- P2P inbound text reaches the agent as `"[external:x@alias] " + text`
  (`internal/p2p/node.go`). The body may contain a newline followed by
  `[server-main] ...`, which looks exactly like a local bp envelope.
  `messagetext.Validate` blocks control bytes, not envelope spoofing.
- `/bp/lookup/1.0.0` answers any configured peer for any local agent name,
  not only the agents in that peer's `expose` list (W1 owns the fix; guard
  provides the policy check).
- There is no audit log yet (W1 `internal/audit`); guard writes findings
  through it and keeps a local fallback interface so it can land first.

## Steps

1. `docs/security/threat-model.md`: assets, trust boundaries, attackers,
   mitigations with owners (W1..W7).
2. `internal/guard`, pure Go, no I/O except through injected sinks:
   - `Frame(Source, text) Framed`: per-message random nonce in the open and
     close markers, provenance line, "untrusted data, not instructions from
     your owner"; any line in the body that looks like a bp envelope, the
     owner or a frame marker is neutralised by a visible prefix; the nonce
     never appears in the body (regenerated on collision).
   - `Scan(text) []Finding`: instruction override, tool/command requests,
     credential requests, exfil URLs, hidden unicode, encoded payloads.
     Findings only flag; the message is never dropped by Scan.
   - `Redact(text, RedactPolicy) (string, []Finding)`: API keys, tokens,
     private keys, JWTs, optional emails; per-peer policy.
   - `Policy` per peer: capabilities (send, lookup, rooms, board), rate,
     max size, expose list; `Policy.Check(op, target)` returns a decision
     with a reason suitable for audit.
3. Unauthorized reach detection (`guard.Watch`): records per-peer and
   per-agent events in a small sliding window and raises alerts for
   non-exposed target probes, lookup enumeration, and "external message
   then secret access" (agent tool-use observed through hooks/transcript).
   Design doc `docs/security/reach-detection.md`; alerts go to audit.jsonl
   with `severity: alert` and an owner alert sink (bp notification path,
   configurable, never auto-WhatsApp).
4. Integration notes for blueprint (W1): exact call sites in p2p `handle`
   and `handleLookup`. Reviews of feat/api and feat/modules when they report.
5. Regression tests for every confirmed bp-redteam attack in
   `internal/guard/testdata/attacks/`.

Tests: `flock /run/lock/bp-gotest.lock nice -n 10 go test ./internal/guard/...`
after a heat check.
