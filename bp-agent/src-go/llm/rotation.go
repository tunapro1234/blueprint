package llm

import "sync"

// Rotator provides round-robin selection of keys.
type Rotator struct {
	mu   sync.Mutex
	keys []string
	next int
}

// NewRotator creates a new Rotator.
func NewRotator(keys []string) *Rotator {
	return &Rotator{keys: keys}
}

// Next returns the next key in rotation.
func (r *Rotator) Next() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.keys) == 0 {
		return ""
	}
	key := r.keys[r.next%len(r.keys)]
	r.next++
	return key
}
