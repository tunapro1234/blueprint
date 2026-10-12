package cache

import "time"

// Activity is a live observation, independent of historical usage. Unknown
// blocks unattended delivery but must never be displayed as working or idle.
type Activity struct {
	BindingConflicts   []string   `json:"binding_conflicts,omitempty"`
	HistoricalBindings []string   `json:"historical_bindings,omitempty"`
	State              string     `json:"state"`
	Source             string     `json:"source"`
	Reason             string     `json:"reason,omitempty"`
	RecoveryHint       string     `json:"recovery_hint,omitempty"`
	ObservedAt         time.Time  `json:"observed_at"`
	LastEventAt        *time.Time `json:"last_event_at,omitempty"`
	ThreadID           string     `json:"thread_id,omitempty"`
	TranscriptPath     string     `json:"transcript_path,omitempty"`
	Binding            string     `json:"binding,omitempty"`
	LastTurnError      bool       `json:"last_turn_error,omitempty"`
	ScreenBusy         *bool      `json:"screen_busy,omitempty"`
	TurnBusy           *bool      `json:"turn_busy,omitempty"`
	// TurnReadErr carries a genuine transcript READ failure (an unreadable or
	// mid-write tail), as distinct from a readable-but-inconclusive transcript,
	// which leaves this empty. Only the Claude runtime sets it, and only the
	// strict delivery-path turn-open probe reads it, to fail closed when it
	// cannot confirm the turn is closed.
	TurnReadErr     string `json:"turn_read_err,omitempty"`
	DeliveryBlocked bool   `json:"delivery_blocked"`
}
