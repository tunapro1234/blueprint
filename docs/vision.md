# bp vision

Status: proposal for the owner, 2026-10-09. It builds on
[direction.md](direction.md), which stays the agreed reference: this page
extends it and does not replace it. Changes it argues for are listed in
section 11; nothing changes until the owner agrees.

Truth rule: a capability called **on dev** exists at `dev` `9314eaa` (checked
in `cmd/bp`, `internal/api`, [api.md](api.md) and an isolated build).
Everything else is *planned* (assigned to a workstream) or an *option* (not
decided). Review IDs (FUN-01, PRD-03, ...) refer to an internal staff review of
2026-10-09 that is not published. Numbers in brackets point to the sources in
Appendix B.

## 1. North star

> Any two agents you own can work together, whatever they run in and wherever
> they run, without interrupting each other or you, and with proof of what
> arrived.

The sentence for people stays the agreed one: *bp lets your agents find each
other, message each other and work as a team, whatever they run in and
wherever they run.*

**North star metric: weekly verified agent pairs.** The number of distinct
sender-to-receiver agent pairs with at least one message bp marked delivered in
the last seven days. Two cuts matter most: **cross-harness pairs** (different
harnesses) and **cross-machine pairs** (over P2P). It counts proof, not
traffic: an unverified message does not count, so the number cannot grow by bp
becoming less honest. It is computed on the user's machine (section 8).

## 2. The bet: why now

1. **People run several agents, from several vendors.** Tools for running
   fleets of parallel agents draw large followings (Orca has over 88,000
   GitHub stars, Gas Town over 18,000 [12]), and bp's own compatibility plan
   (W5) lists eighteen CLI harnesses.
2. **The vendors made agents talk, inside their own walls.** Since August 2026
   Claude Code sessions message each other out of the box, and Codex can queue
   messages into its sessions [1][3][6]. Claude Code's docs are explicit: "In
   every approach the workers are Claude sessions" [1]. This proves the need
   and takes same-vendor messaging off the table as a differentiator. What is
   left is the seam between vendors, and that is bp's ground.
3. **The protocols stop one step short.** MCP connects an agent to tools and
   became stateless in its 2026-07-28 revision [8]. A2A connects agent services
   (v1.0, now under the Agentic AI Foundation) [7]; Gemini CLI and Hermes speak
   it, Claude Code, Codex and OpenCode do not natively [7][9]. ACP connects an
   editor to one agent [9]. None of them says how to put a message into a live
   session that a human is typing into, when to do it, or how to prove it
   arrived.
4. **Once agents talk, safety is the product.** A message from another agent is
   a new path for prompt injection. Claude Code's own messaging states that "a
   message from another session never counts as your consent" [1]. The READMEs
   of Agent Relay, agmsg, Concord, CCB, Gas Town, MCP Agent Mail and Repowire
   do not say how they treat incoming text as untrusted [10][12].
5. **The category is crowded; the combination is open.** agmsg, Agent Relay,
   Concord, CCB, Gas Town, MCP Agent Mail, Repowire and agent-talk all let
   agents of different vendors exchange messages [10][12]. Each covers part of
   bp's promise. None we found combines turn-boundary delivery that never
   touches a human draft, one receipt model that admits "unverified" for every
   harness, untrusted-input framing, and reach across machines without a vendor
   cloud or a central server. Launch history agrees: Show HN posts titled "let
   X and Y talk" drew 2 to 14 points, while agmsg reached #5 on Product Hunt
   with a problem-first pitch [11]. bp must lead with its guarantees and
   prove them.
6. **P2P is the multiplier, not the magnet.** The pull is agents of different
   harnesses working together safely on one machine. "It works across machines
   too" then turns one developer's setup into a team's.

bp starts with real assets. The staff review judges the core idea right and
missing from competitors, and singles out the durable queue, intent before
input, the provenance model and the habit of modeling uncertainty honestly. dev
adds the zero-change installer and modules (W2), HTTP/A2A/MCP with rooms and a
board (W3), the Claude Code hook path (W5) and the guard (W6).

**Risks, and the answer to each**

