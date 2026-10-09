package api

import "blueprint/internal/audit"

// severity is the audit severity of a decision: rejections are warnings.
func severity(decision string) string {
	if decision == "rejected" {
		return audit.Warn
	}
	return audit.Info
}

// Framer frames text that crossed a trust boundary as it reaches an agent.
// It is the one-method seam for guard.Frame (W6): bp-guard ships the
// concrete type and blueprint wires it into Core.Frame. Text is always
// stored raw and framed only at read or delivery time.
type Framer interface {
	Frame(source, text string) string
}

// PassthroughFramer returns text unchanged.
//
// TODO(W6): replace with guard.Frame once feat/guard lands.
type PassthroughFramer struct{}

func (PassthroughFramer) Frame(_, text string) string { return text }
