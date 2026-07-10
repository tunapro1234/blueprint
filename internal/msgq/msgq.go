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

const DefaultRoot = "/srv/server-main/msgq"

type Message struct {
	ID       string  `json:"id"`
	To       string  `json:"to"`
	From     string  `json:"from"`
	Msg      string  `json:"msg"`
	TS       float64 `json:"ts"`
	Status   string  `json:"status,omitempty"`
	Finished float64 `json:"finished,omitempty"`
}

type Queue struct {
	Root string
	Now  func() time.Time
	mu   sync.Mutex
}

func New(root string) *Queue {
	if root == "" {
		root = DefaultRoot
	}
	return &Queue{Root: root, Now: time.Now}
}

func (q *Queue) pending() string { return filepath.Join(q.Root, "pending") }
func (q *Queue) done() string    { return filepath.Join(q.Root, "done") }

func (q *Queue) Enqueue(to, from, text string) (string, error) {
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
	message := Message{ID: base, To: to, From: from, Msg: text, TS: float64(now.UnixNano()) / 1e9}
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
		if captureErr != nil || ansiErr != nil || bptmux.Typing(paneAnsi) || bptmux.Busy(pane) {
			continue
		}
		if err := target.Send(ctx, message.To, message.Msg); err != nil {
			if errors.Is(err, bptmux.ErrTyping) {
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
