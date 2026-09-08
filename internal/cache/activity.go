package cache

import "time"

// Activity is a live observation, independent of historical usage. Unknown
// blocks unattended delivery but must never be displayed as working or idle.
type Activity struct {
	State           string     `json:"state"`
	Source          string     `json:"source"`
	Reason          string     `json:"reason,omitempty"`
	ObservedAt      time.Time  `json:"observed_at"`
	LastEventAt     *time.Time `json:"last_event_at,omitempty"`
	ThreadID        string     `json:"thread_id,omitempty"`
	TranscriptPath  string     `json:"transcript_path,omitempty"`
	Binding         string     `json:"binding,omitempty"`
	ScreenBusy      *bool      `json:"screen_busy,omitempty"`
	TurnBusy        *bool      `json:"turn_busy,omitempty"`
	DeliveryBlocked bool       `json:"delivery_blocked"`
}
