package msgq

import (
	"blueprint/internal/messagetext"
	"time"
)

// StatusDeliveredHook closes a record that the target harness took itself, at a
// turn boundary, through one of its hooks (Claude Code UserPromptSubmit/Stop).
// The harness read the text from the hook's output, so this is a verified
// delivery: nothing was typed into a terminal and nothing waits for a witness.
const StatusDeliveredHook = "delivered (hook)"

// claimDispatchWait bounds how long a hook waits for a running delivery pass.
// A hook runs inside the agent's turn, so it must answer quickly; a message it
// cannot claim stays queued and the ordinary pass delivers it.
const claimDispatchWait = 3 * time.Second

// Claim takes the deliverable head of to's line out of the queue for a caller
// that hands the text to the agent itself (a harness hook), and closes each
// claimed record with status. It returns the claimed messages in delivery
// order.
//
// It holds the dispatch lock (as an operator) and the target's pane lock, the
// same pair a delivery pass holds, so a record is either pasted by the pass or
// claimed here, never both. The line's order is kept: claiming stops at the
// first record that may already be in the agent (NoRepaste, Cleanup) or that a
// pass would refuse, so nothing overtakes it. max <= 0 means no limit.
//
// A record is closed BEFORE its text is returned. If the caller then fails to
// hand it over, the record reads delivered while it was not; the opposite order
// would let a crash between the two deliver it twice, and a duplicate
// instruction is the failure this queue exists to prevent.
func (q *Queue) Claim(to, status string, max int) ([]Message, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	release, err := q.lockDispatchForOperator(claimDispatchWait)
	if err != nil {
		return nil, err
	}
	defer release()
	unlockPane, err := q.lockPane(to)
	if err != nil {
		return nil, err
	}
	defer unlockPane()
	records, _, err := q.pendingRecords()
	if err != nil {
		return nil, err
	}
	_, byTarget := lines(records)
	var claimed []Message
	for _, rec := range byTarget[to] {
		if max > 0 && len(claimed) >= max {
			break
		}
		if rec.NoRepaste || rec.Cleanup {
			break
		}
		if messagetext.Validate(rec.Msg) != nil || messagetext.Label(rec.From) != nil || messagetext.Sender(rec.From) != nil {
			break
		}
		if err := q.finish(rec.path, rec.Message, status); err != nil {
			if len(claimed) > 0 {
				return claimed, nil
			}
			return nil, err
		}
		message := rec.Message
		message.Status = status
		claimed = append(claimed, message)
	}
	return claimed, nil
}