| Risk | Answer |
|---|---|
| Vendors open their messaging to each other | Be the neutral layer on top of it: deliver through their native inboxes (3.3) and keep the guarantees, safety and P2P that a single vendor will not build for its rivals |
| A messenger with more traction adds draft safety and receipts | Ship first with proof: the delivery test matrix, conformance tests per harness, a public threat model |
| A trust incident: a false "delivered", an injection, a bad release | The launch blockers (section 6) and principle 7: never claim what you cannot prove |
| Screen and process reading break when a harness changes (macOS Codex delivery is the live example, section 5) | Front door first (hooks, app-server APIs, native queues), conformance tests on every harness release |
| A small team spread thin (PRD-02) | Small core, tiers, at most two workstreams in flight after launch |

## 3. The wedge and the platform

### 3.1 The wedge: cross-vendor delivery you can trust

The job we win first: *"Let my Claude Code and my Codex work together without
breaking either."* No single guarantee below is unique; the combination is.

| Guarantee | What the user sees | How (on dev) |
|---|---|---|
| Never interrupt | A message to a busy agent waits for the lull, the quiet moment between turns. While the human's half-typed text sits in the prompt, bp waits. | busy, draft and modal gates before typing; Claude Code hooks hand messages over at turn boundaries |
| Honest receipts | Every message has a channel id. `bp qstat` says DELIVERED only with evidence; a paste bp cannot confirm is reported unconfirmed and never typed twice. | transcript witness, hook hand-over, intent before input |
| Any vendor | Claude Code and Codex with verified delivery (Codex on macOS: section 5, item 6); Hermes and OpenCode deliver but are reported unverified until their adapters can prove it; any agent or script through MCP or HTTP | `bp` CLI, `bp mcp`, `bp serve --api` (A2A-shaped bodies) |
| One model across vendors | One hierarchy (parent, role), one expose policy, one audit log and one receipt vocabulary, whatever each agent runs in | agentbooks, `bp tree`, `audit.jsonl`, `msgq` states |
| Across machines, no vendor cloud | "And it works across machines too": P2P to your own relay, nothing routed through a vendor's servers | `bp p2p`, `agent@peer`, expose lists |
| Safe by default | Nothing listens; outside text arrives framed as untrusted data; every decision is in the audit log; `bp uninstall` removes what bp added | `guard.Frame`, expose lists, `bp audit`, loop caps, modules |

bp is not a session manager, an orchestrator or a swarm. It never decides what
agents work on and never calls a model.

### 3.2 The platform: three rings

Each ring needs the trust the previous one earned.

| Ring | What the user gets | On dev | Planned or option |
|---|---|---|---|
| 1. Teams | Agents work as a group: rooms, a shared versioned board, hierarchy, portable project trees | `bp room`, `bp board`, MCP `bp_room_*`/`bp_board_*`, `--parent`, `--role`, `bp tree`, `bp schema export`, `bp continue` | ready-made structures as examples (direction principle 5) |
| 2. Agents everywhere | Agents on another machine, a colleague's agent, a phone | P2P (`bp p2p`, `agent@peer`, expose lists, your own relay); remote MCP gateway for Claude.ai and ChatGPT connectors, off by default and not published | tmux optional (W4); guided phone setup; hosted relay (*option*) |
| 3. The agents' social graph | See how your agents work together; share structures that work | the data: `messages.jsonl`, `audit.jsonl`, hierarchy | local graph view (who waits on whom, loops, unverified share); shareable structure templates (roles, edges, expose lists, no content); research with graph theory and economics |

The graph belongs to its owner: local analytics plus files people choose to
share, not a public network of agents.

### 3.3 Strategic move: integrate with native messaging

Every major harness now has an official way to receive input from outside, and
direction.md's delivery order (terminal, push, hooks, pull) already allows
using it. bp becomes the neutral layer above the vendors' messaging, not a
rival to it, and every path keeps the same receipts, framing and audit:

- **Codex:** the app-server's `thread/queue/add` (experimental, like the
  app-server itself) runs queued input in order after a turn ends, and thread
  status replaces process inspection [6]. `turn/steer` enters a running turn,
  so it stays behind the force path.
- **Claude Code:** hooks today [2]; then its documented messaging socket
  (`CLAUDE_CODE_MESSAGING_SOCKET`) as a push path, posting only when bp sees
  the session idle, because native messages land between tool calls [1].
  Verify the protocol, its receipts and what it does to a draft first.
