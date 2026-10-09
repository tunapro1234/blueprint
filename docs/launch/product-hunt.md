# Product Hunt launch kit

Status: draft for the owner, 2026-10-09. Nothing here has been posted,
scheduled or registered. It goes with [../vision.md](../vision.md). Numbers in
brackets point to the sources at the end.

Ground rules for everything below:

- Every command, flag and output shown must exist in the release people will
  install. Capture outputs from that release; never retype them.
- No invented numbers, quotes, logos, star counts or user counts.
- Lead with mixed-vendor teams and the delivery guarantees (never interrupts,
  never types over your draft, honest receipts), not with "agents can message
  each other": Show HN posts of the "let X and Y talk" kind drew 2 to 14 points,
  while "run agents in parallel" tools drew 96 to 228 [H6]. P2P is "and it
  works across machines too", never the headline.
- The makers write every post, comment and reply in their own words. HN bans
  generated text outright [H1][H2]; this kit gives facts and talking points.
- Do not mention the `accounts`, `wa` or `ui` modules, the owner's servers or
  unmerged work (built-in terminal, local web UI) except as labeled plans.
- The name is not final (vision 10.1). Copy uses `bp` / Blueprint; replace both
  if the name changes. Taglines avoid the name on purpose. Decide the name
  before launch: Product Hunt allows a relaunch only after six months or a
  significant update [P12].

**Launch gate and date.** Launch only when every item in section 7 is closed
in the release the listing installs; the team's triage counted about 47
launch blockers on 2026-10-09 (30 small, 17 medium). Proposed: **Tuesday 8 or
Wednesday 9 December 2026, 00:01 PST (11:01 in Istanbul)**, with **Saturday 12
December** as the alternative: a Saturday top 5 needed a median of 165 points
in 2026, against 210 on a Tuesday [P10]. The weeks before are crowded: GitHub
Universe and Turkey's Republic Day holiday (28 and 29 October), US Election Day
(3 November), KubeCon North America (9 to 12 November), Microsoft Ignite (17 to
20 November), Thanksgiving week with Black Friday (23 to 27 November) and AWS
re:Invent (30 November to 4 December) [C1]. Show HN goes on a different day
from Product Hunt. If section 7 slips past the go/no-go, Tuesday 12 January
2027.

## 1. Listing copy

Field rules [P1][P2]: name only (no description or emoji in it), tagline up to
60 characters, up to 3 launch tags, thumbnail 240x240 under 3 MB, at least two
gallery images (1270x760 recommended), video as a YouTube link only. Product
Hunt's own pages disagree on the description limit (260 or 500 characters);
stay within 260.

**Name:** bp (long form Blueprint), pending the naming decision.

**Tagline options** (character count in brackets):

1. Claude Code, Codex & co. as one team. Never interrupted. [56]
2. Open-source messaging between Claude Code, Codex & co. [54]
3. Messaging between AI agents that waits for the lull [51]
4. Agent-to-agent messages with honest delivery receipts [53]
5. Your coding agents, any vendor, one team. Safely. [49]

**Recommended: option 1.** It names the pull (agents from different vendors
working as one team) and the guarantee no vendor's own messaging gives (it
waits for the turn to end). Option 3 carries the story and belongs in the
first comment and the video. Harness names describe compatibility; use no
vendor logos.

**Description** (250 characters):

> bp is a small open-source CLI that lets your AI agents message each other:
> Claude Code, Codex, Hermes, OpenCode or any script. It waits for the quiet
> moment between turns, never types over your draft, and tells you honestly
> whether a message arrived.

**Launch tags:** Developer Tools, Artificial Intelligence, Open Source.

**Links:** website (`https://bp.tunapro.xyz` until the identity decision), the
GitHub repository. **Pricing:** free, open source (GPL-3.0-only).

**Thumbnail:** the bp mark on white with the `#2b5cd9` blue. If animated, the
first frame must work alone: a GIF animates only on hover [P2].

