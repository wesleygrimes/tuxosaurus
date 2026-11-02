package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/wesleygrimes/tuxosaurus/internal/rexclient"
)

// defaultSession is used by start when neither --session nor --slug is given.
const defaultSession = "rexctl"

type startArgs struct {
	session, label, cwd, worktree, slug string
	noMise, dryRun, closeOnExit         bool
	command                             []string
}

func parseStart(a *app, args []string) (startArgs, error) {
	var s startArgs
	fs := newFlags("start", &a.g)
	fs.StringVar(&s.session, "session", "", "session label or ID; created if missing (default --slug, else \"rexctl\")")
	fs.StringVar(&s.label, "label", "", "thread label (default --slug, else the command name)")
	fs.StringVar(&s.cwd, "cwd", "", "working directory on the server host")
	fs.StringVar(&s.worktree, "worktree", "", "run in this directory; same as --cwd")
	fs.StringVar(&s.slug, "slug", "", "name used for both session and thread when they are not given")
	fs.BoolVar(&s.noMise, "no-mise", false, "accepted for compatibility; has no effect")
	fs.BoolVar(&s.dryRun, "dry-run", false, "print the requests and exit")
	fs.BoolVar(&s.closeOnExit, "close-on-exit", false, "let the server remove the thread when its command exits")
	pos, err := parse(fs, args)
	if err != nil {
		return s, err
	}
	s.command = pos
	if s.cwd != "" && s.worktree != "" {
		return s, usagef("start: use --cwd or --worktree, not both")
	}
	if s.worktree != "" {
		s.cwd = s.worktree
	}
	if s.session == "" {
		s.session = s.slug
	}
	if s.session == "" {
		s.session = defaultSession
	}
	if s.label == "" {
		s.label = s.slug
	}
	if s.label == "" && len(s.command) > 0 {
		s.label = filepath.Base(s.command[0])
	}
	if s.cwd != "" && !filepath.IsAbs(s.cwd) {
		// The server resolves cwd on its own host. Relative paths are taken
		// from rexctl's directory, which is right when both run on one host.
		abs, err := filepath.Abs(s.cwd)
		if err != nil {
			return s, err
		}
		s.cwd = abs
	}
	return s, nil
}

func cmdStart(ctx context.Context, a *app, args []string) error {
	s, err := parseStart(a, args)
	if err != nil {
		return err
	}
	opts := rexclient.StartOptions{Session: s.session, Label: s.label, Cwd: s.cwd, Command: s.command, KeepOnExit: !s.closeOnExit}
	ctx, cancel := a.withTimeout(ctx, false)
	defer cancel()
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if s.dryRun {
		sessions, err := c.ListSessions(ctx)
		if err != nil {
			return err
		}
		plan := rexclient.StartPlan(sessions, opts)
		if a.g.json {
			return a.printJSON(plan)
		}
		for _, r := range plan {
			fmt.Fprintf(a.stdout, "%s %s\n", r.Method, mustJSON(r.Payload))
		}
		return nil
	}
	t, created, err := c.Start(ctx, opts)
	if err != nil {
		return err
	}
	if a.g.json {
		return a.printJSON(map[string]any{"session_id": t.Session.ID, "session_label": t.Session.Label, "block_id": t.Block.ID, "label": t.Block.Label, "session_created": created})
	}
	fmt.Fprintf(a.stdout, "session_id=%s\nblock_id=%s\n", t.Session.ID, t.Block.ID)
	return nil
}

func addSessionFlag(fs interface {
	StringVar(*string, string, string, string)
}, p *string) {
	fs.StringVar(p, "session", "", "look up THREAD only in this session")
}

func (a *app) findThread(ctx context.Context, sessionQuery, query string) (*rexclient.Client, rexclient.Thread, error) {
	c, err := a.connect(ctx)
	if err != nil {
		return nil, rexclient.Thread{}, err
	}
	t, err := c.FindThread(ctx, sessionQuery, query)
	if err != nil {
		c.Close()
		return nil, t, err
	}
	return c, t, nil
}

