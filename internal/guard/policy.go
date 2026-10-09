package guard

import (
	"fmt"
	"sync"
	"time"
)

// Capability is one thing a peer may ask this machine to do.
type Capability string

const (
	CapSend   Capability = "send"
	CapLookup Capability = "lookup"
	CapRooms  Capability = "rooms"
	CapBoard  Capability = "board"
)

// Defaults for peers whose configuration predates per-peer policy.
const (
	DefaultRatePerHour = 120
	DefaultBurst       = 20
	DefaultMaxBytes    = 16 * 1024
)

// Policy is what one peer may do here. It is owned by the receiving machine;
// nothing a peer sends can widen it.
type Policy struct {
	// Capabilities granted. Nil means DefaultCapabilities; empty grants none.
	Capabilities []Capability `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	// Expose lists local agents the peer may address and look up.
	Expose []string `json:"expose,omitempty" yaml:"expose,omitempty"`
	// Rooms lists rooms the peer may join or publish to.
	Rooms []string `json:"rooms,omitempty" yaml:"rooms,omitempty"`
	// RatePerHour caps accepted requests; 0 means DefaultRatePerHour, <0 none.
	RatePerHour int `json:"rate_per_hour,omitempty" yaml:"rate_per_hour,omitempty"`
	// Burst is the token bucket size; 0 means DefaultBurst.
	Burst int `json:"burst,omitempty" yaml:"burst,omitempty"`
	// MaxBytes caps one message body; 0 means DefaultMaxBytes.
	MaxBytes int `json:"max_bytes,omitempty" yaml:"max_bytes,omitempty"`
	// Redact applies to text this machine sends to the peer.
	Redact RedactPolicy `json:"redact,omitempty" yaml:"redact,omitempty"`
}

// DefaultCapabilities keeps existing p2p configs working: send to and look
// up exposed agents only. Rooms and board must be granted explicitly.
var DefaultCapabilities = []Capability{CapSend, CapLookup}

// Decision is the result of a policy check, phrased for the audit log.
type Decision struct {
	Allow bool `json:"allow"`
	// Code is stable for tests and dashboards: ok, no-capability,
	// not-exposed, too-large, rate-limited, room-not-granted.
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

func allow() Decision { return Decision{Allow: true, Code: "ok", Reason: "allowed by peer policy"} }
func deny(code, format string, args ...any) Decision {
	return Decision{Code: code, Reason: fmt.Sprintf(format, args...)}
}

func (p Policy) caps() []Capability {
	if p.Capabilities == nil {
		return DefaultCapabilities
	}
	return p.Capabilities
}

// Can reports whether the capability is granted.
func (p Policy) Can(c Capability) bool {
	for _, x := range p.caps() {
		if x == c {
			return true
		}
	}
	return false
}

// Exposes reports whether the local agent is visible to this peer.
func (p Policy) Exposes(agent string) bool {
	for _, a := range p.Expose {
		if a == agent {
			return true
		}
	}
	return false
}

func (p Policy) maxBytes() int {
	if p.MaxBytes <= 0 {
		return DefaultMaxBytes
	}
	return p.MaxBytes
}

// CheckSend decides whether the peer may deliver size bytes to target.
func (p Policy) CheckSend(target string, size int) Decision {
	switch {
	case !p.Can(CapSend):
		return deny("no-capability", "peer may not send messages")
	case !p.Exposes(target):
		return deny("not-exposed", "target %q is not exposed to this peer", target)
	case size > p.maxBytes():
		return deny("too-large", "message is %d bytes; limit is %d", size, p.maxBytes())
	}
	return allow()
}

// CheckLookup decides whether a lookup may answer for name. A denied lookup
// must look exactly like "not found" on the wire, so a peer cannot tell a
// hidden agent from a missing one; the reason is for the local audit only.
func (p Policy) CheckLookup(name string) Decision {
	switch {
	case !p.Can(CapLookup):
		return deny("no-capability", "peer may not look up agents")
	case !p.Exposes(name):
		return deny("not-exposed", "lookup for %q, which is not exposed to this peer", name)
	}
	return allow()
}

// CheckRoom decides room or board access.
func (p Policy) CheckRoom(c Capability, room string, size int) Decision {
	if !p.Can(c) {
		return deny("no-capability", "peer may not use %s", c)
	}
	granted := false
	for _, r := range p.Rooms {
		if r == room {
			granted = true
		}
	}
	if !granted {
		return deny("room-not-granted", "room %q is not granted to this peer", room)
	}
	if size > p.maxBytes() {
		return deny("too-large", "message is %d bytes; limit is %d", size, p.maxBytes())
	}
	return allow()
}

// Limiter is a per-peer token bucket. Safe for concurrent use.
type Limiter struct {
	Now     func() time.Time
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewLimiter() *Limiter { return &Limiter{Now: time.Now, buckets: map[string]*bucket{}} }

// Allow spends one token for peer under its policy.
func (l *Limiter) Allow(peer string, p Policy) Decision {
	if p.RatePerHour < 0 {
		return allow()
	}
	rate := p.RatePerHour
	if rate == 0 {
		rate = DefaultRatePerHour
	}
	burst := p.Burst
	if burst <= 0 {
		burst = DefaultBurst
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	b := l.buckets[peer]
	if b == nil {
		b = &bucket{tokens: float64(burst), last: now}
		l.buckets[peer] = b
	}
	if el := now.Sub(b.last); el > 0 {
		b.tokens += el.Hours() * float64(rate)
		if b.tokens > float64(burst) {
			b.tokens = float64(burst)
		}
	}
	b.last = now
	if b.tokens < 1 {
		return deny("rate-limited", "peer exceeded %d requests per hour (burst %d)", rate, burst)
	}
	b.tokens--
	return allow()
}