**Requirements, stated wherever the install line appears:** macOS or Linux
(amd64 or arm64); tmux, which at launch is required for everything that lists
or reaches agents, including MCP and HTTP, unless the missing-tmux fix in
section 7 lands first; and at least one signed-in agent CLI.

## 2. Maker's first comment: talking points

Post it the minute the listing goes live; Product Hunt reports that 70% of
Product of the Day, Week or Month winners had one [P2]. The makers write it
themselves, in their own words, and keep only what is true for them. These are
the points to cover, in order:

- **Who and why.** Who you are; that you built bp to run your own Claude Code
  and Codex agents together, and were tired of being the clipboard between
  them, pasting into running turns or on top of a half-typed prompt.
- **What it is, in one line.** A small open-source CLI that lets agents in
  different harnesses message each other. It is not a harness and never calls
  a model.
- **Why not the vendors' own messaging.** Claude Code and Codex now message
  their own sessions, which is great inside one vendor; bp is for mixed teams.
- **The guarantees, one fact each.** It waits for the lull: a busy agent gets
  the message when its turn ends, and bp waits while a draft sits in the
  prompt. Honest receipts: every message has a channel id, and `bp qstat` says
  DELIVERED only with evidence from the agent's transcript or hooks;
  otherwise it says unconfirmed and never pastes twice. One model across
  vendors: one hierarchy, one expose policy, one audit log.
- **Any agent, no SDK.** The CLI, an MCP server (`bp mcp`), a local HTTP API
  with A2A-shaped bodies; rooms and a shared board for group work.
- **Safe by default.** The installer adds the binary, bp's own config and a
  short note that tells your agents bp exists, and lists what it wrote.
  Nothing listens. Outside text reaches an agent framed as untrusted data.
  Every decision is in the audit log. `bp uninstall` removes what bp added.
- **And across machines too:** P2P with your own relay.
- **Honest limits.** macOS and Linux only; tmux required at launch (see the
  requirements line); Hermes and OpenCode deliveries reported as unverified
  because bp cannot prove them yet.
- **How to try.** Ask your agent "install bp from https://bp.tunapro.xyz", or
  run `curl -fsSL https://bp.tunapro.xyz/install.sh | sh`.
- **One or two real questions.** For example: which two agents do you want to
  connect first, and what should a receipt tell you?

## 3. Gallery (6 images, 1270x760)

White background, `#2b5cd9` accents, real terminal captures from the release
build in a clean demo repository (no personal paths, account names or emails).
Under 12 words of text per image. The video sits first in the gallery.

| # | Image | Caption |
|---|---|---|
| 1 | **Hero.** Two terminals side by side, `alice` (Codex) and `bob` (Claude Code). One message travels between them, labeled "waits for the lull". Tagline on top. | Claude Code and Codex, working as one team. |
| 2 | **The lull.** The timeline from `docs/assets/delivery-flow.svg`: alice sends, bob is working, someone types in bob's prompt, bob goes idle, the message arrives as bob's next prompt. | A busy agent is never interrupted. Your draft comes first. |
| 3 | **Receipts.** The `bp qstat` states captured from the release, one line of meaning each: PENDING, DELIVERED, UNCONFIRMED, NOT DELIVERED. | DELIVERED means evidence. If bp can't prove it, it says so. |
| 4 | **Any agent.** Three doors: CLI (`bp msg bob '...'`), MCP (`claude mcp add --scope user bp -- bp mcp`), HTTP (`POST /v1/messages`). Harness names as plain text. | CLI, MCP or HTTP. If it can run a command, it can use bp. |
| 5 | **Teams.** One `bp room post` fanning out to the members' queues; a board key with its version and author. | Rooms and a shared, versioned board for group work. |
| 6 | **Safe by default.** A message from a peer as the receiving agent sees it: the `<<<bp-untrusted ...>>>` frame, its notice, the `guard flags: credential-request` line, and a body that asks for `~/.ssh/id_ed25519`. Two `bp audit` lines. Footer: "...and across machines too: P2P with your own relay." | Outside text arrives as untrusted data. Every decision is audited. |

