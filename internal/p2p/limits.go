package p2p

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"blueprint/internal/audit"
)

// Defaults for inbound limits. A peer may lower or raise them in config.
const (
	DefaultRatePerMinute = 20
	DefaultLoopCap       = 30
	LoopWindow           = 10 * time.Minute
	LoopPause            = 30 * time.Minute
)

// limiter enforces per-peer rate limits and the per-pair loop cap for
// inbound messages. Only newly accepted messages count; replays of a channel
// that was already accepted are free.
type limiter struct {
	mu     sync.Mutex
	tokens map[string]float64
	refill map[string]time.Time
	pairs  map[string][]time.Time
	logged map[string]string // channel ID -> last audited rejection reason
}

func newLimiter() *limiter {
	return &limiter{tokens: map[string]float64{}, refill: map[string]time.Time{}, pairs: map[string][]time.Time{}, logged: map[string]string{}}
}

func ratePerMinute(p Peer) int {
	if p.Rate > 0 {
		return p.Rate
	}
	return DefaultRatePerMinute
}

func loopCap(p Peer) int {
	if p.LoopCap > 0 {
		return p.LoopCap
	}
	return DefaultLoopCap
}

func pairKey(peerID, agent string) string { return peerID + "/" + agent }

// errRate and errLoop are the reasons a sender sees; its channel stays
// outgoing and retries, so held messages arrive once the limit clears.
var (
	errRate = errors.New("rate limited: too many messages from this peer; retrying later")
	errLoop = errors.New("loop cap reached for this agent: the receiving owner must run bp p2p resume")
)

// admit checks both limits for one new inbound message and records it when
// admitted. pausedUntil reports an owner-visible pause for the pair.
func (l *limiter) admit(peerID string, p Peer, agent string, now time.Time, pausedUntil time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(pausedUntil) {
		return errLoop
	}
	rate := float64(ratePerMinute(p))
	burst := rate / 2
	if burst < 3 {
		burst = 3
	}
	tokens, seen := l.tokens[peerID]
	if !seen {
		tokens = burst
	} else {
		tokens += now.Sub(l.refill[peerID]).Minutes() * rate
		if tokens > burst {
			tokens = burst
		}
	}
	l.refill[peerID] = now
	if tokens < 1 {
		l.tokens[peerID] = tokens
		return errRate
	}
	key := pairKey(peerID, agent)
	recent := l.pairs[key][:0]
	for _, t := range l.pairs[key] {
		if now.Sub(t) < LoopWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= loopCap(p) {
		l.pairs[key] = recent
		l.tokens[peerID] = tokens
		return fmt.Errorf("%w (%d messages in %s)", errLoop, len(recent), LoopWindow)
	}
	l.tokens[peerID] = tokens - 1
	l.pairs[key] = append(recent, now)
	return nil
}

// admitLookup rate-limits lookup queries per peer, separately from messages,
// so a peer cannot enumerate names by hammering lookup. It consumes from the
// same per-peer token bucket keyed with a "lookup:" prefix.
func (l *limiter) admitLookup(peerID string, p Peer, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	rate := float64(ratePerMinute(p))
	burst := rate / 2
	if burst < 3 {
		burst = 3
	}
	key := "lookup:" + peerID
	tokens, seen := l.tokens[key]
	if !seen {
		tokens = burst
	} else {
		tokens += now.Sub(l.refill[key]).Minutes() * rate
		if tokens > burst {
			tokens = burst
		}
	}
	l.refill[key] = now
	if tokens < 1 {
		l.tokens[key] = tokens
		return false
	}
	l.tokens[key] = tokens - 1
	return true
}

// resetPair forgets a pair's history after the owner resumes it.
func (l *limiter) resetPair(peerID, agent string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.pairs, pairKey(peerID, agent))
}

// firstRejection reports whether this rejection reason is new for the
// channel, so sender retries do not flood the audit log.
func (l *limiter) firstRejection(channel, reason string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logged[channel] == reason {
		return false
	}
	if len(l.logged) > 4096 {
		l.logged = map[string]string{}
	}
	l.logged[channel] = reason
	return true
}

// Pauses are owner decisions stored in <state>/p2p/paused.json so the CLI
// can lift them while the service runs. Keys are pairKey values.
type Pauses map[string]time.Time

func pausesPath(root string) string { return statePath(root, "paused.json") }

// ReadPauses returns the stored pauses; a missing file means none.
func ReadPauses(root string) (Pauses, error) {
	p := Pauses{}
	err := readJSON(pausesPath(root), &p)
	if errors.Is(err, os.ErrNotExist) {
		return Pauses{}, nil
	}
	return p, err
}

func writePauses(root string, p Pauses) error { return atomicJSON(pausesPath(root), p) }

// Pause holds inbound messages from a peer to one agent until the given time.
func Pause(root, peerID, agent string, until time.Time) error {
	p, err := ReadPauses(root)
	if err != nil {
		return err
	}
	p[pairKey(peerID, agent)] = until
	return writePauses(root, p)
}

// Resume lifts pauses for a peer: one agent, or every agent when agent is "".
// It returns the number of pauses lifted.
func Resume(root string, cfg Config, alias, agent string) (int, error) {
	pr, ok := cfg.Peers[alias]
	if !ok {
		return 0, fmt.Errorf("unknown p2p peer: %s", alias)
	}
	p, err := ReadPauses(root)
	if err != nil {
		return 0, err
	}
	lifted := 0
	for key := range p {
		if key == pairKey(pr.ID, agent) || (agent == "" && len(key) > len(pr.ID) && key[:len(pr.ID)+1] == pr.ID+"/") {
			delete(p, key)
			lifted++
		}
	}
	if lifted > 0 {
		if err := writePauses(root, p); err != nil {
			return 0, err
		}
	}
	_ = audit.Append(root, audit.Event{Kind: "p2p.resume", Peer: alias, PeerID: pr.ID, Target: agent, Actor: "owner", Fields: map[string]string{"lifted": fmt.Sprint(lifted)}})
	return lifted, nil
}
