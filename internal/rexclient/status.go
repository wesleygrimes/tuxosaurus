package rexclient

import (
	"context"
	"path/filepath"
	"sort"
)

// Thread states reported by rexctl.
const (
	StateRunning = "running"
	StateWaiting = "waiting"
	StateDone    = "done"
	StateError   = "error"
	StateExited  = "exited"
)

type ProcessInfo struct {
	PID  int    `json:"pid"`
	Name string `json:"name"`
	Cwd  string `json:"cwd,omitempty"`
}

type ProgramRecord struct {
	ID        string `json:"id,omitempty"`
	State     string `json:"state"`
	Kind      string `json:"kind,omitempty"`
	App       string `json:"app,omitempty"`
	Effective string `json:"effective_app,omitempty"`
	Title     string `json:"title,omitempty"`
	Msg       string `json:"msg,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Status is a snapshot of one thread.
type Status struct {
	SessionID    string         `json:"session_id"`
	SessionLabel string         `json:"session_label"`
	BlockID      string         `json:"block_id"`
	Label        string         `json:"label"`
	State        string         `json:"state"`
	Reason       string         `json:"reason"`
	ExitCode     *int           `json:"exit_code,omitempty"`
	Title        string         `json:"title,omitempty"`
	Pwd          string         `json:"pwd,omitempty"`
	Child        *ProcessInfo   `json:"child,omitempty"`
	Foreground   *ProcessInfo   `json:"foreground,omitempty"`
	Program      *ProgramRecord `json:"program,omitempty"`
}

// Done reports whether the thread has finished its work.
func (s Status) Done() bool {
	return s.State == StateDone || s.State == StateError || s.State == StateExited
}

type processResult struct {
	LastExit *struct {
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error"`
	} `json:"last_exit"`
	Child      *ProcessInfo `json:"child"`
	Foreground *ProcessInfo `json:"foreground"`
}

// Status gathers process, program status, title and working directory for
// a thread. Each source is optional: a server that rejects a method simply
// contributes nothing, so the result degrades instead of failing.
func (c *Client) Status(ctx context.Context, t Thread) (Status, error) {
	st := Status{SessionID: t.Session.ID, SessionLabel: t.Session.Label, BlockID: t.Block.ID, Label: t.Block.Label}
	var proc processResult
	if err := c.Terminal(ctx, t.Session.ID, t.Block.ID, "process", nil, &proc); err != nil {
		if NotFound(err) {
			return st, err
		}
	}
	var programs struct {
		Records []ProgramRecord `json:"records"`
	}
	c.Terminal(ctx, t.Session.ID, t.Block.ID, "program_status", nil, &programs)
	var title struct {
		Title string `json:"title"`
	}
	c.Terminal(ctx, t.Session.ID, t.Block.ID, "title", nil, &title)
	var pwd struct {
		Pwd string `json:"pwd"`
	}
	c.Terminal(ctx, t.Session.ID, t.Block.ID, "pwd", nil, &pwd)
	st.Title, st.Pwd = title.Title, pwd.Pwd
	st.Child, st.Foreground = proc.Child, proc.Foreground
	if proc.LastExit != nil {
		code := proc.LastExit.ExitCode
		st.ExitCode = &code
	}
	st.Program = latestProgram(programs.Records)
	st.State, st.Reason = deriveState(st)
	return st, nil
}

func latestProgram(records []ProgramRecord) *ProgramRecord {
	if len(records) == 0 {
		return nil
	}
	// RFC 3339 timestamps from one server sort lexically.
	sort.SliceStable(records, func(i, j int) bool { return records[i].UpdatedAt < records[j].UpdatedAt })
	r := records[len(records)-1]
	return &r
}

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ash": true, "ksh": true, "mksh": true, "tcsh": true, "csh": true, "nu": true, "elvish": true, "xonsh": true}

func isShell(name string) bool {
	name = filepath.Base(name)
	if len(name) > 0 && name[0] == '-' {
		name = name[1:]
	}
	return shells[name]
}

// deriveState applies the heuristics documented in docs/client.md, in order:
// a recorded child exit wins; then the newest program status record (sent by
// programs through OSC or inferred by the server for tools such as Claude
// Code and Codex); then the foreground process, where a shell in the
// foreground means the thread is at a prompt.
func deriveState(st Status) (string, string) {
	if st.ExitCode != nil {
		return StateExited, "child process exited"
	}
	if p := st.Program; p != nil {
		switch p.State {
		case "working":
			return StateRunning, "program status working"
		case "blocked":
			reason := "program status blocked"
			if p.Kind != "" {
				reason += " (" + p.Kind + ")"
			}
			return StateWaiting, reason
		case "idle":
			return StateWaiting, "program status idle"
		case "done":
			return StateDone, "program status done"
		case "error":
			return StateError, "program status error"
		}
	}
	if fg := st.Foreground; fg != nil {
		// The Mac server starts an interactive shell under login(1), so
		// the child is login and the shell is its child.
		if isShell(fg.Name) && (st.Child == nil || st.Child.PID == fg.PID || filepath.Base(st.Child.Name) == "login") {
			return StateWaiting, "shell is in the foreground"
		}
		return StateRunning, "foreground process " + fg.Name
	}
	return StateRunning, "no status information"
}
