package msgq

import "testing"

// legacyP2PState is internal/p2p/node.go's inline mapping before 3239bad moved
// it into DeliveryState. Stored P2P records must keep rendering the same.
func legacyP2PState(m Message) (state, reason string) {
	state, reason = "accepted", m.Reason
	switch {
	case m.Cleanup:
		state = "delivered"
	case IsUnverifiedDelivery(m.Status):
		state = "unverified"
	case IsVerifiedDelivery(m.Status):
		state = "delivered"
	case m.Status != "":
		state, reason = "failed", m.Status
	}
	return state, reason
}

func TestDeliveryStateMatchesLegacyP2PMapping(t *testing.T) {
	statuses := []string{
		"", "delivered", "delivered (pasted)", "delivered (unverified)",
		StatusUnconfirmed, StatusHangingComposer, "not delivered (busy)",
		"failed", "failed: no pane", "canceled", "cancelled", "canceled by operator",
		"expired", "expired (ttl)", "pending", "retry",
	}
	for _, status := range statuses {
		for _, cleanup := range []bool{false, true} {
			for _, reason := range []string{"", "some reason"} {
				m := Message{Status: status, Cleanup: cleanup, Reason: reason}
				gotState, gotReason := DeliveryState(m)
				wantState, wantReason := legacyP2PState(m)
				if gotState != wantState || gotReason != wantReason {
					t.Errorf("status=%q cleanup=%v reason=%q: got (%q, %q), legacy (%q, %q)",
						status, cleanup, reason, gotState, gotReason, wantState, wantReason)
				}
			}
		}
	}
	// A canceled record was "failed" with the status as reason before too.
	if state, reason := DeliveryState(Message{Status: "canceled"}); state != DeliveryFailed || reason != "canceled" {
		t.Fatalf("canceled = (%q, %q), want (failed, canceled)", state, reason)
	}
}
