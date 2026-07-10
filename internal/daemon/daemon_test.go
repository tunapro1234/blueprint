package daemon

import (
	"context"
	"testing"
	"time"
)

func TestStartLoopHonorsInitialDelay(t *testing.T) {
	service := &Service{}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	service.startLoop(ctx, "test", 80*time.Millisecond, time.Hour, func(context.Context, time.Duration) {
		started <- struct{}{}
	})

	select {
	case <-started:
		cancel()
		service.wg.Wait()
		t.Fatal("work ran before the initial delay")
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		service.wg.Wait()
		t.Fatal("work did not run after the initial delay")
	}
	cancel()
	service.wg.Wait()
}