func cmdSend(ctx context.Context, a *app, args []string) error {
	var session string
	var enter bool
	var enterDelay time.Duration
	fs := newFlags("send", &a.g)
	addSessionFlag(fs, &session)
	fs.BoolVar(&enter, "enter", false, "press Enter after the text")
	fs.DurationVar(&enterDelay, "enter-delay", 150*time.Millisecond, "pause before Enter so TUIs do not read it as part of a paste")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usagef("usage: rexctl %s", commands["send"].usage)
	}
	text := strings.Join(pos[1:], " ")
	if len(pos) == 2 && pos[1] == "-" {
		b, err := io.ReadAll(a.stdin)
		if err != nil {
			return err
		}
		text = string(b)
	}
	ctx, cancel := a.withTimeout(ctx, false)
	defer cancel()
	c, t, err := a.findThread(ctx, session, pos[0])
	if err != nil {
		return err
	}
	defer c.Close()
	if text != "" {
		if err := c.Write(ctx, t.Session.ID, t.Block.ID, text); err != nil {
			return err
		}
	}
	if enter {
		if text != "" && enterDelay > 0 {
			select {
			case <-time.After(enterDelay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return c.Write(ctx, t.Session.ID, t.Block.ID, "\r")
	}
	return nil
}

func cmdKey(ctx context.Context, a *app, args []string) error {
	var session string
	fs := newFlags("key", &a.g)
	addSessionFlag(fs, &session)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usagef("usage: rexctl %s", commands["key"].usage)
	}
	data, err := rexclient.KeysBytes(pos[1:])
	if err != nil {
		return usageError{err.Error()}
	}
	ctx, cancel := a.withTimeout(ctx, false)
	defer cancel()
	c, t, err := a.findThread(ctx, session, pos[0])
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Write(ctx, t.Session.ID, t.Block.ID, data)
}

type readArgs struct {
	session    string
	lines      int
	vt, follow bool
	noTrim     bool
	thread     string
}

func parseRead(a *app, args []string) (readArgs, error) {
	var r readArgs
	fs := newFlags("read", &a.g)
	addSessionFlag(fs, &r.session)
	fs.IntVar(&r.lines, "lines", 0, "print only the last N lines (0 for all)")
	fs.BoolVar(&r.vt, "vt", false, "print with VT escape sequences (colors, cursor); raw stream with --follow")
	fs.BoolVar(&r.follow, "follow", false, "after the screen, stream new output until interrupted, timed out, or the process exits")
	fs.BoolVar(&r.noTrim, "no-trim", false, "keep trailing blank lines and spaces")
	pos, err := parse(fs, args)
	if err != nil {
		return r, err
	}
	if len(pos) != 1 {
		return r, usagef("usage: rexctl %s", commands["read"].usage)
	}
	if r.lines < 0 {
		return r, usagef("read: --lines must be >= 0")
	}
	r.thread = pos[0]
	return r, nil
}

func lastLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	trailing := strings.HasSuffix(s, "\n")
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := strings.Join(lines, "\n")
	if trailing {
		out += "\n"
	}
	return out
}

func cmdRead(ctx context.Context, a *app, args []string) error {
	r, err := parseRead(a, args)
	if err != nil {
		return err
	}
	ctx, cancel := a.withTimeout(ctx, r.follow)
	defer cancel()
	c, t, err := a.findThread(ctx, r.session, r.thread)
	if err != nil {
		return err
	}
	defer c.Close()
	var stream *rexclient.Stream
	if r.follow {
		// Open the stream before formatting so no output falls between the
		// two; a little may be printed twice instead.
		if stream, err = c.OpenStream(ctx, t.Session.ID, t.Block.ID); err != nil {
			return err
		}
		defer stream.Close()
	}
	format := "plain"
	if r.vt {
		format = "vt"
	}
	screen, err := c.Format(ctx, t.Session.ID, t.Block.ID, format, !r.noTrim)
	if err != nil {
		return err
	}
	screen = lastLines(screen, r.lines)
	if a.g.json && !r.follow {
		return a.printJSON(map[string]any{"session_id": t.Session.ID, "block_id": t.Block.ID, "format": format, "content": screen})
	}
	io.WriteString(a.stdout, screen)
	if screen != "" && !strings.HasSuffix(screen, "\n") {
		io.WriteString(a.stdout, "\n")
	}
	if !r.follow {
		return nil
	}
	return follow(ctx, stream, a.stdout, r.vt)
}

// follow copies stream output until the context ends, the stream closes or
// the child exits. Interrupts and timeouts end it without error.
func follow(ctx context.Context, s *rexclient.Stream, w io.Writer, raw bool) error {
	var plain rexclient.PlainFilter
	for {
		out, markers, err := s.Next(ctx)
		if len(out) > 0 {
			if !raw {
				out = plain.Write(out)
			}
			w.Write(out)
		}
		for _, m := range markers {
			if m.Type == "child_exited" {
				return nil
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return rexclient.ErrTimeout
				}
				return nil
			}
			return nil // stream closed: block gone
		}
	}
}