Image 6 shows a defense, not a recipe: a generic request for a key file, never
a working bypass. Render it from the release (`internal/guard` produces it).

## 4. Demo video (45 to 60 seconds)

Record the release build on a clean machine with real Claude Code and Codex
sessions. Burn in captions so it works muted; 1080p, 16:9; label any
sped-up segment "2x". YouTube only [P2].

| Time | Shot | On screen | Caption |
|---|---|---|---|
| 0:00-0:04 | Cold open | Codex left, Claude Code right | "Two agents. You're the clipboard." |
| 0:04-0:12 | Install | In Claude Code: "install bp from https://bp.tunapro.xyz". The agent runs the installer; the signature line and the installer's list of the files it wrote are visible. | "Ask your agent. Signed install; everything else is opt-in." |
| 0:12-0:18 | Start | `bp run --name alice codex` (left), `bp run --name bob claude` (right) | "Start each agent with bp run." |
| 0:18-0:32 | The lull | Ask alice: "Use bp to ask bob to review the diff in src/." alice runs `bp msg bob ...`; bob is mid-turn; the message waits; bob finishes; it appears as bob's next prompt, starting with `[alice]`. | "bob is busy, so the message waits for the lull." |
| 0:32-0:38 | Receipt | `bp qstat <channel>` shows DELIVERED | "Delivered means evidence." |
| 0:38-0:44 | Your draft first | The human types half a sentence in bob's prompt; a second message waits; the human sends; then the message arrives. | "bp waits for your draft, too." |
| 0:44-0:52 | Team | alice: `bp room join review --with bob`, then `bp room post review '...'`; bob receives `[room review] ...` | "Rooms and a shared board for group work." |
| 0:52-0:57 | Safe | `bp audit -n 3`, then `bp uninstall --dry-run` | "Nothing listens. Everything is audited. Uninstall removes it." |
| 0:57-1:00 | End card | Name, tagline, URL, "Open source. macOS and Linux." | none |

Notes: depending on timing, `bp msg` to a busy agent prints either
`QUEUED (channel: ...)` or `NOT DELIVERED: ... message queued (channel: ...)`.
The second reads like a failure; either its wording changes before launch
(section 7) or the shot cuts to `bp qstat`. Do not show the `accounts`, `wa`
or `ui` modules, or the installer line that lists them.

## 5. Launch-day timeline

Product Hunt days run 00:01 to 23:59 Pacific, and posts go live at 00:01 [P1].
In December Pacific time is UTC-8 and Istanbul UTC+3: **00:01 PT is 11:01
TRT**. A launch can be scheduled up to 30 days ahead; a scheduled launch is
private until it goes live, unless you share its link [P3].

| When (PT) | When (TRT) | What |
|---|---|---|
| T-30 days | | Assets frozen; private beta with 10 to 20 outside users who agreed to give blunt feedback |
| T-21 days | | Listing drafted and scheduled; the product's forum page on PH opened (it replaced "coming soon" pages; people can follow it to be notified [P4]) |
| T-14 days | | Build-in-public post with the GIF; technical post "How bp knows a message was delivered" |
| T-7 days | | **Go/no-go** on section 7; release candidate to beta users |
| T-2 days | | Final release signed and published on the canonical channel; clean-machine install tests; site, docs and video live |
| T-1 day | | First comment and answers to section 8 ready; reply rota set |
| 00:01 | 11:01 | Listing live; maker posts the first comment |
| 00:05-00:30 | 11:05-11:30 | Tell your own followers on X and LinkedIn, in GitHub Discussions and the README: "we launched, feedback welcome" |
| 00:30-06:00 | 11:30-17:00 | Europe awake: reply to every comment within 30 minutes; turn bugs into issues and answer with the link |
| 06:00-12:00 | 17:00-23:00 | US morning, peak traffic: both makers on; a short follow-up post with the 20-second GIF |
| 12:00-17:00 | 23:00-04:00 | One maker on watch; docs fixes only, no risky code |
| 17:00-23:59 | 04:00-10:59 | Low traffic; catch-up at 08:00 TRT (21:00 PT) |
| A different day, US morning | that day, evening | Show HN (section 9): Wednesday or Thursday after a Tuesday launch; the following Tuesday after a Saturday launch |

