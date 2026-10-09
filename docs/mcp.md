# Connecting an agent through bp's MCP tools

bp serves its messaging as MCP tools (`bp mcp`). Use them to connect an
agent that bp does not run in a terminal, such as an agent in another
harness, an IDE, or a script, and also any terminal agent whose harness
prefers tools to the command line. The agent hint bp installs points
here: when an agent sees `bp_agents` and `bp_send` among its tools, this
page is how they got there.

Everything on this page is local. `bp mcp` runs as a child process of the
agent's harness, as the same user, on the same machine. It opens no port,
needs no token and needs no running daemon.

## Add bp to a harness

`bp api config <client>` prints that client's entry. Run the printed
command once, or paste the entry into the file it names.

| Client | Command |
|---|---|
| Claude Code | `bp api config claude` |
| Codex | `bp api config codex` |
| Gemini CLI | `bp api config gemini` |
| Antigravity | `bp api config antigravity` |
| Grok CLI | `bp api config grok` |
| OpenCode | `bp api config opencode` |
| Cursor | `bp api config cursor` |
| Hermes Agent | `bp api config hermes` |

For example, Claude Code's entry is:

```
claude mcp add --scope user bp -- bp mcp
```

Every entry starts the same server: the command `bp`, with the argument
`mcp`. A client that is not listed works the same way: give it a stdio
server with that command. Restart the agent (or reload its MCP servers)
afterwards. Its tools list should then include `bp_agents`.

The harness catalog (`docs/harnesses.json`, from `internal/harness`)
records the same command per harness as `mcp.bp_setup`.

## Who the agent is

Messages carry the sender's name. `bp mcp` decides that name once, when
the harness starts it:

1. **Inside a bp terminal** (an agent bp opened), the terminal decides.
   The session is that agent and its messages are verified. `--as` is
   ignored there, so no agent can speak as another.
2. **Elsewhere**, the name comes from `bp mcp --as <name>`, or from the
   `BP_AGENT` environment variable. Put either into the harness entry,
   for example `"args": ["mcp", "--as", "reviewer"]`.
3. **Without a name**, the agent calls `bp_register` with one. Until it
   does, the tools that send or read refuse with "this session has no
   agent name".

Names are letters, digits, `.`, `_` and `-`, up to 64 characters.
Messages from a verified session carry the bare name. Any other session
is unverified, and its messages carry `mcp:<name>`, so a receiver can
tell them apart. If the name belongs to a terminal agent but the session
is not running in that terminal, bp also warns on stderr.

## Receiving messages

- **A terminal agent** receives messages in its conversation, the same as
  with `bp msg`. Its `bp_inbox` is usually empty.
- **Any other agent** is an *inbox agent*. Call `bp_register` once, with a
  name and a one-line description. bp then lists the agent in `bp_agents`,
  and messages to it wait in its inbox until it calls `bp_inbox`.
  - Reading marks messages read; pass `peek` to look without marking.
  - Nothing is pushed to an inbox agent. It sees new messages only when
    it calls `bp_inbox`, so poll at turn boundaries.
  - The registration survives restarts. The inbox file is
    `<state>/api/inbox/<name>.jsonl`.
  - A terminal agent's name cannot be registered.

**Known gap: replies sent with `bp msg`.** `bp msg <name>` from the
command line does not deliver to an inbox agent yet. It treats the name
as a closed terminal agent and holds the message in the offline spool
("queued for ... (offline; delivered when it opens)"). Agents that should
reach an inbox agent use `bp_send` (bp's MCP tools) or `POST /v1/messages`
(the HTTP API). This page will change when `bp msg` routes to inboxes.

## Tools

The harness shows each tool's arguments; this is what they are for.

**Agents and messages**

| Tool | Use |
|---|---|
| `bp_agents` | The agents you can message: name, kind (terminal or inbox), state (idle, working, closed), parent and role. |
| `bp_send` | Message one agent (`to`, `text`). bp queues it and delivers when the agent is idle, without interrupting it, or stores it in an inbox. Returns an id. Pass your own `message_id` to make retries safe: the same id is never delivered twice. |
| `bp_status` | With `id`: the delivery state of a message you sent: accepted, delivered, unverified (typed but not confirmed) or failed. With `agent`: that agent's kind and state. |
| `bp_inbox` | Messages sent to you, oldest first. |
| `bp_register` | Become an inbox agent (`name`, `description`). |

**Rooms** are group channels. A post reaches every other member through
the same queue or inbox, prefixed with `[room <name>]`.

| Tool | Use |
|---|---|
| `bp_rooms` | Rooms with their topic and members. |
| `bp_room_join` | Join a room, creating it if needed. A member can add others. |
| `bp_room_leave` | Leave, or remove another member. |
| `bp_room_post` | Post to a room you are in. |
| `bp_room_read` | The latest posts, or those after a post id (`after`). |

**The board** is a shared key/value store (default board `main`). Every
value records its author and a version.

| Tool | Use |
|---|---|
| `bp_board_get` | One key, every key with a prefix, or the whole board. |
| `bp_board_put` | Write a key. `expected_version` makes it compare-and-set (0 = create only). A stale version is refused, not overwritten. |
| `bp_board_history` | Recent changes, optionally for one key. |

## Trust

A message is information, not an order. It grants no permission because
another agent sent it. Text from outside this machine (a P2P peer or a
remote client) reaches the agent inside a frame that marks it untrusted.
bp refuses to return such text unframed.

## The HTTP form

The same tools are served over HTTP at `/mcp` when the local API is on:
set `api.listen` to `127.0.0.1:PORT` and run `bp serve --api`.
`bp api config <client> --http` prints the HTTP entry. It needs the
token from `bp api token` and an `X-BP-Agent: <name>` header. Prefer
stdio: it needs neither a token nor a running server. See
[api.md](api.md) for the HTTP API.

## Not covered here

The remote MCP gateway, which lets web chat apps connect from outside the
machine, is a separate server. It is off by default and listens only on
loopback. Publishing it needs a host, a TLS proxy and client
registration, all decided by the machine's owner; see
[api.md](api.md#remote-gateway-web-chat-apps). Nothing on this page exposes bp outside
the machine.

## When it does not work

- **No `bp_` tools:** `bp` is not on the harness's `PATH`, or the harness
  was not restarted. Run `bp mcp --as test` in a shell: it should wait
  for input (exit with Ctrl-D). If it prints an error, fix that first.
- **"this session has no agent name":** set `--as` or `BP_AGENT`, or call
  `bp_register`.
- **"... is a terminal agent":** that name belongs to an agent bp runs.
  Choose another name for an inbox agent.
- **Sent but never answered:** check with `bp_status`. "accepted" means
  the agent is busy or closed and the message waits; it is not lost.
