package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrWaitTimeout      = errors.New("workflow wait timed out")
	ErrAgentUnavailable = errors.New("agent is closed or runtime is unknown")
)

type WaitResult struct {
	Agent       string      `json:"agent"`
	Until       AgentState  `json:"until"`
	Confirmed   int         `json:"confirmed"`
	Observation Observation `json:"observation"`
	TimedOut    bool        `json:"timed_out,omitempty"`
}

// Wait observes one agent until its requested state is confirmed. It never
// sends input to the agent.
func Wait(ctx context.Context, driver Driver, agent string, until AgentState, confirm int, timeout, poll time.Duration, now func() time.Time, sleep func(context.Context, time.Duration) error) (WaitResult, error) {
	if driver == nil {
		return WaitResult{}, errors.New("workflow wait needs a driver")
	}
	if until != Idle && until != Working {
		return WaitResult{}, fmt.Errorf("wait target must be idle or working")
	}
	if confirm <= 0 {
		confirm = 1
	}
	if poll <= 0 {
		poll = PollInterval
	}
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = sleepContext
	}
	var deadline time.Time
	if timeout > 0 {
		deadline = now().Add(timeout)
	}
	result := WaitResult{Agent: agent, Until: until}
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		observation, err := driver.Observe(ctx, agent)
		if err != nil {
			return result, fmt.Errorf("observe %s: %w", agent, err)
		}
		if observation.ObservedAt.IsZero() {
			observation.ObservedAt = now().UTC()
		}
		result.Observation = observation
		switch observation.State {
		case Dead:
			return result, fmt.Errorf("%w: %s", ErrAgentUnavailable, observationSummary(observation))
		case Unknown:
			result.Confirmed = 0
		case AgentState(until):
			result.Confirmed++
			if result.Confirmed >= confirm {
				return result, nil
			}
		default:
			result.Confirmed = 0
		}
		if !deadline.IsZero() && !now().Before(deadline) {
			result.TimedOut = true
			return result, ErrWaitTimeout
		}
		if err := sleep(ctx, poll); err != nil {
			return result, err
		}
	}
}