- **OpenCode and Hermes:** `opencode serve` accepts prompts over HTTP; Hermes
  queues input while busy and accepts A2A tasks [9].
- **Fallback:** the terminal path stays, for every harness without a door.

## 4. Personas and jobs to be done

| Persona | Today | Job to be done | Ring |
|---|---|---|---|
| **P1 Cross-vendor developer** (launch persona) | Claude Code and Codex in two terminals, copy-pasting between them | "When one agent finishes, have the other review it, without me as the clipboard and without breaking either session." | 1 |
| **P2 Fleet operator** | 5 to 30 agents with a lead and workers; the owner's own setup | "Coordinate many agents, know what was delivered, stop loops, see everything." | 1, 3 |
| **P3 Builder** | Scripts, CI jobs and bots that need a live coding agent | "Post to my coding agent from a script and know whether it arrived." | 1 |
| **P4 Pair across machines** | Two people, each with their own agents | "Let my agent ask my colleague's agent, with both owners' permission and a record." | 2 |
| **P5 Security-minded lead** | Wants collaboration without an injection highway | "Allow it with defaults I can defend: least reach, framing, audit, uninstall." | all |

Who bp is not for: someone who uses one vendor and is happy with its built-in
messaging. The comparison page should say so (PRD-03).

## 5. The activation moment

**Two agents in different harnesses exchange a message that bp marks
delivered, within five minutes of install.** It proves the three core
guarantees at once, and it is the moment people describe to someone else.

The path on dev: install; `bp run --name alice codex` and
`bp run --name bob claude` in two terminals; ask alice in plain words to message
bob; `bp qstat` moves from PENDING to DELIVERED. It needs tmux and two
signed-in CLIs. The reverse direction (anything sent to Codex) must work too
before launch; see item 6.

Friction found on 2026-10-09 (items 1 to 5 in an isolated build of dev
`9314eaa`):

1. **dev does not build on macOS:** `internal/api/token.go` reads peer
   credentials with Linux-only calls (a fix is in progress on this branch).
2. **No tmux, no bp.** Without tmux, or with no tmux server running (fresh
   machine, after a reboot), `bp status` exits 1 with a raw tmux error; the
   MCP tools and the HTTP API directory fail the same way. The agent hint
   makes `bp status` an agent's first command.
3. **Agents outside a bp terminal speak as the human.** `bp mcp` started by a
   harness in a plain terminal resolves to the login name as verified, ignores
   `--as`, refuses `bp_register`, and its messages arrive as if the user typed
   them (SEC-27 class). Two such agents cannot be told apart.
4. **The hint teaches double-quoted `bp msg`,** so backticks and `$()` in a
   message run in the sender's shell (SEC-18). MCP's `bp_send` avoids this.
5. **A typo queues silently:** `bp msg nosuchagent '...'` exits 0 with "queued
   ... delivered when it opens" (FUN-01). A fresh `bp status` also lists a
   phantom `server-main` (UX-08).
6. **On macOS, messages to Codex stay queued** (confirmed with Codex 0.162):
   macOS `lsof` does not report flock locks and Codex holds its writer lock in
   its app-server child, so bp cannot read Codex's turn state and every message
   to a Codex agent on a Mac waits forever. Linux may be affected too (README,
   Known issues). `bp msg` does not reach MCP or HTTP inbox agents yet either.

How to engineer the moment:

- Close items 1 to 6 before launch (section 6).
- "Ask your agent to install bp" ends with a question, not a lecture: the agent
  confirms bp works, then offers to say hello to an agent in another harness.
- *Planned:* a zero-cost first run (`bp try`, name to decide): two fake agents
  from the W5 conformance kit in a private tmux server, one busy, one message
  waiting for the lull, the receipt. No model calls, nothing left behind.
- *Planned:* when only one agent exists, `bp status` and the hint show the next
  step (`bp run --name bob claude`).
- Measure the time to first verified cross-harness delivery locally (section 8).

## 6. Horizons

### Now: launch readiness

