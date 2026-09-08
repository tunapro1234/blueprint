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
