package fed

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"

	"blueprint/internal/messagetext"
	"blueprint/internal/msgq"
)

// Transport is the msgq.Origin transport of federated messages. It is not a
// local transport, so msgq frames these messages as untrusted at delivery.
const Transport = "fed"

// enqueueInbound queues one message that arrived over federation. The body is
// stored raw with an Origin, like P2P inbound; the shared frame is applied at
// delivery. Each call is a new record: fed has no transport-level replay key
// (the client's seen.json and the hub outbox ack handle resends).
func enqueueInbound(queue *msgq.Queue, to, from, text string, origin *msgq.Origin) (string, error) {
	// The plain enqueue refused anonymous senders; keep that.
	if err := messagetext.Sender(from); err != nil {
		return "", err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	origin.Transport = Transport
	m, err := queue.EnqueueOnceOrigin("bp-fed-v1:"+hex.EncodeToString(nonce[:]), to, from, "["+from+"] "+text, origin)
	return m.ID, err
}

// hubOrigin describes a message a peer posted to this hub. The peer is
// authenticated by its bearer token; the agent name it gives is a claim.
func hubOrigin(peerName, agent string) *msgq.Origin {
	return &msgq.Origin{PeerAlias: peerName, PeerID: "fed-peer:" + peerName, PeerAuthenticated: true, AgentClaim: agent}
}

// clientOrigin describes a message polled from a remote hub. The hub is the
// only party this client authenticates (TLS and its token); the sender label
// is whatever the hub reports.
func clientOrigin(hub, channel, from string) *msgq.Origin {
	alias := hub
	if u, err := url.Parse(hub); err == nil && u.Host != "" {
		alias = u.Host
	}
	return &msgq.Origin{PeerAlias: alias, PeerID: "fed-hub:" + hub, ChannelID: channel, PeerAuthenticated: true, AgentClaim: from}
}
