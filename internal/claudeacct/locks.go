package claudeacct

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Claude Code guards its login with proper-lockfile directory locks: mkdir is
// the mutex, a holder touches the directory mtime while it works, and a lock
// whose mtime is older than its staleness belongs to a dead holder.
const (
	credentialsLockStale = 60 * time.Second
	configLockStale      = 10 * time.Second
	lockTouchInterval    = 3 * time.Second
	defaultLockTimeout   = 9 * time.Second
)

// LockBusyError means Claude Code held one of its locks past the wait budget.
type LockBusyError struct{ Lock string }

func (e *LockBusyError) Error() string {
	return fmt.Sprintf("Claude Code is refreshing credentials (%s is held); retry in a few seconds", filepath.Base(e.Lock))
}

type lockOptions struct {
	timeout time.Duration
	touch   time.Duration
}

// acquireDirLock takes one proper-lockfile lock. The returned release stops
// the toucher and removes the directory.
func acquireDirLock(ctx context.Context, dir string, stale time.Duration, opts lockOptions) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(dir), secretDirMode); err != nil {
		return nil, fmt.Errorf("prepare %s: %w", filepath.Base(dir), err)
	}
	deadline := time.Now().Add(opts.timeout)
	for {
		err := os.Mkdir(dir, secretDirMode)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("take %s: %w", filepath.Base(dir), err)
		}
		if time.Now().After(deadline) {
			return nil, &LockBusyError{Lock: dir}
		}
		info, statErr := os.Stat(dir)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr == nil && time.Since(info.ModTime()) > stale {
			// Dead holder: remove and retake. Losing the race to another
			// waiter only means another loop.
			if os.Remove(dir) != nil {
				time.Sleep(50 * time.Millisecond)
			}
			continue
		}
		wait := 250*time.Millisecond + time.Duration(rand.Int64N(int64(250*time.Millisecond)))
		select {
		case <-ctx.Done():
			return nil, &LockBusyError{Lock: dir}
		case <-time.After(wait):
		}
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(opts.touch)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				now := time.Now()
				if os.Chtimes(dir, now, now) != nil {
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			wg.Wait()
			_ = os.Remove(dir)
		})
	}, nil
}

// acquireClaudeLocks takes Claude Code's locks in its own order: the OAuth
// refresh lock, the legacy config-home lock, then the global config lock.
// Callers must not do network I/O while holding them.
func acquireClaudeLocks(ctx context.Context, paths ClaudePaths, opts lockOptions) (func(), error) {
	order := []struct {
		dir   string
		stale time.Duration
	}{
		{paths.refreshLock(), credentialsLockStale},
		{paths.legacyLock(), credentialsLockStale},
		{paths.configLock(), configLockStale},
	}
	var releases []func()
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, lock := range order {
		release, err := acquireDirLock(ctx, lock.dir, lock.stale, opts)
		if err != nil {
			releaseAll()
			return nil, err
		}
		releases = append(releases, release)
	}
	return releaseAll, nil
}
