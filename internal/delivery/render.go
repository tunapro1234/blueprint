// Package delivery wires guard framing into the message queue's delivery path.
// It lives apart from both cmd/bp and internal/daemon so the two dispatchers
// share one renderer and frame every record identically.
package delivery

import (
	"errors"

	"blueprint/internal/guard"
	"blueprint/internal/msgq"
)

// Renderer returns a msgq.Queue.Render that frames the body of any record whose
// Origin crossed a trust boundary (a P2P peer, a remote caller) in an
// untrusted-input frame, and returns a local record's body unchanged. It loads
// one guard framer from stateDir so every render of a record is identical across
// the daemon, a synchronous bp msg and a restart — the delivery witness compares
// the rendered text, so a drifting nonce would strand the message.
//
// stateDir must be the same directory p2p and audit use (config.StateDir); an
// empty stateDir or a key-load failure is returned as an error, and the caller
// decides whether to run without framing.
func Renderer(stateDir string) (func(msgq.Message) (string, error), error) {
	framer, err := guard.LoadFramer(stateDir)
	if err != nil {
		return nil, err
	}
	return func(m msgq.Message) (string, error) {
		if !External(m) {
			return m.Msg, nil
		}
		framed, err := framer.Envelope(m.From, guard.Source{
			Transport:  m.Origin.Transport,
			Peer:       m.Origin.PeerAlias,
			PeerID:     m.Origin.PeerID,
			AgentClaim: m.Origin.AgentClaim,
			Channel:    m.ID,
		}, m.Msg)
		if err != nil {
			return "", err
		}
		return framed.Text, nil
	}, nil
}

// External reports whether a record's Origin crossed a trust boundary and so
// must be framed. The decision is NeedsFrame(transport), not whether the peer
// authenticated: an authenticated P2P peer is still outside this machine. It is
// wired as msgq.Queue.FrameExternal so owner-facing notices can refuse to quote
// an external body raw (bp-guard D1), and it is the predicate Renderer uses.
func External(m msgq.Message) bool {
	return m.Origin != nil && guard.NeedsFrame(m.Origin.Transport)
}

// FailClosedRenderer is installed when the guard framer cannot be loaded (a
// missing or unreadable frame key, a full disk, the wrong owner). It delivers
// local records unchanged but refuses every external record with an error, so
// dispatch holds it in the queue — visible in bp qstat — instead of pasting
// attacker text unframed. Framing failing OPEN was bp-guard D2. The decision
// matches Renderer's, so turning framing on later reframes the held records.
func FailClosedRenderer() func(msgq.Message) (string, error) {
	return func(m msgq.Message) (string, error) {
		if !External(m) {
			return m.Msg, nil
		}
		return "", errors.New("inbound framing unavailable: guard framer failed to load")
	}
}
