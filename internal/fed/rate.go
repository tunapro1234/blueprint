package fed

import (
	"sync"
	"time"
)

type rateWindow struct {
	Started time.Time
	Count   int
}

type RateLimiter struct {
	Limit int
	Now   func() time.Time
	mu    sync.Mutex
	peers map[string]rateWindow
}

func NewRateLimiter(limit int) *RateLimiter {
	return &RateLimiter{Limit: limit, Now: time.Now, peers: map[string]rateWindow{}}
}

func (r *RateLimiter) Allow(peer string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.Now()
	window := r.peers[peer]
	if window.Started.IsZero() || now.Sub(window.Started) >= time.Hour {
		window = rateWindow{Started: now}
	}
	if window.Count >= r.Limit {
		r.peers[peer] = window
		return false
	}
	window.Count++
	r.peers[peer] = window
	return true
}

func (r *RateLimiter) Snapshot() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.Now()
	result := map[string]int{}
	for peer, window := range r.peers {
		if now.Sub(window.Started) < time.Hour {
			result[peer] = window.Count
		}
	}
	return result
}
