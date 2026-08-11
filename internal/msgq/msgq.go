package msgq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	bptmux "blueprint/internal/tmux"
)

type Message struct {
	ID   string  `json:"id"`
	To   string  `json:"to"`
	From string  `json:"from"`
	Msg  string  `json:"msg"`
	TS   float64 `json:"ts"`
	// Reason says WHY the message is still waiting, in words the operator can act
	// on ("composer'da okunamayan bir paste var (chip)"). It is refreshed on every
	// dispatch pass that has to skip the target, and cleared when the pane frees
	// up. Empty means the plain, uninformative-but-honest "still busy": either the
	// record predates this field, or nobody has looked at the pane yet. An empty
	// value therefore keeps the old output exactly as it was.
	//
	// It exists because the worst version of this queue is the SILENT one: a
	// message sat for four days behind bp's own hanging paste while `bp qstat`
	// reported "is still busy" at an idle agent. A reason turns that into a
	// one-glance fix.
	Reason   string  `json:"reason,omitempty"`
	Status   string  `json:"status,omitempty"`
	Finished float64 `json:"finished,omitempty"`
}

type Queue struct {
	Root string
	Now  func() time.Time
	// Witness, when set, reports whether a message has ALREADY reached the target
	// — read out of the agent's own transcript, not off the screen. Dispatch asks
	// it before pasting a queued message again, because a message can land
	// without the queue ever learning about it: the operator hand-delivered one
	// during the 2026-08 incident and the daemon pasted a second copy on top of
	// it. since is the moment the record was queued; only a later appearance in
	// the transcript counts, so an older, identical message cannot close a fresh
	// record.
	Witness func(to, text string, since time.Time) bool
	mu      sync.Mutex
}

func New(root string) *Queue {
	return &Queue{Root: root, Now: time.Now}
}

func (q *Queue) pending() string { return filepath.Join(q.Root, "pending") }
func (q *Queue) done() string    { return filepath.Join(q.Root, "done") }

// Enqueue records a message with no reason attached (the caller does not know why
// the target could not take it, or there is nothing to say).
func (q *Queue) Enqueue(to, from, text string) (string, error) {
	return q.EnqueueReason(to, from, text, "")
}

// EnqueueReason records a message together with the reason its target could not
// take it right now, so `bp q` and `bp qstat` can show WHY it is waiting instead
// of an unqualified "still busy".
func (q *Queue) EnqueueReason(to, from, text, reason string) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := os.MkdirAll(q.pending(), 0755); err != nil {
		return "", err
	}
	now := q.Now()
	base := fmt.Sprintf("q%09d", now.UnixNano()%1_000_000_000)
	tmp, err := os.CreateTemp(q.pending(), ".pending-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return "", err
	}
	message := Message{ID: base, To: to, From: from, Msg: text, TS: float64(now.UnixNano()) / 1e9, Reason: reason}
	if err = json.NewEncoder(tmp).Encode(message); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	id := base
	for suffix := 1; ; suffix++ {
		path := filepath.Join(q.pending(), id+".json")
		err = os.Link(tmpName, path)
		if errors.Is(err, os.ErrExist) {
			id = fmt.Sprintf("%s-%d", base, suffix)
			message.ID = id
			data, marshalErr := json.Marshal(message)
			if marshalErr != nil {
				return "", marshalErr
			}
			if writeErr := os.WriteFile(tmpName, append(data, '\n'), 0644); writeErr != nil {
				return "", writeErr
			}
			if tempFile, openErr := os.Open(tmpName); openErr == nil {
				_ = tempFile.Sync()
				_ = tempFile.Close()
			}
			continue
		}
		if err != nil {
			return "", err
		}
		if dir, openErr := os.Open(q.pending()); openErr == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
		return id, nil
	}
}

func read(path string) (Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Message{}, err
	}
	var message Message
	err = json.Unmarshal(data, &message)
	if err == nil && (message.Status == "" || message.Finished == 0) {
		// Read legacy queue records written before the storage keys became English.
		var legacy struct {
			Status   string  `json:"durum"`
			Finished float64 `json:"bitis"`
		}
		if json.Unmarshal(data, &legacy) == nil {
			if message.Status == "" {
				message.Status = legacy.Status
			}
			if message.Finished == 0 {
				message.Finished = legacy.Finished
			}
		}
	}
	message.Status = englishStatus(message.Status)
	return message, err
}