func cmdStatus(ctx context.Context, a *app, args []string) error {
	var session string
	fs := newFlags("status", &a.g)
	addSessionFlag(fs, &session)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usagef("usage: rexctl %s", commands["status"].usage)
	}
	ctx, cancel := a.withTimeout(ctx, false)
	defer cancel()
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	sessions, err := c.ListSessions(ctx)
	if err != nil {
		return err
	}
	if session != "" {
		s, err := rexclient.MatchSession(sessions, session)
		if err != nil {
			return err
		}
		sessions = []rexclient.Session{s}
	}
	threads, err := c.Threads(ctx, sessions)
	if err != nil {
		return err
	}
	single := false
	if len(pos) == 1 {
		// A thread name wins; otherwise treat the argument as a session.
		t, err := rexclient.MatchThread(threads, pos[0])
		switch {
		case err == nil:
			threads, single = []rexclient.Thread{t}, true
		case errors.Is(err, rexclient.ErrNotFound):
			s, serr := rexclient.MatchSession(sessions, pos[0])
			if serr != nil {
				return fmt.Errorf("no thread or session %q: %w", pos[0], rexclient.ErrNotFound)
			}
			var in []rexclient.Thread
			for _, t := range threads {
				if t.Session.ID == s.ID {
					in = append(in, t)
				}
			}
			threads = in
		default:
			return err
		}
	}
	statuses := make([]rexclient.Status, 0, len(threads))
	for _, t := range threads {
		if err := c.Attach(ctx, t.Session.ID); err != nil {
			return err
		}
		st, err := c.Status(ctx, t)
		if err != nil {
			if rexclient.NotFound(err) && !single {
				continue
			}
			return err
		}
		statuses = append(statuses, st)
	}
	if a.g.json {
		if single {
			return a.printJSON(statuses[0])
		}
		return a.printJSON(statuses)
	}
	printStatuses(a.stdout, statuses)
	return nil
}

func printStatuses(w io.Writer, statuses []rexclient.Status) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tTHREAD\tSTATE\tEXIT\tPROGRAM\tTITLE\tPWD\tID")
	for _, s := range statuses {
		exit := "-"
		if s.ExitCode != nil {
			exit = fmt.Sprint(*s.ExitCode)
		}
		program := "-"
		if s.Program != nil {
			program = s.Program.State
			if app := s.Program.Effective; app != "" {
				program = app + ":" + program
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.SessionLabel, s.Label, s.State, exit, program, dash(s.Title), dash(s.Pwd), s.BlockID)
	}
	tw.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type waitArgs struct {
	session    string
	thread     string
	opts       rexclient.WaitOptions
	exitStatus bool
}

func parseWait(a *app, args []string) (waitArgs, error) {
	var w waitArgs
	var pattern, idle string
	fs := newFlags("wait", &a.g)
	addSessionFlag(fs, &w.session)
	fs.StringVar(&w.opts.For, "for", "", "condition: exit, done, idle or pattern (default done, or pattern with --pattern)")
	fs.StringVar(&pattern, "pattern", "", "regular expression to find in the plain screen text; ^ and $ match at line ends")
	fs.StringVar(&idle, "idle", "5", "seconds without output that count as idle")
	fs.BoolVar(&w.exitStatus, "exit-status", false, "exit with the thread's exit code when it is known")
	pos, err := parse(fs, args)
	if err != nil {
		return w, err
	}
	if len(pos) != 1 {
		return w, usagef("usage: rexctl %s", commands["wait"].usage)
	}
	w.thread = pos[0]
	if w.opts.Idle, err = parseDuration(idle); err != nil {
		return w, usagef("wait: --idle: %v", err)
	}
	if pattern != "" {
		if w.opts.Pattern, err = regexp.Compile("(?m)" + pattern); err != nil {
			return w, usagef("wait: --pattern: %v", err)
		}
		if w.opts.For == "" {
			w.opts.For = "pattern"
		}
	}
	if w.opts.For == "" {
		w.opts.For = "done"
	}
	switch w.opts.For {
	case "exit", "done", "idle":
	case "pattern":
		if w.opts.Pattern == nil {
			return w, usagef("wait: --for pattern needs --pattern")
		}
	default:
		return w, usagef("wait: unknown --for %q", w.opts.For)
	}
	return w, nil
}

func cmdWait(ctx context.Context, a *app, args []string) error {
	w, err := parseWait(a, args)
	if err != nil {
		return err
	}
	ctx, cancel := a.withTimeout(ctx, true)
	defer cancel()
	// Lookups get their own short deadline so a bad name fails fast.
	lookupCtx, lookupCancel := context.WithTimeout(ctx, defaultTimeout)
	c, t, err := a.findThread(lookupCtx, w.session, w.thread)
	lookupCancel()
	if err != nil {
		return err
	}
	defer c.Close()
	res, err := c.Wait(ctx, t, w.opts)
	if err != nil && errors.Is(err, rexclient.ErrTimeout) || err == nil {
		if a.g.json {
			out := map[string]any{"condition": w.opts.For, "met": err == nil, "status": res.Status, "closed": res.Closed}
			if res.Match != "" {
				out["match"] = res.Match
			}
			a.printJSON(out)
		} else if res.Status.BlockID != "" {
			printStatuses(a.stdout, []rexclient.Status{res.Status})
		}
	}
	if err != nil {
		return err
	}
	if w.exitStatus && res.Status.ExitCode != nil {
		a.exitCode = *res.Status.ExitCode
	}
	return nil
}

func cmdStop(ctx context.Context, a *app, args []string) error {
	var session, signal string
	var grace time.Duration
	fs := newFlags("stop", &a.g)
	addSessionFlag(fs, &session)
	fs.StringVar(&signal, "signal", "", "\"int\" sends Ctrl-C and waits --grace before closing")
	fs.DurationVar(&grace, "grace", 5*time.Second, "how long to wait after Ctrl-C")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: rexctl %s", commands["stop"].usage)
	}
	if signal != "" && signal != "int" {
		return usagef("stop: --signal supports only \"int\"")
	}
	ctx, cancel := a.withTimeout(ctx, false)
	defer cancel()
	c, t, err := a.findThread(ctx, session, pos[0])
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Stop(ctx, t, signal == "int", grace); err != nil {
		return err
	}
	if a.g.json {
		return a.printJSON(map[string]any{"session_id": t.Session.ID, "block_id": t.Block.ID, "stopped": true})
	}
	return nil
}