| Track | What | Owner | Review IDs |
|---|---|---|---|
| Release integrity | Signing off the agent host, CI as a release gate, supported Go toolchain, one download channel, changelog | owner, blueprint | SEC-01, DEP-01, SEC-13, DEP-04, DEP-05 |
| Delivery truth | **Top priority: delivery to Codex on macOS.** Unknown target refused; witness false positives; pre-paste errors; an unrecognized prompt never read as empty; old dispatchers fenced after update | W1, W5 | FUN-01..04, FUN-08, FUN-09, ARC-02 |
| First run | macOS build verified in CI; missing tmux or no server read as "no live terminal agents"; honest identity outside a bp terminal; `bp msg` to inbox agents; safe quoting; no phantom coordinator | W2, W3, W5 | SEC-27, SEC-18, UX-07, UX-08 |
| Install and modules | Zero-change installer, `bp enable`/`disable`/`modules`, `bp uninstall` (on dev) | W2 | ONB-03 |
| Any agent | HTTP/A2A, MCP, inbox agents, rooms, board (on dev); conformance for Claude Code and Codex | W3, W5 | ARC-05 |
| Safety | Threat model, framing, scan, redact, reach detection, audit, loop caps (on dev, partly wired) | W6, W1 | T-E1..T-L5 |
| Provider policy | Account rotation, token refresh and keepalive out of the release (10.3) | W2 | PRD-01 |
| Owner coupling | Owner jobs behind config; personal data out of the public tree | W2 | ARC-01, SEC-12 |
| Positioning | README (in progress), site, comparison page, `llms.txt` | site, docs | PRD-03, ONB-08, ONB-09 |
| Contributors | Private vulnerability reporting, SECURITY.md, CONTRIBUTING, license decision | owner | PRD-04, PRD-07 |

### Next: one to three months after launch

- **W4 terminal layer:** a built-in pty so `bp run` works without tmux, and a
  control-mode tmux backend for fast status. The largest adoption unlock.
- **Communicate-only without tmux:** MCP and HTTP inbox agents with honest,
  distinct identities and no tmux at all; then terminal delivery without tmux
  through W4.
- **W5 native delivery and coverage:** the paths in 3.3, starting with the
  Codex app-server; verified delivery for OpenCode and Hermes (B3 is partial
  today); an A2A interop test with Hermes and Gemini CLI; more harnesses
  through MCP and the inbox first.
- **W7 red team:** canary and LLM-in-the-loop results published, then kept as
  CI regressions.
- **W3 phone access:** guided gateway setup for Claude.ai and ChatGPT
  connectors, released only after W7 signs off.
- **P2P packaging and relay hardening:** decision 10.2; relay ACL, connection
  gating and per-peer quotas (SEC-15, SEC-16, SEC-17).
- **Delivery architecture:** explicit state machine (ARC-04), hook evidence
  before screen evidence (ARC-05), one dispatcher per host (OPS-03).
- **Learning loop and local UI:** the plan in section 8; the `ui` module
  rebuilt as a read-mostly view of agents, message graph, hierarchy and audit.
  The dashboard behind `ui` today is not that product (SEC-28).

### Later: three to twelve months

- Ready-made structures, then shareable structure templates.
- The social graph view and research on agent interaction.
- Hosted or community relay, teams features (*options*, section 9).
- Windows: WSL2 documented once verified by hand; native Windows through
  ConPTY in the built-in backend.
- Claude Code channels as a push path once they leave research preview.

### Non-goals

- Not a harness; never calls a model; never uses a provider's credentials
  outside that provider's own client.
- Does not decide what agents work on. Structures are examples, not rules.
- Not an OS sandbox: agents under one Unix user share a trust domain.
- No usage-limit pooling and no account rotation.
- No cloud dependency: nothing listens by default, nothing is sent beyond the
  update check, which can be turned off.
- No public directory of agents; discovery stays between configured peers.
- Not pitched as a session manager; `sessions` is a module for those who want it.

## 7. Principles

direction.md's six principles stand as written: installing bp changes nothing;
agents learn about bp, users don't have to; open to every agent; light and
fast; structures are examples, not rules; humans can always see and steer. Five
are missing (amendment A1):

7. **Never claim what you cannot prove.** Delivered means evidence. Unknown
   stays unknown, in receipts, identity labels, status and marketing.
8. **Respect the turn.** The agent's current turn and the human's draft always
   win. bp never types into a busy agent to look fast.
