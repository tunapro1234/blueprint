package api

import (
	"errors"
	"sync"
	"time"

	"blueprint/internal/audit"
	"blueprint/internal/guard"
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
// here. GuardFramer is the real one; cmd/bp wires it into Core.Frame. On
// error the read fails: raw external text is never returned in place of a
// frame.
type Framer interface {
	Frame(src FrameSource, text string) (string, error)
}

// GuardFramer frames with guard: the nonce is keyed by the framer key, the
// peer and the stable record id, so a record reads the same every time.
type GuardFramer struct{ F *guard.Framer }

func (g GuardFramer) Frame(src FrameSource, text string) (string, error) {
	if g.F == nil {
		return "", errors.New("guard framer not loaded")
	}
	framed, err := g.F.Frame(guard.Source(src), text)
	if err != nil {
		return "", err
	}
	return framed.Text, nil
}

// PassthroughFramer returns text unchanged. It is the default until cmd/bp
// loads the guard framer, and the gateway refuses to run behind it.
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
// has passed. Any later call flushes every expired window, so a source that
// never returns is still reported.
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

// maxAuditWindows bounds the sources tracked at once.
const maxAuditWindows = 10000

// admit reports whether to write this event, and the suppressed counts of
// expired windows (any source) to report first.
func (a *auditBudget) admit(source string, now time.Time) (bool, map[string]int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.windows == nil {
		a.windows = map[string]*auditWindow{}
	}
	var flushed map[string]int
	for key, w := range a.windows {
		if now.Sub(w.start) >= time.Minute {
			if w.suppressed > 0 {
				if flushed == nil {
					flushed = map[string]int{}
				}
				flushed[key] = w.suppressed
			}
			delete(a.windows, key)
		}
	}
	w, ok := a.windows[source]
	if !ok {
		if len(a.windows) >= maxAuditWindows {
			// Every window is live: count this one against a shared bucket.
			source = "(overflow)"
			if w, ok = a.windows[source]; !ok {
				w = &auditWindow{start: now}
				a.windows[source] = w
			}
		} else {
			w = &auditWindow{start: now}
			a.windows[source] = w
		}
	}
	if w.count < auditPerMinute {
		w.count++
		return true, flushed
	}
	w.suppressed++
	return false, flushed
}
