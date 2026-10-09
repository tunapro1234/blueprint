package api

import (
	"sync"
	"time"

	"blueprint/internal/audit"
)

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

// interim marks a Framer that does not actually frame. The gateway refuses
// to run behind one.
type interim interface{ interimFramer() }

func (PassthroughFramer) interimFramer() {}

// RealFramer reports whether f really frames text: not nil and not the
// interim passthrough.
func RealFramer(f Framer) bool {
	if f == nil {
		return false
	}
	_, fake := f.(interim)
	return !fake
}

// limiter is a set of token buckets, one per key. Idle buckets are dropped.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

// allow spends cost tokens from key's bucket, which refills at perMinute
// tokens a minute up to perMinute.
func (l *limiter) allow(key string, perMinute int, cost float64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string]*bucket{}
	}
	if now.Sub(l.swept) > time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.at) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.swept = now
	}
	capacity := float64(perMinute)
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: capacity, at: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.at).Minutes() * capacity
	if b.tokens > capacity {
		b.tokens = capacity
	}
	b.at = now
	if b.tokens < cost {
		return false
	}
	b.tokens -= cost
	return true
}

// size is the number of live buckets (for tests).
func (l *limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// auditBudget bounds rejection events per source so a flood of bad requests
// cannot flood the audit log. Over budget, events are counted, and one
// api.audit.suppressed event per source reports the count once the window
// has passed.
type auditBudget struct {
	mu      sync.Mutex
	windows map[string]*auditWindow
}

type auditWindow struct {
	start      time.Time
	count      int
	suppressed int
}

const auditPerMinute = 20

// admit reports whether to write this event, and a pending suppressed count
// to report first (0 if none).
func (a *auditBudget) admit(source string, now time.Time) (bool, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.windows == nil {
		a.windows = map[string]*auditWindow{}
	}
	w, ok := a.windows[source]
	if !ok || now.Sub(w.start) >= time.Minute {
		pending := 0
		if ok {
			pending = w.suppressed
		}
		if len(a.windows) > 10000 {
			a.windows = map[string]*auditWindow{}
		}
		a.windows[source] = &auditWindow{start: now, count: 1}
		return true, pending
	}
	if w.count < auditPerMinute {
		w.count++
		return true, 0
	}
	w.suppressed++
	return false, 0
}