## 6. Pre-launch checklist

**Accounts and rules** [P1][P5][P8]

- [ ] Both makers post from personal Product Hunt accounts (company accounts
      cannot post) that are older than a week, with photo, headline and links.
- [ ] Self-hunt: most featured launches are self-hunted [P5]. Never pay a
      hunter.
- [ ] The Show HN poster has an HN account with real history; Show HN is
      restricted for new accounts in 2026 [H4].

**Assets**

- [ ] Gallery images 1 to 6 at 1270x760, captured from the release build.
- [ ] Video on YouTube, captions burned in, under 60 seconds.
- [ ] Thumbnail, GitHub social preview, site Open Graph image.
- [ ] A 20-second GIF of the lull for X, Reddit and the README (record it
      with `docs/demo/demo.tape`).

**Product and repository**

- [ ] Section 7 closed; the installer serves the launch release.
- [ ] Clean-machine runs on macOS (Apple silicon and Intel) and Linux (amd64,
      arm64): install, first verified delivery in both directions (Codex to
      Claude Code and Claude Code to Codex) within five minutes by stopwatch,
      `bp uninstall`.
- [ ] "Ask your agent to install bp" works from Claude Code and from Codex.
- [ ] README, site, `llms.txt` and the comparison page match the release.
- [ ] GitHub: description, topics, website link; Discussions on with "First
      message" and "Show your setup"; issue templates asking for version,
      platform, harness and `bp doctor --json`.
- [ ] SECURITY.md with private vulnerability reporting; CONTRIBUTING; a
      changelog entry for the launch release.

**Community warm-up, within the rules**

- Never ask for upvotes, never trade or incentivize votes, never mass-message
  people, never use bots. Product Hunt says asking for upvotes can push a
  product down the rankings [P6][P7].
- Allowed: ask for feedback, comments and opinions, and for help spreading the
  word [P8]. Tell people the date and that you would value their feedback.
- Post only where you already take part: "Launch day is not a time to go
  spamming Reddit and Indie Hackers if you haven't already been posting there"
  [P8]. Start taking part in the communities in section 9 now.
- Featuring is curated for products that are useful, novel, well crafted and
  creative; waitlists and vaporware are not featured [P9]. A working install is
  the best warm-up.

## 7. Launch blockers

Phrased as outcomes the release must show. IDs refer to internal review notes;
no item needs exploit details to verify.

**Top priority**

- [ ] On macOS, messages to Codex agents are delivered and confirmed. Today
      they stay queued: macOS `lsof` does not report flock locks, and Codex
      0.162 holds its writer lock in its app-server child process, so bp cannot
      read Codex's turn state. Check whether Linux fails the same way. Codex and
      Claude Code messaging each other on a Mac is the core demo: record it
      only after the fix.

**Build and release**

- [ ] dev builds and passes its tests on macOS and Linux, verified in CI with
      cross-compiles and native runs on both (dev `9314eaa` does not build on
      macOS; TST-01).
- [ ] Releases are signed outside any machine that runs agents, CI gates every
      release, and the Go toolchain is a supported one (SEC-01, DEP-01, SEC-13).
- [ ] One canonical download channel: site, README and GitHub Releases point at
      the same release (DEP-04); the signature check works on a stock Mac or
      names the one step needed (ONB-04).

