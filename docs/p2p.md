# Peer-to-peer messaging

BP uses libp2p connections authenticated with persistent Ed25519 keys. Messages
use direct TCP/QUIC connections when reachable, or a circuit-relay-v2 connection
through your configured server. DCUtR can upgrade a relay connection to a direct
one. NAT traversal is not guaranteed; relay service remains necessary on some
networks. No public DHT, public bootstrap nodes, or third-party relay is used.

## Two machines

Run `bp p2p id` on each machine. This creates a private identity under
`state/p2p/identity.key` and prints only its public Peer ID. Keep the key with the
machine's BP data; changing it creates a different peer.

Add the following to each machine's existing `config.yaml`:

```yaml
p2p:
  enabled: true
  rendezvous:
    - /dns4/your-relay.example/tcp/443/wss/p2p/RELAY_PEER_ID
  peers:
    laptop:
      id: OTHER_MACHINE_PEER_ID
      expose: [main]
```

`laptop` is your local alias for the other machine. `expose` lists which **local**
agents that peer may message. An empty list allows none. On the other machine,
use this machine's Peer ID and its own alias/expose list. Peer authentication
does not authenticate an individual agent on that machine; incoming messages
visibly carry an `external:sender@peer` envelope and cannot use force-busy.

```sh
bp config check
bp p2p start
bp p2p status --json
bp p2p ping laptop
bp p2p lookup work
bp msg main@laptop 'Please review the change'
bp qstat pCHANNEL_ID --json
bp p2p channels
```

The network worker runs independently of individual agent panes. BP starts it
when sending or opening a wrapped local CLI with P2P enabled. For unattended
receiving, start it at login or run `bp p2p serve` under a service manager.
`bp p2p stop` stops only this worker. After changing P2P settings, stop/start it;
agent sessions and channel files remain intact. All machines must run a version
that supports the `/bp/*/1.0.0` protocols.
Lookup checks connected configured peers in parallel and returns only a canonical
agent name and whether it is live, closed or archived. An older peer without the
lookup protocol is skipped in text output and marked as an error in JSON.

For a reachable LAN peer, `addresses: [/ip4/192.168.1.20/tcp/PORT]` under its peer
entry avoids the rendezvous service. `mdns: true` discovers configured identities
on the LAN; discovering an unknown identity never grants permission.

## Your own rendezvous and relay

A server can run the same worker with `relay: true`, fixed `listen` addresses,
and public `advertise` addresses. For example, terminate WSS at your HTTPS proxy
and pass WebSocket upgrades to `/ip4/127.0.0.1/tcp/8788/ws`. Advertise
`/dns4/your-relay.example/tcp/443/wss`. Reserve the listener port locally first.
Clients use this advertised address plus `/p2p/` and the server's public Peer ID.

Relay reservations and traffic have bounded resources. The server's discovery
registry only accepts a peer's own authenticated address registration and expires
it after ten minutes. Clients refresh every minute, including after relay restart.
This small BP address registry uses `/bp/discovery/1.0.0`; it is **not** an
implementation of the separate libp2p rendezvous wire protocol. The transport,
relay and hole-punching protocols are standard libp2p.

If the rendezvous is down, existing direct connections can still carry messages.
Peers behind restrictive NATs may lose connectivity until their relay returns;
cached addresses do not guarantee reconnection. Unsent channels stay on disk.

## Channel guarantees and limits

A channel ID is created and persisted on the sender before attempting transport.
Retries reuse that ID. The receiver maps the authenticated Peer ID and channel ID
to one durable local queue record. Reusing an ID with different content is refused.
Messages to the same target are accepted in sender order; different targets/peers
can progress independently. Busy, unknown, modal and user-draft decisions belong
to the receiving machine's ordinary guarded BP queue.

- `outgoing`: persisted here; acceptance has not been confirmed.
- `accepted`: durably queued at the receiver, not necessarily seen by the agent.
- `delivered`: the receiver's queue reports delivery.
- `unverified`: input may have reached the agent but delivery is not proven.
- `failed`: the receiver reports a terminal unsuccessful queue outcome.

`bp qstat` includes the last observation time and connection error; an old receipt
is not a live reachability claim. A missing ACK or timeout is never delivery proof.
No transport can guarantee that an agent performed the requested action. Queue
receipts and channels are retained for replay protection; manually deleting them
removes that protection. P2P cancellation and remote shell/peek control are not
provided. Existing HTTP federation can still receive traffic during migration;
when P2P is enabled, `agent@peer` sends use its configured peer list.

## Moving an agent (`bp hop`)

`bp hop <agent> --to <peer>` moves a whole agent to another machine: its
conversation transcript and, by default, its working-directory files travel in a
single gzip-tar bundle streamed over the same authenticated connection the
messages use. It is a separate opt-in from messaging. The receiving machine
accepts a hop from a peer only when it sets `allowHop` for that peer, and caps
one bundle with `hopMaxBytes` (0 means the 2 GiB default):

```yaml
p2p:
  peers:
    laptop:
      id: OTHER_MACHINE_PEER_ID
      expose: [main]
      allowHop: true        # off by default; connectivity/expose never grant it
  hopMaxBytes: 2147483648   # optional; per-bundle size cap
```

