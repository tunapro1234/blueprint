package tmux

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

// --- the pane lock: one bp writer per pane, ACROSS PROCESSES -----------------
//
// Every other guard in this package protects a pane from the agent and from the
// human: Busy, Typing, the paste verification. Nothing protected it from bp
// ITSELF, and on 2026-08-17 that cost a delivery. `bp open probot-outreach`
// flushed a two-item announcement digest into the composer while, in a different
// process, `bp msg` pasted probot-business's instruction into the SAME composer;
// one Enter submitted both and the agent read a single 590-character message with
// two senders inside it. Neither process did anything wrong on its own — they
// were simply looking at, and typing into, one pane at the same moment.
//
// So the whole critical section — capture, verify, paste, submit, verify — is
// made exclusive per TARGET SESSION. The lock is an flock on a file under the
// shared message-queue root, for two reasons: that directory (config.msgqRoot) is
// the one path every bp on this machine already resolves from the same config
// key, so two processes cannot end up locking different files; and an flock dies
// with the process, so a bp killed mid-paste cannot leave a pane locked forever.
//
// LOCK ORDER, once and for all: the queue's .dispatch.lock is taken BEFORE a pane
// lock, never after. Dispatch is the only code that holds both.

const (
	// paneLockDir is the subdirectory of the shared queue root the lock files live
	// in. It is created with 0755 so bp and the daemon (same machine, same owner
	// today) can both open it.
	paneLockDir = ".panelock"
	// paneLockPoll is the retry interval. flock has no timed wait in the standard
	// library, so the wait is a poll; 100 ms is short enough that it does not
	// noticeably extend a normal hold.
	paneLockPoll = 100 * time.Millisecond
)

// PaneLockWait bounds an acquire. It is sized on the longest HONEST hold: a full
// Send is a paste, a settle window and two verifying captures, measured at a few
// seconds. Ten seconds covers that with margin, and giving up costs little — the
// caller queues the message and the next dispatch pass delivers it.
//
// It is a variable rather than a constant for one reason: a test that proves a
// collision would otherwise have to sit out the full budget.
var PaneLockWait = 10 * time.Second

// BlockedByPaneLock is what a waiting queue record says while another bp process
// holds this pane. It belongs next to the other BlockedBy… reasons: from the
// operator's side it is one more kind of "not now", and it is the only one that
// points at bp itself rather than at the agent or a human.
const BlockedByPaneLock = "baska bir bp sureci bu pane'e yaziyor (pane kilidi)"

// ErrPaneLocked says another bp process is inside the critical section for this
// pane. It WRAPS ErrBusy on purpose: every caller already knows how to treat a
// busy pane (queue the message, record the reason, try again later), and "another
// bp is typing there" needs exactly that treatment.
var ErrPaneLocked = fmt.Errorf("%w: %s", ErrBusy, BlockedByPaneLock)

// ErrDialog is a pane waiting on a human decision (a Hermes permission prompt).
// It wraps ErrBusy on purpose: every caller that already knows how to wait for a
// working pane — the queue, the CLI, the WhatsApp bridge — waits for this one
// too without a line of new code. What differs is the REASON shown to the
// operator, because this state does not clear on its own (see BlockedByDialog).
var ErrDialog = fmt.Errorf("%w: %s", ErrBusy, BlockedByDialog)

// AcquirePaneLock takes the exclusive lock for one target session and returns the
// function that releases it. dir is the shared queue root (config.msgqRoot); the
// package deliberately holds no path of its own, so the caller's config stays the
// single source of truth for where the lock lives.
//
// It waits up to paneLockWait and then returns ErrPaneLocked, which is an ErrBusy:
// the caller queues the message instead of typing into a pane somebody else is
// already typing into.
//
// NOT REENTRANT. flock treats two file descriptors of the same process as
// competitors, so asking twice for one pane deadlocks against itself until the
// wait runs out. Code paths that nest (compact: clear the composer, then deliver)
// must hold ONE lock around both steps — cmd/bp does that with a per-process
// counter.
//
// An empty dir or session yields a no-op release and no error: there is no shared
// directory to coordinate through, which is the case only in tests and in an app
// assembled without config.
func AcquirePaneLock(dir, session string) (func(), error) {
	return acquirePaneLock(dir, session, PaneLockWait)
}

// acquirePaneLock is AcquirePaneLock with the wait budget in the caller's hands,
// so a test can prove the collision without waiting ten seconds for it.
func acquirePaneLock(dir, session string, budget time.Duration) (func(), error) {
	if dir == "" || session == "" {
		return func() {}, nil
	}
	base := filepath.Join(dir, paneLockDir)
	if err := os.MkdirAll(base, 0755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(base, paneLockName(session)+".lock"), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(budget)
	for {
		lockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(lockErr, syscall.EWOULDBLOCK) && !errors.Is(lockErr, syscall.EAGAIN) {
			_ = file.Close()
			return nil, lockErr
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, ErrPaneLocked
		}
		time.Sleep(paneLockPoll)
	}
}

// paneLockUnsafe matches everything a lock FILE name may not contain. Session
// names are tmux-legal, not path-legal: a name with a "/" in it would put the
// lock file in some other directory, where it would guard nothing at all. Two
// names differing only in an unsafe character collapse onto one lock, which is
// the harmless direction — more mutual exclusion, never less.
var paneLockUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func paneLockName(session string) string {
	return paneLockUnsafe.ReplaceAllString(session, "_")
}