func englishStatus(status string) string {
	switch status {
	case "iletildi":
		return "delivered"
	case "iptal (hedef kapali)":
		return "canceled (target closed)"
	default:
		return status
	}
}

func (q *Queue) List() ([]Message, error) {
	paths, err := filepath.Glob(filepath.Join(q.pending(), "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	messages := make([]Message, 0, len(paths))
	for _, path := range paths {
		message, readErr := read(path)
		if readErr != nil {
			return nil, fmt.Errorf("%s: %w", path, readErr)
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func (q *Queue) Status(id string) (string, error) {
	if message, err := read(filepath.Join(q.pending(), id+".json")); err == nil {
		seconds := int(q.Now().Sub(time.Unix(0, int64(message.TS*1e9))).Seconds())
		if seconds < 0 {
			seconds = 0
		}
		// With a known reason, say it and say what to do about it. "is still busy"
		// is kept ONLY for records nobody has a reason for, because that sentence
		// is what made a four-day-old message look like an ordinary wait.
		if message.Reason != "" {
			return fmt.Sprintf("PENDING: %s — %s (%d seconds queued); bak: bp peek %s",
				message.To, message.Reason, seconds, message.To), nil
		}
		return fmt.Sprintf("PENDING: %s is still busy (%d seconds queued)", message.To, seconds), nil
	}
	if message, err := read(filepath.Join(q.done(), id+".json")); err == nil {
		when := time.Unix(0, int64(message.Finished*1e9)).Local().Format("15:04")
		return fmt.Sprintf("%s: %s (at %s)", strings.ToUpper(message.Status), message.To, when), nil
	}
	return fmt.Sprintf("UNKNOWN: %s is not in the records (records older than 2 days are removed)", id), nil
}

type Target interface {
	HasSession(context.Context, string) bool
	Capture(context.Context, string) (string, error)
	CaptureAnsi(context.Context, string) (string, error)
	Send(context.Context, string, string) error
	// ClearDelivered erases a composer that provably holds one of texts, and
	// nothing else. Dispatch calls it in exactly one situation: the transcript
	// proved a message already arrived, and the same text is STILL hanging in the
	// composer. Once the record is closed nothing can recognise that text as ours
	// again, so leaving it would block the target permanently — the deadlock, back
	// from the other end.
	ClearDelivered(context.Context, string, []string) (bool, error)
}

// PendingFor returns the queued message texts for one target, oldest first.
//
// They exist for a single purpose: handing them to the delivery step so it can
// recognise its OWN unsubmitted paste hanging in a composer instead of reading it
// as "the agent is busy" and queueing behind itself — the deadlock that held one
// message for four days.
func (q *Queue) PendingFor(to string) []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	paths, err := filepath.Glob(filepath.Join(q.pending(), "*.json"))
	if err != nil {
		return nil
	}
	sort.Strings(paths)
	var texts []string
	for _, path := range paths {
		if message, readErr := read(path); readErr == nil && message.To == to {
			texts = append(texts, message.Msg)
		}
	}
	return texts
}

// Reason returns the recorded reason a pending message is still waiting, or ""
// when there is none (an unknown id, a closed record, or a record nobody has
// looked at a pane for yet).
func (q *Queue) Reason(id string) string {
	message, err := read(filepath.Join(q.pending(), id+".json"))
	if err != nil {
		return ""
	}
	return message.Reason
}

// CloseDelivered closes the oldest pending record for `to` whose text is exactly
// text, with the given status. It is how a caller reports that a queued message
// left the composer some other way than through Dispatch — for instance because
// the delivery step found it hanging there and pressed Enter on it. Leaving such
// a record open is what makes the queue paste an already-delivered message a
// second time.
func (q *Queue) CloseDelivered(to, text, status string) (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	paths, err := filepath.Glob(filepath.Join(q.pending(), "*.json"))
	if err != nil {
		return "", false
	}
	sort.Strings(paths)
	for _, path := range paths {
		message, readErr := read(path)
		if readErr != nil || message.To != to || message.Msg != text {
			continue
		}
		if err := q.finish(path, message, status); err != nil {
			return "", false
		}
		return message.ID, true
	}
	return "", false
}

// Cancel removes a pending message by channel id, archiving it as canceled.
func (q *Queue) Cancel(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	path := filepath.Join(q.pending(), id+".json")
	message, err := read(path)
	if err != nil {
		return fmt.Errorf("no pending message %s: %w", id, err)
	}
	return q.finish(path, message, "canceled (by operator)")
}

// remember refreshes the reason recorded on a pending message so `bp q` and
// `bp qstat` can name why it is still waiting. It writes only when the reason
// actually changed, so a target that stays busy for an hour is rewritten once,
// not every five seconds. A failure to write is reported but never fatal: the
// message itself is untouched and delivery is unaffected.
func (q *Queue) remember(path string, message Message, reason string, report func(string)) {
	if message.Reason == reason {
		return
	}
	message.Reason = reason
	if err := writePending(path, message); err != nil && report != nil {
		report(fmt.Sprintf("msgq: could not record the reason for %s: %v", message.ID, err))
	}
}

// writePending rewrites a pending record in place, atomically. The temp file is
// created without a .json suffix so a concurrent List/Dispatch glob cannot pick
// it up half-written.
func writePending(path string, message Message) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".reason-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return err
	}
	err = json.NewEncoder(tmp).Encode(message)
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (q *Queue) finish(path string, message Message, status string) error {
	if err := os.MkdirAll(q.done(), 0755); err != nil {
		return err
	}
	message.Status = status
	message.Finished = float64(q.Now().UnixNano()) / 1e9
	target := filepath.Join(q.done(), message.ID+".json")
	tmp, err := os.CreateTemp(q.done(), ".done-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	err = json.NewEncoder(tmp).Encode(message)
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, target); err != nil {
		return err
	}
	return os.Remove(path)
}

// Dispatch makes one sorted pass. Busy or typed composers remain pending.
func (q *Queue) Dispatch(ctx context.Context, target Target, report func(string)) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := os.MkdirAll(q.Root, 0755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(q.Root, ".dispatch.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil
		}
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	paths, err := filepath.Glob(filepath.Join(q.pending(), "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for _, path := range paths {
		message, readErr := read(path)
		if readErr != nil {
			if report != nil {
				report(fmt.Sprintf("msgq: could not read %s: %v", path, readErr))
			}
			continue
		}
		if !target.HasSession(ctx, message.To) {
			if err := q.finish(path, message, "canceled (target closed)"); err != nil {
				if report != nil {
					report(fmt.Sprintf("msgq: could not finish %s: %v", message.ID, err))
				}
			}
			continue
		}
		pane, captureErr := target.Capture(ctx, message.To)
		paneAnsi, ansiErr := target.CaptureAnsi(ctx, message.To)
		if captureErr != nil || ansiErr != nil {
			continue
		}
		// The transcript is the witness, and it is asked FIRST — before the pane is
		// allowed to block anything. Whether the message already arrived has nothing
		// to do with what the composer looks like now, and a target whose composer
		// is stuck must still be able to close records for messages it did receive.
		if q.Witness != nil && q.Witness(message.To, message.Msg, time.Unix(0, int64(message.TS*1e9))) {
			if err := q.finish(path, message, "delivered (found in transcript)"); err != nil {
				if report != nil {
					report(fmt.Sprintf("msgq: could not finish %s: %v", message.ID, err))
				}
				continue
			}
			if report != nil {
				report(fmt.Sprintf("delivered earlier: %s -> %s (transcript'te bulundu, tekrar yazilmadi)", message.ID, message.To))
			}
			// Two proofs meeting: the transcript says this text was delivered, and
			// the composer may still be holding it from the paste whose Enter never
			// registered. Closing the record destroys the only evidence that the
			// hanging text is ours, so it would read as foreign from now on and block
			// the target forever. Clear it here, while the proof is still in hand —
			// and say so, because bp erasing a composer is never a silent act.
			cleared, clearErr := target.ClearDelivered(ctx, message.To, []string{message.Msg})
			if report != nil {
				switch {
				case cleared:
					report(fmt.Sprintf("msgq: %s composer'inda asili duran teslim edilmis metin C-u ile temizlendi (%s)", message.To, message.ID))
				case clearErr != nil:
					report(fmt.Sprintf("msgq: %s composer'i temizlenemedi (%v); elle bak: bp peek %s", message.To, clearErr, message.To))
				}
			}
			continue
		}
		if bptmux.Busy(pane) {
			q.remember(path, message, bptmux.BlockedByBusyPane, report)
			continue
		}
		// A non-empty composer used to end the attempt right here, and that is how
		// the queue blocked itself: our OWN unsubmitted paste of this very message
		// looked exactly like a busy human. Now the pane is asked WHY it cannot take
		// the message, and an empty answer means Send may proceed — including when
		// the composer holds text provably ours, which Send knows how to finish.
		// Every other answer is recorded on the queue record, so a message that
		// waits does not wait silently.
		if reason := bptmux.ComposerBlockReason(paneAnsi, []string{message.Msg}); reason != "" {
			q.remember(path, message, reason, report)
			continue
		}
		// The pane is free: drop any stale reason before handing over to Send.
		q.remember(path, message, "", report)
		if err := target.Send(ctx, message.To, message.Msg); err != nil {
			if errors.Is(err, bptmux.ErrNotAgent) {
				// The target dropped to a shell: leave the message PENDING (never
				// lose it, never type into the shell) and report the skip. A later
				// pass delivers it if the target becomes an agent again.
				if report != nil {
					report(fmt.Sprintf("msgq: %s -> %s skipped: target not an agent", message.ID, message.To))
				}
				continue
			}
			if errors.Is(err, bptmux.ErrTyping) {
				continue
			}
			if errors.Is(err, bptmux.ErrNotReady) {
				// PROVEN non-delivery (expired login, foreign composer): the
				// message stays PENDING for the next pass, and the reason is
				// reported so the state is visible instead of silent.
				if report != nil {
					report(fmt.Sprintf("msgq: %s -> %s not delivered (%v); still queued", message.ID, message.To, err))
				}
				continue
			}
			if errors.Is(err, bptmux.ErrUnverified) {
				// Injected and submitted, but nothing confirmed it. Re-sending
				// could deliver the same message twice, so the record is closed
				// with an honest status rather than retried.
				if err := q.finish(path, message, "delivered (unverified)"); err != nil && report != nil {
					report(fmt.Sprintf("msgq: could not finish %s: %v", message.ID, err))
				}
				if report != nil {
					report(fmt.Sprintf("delivered (UNVERIFIED): %s -> %s; check with bp peek %s", message.ID, message.To, message.To))
				}
				continue
			}
			if report != nil {
				report(fmt.Sprintf("msgq: could not send %s: %v", message.ID, err))
			}
			continue
		}
		if err := q.finish(path, message, "delivered"); err != nil {
			if report != nil {
				report(fmt.Sprintf("msgq: could not finish %s: %v", message.ID, err))
			}
			continue
		}
		if report != nil {
			report(fmt.Sprintf("delivered: %s -> %s", message.ID, message.To))
		}
	}
	return q.Cleanup()
}

func (q *Queue) Cleanup() error {
	paths, err := filepath.Glob(filepath.Join(q.done(), "q*.json"))
	if err != nil {
		return err
	}
	cutoff := q.Now().Add(-48 * time.Hour)
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
	return nil
}

func (q *Queue) Run(ctx context.Context, interval time.Duration, target Target, report func(string)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	work := make(chan struct{}, 1)
	work <- struct{}{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			select {
			case work <- struct{}{}:
			default:
			}
		case <-work:
			if err := q.Dispatch(ctx, target, report); err != nil && report != nil {
				report("msgq: " + err.Error())
			}
		}
	}
}