func cmdLs(ctx context.Context, a *app, args []string) error {
	fs := newFlags("ls", &a.g)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usagef("usage: rexctl %s", commands["ls"].usage)
	}
	ctx, cancel := a.withTimeout(ctx, false)
	defer cancel()
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	sessions, err := c.ListSessions(ctx)
	if err != nil {
		return err
	}
	if len(pos) == 1 {
		s, err := rexclient.MatchSession(sessions, pos[0])
		if err != nil {
			return err
		}
		threads, err := c.Threads(ctx, []rexclient.Session{s})
		if err != nil {
			return err
		}
		type row struct {
			BlockID string `json:"block_id"`
			Label   string `json:"label"`
			State   string `json:"state"`
		}
		rows := []row{}
		for _, t := range threads {
			st, err := c.Status(ctx, t)
			state := st.State
			if err != nil {
				state = "unknown"
			}
			rows = append(rows, row{t.Block.ID, t.Block.Label, state})
		}
		if a.g.json {
			return a.printJSON(rows)
		}
		tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "THREAD\tSTATE\tID")
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Label, r.State, r.BlockID)
		}
		return tw.Flush()
	}
	type row struct {
		SessionID string `json:"session_id"`
		Label     string `json:"label"`
		Threads   int    `json:"threads"`
	}
	rows := []row{}
	for _, s := range sessions {
		threads, err := c.Threads(ctx, []rexclient.Session{s})
		if err != nil {
			return err
		}
		rows = append(rows, row{s.ID, s.Label, len(threads)})
	}
	if a.g.json {
		return a.printJSON(rows)
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tTHREADS\tID")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%d\t%s\n", r.Label, r.Threads, r.SessionID)
	}
	return tw.Flush()
}

func cmdClose(ctx context.Context, a *app, args []string) error {
	fs := newFlags("close", &a.g)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: rexctl %s", commands["close"].usage)
	}
	ctx, cancel := a.withTimeout(ctx, false)
	defer cancel()
	c, err := a.connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	s, err := c.FindSession(ctx, pos[0])
	if err != nil {
		return err
	}
	if err := c.Attach(ctx, s.ID); err != nil {
		return err
	}
	if err := c.DestroySession(ctx, s.ID); err != nil {
		return err
	}
	if a.g.json {
		return a.printJSON(map[string]any{"session_id": s.ID, "closed": true})
	}
	return nil
}