**First run**

- [ ] A missing tmux, or no tmux server, is read as "no live terminal agents":
      `bp status`, the MCP tools and the HTTP API directory answer instead of
      failing (UX-07 class). Until this lands, tmux is a hard requirement and
      every page says so.
- [ ] Agents outside a bp terminal get honest, distinct identities and are
      never labeled as the human user (SEC-27 class).
- [ ] A fresh install shows no phantom coordinator (UX-08).
- [ ] Messages containing code can be sent without shell-quoting pitfalls, and
      the agent hint shows the safe form (SEC-18).
- [ ] The installer's closing lines and `bp modules` describe only modules the
      launch supports publicly.
- [ ] The installer and `bp setup` list exactly what they wrote, and never
      print "nothing else was changed" after writing the agent hint files
      (ONB-03).
- [ ] Status and delivery work in shells without a UTF-8 locale (cron,
      launchd, some ssh and agent shells).

**The promise**

- [ ] `bp msg` reaches MCP and HTTP inbox agents.
- [ ] A message to an unknown agent is refused, not quietly queued (FUN-01).
- [ ] DELIVERED is reported only with evidence the target received the whole
      message (FUN-03, FUN-04, FUN-08).
- [ ] A prompt bp does not recognize is never treated as empty, so a human
      draft is never typed over (FUN-09).
- [ ] An update reaches the processes that deliver (ARC-02).
- [ ] The busy-case result of `bp msg` reads as "waiting", not as a failure.

**Policy, privacy and trust**

- [ ] The release does not read, store or refresh Claude credentials (no
      slots, token refresh, usage endpoint or keepalive), and `accounts` is not
      advertised (PRD-01, vision 10.3). If the owner keeps the module, drop the
      "never reads, stores or shares your credentials" answer in section 8.
- [ ] No personal data of the owner in the public tree; owner-only jobs are off
      unless configured (SEC-12, ARC-01).
- [ ] The dashboard behind `ui` is not advertised (SEC-28).
- [ ] Private vulnerability reporting is on and SECURITY.md explains it
      (PRD-04).
- [ ] Every command, flag and output in the listing, README, site and video
      exists in the release; the site links source, license, security policy,
      changelog and uninstall (ONB-09).
- [ ] Every item on the internal launch-blocker list is closed or explicitly
      accepted by the owner, and CI is green on macOS and Linux.

## 8. Objection handling

Facts for replies, not text to paste: on HN every reply must be written by
hand [H2], and on Product Hunt replies read better in the makers' own words.
Answer briefly, link the doc, thank the person, never argue.

**"Why not just use Claude Code's agent teams or its built-in messaging?"**
If all your agents are Claude Code, they may be all you need. Agent teams
(still experimental) and cross-session messaging work between Claude sessions:
the docs say "the workers are Claude sessions"; messages arrive between tool
calls during a turn; cross-machine messages go through Anthropic's servers
[V1]. Codex's queue is likewise Codex-only. bp is for mixed teams: it waits
for the turn to end and for your draft, gives the same receipts for every
harness, and crosses machines peer to peer through a relay you run. A Claude
Code session can use bp through MCP next to its own messaging.

**"How is this different from Agent Relay, agmsg, CCB, Concord MCP or MCP
Agent Mail?"** They are good projects, and they show people want this. From
their public docs in October 2026 [V2]:

- *Agent Relay* has receipts, an on-idle mode and cross-machine reach through
  its cloud or a server you host. bp waits for the turn by default, says
  "unverified" when it cannot prove delivery, and goes P2P to your own relay.
- *agmsg* supports nine harnesses through a shared SQLite file; its README does
  not describe receipts, draft protection or untrusted-input handling.
- *CCB* also waits for the turn and protects a draft, for up to 180 seconds
  before clearing it; it is a workspace that owns the panes.
- *Concord MCP* is as candid as bp about what it cannot confirm, but it steers
  a busy turn and works on one machine.