9. **Private by default.** Messages, logs and graphs stay on the machine. Any
   learning about usage is opt-in, shown before it is sent, and counts only.
10. **Use the front door.** Prefer each harness's official integration points
    (hooks, native queues, MCP, A2A) over reading the screen, follow each
    provider's terms, never touch credentials.
11. **Small core, earned surface.** A new module or surface needs an owner,
    conformance tests, docs and a removal path, and, if it carries outside
    text, a threat-model entry before it ships.

## 8. Success metrics, and how to learn them privately

| Area | Metric | First target (proposal) |
|---|---|---|
| Activation | Share of new installs with a first verified cross-harness delivery within 24 hours; median time to it | under 5 minutes on the README path |
| Engagement | Weekly verified agent pairs per active install | grows month over month |
| Cross-harness | Share of verified pairs whose ends run different harnesses | most pairs |
| Cross-machine | Weekly verified pairs over P2P | tracked, not a launch goal |
| Retention | Installs with a verified pair in week 4 after activation | owner sets after a month of data |
| Honesty (guardrail) | False "delivered"; deliveries into a busy turn or a draft | zero; each one blocks a release |
| Quality | Unverified share per harness | falls as adapters improve |
| Community | Outside issues, pull requests, adapters and structures; time to first reply | first reply within 2 days |

How to learn without watching users:

1. **Today:** by default bp sends nothing but the daily update check, which
   `updateCheck: false` or `BP_NO_UPDATE_CHECK=1` turns off. P2P and the
   modules you enable add their own traffic.
2. *Planned (PRD-05):* the update check sends `bp/<version> (<os>/<arch>)` as
   its user agent, with no identifier; server logs give a weekly histogram of
   versions and platforms without storing IP addresses.
3. *Planned:* `bp stats` computes the metrics above from the local
   `messages.jsonl` and `audit.jsonl` and shows them to the user.
4. *Planned:* `bp stats --share` prints the same counts as a short JSON the user
   reads and pastes into a GitHub Discussion. Counts only: no agent names,
   paths, peers or text. An automatic report could follow, off by default.
5. Qualitative: a "first message" Discussion at launch, the five-person
   30-second test (ONB-08), issue templates that ask for harness and version.

## 9. Sustainability options (for the owner, not decisions)

| Option | What it is | Fits the principles if | Risk |
|---|---|---|---|
| Open core, sponsors | CLI and daemon stay open; GitHub Sponsors or similar | always | small, unpredictable income |
| Hosted relay | Managed rendezvous and relay so P2P works without a server; free for individuals, paid for teams | optional, self-hosting stays first-class | abuse, cost, legal exposure; needs SEC-15/16/17 and an abuse plan |
| Hosted gateway tunnel | Phone access through Claude.ai or ChatGPT connectors without the user's own TLS proxy | pairing stays local, payloads are not stored | the riskiest surface; only after W7 |
| Teams | Central expose policy, shared audit, single sign-on; self-hosted or hosted | a single user never needs it | scope creep (PRD-02) |
| Support | Help for companies that run agent fleets | always | time taken from the core |
| Commercial license | Dual licensing next to GPL | decided before outside contributions | needs a CLA, which conflicts with a DCO (PRD-07) |

## 10. Open decisions for the owner

**10.1 Name.** An internal naming study (2026-10-09, not published)
recommends Lullwire (`lw`): it tells the product's story ("waits for the
lull") and every registry and domain checked was free. Its caveats are real:
"lull" can read as drowsy or as "lulled into a false sense of security", and
it is crude slang in Dutch. "Blueprint" is hard to search (PRD-06), and 2026
names crowd the space. Decide **before** launch: a launch spends its
attention, links and search ranking on one name, and Product Hunt allows a
relaunch only after six months or a significant update [13]. Run the study's
ten-minute test with native speakers and a trademark search first. If the name
stays, keep "Blueprint" as the long form and use "blueprints" for shareable
team structures.