The default is a **move**: the bundle is delivered for the peer owner's review,
then the source agent is stopped and archived with its files kept — never
deleted. Flags change the behaviour:

- `--copy` — leave the source agent running; deliver a copy.
- `--no-files` — carry only the conversation, not the working directory.
- `--delete-source` — after a confirmed move, delete the source files too
  (explicit opt-in; refuses obviously unsafe paths).
- `--raw` — include `.git/` and gitignored files (normally git's ignore rules
  and `.git` are skipped).
- `--dry-run` — print the bundle's file list and size without sending.

A busy agent is not hopped; wait for it to go idle so its transcript and files
are not captured mid-write.

An incoming bundle is **quarantined**, not applied. It lands under
`state/p2p/hop/incoming/<id>/` with a pending record; nothing is written into a
working directory, registered in the agentbook, or launched until the owner
reviews it:

```sh
bp hop list                     # what arrived, from which peer, how large
bp hop accept <id> [--dir <path>] [--yes]
bp hop reject <id>              # discard the staged bundle
```

`accept` re-validates the bundle with the same fail-closed extractor the format
defines — the manifest is checked first, every entry must sit under the bundle's
`work/` or `transcript/` root, path-traversal and symlinks are refused, and the
cumulative size is capped — then it places the files and the Claude transcript
and resumes the agent with `bp open --resume`. `--dir` chooses the destination
working directory (default: the bundle's folder basename under the current
directory); a non-empty target needs `--yes`.

### Moving a whole hierarchy (`--recursive`)

`bp hop <root> --to <peer> --recursive` moves the agent **and every descendant
under it** as one bundle, preserving the parent/child structure — a whole fleet
migrates in a single transfer. The subtree is captured parent-first, every agent
must be idle, and one recursive bundle carries at most a thousand agents
(`MaxHopAgents`), a bound checked when the bundle is sent, when it is received,
and when its manifest is validated. `--dry-run` lists the agents and their
parents without sending.

A recursive bundle uses a tree layout — each agent's files and transcript live
under `agents/<name>/` — and the receiver dispatches on it automatically. On
`accept`, every agent's files and transcript are rebuilt under `--dir` (each at
its own folder basename) and the agents are registered in the agentbook **as
closed, revivable entries with their hierarchy intact — nothing is launched.**
This is deliberate: accepting a migrated fleet must never start dozens of
sessions at once. Revive an agent later with `bp open <name> <dir> --resume`.
Two flags change this:

- `--parent <name>` — attach the subtree's root under an existing local agent
  (the in-bundle parent/child links are always preserved; this only sets where
  the root hangs). Only the operator chooses the root's new parent — the bundle
  never names a local agent to attach under.
- `--resume` — launch every agent, parent-first, right after accept, instead of
  leaving them closed.

The manifest validation also rejects a malformed hierarchy before anything is
written: the declared root must be parentless and present, every other agent's
parent must reference an agent in the same bundle, names must be unique, and the
graph must be acyclic and fully reachable from the root. As with a single hop,
the advisory source host/agent fields are shown to the owner but never trusted,
and the owner's explicit `accept` is the only thing that materializes anything.

Hop is a new ingress, so it verifies the **peer**, never the bundle's claims:
the advisory source host/agent in the bundle are shown to the owner but never
trusted for authorization, and the owner's explicit `accept` is the only thing
that materializes anything. Decisions are audited as `p2p.hop.received`,
`p2p.hop.denied` (peer not allowed to hop) and `p2p.hop.rejected` (bad header or
over the size cap), alongside the message events.

## Who verifies what

The sending BP resolves the local sender using its existing identity probes. It
records the label, native thread, evidence source and attribution confidence in
the outbound channel. Unknown attribution remains unknown. The channel also pins
both source and destination Peer IDs: replacing a machine key cannot silently
resend an old channel as a different source.

The receiving BP authenticates the connection cryptographically and applies its
own Peer-ID allowlist and target `expose` policy. The rendezvous/relay cannot grant
that permission. An authenticated but unknown peer may use discovery; it still
cannot send to local agents or inspect channels. The receiver constructs the
external envelope itself and records immutable `origin` provenance with the full
Peer ID, original local peer alias, channel ID and reported agent evidence. Use
`bp qstat RECEIVER_QUEUE_ID --json` to inspect it after either queueing or delivery.
Changing a peer's display alias does not rewrite historical provenance.

The receiving BP verifies the **peer**, not the peer's individual agent assertion:
`peer_authenticated: true` and `agent_verified: false` explicitly distinguish them.
A remote `certain: true`, claimed `server-main` name or claimed thread does not
confer local hierarchy privileges. The peer owner is accountable for assertions
made using that machine key. Approval to send a message is not approval to execute
its content, and an authorized peer can still send misleading text.

Agents sharing a Unix account/root and access to the same BP state cannot be
cryptographically isolated from one another by another JSON field or an agent
name. Stronger mutually distrustful local agents require separate OS/sandbox
boundaries and broker-owned credentials; this transport does not pretend those
boundaries already exist. Keep the machine key and BP state private to the owner.