- *MCP Agent Mail* gives agents inboxes they pull from, with optional
  acknowledgements; nothing is delivered into a live session.

If one of them fits you better, use it.

**"Isn't this a prompt-injection highway?"** It is the risk we design for.
Nothing listens by default. Text that crosses a trust boundary reaches an agent
inside a frame that names its source and says it is untrusted data, not
instructions. Peers reach only the agents their owner exposed. Everything is in
`bp audit`, and a peer whose agents keep answering each other is paused by a
loop cap (limits for loops between local agents are still in progress). The
limits are in the threat model: framing reduces the risk but cannot make a
model immune, and agents under one Unix user share a trust domain.

**"Does this break Anthropic's or OpenAI's terms?"** bp never calls a model,
never reads, stores or shares your credentials, and never pools usage. It types
into your own signed-in client at a turn boundary, the way you would, and uses
official integration points such as hooks and MCP where they exist. Your plan's
terms still apply to how much you automate.

**"I don't use tmux."** You don't need to learn it: `bp run` opens each agent in
its own tmux session and passes your arguments through. At launch tmux must be
installed, because bp uses it to find and reach agents, including over MCP and
HTTP. If the fix that reads a missing tmux as "no terminal agents" lands first,
MCP and HTTP inbox agents work without it. A built-in terminal layer that
removes the requirement for terminal delivery is in development, not released.

**"Windows?"** Not yet: macOS and Linux. WSL2 may work but nobody has verified
it, so we don't claim it. Native Windows is on the later list.

**"What do you collect?"** No telemetry. bp makes one update check a day, which
`updateCheck: false` or `BP_NO_UPDATE_CHECK=1` turns off. Messages and logs
stay on your machine. P2P traffic is encrypted between your peers; a relay sees
only connection metadata.

**"Another agent orchestrator?"** No. bp does not run models or decide what
agents do. It delivers messages and shows you what happened.

**"MCP and A2A already exist."** bp speaks both. They define how to call a tool
or an agent service; bp handles what they leave open: getting a message into a
live, interactive session at the right moment, and proving it arrived.

**"40 MB for a CLI?"** Most of it is the P2P stack. We are looking at shipping
it as an on-demand component so the default install gets much smaller.

## 9. Cross-posting plan

One post per community, written for that community by a maker, spread over
the launch week. Link the GitHub repository first where the community prefers
code.

**Show HN** (on a different day from the PH launch, US morning; section 5).
HN asks makers to write their text by hand, with no LLM generation or editing
at all [H1][H2], so this kit gives facts, not prose. Do not paste anything from
this file into HN.

- Title: starts with "Show HN:", says plainly what bp does, no marketing words
  [H1][H3]; HN's form allows 80 characters. Lead with the mixed-vendor team and
  the guarantees, not "let X and Y talk" [H6].
- Make it easy to try without a signup: the install line and a two-agent
  quickstart [H3].
- Facts worth covering in the first comment: the backstory (running Claude Code
  and Codex together); what bp checks before typing (between turns, no
  foreign text in the prompt, no open menu); the Claude Code hook path; what
  DELIVERED means and when bp says unconfirmed instead; MCP and the loopback
  HTTP API; rooms and the board; the limits (macOS and Linux, tmux required,
  Hermes and OpenCode unverified, one Unix user is one trust domain); links to
  the threat model and the delivery test matrix; one honest question for the
  thread.
- No friends asked to upvote or comment, no booster comments [H1][H3][H5].

**X thread** (launch morning, written by a maker): the GIF and the tagline; the
lull in one sentence; a `bp qstat` screenshot; CLI, MCP and HTTP; the safety
defaults with image 6; the PH link, the repository and "feedback welcome".

**Reddit.** Post only in communities where a maker already takes part, one per
day from launch day +1. Reddit blocked our fetcher, so the rules below come
from secondary sources: read each subreddit's rules on the day, use the
required flair, answer every comment [R1].

