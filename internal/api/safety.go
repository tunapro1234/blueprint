package api

import "blueprint/internal/audit"

// severity is the audit severity of a decision: rejections are warnings.
func severity(decision string) string {
	if decision == "rejected" {
		return audit.Warn
	}
	return audit.Info
}

// FrameSource says where stored external text came from. It mirrors
// guard.Source (docs/security/guard-api.md on feat/guard) field for field,
// so the adapter to guard's Framer is a plain copy.
type FrameSource struct {
	Transport  string `json:"transport"`
	Peer       string `json:"peer,omitempty"`
	PeerID     string `json:"peerId,omitempty"`
	AgentClaim string `json:"agentClaim,omitempty"`
	Room       string `json:"room,omitempty"`
	// Channel is the stable record id, so one record always frames the same.
	Channel string `json:"channel,omitempty"`
}

// Framer frames external text as bp-api's own stores (inbox, rooms, board)
// hand it to an agent. Queue records are framed by msgq at delivery, not
// here. It is the one-method seam for guard.Framer (W6); blueprint wires the
// concrete type into Core.Frame. On error the read fails: raw external text
// is never returned in place of a frame.
type Framer interface {
	Frame(src FrameSource, text string) (string, error)
}

// PassthroughFramer returns text unchanged.
//
// TODO(W6): replace with guard.Framer (guard.LoadFramer) once feat/guard
// lands on dev.
type PassthroughFramer struct{}

func (PassthroughFramer) Frame(_ FrameSource, text string) (string, error) { return text, nil }
