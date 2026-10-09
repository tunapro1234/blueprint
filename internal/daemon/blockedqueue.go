package daemon

import (
	"fmt"
	"sort"
	"time"
)

// blockedQueue is the fourth watchdog on the shared beat, and it answers the one
// question the other three do not: is a message SITTING in the queue, undelivered,
// while the agent it is addressed to is idle enough to take it?
//
// This is the failure the Reason field on a pending message was added for
// (msgq.Message.Reason): "a message sat for four days behind bp's own hanging
// paste while `bp qstat` reported 'is still busy' at an idle agent." bp already
// REFUSES to type behind foreign composer text — a paste chip, an unsent slash
// command, a torn copy — which is correct: it must never corrupt an agent's
// input. But refusing is silent. Until a human runs `bp qstat`, the queue drains
// nowhere and nobody is told. On 2026-10-09 two campaign agents (bp-term,
// bp-redteam) sat stuck this way for ~2.2h and it took the orchestrator noticing
// by hand to recover them. This watchdog makes the next one noticed by bp.
//
// It fires on the AGREEMENT of two independent signals, which is what keeps it
// from crying wolf:
//
//	IDLE      the agent's pane does not read as busy. A busy agent is working
//	          through a turn and legitimately holds its queue — bp will deliver
//	          the moment the turn ends, so a queued message behind a busy agent
//	          is not stuck, it is waiting its turn.
//	SKIPPED   the head-of-line record carries a Reason, meaning a dispatch pass
//	          actively tried the target and had to skip it. The Reason is cleared
//	          the moment the pane frees and delivery succeeds, so a Reason that
//	          PERSISTS past the threshold is a block that is not clearing itself.
//
// A record scheduled to wait (usage-limit backoff, notReady retry) has NextTry
// in the future; that is not stuck, it is waiting on a clock, so it is excluded.
const (
	// blockedQueueThreshold is how long the head record must have waited, with a
	// skip Reason, at an idle agent, before it is called stuck. The beat runs
	// every five minutes and dispatch far more often, so a Reason surviving this
	// long at an idle pane is a block a human has to clear, not a transient one.
	blockedQueueThreshold = 20 * time.Minute
	// blockedQueueCooldown keeps the alarm to once a day per agent, matching the
	// other watchdogs: the block needs a human, and a human does not need telling
	// twice an hour.
	blockedQueueCooldown = 24 * time.Hour
)

// blockedObservation is one idle agent's stuck head record, gathered away from
// the queue IO so the finding rules can be tested without a daemon.
type blockedObservation struct {
	Session string
	// ID is the channel id of the head record, so the alarm can point `bp qstat`
	// straight at it.
	ID string
	// Reason is the record's own operator-words explanation, quoted verbatim into
	// the alarm — the whole value of the field is that it already says what to do.
	Reason string
	// Age is how long the head record has waited since it was sent.
	Age time.Duration
	// Count is how many records are queued for this agent, so the reader knows the
	// size of the backlog one clear would drain.
	Count int
}

// blockedQueueFindings returns one line per agent whose head record has been
// stuck past the threshold, in session order so a sweep's output is stable.
func blockedQueueFindings(observations []blockedObservation) map[string]string {
	findings := make(map[string]string)
	sorted := append([]blockedObservation(nil), observations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Session < sorted[j].Session })
	for _, o := range sorted {
		if o.Age < blockedQueueThreshold {
			continue
		}
		reason := o.Reason
		if reason == "" {
			reason = "the composer is not free"
		}
		findings[o.Session] = fmt.Sprintf(
			"bp: %s is idle but message %s has waited %s undelivered — reason: %q. The queue is stuck behind the composer: bp will not type behind foreign text (a paste chip, an unsent slash command, a torn copy), so it will sit until the pane is cleared. %d message(s) are queued for this agent. Inspect: bp peek %s; bp qstat %s.",
			o.Session, o.ID, o.Age.Round(time.Minute), reason, o.Count, o.Session, o.ID)
	}
	return findings
}

// blockedQueueScan turns one sweep's idle agents into alarms. Like the other
// three watchdogs on this beat it never fails the sweep, keeps only its cooldown
// state, and sends its findings to the log and to the coordinator (alarm). It is
// the one watchdog that runs on every install: a stuck queue is a delivery
// failure, not a monitoring concern. busy is the
// per-session busy verdict read on the same sweep; an agent absent from it (not
// an agent pane, or capture failed) is treated as not-idle and skipped, which is
// the safe direction for a watchdog that must not guess.
func (s *Service) blockedQueueScan(observations []paneObservation, busy map[string]bool, state *busySanityState, now time.Time) {
	if s.queue == nil {
		return
	}
	if state.BlockedQueueReported == nil {
		state.BlockedQueueReported = make(map[string]string)
	}
	var gathered []blockedObservation
	for _, o := range observations {
		// Only a live agent pane that bp recognises and that reads as idle can be
		// stuck in the sense this watchdog means. A busy agent holds its queue
		// legitimately; a pane bp does not recognise is paneSanity's case, not this
		// one.
		if !o.Open || !o.IsAgent || busy[o.Session] {
			continue
		}
		messages, err := s.queue.PendingForTarget(o.Session)
		if err != nil || len(messages) == 0 {
			continue
		}
		// PendingForTarget returns delivery order, oldest first, so the head is the
		// record that gates the line. A head with no skip Reason was never tried
		// (nothing to report yet); one scheduled to retry later (NextTry ahead) is
		// waiting on a clock, not a composer.
		head := messages[0]
		if head.Reason == "" {
			continue
		}
		if head.NextTry > 0 && time.Unix(0, int64(head.NextTry*1e9)).After(now) {
			continue
		}
		gathered = append(gathered, blockedObservation{
			Session: o.Session,
			ID:      head.ID,
			Reason:  head.Reason,
			Age:     now.Sub(time.Unix(0, int64(head.TS*1e9))),
			Count:   len(messages),
		})
	}
	for _, message := range dueFindingsWithin(blockedQueueFindings(gathered), state.BlockedQueueReported, now, blockedQueueCooldown) {
		s.alarm("blocked-queue", message)
	}
}