| Community | Angle | Rule notes to verify |
|---|---|---|
| r/ClaudeAI | Claude Code working with Codex through hooks and MCP | "Built with Claude" and "Promotion" flairs reported [R2] |
| r/ChatGPTCoding | Codex and Claude Code in one workflow | may route tools to a self-promotion thread [R3] |
| r/golang | the design: durable queue, transcript witness, libp2p | must be Go-related; small projects may belong in a weekly thread [R4] |
| r/coolgithubprojects | the GIF and one paragraph | GitHub-hosted projects; title format rule [R5] |
| r/SideProject | the story and the GIF | reported as promotion-friendly [R6] |
| r/commandline, r/opensource | a small CLI with honest exit codes; the license and how to contribute | rules not found; read first |

Skip r/LocalLLaMA: it is about local models [R7].

**Turkish developer communities** (launch week, in Turkish, by the
Turkish-speaking maker): the DonanımHaber "Yazılım Geliştirme" forum's
"Projeler" subforum [T1]; DEV posts tagged #turkish and #turkce [T2]; the
Kodluyoruz community channels [T3]; a link suggestion to the "Yazılımcılar İçin
Hafta Sonu Okumaları" newsletter, if it takes suggestions [T4]; the makers' own
X and LinkedIn. Talks: GDG İstanbul's DevFest on 19 December 2026 is a good
post-launch slot for "How bp knows a message was delivered"; GDG Ankara's
DevFest on 31 October comes before launch, so use it only if a lightning slot
is open [T5]. Tech news sites (Webrazzi, Webtekno) take news by email; pitch
only something new, such as the receipts design [T6]. r/CodingTR could not be
verified; check that it exists and read its rules first.

## 10. Post-launch week

| Day | Work |
|---|---|
| Launch day | Replies within 30 minutes; every bug becomes an issue with a link back |
| +1 | Triage issues; first fixes |
| +1 to +3 | Show HN on its own day (section 5) |
| +2 | Patch release with the top fixes; the changelog thanks reporters by handle (with permission) |
| +3 | Post "what we learned from launch feedback" with the issue list |
| +4 to +5 | Reddit and Turkish community posts, one per day |
| +7 | Retro against section 11; roadmap updated from what users asked for; every launch issue closed or labeled |

## 11. Success thresholds

Proposals for the owner; set them before launch and judge against them after.
People reaching the activation moment matter more than rankings.

| Signal | Good | Great | Reference points |
|---|---|---|---|
| People who report a first verified cross-harness delivery in launch week (Discussions, issues, comments) | 25 | 100 | first launch, no baseline |
| Outside contributors who open an issue or pull request in launch week | 10 | 30 | no baseline |
| Product Hunt on launch day | featured, top 10 | top 5 | a 2026 Tuesday top 5 needed a median of 210 points [P10]; agmsg was #5 with 239 upvotes [P11] |
| Show HN | front page | 100+ points | "let X and Y talk" posts drew 2 to 14 points; tools for running agents in parallel drew 96 to 228 [H6] |
| Launch-week installs of the release (download logs, no identifiers) | baseline from the beta | 2x the baseline | none yet |
| Guardrail: false DELIVERED or interrupted-turn reports | 0 | 0 | |

## Sources

All accessed 2026-10-09. Product Hunt, HN and Reddit rules change; re-read
them the week before launch.

- [P1] http://help.producthunt.com/en/articles/479557-how-to-post-a-product
- [P2] https://www.producthunt.com/launch/preparing-for-launch
- [P3] http://help.producthunt.com/en/articles/2724119-how-to-schedule-a-post ,
  http://help.producthunt.com/en/articles/15706445-how-to-share-a-scheduled-launch
