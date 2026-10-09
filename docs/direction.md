# bp direction

Status: agreed with the owner in October 2026. This page is the reference for
everyone working on bp: core, website, local UI and adoption.

## What bp is

**bp is communication and hierarchy for AI agents.** It makes agents work
together across harnesses, and makes that work more efficient: Claude Code,
Codex, Hermes, OpenCode, a chat bot, or a script that can only send HTTP
requests.

One sentence for people: *bp lets your agents find each other, message each
other and work as a team, whatever they run in and wherever they run.*

## Principles

1. **Installing bp changes nothing.** One command installs it, and nothing the
   user is used to behaves differently afterwards. bp does not open a
   coordinator agent, does not require tmux, does not keep agents alive after
   their terminal closes, does not switch accounts and does not restyle
   anything. Every such behavior is an opt-in module. Everything bp adds is
   additive and `bp uninstall` removes it.
2. **Agents learn about bp; users don't have to.** After installing, bp tells
   the user's agents that it exists: "you have the bp tool; you can see and
   message the other agents on this computer; here is how; here is where to
   learn more". It does this through the least invasive channel each harness
   offers: an MCP server, a skill, or a one-line hint. Agents then start talking
   on their own.
3. **Open to every agent.** The core is reachable without any SDK:
   - the `bp` CLI;
   - an MCP server;
   - plain HTTP on localhost: POST to send a message, GET to read the inbox.

   Message bodies follow the open A2A shape so other A2A agents (Hermes and
   others) can join without adapters. If an agent can make an HTTP request, it
   can use bp.
4. **Light and fast.** Small binary, fast commands, no background work that
   the user did not ask for. Status reads cached state; they never rescan
   everything.
5. **Structures are examples, not rules.** Once agents can talk, social
   structure appears: who assigns work, who may talk to whom, who reports to
   whom. bp ships ready-made examples (flat team, lead and workers, a deep
   hierarchy like ours), but the user decides. No structure is mandatory.
6. **Humans can always see and steer.** Every exchange is visible to the owner.
   bp provides:
   - per-peer expose lists;
   - an audit log;
   - loop caps, so two agents cannot answer each other forever;
   - framing of messages from outside as untrusted input.

   This is not an optional module.

## Two kinds of users

| | Communicate only | Full setup |
|---|---|---|
| Install | one command, or ask your agent to install bp | the same, then the agent asks what to enable |
| What changes | nothing; agents get the bp tool | the modules they choose |
| Typical use | let my Claude Code and my Codex talk; talk to a friend's agent | run many agents, hierarchy, phone access |

The main way to install is **to ask an agent**: "install bp from
https://bp.tunapro.xyz". The agent installs it, confirms that it works, and
then asks the user how far to go: stop here, connect to a friend, or set up
modules on this machine or server.

## Core and modules

**Core (always on, changes nothing):**
- identity and addressing (`agent@peer`);
- delivery: a queue that never interrupts a busy agent, plus delivery
  confirmation;
- the P2P network with relay, so no open ports or public IP are needed;
- expose lists;
- hierarchy data;
- the protocol and its safety rules;
- harness adapters: CLI, MCP and HTTP.

**Modules (off until enabled with `bp enable <module>`, removed with
`bp disable <module>`):**

| Module | What it does | Must not conflict with |
|---|---|---|
| `sessions` | open, close and resume agents in tmux; agents survive a closed terminal | the user's own tmux config and habits |
| `bar` | tmux status bar with live agent state | an existing status line: offer `#(bp bar)` instead of replacing it |
| `accounts` | several Claude accounts, usage limits, staggered keepalive | other tools that manage the Claude credentials: detect them and refuse |
| `wa` | WhatsApp bridge | another WhatsApp session on the same number |
| `ui` | local web interface | nothing; it is read-mostly |

## Surfaces

- **Website:** explains bp in one sentence. The install path is "send this to
  your agent". It has documentation written for agents as well as people
  (`llms.txt`, plain HTML, copyable examples). Visual identity: white with the
  #2b5cd9 blue.
- **Local UI (module):** shows the agents on this machine and on peers, the
  message graph, the hierarchy, the audit log and module settings. It is
  optional and works without tmux.

## Later

- Move the public relay from the owner's server to AWS once the core is done.
- Study agent interaction with graph theory and economic tools. The message
  graph and the audit log are the dataset.

## Not a goal

bp is not a harness and does not compete with harnesses. It does not run model
calls itself and does not use any provider's credentials outside that
provider's own client.