**10.2 P2P packaging.** direction.md puts the P2P network in core; the review
suggests a build tag and two binaries (PRD-02, DEP-10). Measured on dev
`9314eaa`: `./cmd/bp` pulls 330 third-party packages; outside the P2P code bp
imports only two (`go.yaml.in/yaml/v3`, `golang.org/x/sys/unix`); the build is
about 41 MB. Proposed reconciliation: **P2P stays a core capability; libp2p
ships as an on-demand, separately signed network component.** Core keeps
identity, `agent@peer` addressing, expose lists, channel semantics, provenance
and protocol rules. The network worker already runs as its own process behind
a control socket, so it can become a `bp-net` binary that `bp p2p start`
downloads, verifies with the same Ed25519 key and runs. One install, P2P one
step away, and most users never carry the libp2p tree. Prerequisite: move the
P2P config types out of `internal/p2p` (ARC-12). Wire protocol IDs stay.

**10.3 The accounts module.** direction.md lists `accounts` (several Claude
accounts, usage limits, staggered keepalive) as a module, and also says bp
"does not use any provider's credentials outside that provider's own client".
On dev the module refreshes tokens with Claude Code's client id, stores
subscription tokens, reads the usage endpoint and schedules keepalive prompts.
That contradicts the non-goal and Anthropic's policy that third-party
developers "may not collect, store, or intermediate Claude.ai credentials or
session tokens" [5] (PRD-01). Options: (a) remove it; (b) keep only
`profiles`, where each profile is a separate Claude config directory signed in
through Claude Code's own login and bp never reads a token (recommended);
(c) keep it (not recommended: users risk their paid accounts and bp risks its
standing with the vendors it integrates with). In every case it is not
advertised, and keepalive stays out of the launch release.

**10.4 Website identity.** Today: `bp.tunapro.xyz`, the `tunapro1234` GitHub
account and the `@tunapro` npm scope (PRD-06). Proposal: move the repository to
a GitHub organization before launch (trust and bus factor, PRD-04), choose the
product domain with the name, and keep `bp.tunapro.xyz` serving releases
indefinitely because shipped binaries fetch updates from it.

**10.5 Launch version: 2.0.0.** Default install behavior changed, new public
surfaces appeared (HTTP/A2A, MCP, rooms, board, guard), dev is more than a
hundred commits ahead of stable, and the review plans breaking removals for 2.0
(fed sunset, ARC-15). Ship 2.0.0 only when the launch blockers are closed, with
a release candidate a week earlier for testers.

**10.6 License (added).** GPL-3.0-only suits the CLI. Protocol schemas, the
agent hint and future client libraries may need a permissive license so other
tools can embed them (PRD-07). Decide before outside contributions; add a DCO.

**10.7 Learning (added).** Approve or reject the plan in section 8.

## 11. Proposed amendments to direction.md

Listed for the owner; this page does not edit direction.md.

- **A1.** Add principles 7 to 11 (section 7).
- **A2.** Principle 1: name what install adds outside bp's own directory, the
  agent hint files for Claude Code and Codex, listed by the installer and
  removed by `bp uninstall`. Nothing else.
- **A3.** Replace `accounts` with official-sign-in `profiles`, or remove it (10.3).
- **A4.** Mark `wa` and `monitor` as the owner's overlay, out of the public
  module list; define `ui` as the new read-mostly local view.
- **A5.** P2P stays core as a capability; its transport ships as a signed
  on-demand component (10.2).
- **A6.** Identity: a session outside a bp terminal (MCP, HTTP) is never labeled
  as the human owner; it is an unverified agent label unless proven.
- **A7.** Tiers: Core, Supported and Labs in the module table and the docs;
  conformance tests are the bar for Supported (PRD-02).
- **A8.** Delivery: name native paths (Codex app-server queue, Claude Code's
  messaging socket, OpenCode server, Hermes A2A) as push paths, held to the
  same receipts and framing (3.3).
- **A9.** Add a "Measurement" section: no telemetry by default; section 8.
- **A10.** Later: publishing the gateway or any public relay waits for W7 and an
  abuse plan; add the `fed` sunset (ARC-15).
- **A11.** Platforms: macOS and Linux; WSL2 once verified; native Windows Later.
- **A12.** Website: a comparison page and a "what changes on your machine" box
  next to the install command.

## Appendix A. Landscape, October 2026

