package rexclient

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Wait blocks until the condition holds or ctx ends. Child exit is seen from
// three sources: the child_exited block event, the child-exit marker on the
// data stream, and com.superlogical.terminal.process polling. Any one of them
// is enough, so a missed event does not hang the wait. If the block
// disappears (the server closes blocks whose process exited unless they were
// started with KeepOnExit), exit and done conditions are met with an
// unknown exit code unless an event reported it first.
func (c *Client) Wait(ctx context.Context, t Thread, o WaitOptions) (WaitResult, error) {
	if o.Poll <= 0 {
		o.Poll = time.Second
	}
	switch o.For {
	case "":
		o.For = "done"
	case "exit", "done":
	case "idle":
		if o.Idle <= 0 {
			o.Idle = 5 * time.Second
		}
	case "pattern":
		if o.Pattern == nil {
			return WaitResult{}, fmt.Errorf("wait: pattern is required")
		}
	default:
		return WaitResult{}, fmt.Errorf("wait: unknown condition %q", o.For)
	}
	events, unsubscribe := c.Subscribe()
	defer unsubscribe()
	if err := c.Attach(ctx, t.Session.ID); err != nil {
		return WaitResult{}, err
	}

	// The data stream signals output (for idle and as a cheap recheck
	// trigger) and carries the child-exit marker.
	output := make(chan struct{}, 1)
	exits := make(chan int, 1)
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	if stream, err := c.OpenStream(ctx, t.Session.ID, t.Block.ID); err == nil {
		defer stream.Close()
		go func() {
			for {
				out, markers, err := stream.Next(streamCtx)
				for _, m := range markers {
					if m.Type == "child_exited" && m.ExitCode != nil {
						select {
						case exits <- *m.ExitCode:
						default:
						}
					}
				}
				if len(out) > 0 {
					select {
					case output <- struct{}{}:
					default:
					}
				}
				if err != nil {
					return
				}
			}
		}()
	}

	var exitCode *int
	lastOutput := time.Now()
	ticker := time.NewTicker(o.Poll)
	defer ticker.Stop()
	var res WaitResult
	for {
		var met bool
		var err error
		res, met, err = c.checkWait(ctx, t, o, exitCode, lastOutput)
		if err != nil {
			if ctx.Err() != nil {
				return res, ErrTimeout
			}
			return res, err
		}
		if met {
			return res, nil
		}
		if res.Closed {
			return res, fmt.Errorf("thread closed before the pattern appeared: %w", ErrNotFound)
		}
	wait:
		select {
		case <-ctx.Done():
			return res, ErrTimeout
		case <-ticker.C:
		case <-output:
			lastOutput = time.Now()
			if o.For == "idle" {
				// Output only postpones idleness; the ticker rechecks.
				goto wait
			}
		case code := <-exits:
			exitCode = &code
		case ev, ok := <-events:
			if !ok {
				return res, fmt.Errorf("wait: connection closed")
			}
			be, isBlock := ev.AsBlockEvent()
			if !isBlock || be.BlockID != t.Block.ID {
				if _, kind, payload := ev.SessionEventType(); kind == "block_closed" && blockIDIn(payload) == t.Block.ID {
					break
				}
				goto wait
			}
			if be.Name == "child_exited" {
				var p struct {
					ExitCode *int `json:"exit_code"`
				}
				if json.Unmarshal(be.Payload, &p) == nil && p.ExitCode != nil {
					exitCode = p.ExitCode
				}
			}
		}
	}
}

func blockIDIn(payload json.RawMessage) string {
	var p struct {
		BlockID string `json:"block_id"`
	}
	json.Unmarshal(payload, &p)
	return p.BlockID
}

func (c *Client) checkWait(ctx context.Context, t Thread, o WaitOptions, exitCode *int, lastOutput time.Time) (WaitResult, bool, error) {
	st, err := c.Status(ctx, t)
	if NotFound(err) {
		st.State, st.Reason, st.ExitCode = StateExited, "block closed", exitCode
		res := WaitResult{Status: st, Closed: true}
		return res, o.For != "pattern", nil
	}
	if err != nil {
		return WaitResult{Status: st}, false, err
	}
	if st.ExitCode == nil && exitCode != nil {
		// The event arrived before process polling caught up.
		st.ExitCode = exitCode
		st.State, st.Reason = StateExited, "child exit reported"
	}
	res := WaitResult{Status: st}
	switch o.For {
	case "exit":
		return res, st.ExitCode != nil, nil
	case "done":
		return res, st.Done(), nil
	case "idle":
		return res, st.ExitCode != nil || time.Since(lastOutput) >= o.Idle, nil
	case "pattern":
		screen, err := c.Format(ctx, t.Session.ID, t.Block.ID, "text", true)
		if err != nil {
			return res, false, err
		}
		if loc := o.Pattern.FindStringIndex(screen); loc != nil {
			res.Match = screen[loc[0]:loc[1]]
			return res, true, nil
		}
	}
	return res, false, nil
}
