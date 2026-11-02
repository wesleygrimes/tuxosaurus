package rexclient

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// StartOptions describes a new thread.
type StartOptions struct {
	Session string   // session label or ID; created with this label if missing
	Label   string   // block label
	Cwd     string   // working directory on the server host
	Command []string // program and arguments; empty starts the default shell
	// KeepOnExit asks the server to keep the block after its process exits,
	// so output and the exit code stay readable. Tuxosaurus implements this
	// as the shell option exit.on_completion=false.
	KeepOnExit bool
}

// BlockSpec returns the block description for these options.
func (o StartOptions) BlockSpec() BlockSpec {
	opts := map[string]any{}
	if len(o.Command) > 0 {
		opts["command"] = o.Command
	}
	if o.Cwd != "" {
		opts["cwd"] = o.Cwd
	}
	if o.KeepOnExit {
		opts["exit"] = map[string]any{"on_completion": false}
	}
	return BlockSpec{Label: o.Label, Options: opts}
}

// Request is one control request, used for --dry-run output.
type Request struct {
	Method  string `json:"method"`
	Payload any    `json:"payload"`
}

// StartPlan returns the requests Start would send given the current
// sessions. A placeholder stands in for a session ID not yet created.
func StartPlan(sessions []Session, o StartOptions) []Request {
	spec := o.BlockSpec()
	if s, err := MatchSession(sessions, o.Session); err == nil {
		return []Request{
			{"session.attach", map[string]any{"session_id": s.ID}},
			{"session.new_window", NewWindowRequest(s.ID, spec)},
		}
	}
	return []Request{
		{"session.create", CreateSessionRequest(o.Session, &spec)},
		{"session.attach", map[string]any{"session_id": "<new session_id>"}},
	}
}

// Start creates the session if needed and a thread in it.
func (c *Client) Start(ctx context.Context, o StartOptions) (Thread, bool, error) {
	if o.Session == "" {
		return Thread{}, false, fmt.Errorf("start: a session label is required")
	}
	sessions, err := c.ListSessions(ctx)
	if err != nil {
		return Thread{}, false, err
	}
	spec := o.BlockSpec()
	s, err := MatchSession(sessions, o.Session)
	created := false
	var bid string
	switch {
	case err == nil:
		if err := c.Attach(ctx, s.ID); err != nil {
			return Thread{}, false, err
		}
		bid, err = c.NewWindow(ctx, s.ID, spec)
		if err != nil {
			return Thread{}, false, err
		}
	case errors.Is(err, ErrNotFound):
		s = Session{Label: o.Session}
		s.ID, bid, err = c.CreateSession(ctx, o.Session, &spec)
		if err != nil {
			return Thread{}, false, err
		}
		created = true
		if err := c.Attach(ctx, s.ID); err != nil {
			return Thread{}, false, err
		}
	default:
		return Thread{}, false, err
	}
	if bid == "" {
		return Thread{}, created, fmt.Errorf("start: server did not return a block ID")
	}
	label := o.Label
	if label == "" {
		label = "shell"
	}
	return Thread{Session: s, Block: Block{ID: bid, Label: label, Flavor: ShellFlavor}}, created, nil
}

// ErrTimeout is returned by Wait when its context ends first.
var ErrTimeout = errors.New("timed out")

// WaitOptions selects a wait condition.
type WaitOptions struct {
	For     string         // "exit", "done", "idle" or "pattern"
	Pattern *regexp.Regexp // for "pattern": matched against the plain screen
	Idle    time.Duration  // for "idle": quiet period with no output
	// Poll is the fallback status interval; events trigger earlier checks.
	Poll time.Duration
}

// WaitResult is the final state seen by Wait.
type WaitResult struct {
	Status Status `json:"status"`
	Closed bool   `json:"closed"` // block no longer exists
	Match  string `json:"match,omitempty"`
}

// Stop ends a thread. With interrupt, it first sends Ctrl-C and waits up to
// grace for the process to exit. It then closes the block, which makes the
// server hang up the process group.
func (c *Client) Stop(ctx context.Context, t Thread, interrupt bool, grace time.Duration) error {
	if interrupt {
		if err := c.Write(ctx, t.Session.ID, t.Block.ID, "\x03"); err != nil {
			return err
		}
		wctx, cancel := context.WithTimeout(ctx, grace)
		_, err := c.Wait(wctx, t, WaitOptions{For: "exit", Poll: 200 * time.Millisecond})
		cancel()
		if err != nil && errors.Is(err, ErrNotFound) {
			return nil // already closed by the server after exit
		}
	}
	err := c.CloseBlock(ctx, t.Session.ID, t.Block.ID)
	if NotFound(err) {
		return nil
	}
	return err
}