| Tool | Cross-vendor | Waits for turn end | Draft safety | Receipts | Untrusted framing | Across machines |
|---|---|---|---|---|---|---|
| Claude Code cross-session messaging [1] | no, Claude only | between tool calls | not documented | delivered, held, refused | "never counts as your consent" | via Anthropic servers [4] |
| Codex queue [6] | no, Codex only | yes, after a turn | not documented | not documented | not documented | `--remote` |
| Agent Relay [10] | yes | optional mode | not documented | accepted, delivered, deferred, failed | not documented | its cloud or self-hosted server |
| agmsg [10] | yes (9 harnesses) | push or between turns | not documented | none in README | not documented | self-hosted server |
| Concord MCP [10] | yes | steers a busy turn | not documented | says what it cannot confirm | not documented | local only |
| CCB [10] | yes | yes | holds a draft up to 180 s, then clears it | not documented | not documented | local |
| MCP Agent Mail [10] | yes | pull only | n/a | optional acknowledgements | not documented | local server |
| bp (on dev) | yes | yes | waits while a draft is present | accepted, delivered, unverified, failed; the same for every harness | outside text framed (`guard.Frame`) | P2P with your own relay |

Other adjacent tools: Orca, Gas Town, claude-squad, ccmanager, agent-deck,
cmux, Repowire, agent-talk [10][12]. Of the names the naming study found,
keryx.sh (relay, receipts, waitlist only) is the closest idea; kelpie.sh,
stillroom.sh, mola.sh, Angy, skep, tracon and Tayfa are adjacent or unrelated
[10]. Rows reflect each project's public docs on 2026-10-09; "not documented"
means we did not find it, not that it is absent.

## Appendix B. Sources

All accessed 2026-10-09.

1. Claude Code: https://code.claude.com/docs/en/agents , https://code.claude.com/docs/en/cross-session-messaging ,
   https://code.claude.com/docs/en/agent-teams , https://code.claude.com/docs/en/channels
2. Claude Code hooks: https://code.claude.com/docs/en/hooks
3. Claude Code changelog: https://github.com/anthropics/claude-code/blob/main/CHANGELOG.md
4. Claude Code Remote Control: https://code.claude.com/docs/en/remote-control
5. Claude Code legal and compliance: https://code.claude.com/docs/en/legal-and-compliance
6. Codex: https://learn.chatgpt.com/docs/app-server , https://github.com/openai/codex/releases/tag/rust-v0.149.0 ,
   https://github.com/openai/codex/pull/38456 , https://github.com/openai/codex/pull/39092
7. A2A: https://github.com/a2aproject/A2A/releases , https://aaif.io/blog/a2a-joins-aaif ,
   https://www.linuxfoundation.org/press/a2a-protocol-surpasses-150-organizations-lands-in-major-cloud-platforms-and-sees-enterprise-production-use-in-first-year
8. MCP: https://modelcontextprotocol.io/specification/2026-07-28/changelog ,
   https://www.anthropic.com/news/donating-the-model-context-protocol-and-establishing-of-the-agentic-ai-foundation
9. ACP, Gemini CLI, Hermes, OpenCode: https://agentclientprotocol.com/overview/agents , https://geminicli.com/docs/core/remote-agents ,
   https://hermes-agent.nousresearch.com/docs/developer-guide/programmatic-integration , https://opencode.ai/docs/server/
10. Messengers: https://github.com/AgentWorkforce/relay , https://agentrelay.com/docs/markdown/delivery.md , https://github.com/fujibee/agmsg ,
    https://github.com/Get-Concord-AI/concord-mcp , https://github.com/SeemSeam/claude_codex_bridge , https://github.com/Dicklesworthstone/mcp_agent_mail ,
    https://github.com/prassanna-ravishankar/repowire , https://github.com/xhluca/agent-talk , https://keryx.sh , https://kelpie.sh
11. Launches: https://www.producthunt.com/products/agmsg , https://news.ycombinator.com/item?id=48313306 ,
    https://news.ycombinator.com/item?id=47934957 , https://news.ycombinator.com/item?id=49464704
12. Fleet tools: https://github.com/stablyai/orca , https://github.com/gastownhall/gastown ,
    https://github.com/smtg-ai/claude-squad , https://github.com/kbwo/ccmanager
13. Product Hunt relaunch rule: http://help.producthunt.com/en/articles/484934-can-i-relaunch-my-product