- [P4] https://www.producthunt.com/p/producthunt/changelog-august-28-no-more-coming-soon-and-a-couple-other-shiny-updates
- [P5] https://www.producthunt.com/launch/before-launch
- [P6] https://help.producthunt.com/en/articles/3615694-community-guidelines
- [P7] http://help.producthunt.com/en/articles/484935-can-i-ask-my-community-friends-family-to-upvote-a-product
- [P8] https://www.producthunt.com/launch/sharing-your-launch
- [P9] http://help.producthunt.com/en/articles/9883485-product-hunt-featuring-guidelines
- [P10] https://www.producthunt.com/p/databox/best-day-to-launch-on-product-hunt-here-s-what-4-962-launches-in-2026-actually-show-2
- [P11] https://www.producthunt.com/products/agmsg
- [P12] http://help.producthunt.com/en/articles/484934-can-i-relaunch-my-product
- [H1] https://news.ycombinator.com/item?id=22336638 (Show HN tips, edited 2026-03-28)
- [H2] https://news.ycombinator.com/newsguidelines.html
- [H3] https://news.ycombinator.com/showhn.html
- [H4] https://news.ycombinator.com/showlim , https://news.ycombinator.com/item?id=49938039
- [H5] https://news.ycombinator.com/newsfaq.html
- [H6] https://news.ycombinator.com/item?id=44594584 , https://news.ycombinator.com/item?id=45427697 ,
  https://news.ycombinator.com/item?id=46368739 , https://news.ycombinator.com/item?id=45793684 ,
  https://news.ycombinator.com/item?id=47140322 , https://news.ycombinator.com/item?id=47934957 ,
  https://news.ycombinator.com/item?id=49464704
- [V1] https://code.claude.com/docs/en/agents , https://code.claude.com/docs/en/cross-session-messaging ,
  https://code.claude.com/docs/en/agent-teams
- [V2] https://agentrelay.com/docs/markdown/delivery.md , https://github.com/fujibee/agmsg ,
  https://github.com/SeemSeam/claude_codex_bridge , https://github.com/Get-Concord-AI/concord-mcp ,
  https://github.com/Dicklesworthstone/mcp_agent_mail
- [C1] https://www.opm.gov/policy-data-oversight/pay-leave/federal-holidays/ ,
  https://cncf.io/blog/2026/09/11/kubecon-cloudnativecon-north-america-2026-your-week-in-salt-lake-city ,
  https://www.moscone.com/events/microsoft-ignite-2026 , https://aws.amazon.com/events/reinvent ,
  https://newsone.com/6873482/2026-midterm-elections-what-voters-need-to-know/ , https://githubuniverse.com/ ,
  https://www.yeniankara.com.tr/guncel/29-ekim-2026-hangi-gune-denk-geliyor-hangi-kurumlar-kapali-olacak-134726
- [R1] https://redship.io/blog/reddit-self-promotion-rules-2026
- [R2] https://gummysearch.com/r/ClaudeAI/
- [R3] https://redlib.hackliberty.org/r/ChatGPTCoding/comments/1fjhnw1/selfpromotion_thread_8/
- [R4] https://redlib.hbubli.cc/r/golang/comments/u3p0mv/two_new_subreddit_rules/ , https://r.datuan.dev/r/golang
- [R5] https://axebps-redlib.hf.space/r/coolgithubprojects/top
- [R6] https://www.teract.ai/resources/reddit-subreddit-marketing-2026
- [R7] https://r.datuan.dev/r/LocalLLaMA/wiki/index
- [T1] https://forum.donanimhaber.com/yazilim-gelistirme--f202
- [T2] https://dev.to/t/turkish , https://dev.to/t/turkce
- [T3] https://www.kodluyoruz.org/
- [T4] https://mhkoca.substack.com/
- [T5] https://gdg.community.dev/gdg-istanbul/ , https://gdg.community.dev/gdg-ankara/
- [T6] https://webrazzi.com/girisim-formu/ , https://www.webtekno.com/hakkimizda
