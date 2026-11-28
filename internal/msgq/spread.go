package msgq

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// KindAnnounce marks one target's copy of a spread announcement (bp announce).
const KindAnnounce = "announce"

// slotMarkFile records the latest slot any spread announcement was given. It is
// a hint for the release timer only: without it the timer would have to read
// every pending record on every poll of an installation that never announces.
const slotMarkFile = ".announce-slot"

// releaseRetry is how soon the release timer tries again when its pass could
// not run because another pass held the dispatch lock.
const releaseRetry = time.Second

func unixSeconds(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

func fromUnixSeconds(seconds float64) time.Time {
	whole, frac := math.Modf(seconds)
	return time.Unix(int64(whole), int64(frac*1e9))
}

// HeldUntil reports whether the record is held for a slot at now, and until
// when. Only a record whose text has never gone near a pane can be held: once
// a released announcement was pasted, its settlement (the witness, the hanging
// paste checks) must run on every pass like any other record's.
func (m Message) HeldUntil(now time.Time) (time.Time, bool) {
	if m.NotBefore <= 0 || touched(m) || unixSeconds(now) >= m.NotBefore {
		return time.Time{}, false
	}
	return fromUnixSeconds(m.NotBefore), true
}

// touched reports whether a delivery of this record may already have put text
// into the pane, or proved that it did not.
func touched(m Message) bool {
	return m.NoRepaste || m.UnverifiedAt > 0 || m.TornClears > 0 || m.ForcedAt > 0 || m.Cleanup || m.Attempts > 0
}

// releasedLine applies the hold rule to one target's line. The line is released
// when at least one record in it is not held, and then every held record in it
// is released too and the line runs in ordinary order: a held announcement
// must never delay a message queued behind it, and nothing queued before it
// was ever affected. A line of held records only waits. ordinary reports
// whether the line holds traffic other than spread announcements; only a line
// without any is a slot release, the one moment DeferCold is asked.
func releasedLine(line []record, now time.Time) (ordinary, released bool) {
	for _, rec := range line {
		if rec.NotBefore <= 0 {
			ordinary = true
		}
		if _, held := rec.HeldUntil(now); !held {
			released = true
		}
	}
	return ordinary, released
}

// deferCold moves a due spread announcement to the target's offline spool
// when the target is no longer warm (see Queue.DeferCold), and reports whether
// it took the record out of this pass. It asks only about a record the pass
// would otherwise act on now, and never about one that may already be in the
// pane: spooling that would deliver it twice.
func (q *Queue) deferCold(rec record, line *lineState, report func(string)) bool {
	if q.DeferCold == nil || rec.Kind != KindAnnounce || rec.NotBefore <= 0 || touched(rec.Message) || rec.renderErr != nil || line.hold(rec) != "" {
		return false
	}
	spooled, err := q.DeferCold(rec.Message)
	if err != nil {
		q.remember(rec.path, rec.Message, "announcement slot reached but the warm check could not finish: "+err.Error(), report)
		line.block(rec.ID)
		if report != nil {
			report(fmt.Sprintf("msgq: %s held: %v", rec.ID, err))
		}
		return true
	}
	if !spooled {
		return false
	}
	if err := q.finish(rec.path, rec.Message, StatusDeferredCold); err != nil {
		line.block(rec.ID)
		if report != nil {
			report(fmt.Sprintf("msgq: could not close deferred announcement %s: %v", rec.ID, err))
		}
		return true
	}
	if report != nil {
		report(fmt.Sprintf("deferred: %s -> %s (no longer warm; moved to the pending spool)", rec.ID, rec.To))
	}
	return true
}

// EnqueueSpread is EnqueueUnique for one target of a spread announcement. The
// record is held until its slot: one interval after the latest slot any
// pending announcement holds, or now when there is none. The slot is chosen
// under the same locks as the duplicate check, so concurrent bp processes
// continue one schedule instead of starting two. A duplicate of a pending
// record returns that record's id and slot.
func (q *Queue) EnqueueSpread(to, from, text string, interval, within time.Duration) (string, time.Time, error) {
	if interval <= 0 {
		return "", time.Time{}, fmt.Errorf("spread interval must be positive")
	}
	id, slot, err := q.enqueueUnique(to, from, text, false, within, interval)
	return id, fromUnixSeconds(slot), err
}

// nextSlot is the slot for a new spread record given the pending records.
func nextSlot(records []record, now time.Time, interval time.Duration) float64 {
	slot := unixSeconds(now)
	for _, rec := range records {
		if rec.Kind != KindAnnounce || rec.NotBefore <= 0 {
			continue
		}
		if next := rec.NotBefore + interval.Seconds(); next > slot {
			slot = next
		}
	}
	return slot
}

// markSlot raises the slot hint to slot. A failed write is not an error for
// the enqueue: the record is durable and the periodic pass still releases it,
// only less punctually.
func (q *Queue) markSlot(slot float64) {
	if last, ok := q.slotMark(); ok && last >= slot {
		return
	}
	tmp, err := os.CreateTemp(q.Root, ".announce-slot-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, err = tmp.WriteString(strconv.FormatFloat(slot, 'f', 6, 64) + "\n")
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		_ = os.Rename(name, filepath.Join(q.Root, slotMarkFile))
	}
}

func (q *Queue) slotMark() (float64, bool) {
	data, err := os.ReadFile(filepath.Join(q.Root, slotMarkFile))
	if err != nil {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	return value, err == nil
}

// NextRelease returns the earliest slot later than after that a pending spread
// announcement is held for. A queue whose slot hint is not later than after
// has none, and answers without reading a single record.
func (q *Queue) NextRelease(after time.Time) (time.Time, bool) {
	cut := unixSeconds(after)
	if last, ok := q.slotMark(); !ok || last <= cut {
		return time.Time{}, false
	}
	records, _, err := q.pendingRecords()
	if err != nil {
		return time.Time{}, false
	}
	best := 0.0
	for _, rec := range records {
		if rec.Kind != KindAnnounce || rec.NotBefore <= cut || touched(rec.Message) {
			continue
		}
		if best == 0 || rec.NotBefore < best {
			best = rec.NotBefore
		}
	}
	if best == 0 {
		return time.Time{}, false
	}
	return fromUnixSeconds(best), true
}

// dueReleases lists the targets with a spread announcement whose slot falls in
// (after, now] and whose text has never gone near a pane.
func (q *Queue) dueReleases(after, now time.Time) map[string]struct{} {
	cut, end := unixSeconds(after), unixSeconds(now)
	if last, ok := q.slotMark(); !ok || last <= cut {
		return nil
	}
	records, _, err := q.pendingRecords()
	if err != nil {
		return nil
	}
	due := map[string]struct{}{}
	for _, rec := range records {
		if rec.Kind == KindAnnounce && rec.NotBefore > cut && rec.NotBefore <= end && !touched(rec.Message) {
			due[rec.To] = struct{}{}
		}
	}
	return due
}

// RunReleases runs a dispatch pass over the due targets' lines when a spread
// announcement's slot comes, so slots are kept to within about a second instead
// of being released in bursts by the periodic pass. Between slots it sleeps until the next one, and with no
// schedule it reads only the slot hint once per poll. It returns when ctx ends.
func (q *Queue) RunReleases(ctx context.Context, target Target, poll time.Duration, report func(string)) {
	handled := q.Now()
	pause := time.Duration(0)
	for {
		wait := poll
		next, ok := q.NextRelease(handled)
		if ok {
			if until := next.Sub(q.Now()); until < wait {
				wait = until
			}
		}
		if wait < pause {
			wait = pause
		}
		pause = 0
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		} else if ctx.Err() != nil {
			return
		}
		if !ok || q.Now().Before(next) {
			continue
		}
		started := q.Now()
		// Only the lines whose slot came are walked: a release is not a full
		// pass, and every other target is left to the periodic one.
		selected := q.dueReleases(handled, started)
		if len(selected) == 0 {
			handled = started
			continue
		}
		q.mu.Lock()
		ran, err := q.dispatchPass(ctx, target, selected, report)
		q.mu.Unlock()
		if err != nil && report != nil {
			report("msgq: " + err.Error())
		}
		if ran {
			handled = started
		} else {
			pause = releaseRetry
		}
	}
}

// NextSlot is the slot EnqueueSpread would give a new record now. It reads the
// queue without taking its locks and changes nothing; bp announce --dry-run
// reports it.
func (q *Queue) NextSlot(interval time.Duration) time.Time {
	records, _, err := q.pendingRecords()
	if err != nil {
		return q.Now()
	}
	return fromUnixSeconds(nextSlot(records, q.Now(), interval))
}
