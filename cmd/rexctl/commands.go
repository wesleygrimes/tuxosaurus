package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/wesleygrimes/tuxosaurus/internal/rexclient"
)

type startArgs struct {
	bot, run, cwd, label, worktree, slug string
	noMise, dryRun, closeOnExit          bool
	command                              []string
}

func parseStart(a *app, args []string) (startArgs, error) {
	var s startArgs
	fs := newFlags("start", &a.g)
	fs.StringVar(&s.label, "label", "", "window label (default RUN)")
	fs.StringVar(&s.worktree, "worktree", "", "git branch to run in, such as wes/fix-login; its worktree is made next to CWD if missing")
	fs.StringVar(&s.slug, "slug", "", "worktree directory suffix (default the branch without wes/, with / as -)")
	fs.BoolVar(&s.noMise, "no-mise", false, "accepted for compatibility; has no effect")
	fs.BoolVar(&s.dryRun, "dry-run", false, "print the requests and exit")
	fs.BoolVar(&s.closeOnExit, "close-on-exit", false, "let the server remove the run when its command exits")
	pos, err := parse(fs, args)
	if err != nil {
		return s, err
	}
	if len(pos) < 3 || pos[0] == "" || pos[1] == "" || pos[2] == "" {
		return s, usagef("usage: rexctl %s", commands["start"].usage)
	}
	s.bot, s.run, s.cwd, s.command = pos[0], pos[1], pos[2], pos[3:]
	if s.slug != "" && s.worktree == "" {
		return s, usagef("start: --slug needs --worktree")
	}
	server, err := a.server()
	if err != nil {
		return s, err
	}
	if s.cwd, err = resolveCwd(s.cwd, localServer(server)); err != nil {
		return s, err
	}
	if s.worktree != "" && s.cwd == "" {
		return s, usagef("start: --worktree needs an absolute CWD on a remote server")
	}
	if s.worktree != "" {
		if strings.HasPrefix(s.worktree, "-") || strings.ContainsAny(s.worktree, " \t\n") {
			return s, usagef("start: bad --worktree branch %q", s.worktree)
		}
		if s.slug == "" {
			s.slug = strings.ReplaceAll(strings.TrimPrefix(s.worktree, "wes/"), "/", "-")
		}
		if s.slug == "" || strings.Contains(s.slug, "/") || s.slug == "." || s.slug == ".." {
			return s, usagef("start: bad --slug %q", s.slug)
		}
	}
	return s, nil
}

// resolveCwd turns CWD into the path sent to the server, which uses it on
// its own host. For a local server, ~ and relative paths are resolved here.
// A remote server needs an absolute path, because neither server expands ~
// and the Mac server silently falls back to the home directory for a path it
// cannot use. Bare ~ on a remote server sends no cwd, which means its home.
func resolveCwd(cwd string, local bool) (string, error) {
	if filepath.IsAbs(cwd) {
		return filepath.Clean(cwd), nil
	}
	if !local {
		if cwd == "~" {
			return "", nil
		}
		return "", usagef("start: CWD %q must be absolute (or ~) on a remote server", cwd)
	}
	if cwd == "~" || strings.HasPrefix(cwd, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(cwd, "~")), nil
	}
	return filepath.Abs(cwd)
}

// localServer reports whether the server shares rexctl's host: a unix
// socket or a loopback address.
func localServer(raw string) bool {
	if strings.HasPrefix(raw, "unix://") {
		return true
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// worktreeScript runs as sh -c SCRIPT rexctl-worktree REPO DIR BRANCH CMD...
// It adds the worktree when DIR is missing, then runs CMD (or a shell) in it.
const worktreeScript = `set -e
repo=$1 dir=$2 branch=$3
shift 3
if [ ! -d "$dir" ]; then
  if git -C "$repo" show-ref --verify --quiet "refs/heads/$branch"; then
    git -C "$repo" worktree add "$dir" "$branch"
  else
    git -C "$repo" worktree add -b "$branch" "$dir"
  fi
fi
cd "$dir"
[ $# -gt 0 ] || set -- "${SHELL:-/bin/sh}"
exec "$@"`

// worktreeDir is where start --worktree puts the worktree: a sibling of
// CWD named CWD-SLUG.
func worktreeDir(cwd, slug string) string {
	return filepath.Join(filepath.Dir(cwd), filepath.Base(cwd)+"-"+slug)
}

func (s startArgs) options() rexclient.StartOptions {
	cmd := s.command
	if s.worktree != "" {
		cmd = append([]string{"/bin/sh", "-c", worktreeScript, "rexctl-worktree", s.cwd, worktreeDir(s.cwd, s.slug), s.worktree}, s.command...)
	}
	return rexclient.StartOptions{Session: s.bot, Label: s.run, WindowLabel: s.label, Cwd: s.cwd, Command: cmd, KeepOnExit: !s.closeOnExit}
}

func cmdStart(ctx context.Context, a *app, args []string) error {
	s, err := parseStart(a, args)
	if err != nil {
		return err
	}
	opts := s.options()
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
	// RUN names one run in BOT, so refuse to start a second with that name.
	if bot, err := rexclient.MatchSession(sessions, s.bot); err == nil {
		threads, err := c.Threads(ctx, []rexclient.Session{bot})
		if err != nil {
			return err
		}
		for _, t := range threads {
			if t.Block.Label == s.run {
				return fmt.Errorf("run %q already exists in %q; stop it first", s.run, s.bot)
			}
		}
	}
	if s.dryRun {
		plan := rexclient.StartPlan(sessions, opts)
		if a.g.json {
			return a.printJSON(plan)
		}
		if s.worktree != "" {
			fmt.Fprintf(a.stdout, "# worktree %s on branch %s\n", worktreeDir(s.cwd, s.slug), s.worktree)
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

// botRun splits the BOT RUN positionals every thread command starts with.
func botRun(name string, pos []string, min, max int) (bot, run string, rest []string, err error) {
	if len(pos) < min || (max >= 0 && len(pos) > max) {
		return "", "", nil, usagef("usage: rexctl %s", commands[name].usage)
	}
	return pos[0], pos[1], pos[2:], nil
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
	var enter bool
	var enterDelay time.Duration
	fs := newFlags("send", &a.g)
	fs.BoolVar(&enter, "enter", false, "press Enter after the text")
	fs.DurationVar(&enterDelay, "enter-delay", 150*time.Millisecond, "pause before Enter so TUIs do not read it as part of a paste")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	bot, run, rest, err := botRun("send", pos, 3, 3)
	if err != nil {
		return err
	}
	text := rest[0]
	if text == "-" {
		b, err := io.ReadAll(a.stdin)
		if err != nil {
			return err
		}
		text = string(b)
	}
	ctx, cancel := a.withTimeout(ctx, false)
	defer cancel()
	c, t, err := a.findThread(ctx, bot, run)
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
	fs := newFlags("key", &a.g)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	bot, run, keys, err := botRun("key", pos, 3, -1)
	if err != nil {
		return err
	}
	data, err := rexclient.KeysBytes(keys)
	if err != nil {
		return usageError{err.Error()}
	}
	ctx, cancel := a.withTimeout(ctx, false)
	defer cancel()
	c, t, err := a.findThread(ctx, bot, run)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Write(ctx, t.Session.ID, t.Block.ID, data)
}

type readArgs struct {
	bot, run   string
	lines      int
	vt, follow bool
	noTrim     bool
}

func parseRead(a *app, args []string) (readArgs, error) {
	var r readArgs
	fs := newFlags("read", &a.g)
	fs.IntVar(&r.lines, "tail", 0, "print only the last N lines (0 for all)")
	fs.IntVar(&r.lines, "lines", 0, "same as --tail")
	fs.BoolVar(&r.vt, "vt", false, "print with VT escape sequences (colors, cursor); raw stream with --follow")
	fs.BoolVar(&r.follow, "follow", false, "after the screen, stream new output until interrupted, timed out, or the process exits")
	fs.BoolVar(&r.noTrim, "no-trim", false, "keep trailing blank lines and spaces")
	pos, err := parse(fs, args)
	if err != nil {
		return r, err
	}
	if r.bot, r.run, _, err = botRun("read", pos, 2, 2); err != nil {
		return r, err
	}
	if r.lines < 0 {
		return r, usagef("read: --tail must be >= 0")
	}
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
	c, t, err := a.findThread(ctx, r.bot, r.run)
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
	format := "text"
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
	fs := newFlags("status", &a.g)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	bot, run, _, err := botRun("status", pos, 2, 2)
	if err != nil {
		return err
	}
	ctx, cancel := a.withTimeout(ctx, false)
	defer cancel()
	c, t, err := a.findThread(ctx, bot, run)
	if err != nil {
		return err
	}
	defer c.Close()
	st, err := c.Status(ctx, t)
	if err != nil {
		return err
	}
	return a.printLine(st)
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
	bot, run   string
	opts       rexclient.WaitOptions
	exitStatus bool
}

func parseWait(a *app, args []string) (waitArgs, error) {
	var w waitArgs
	var pattern, idle, limit string
	fs := newFlags("wait", &a.g)
	fs.StringVar(&limit, "for", "", "give up after this long, such as 30m or 90 (seconds); same as --timeout")
	fs.StringVar(&w.opts.For, "until", "", "condition: exit, done, idle or pattern (default exit, or pattern with --pattern)")
	fs.StringVar(&pattern, "pattern", "", "regular expression to find in the plain screen text; ^ and $ match at line ends")
	fs.StringVar(&idle, "idle", "5", "seconds without output that count as idle")
	fs.BoolVar(&w.exitStatus, "exit-status", false, "exit with the run's exit code when it is known")
	pos, err := parse(fs, args)
	if err != nil {
		return w, err
	}
	if w.bot, w.run, _, err = botRun("wait", pos, 2, 2); err != nil {
		return w, err
	}
	if limit != "" {
		d, err := parseDuration(limit)
		if err != nil {
			return w, usagef("wait: --for takes a timeout such as 30m (use --until for the condition): %v", err)
		}
		a.g.timeout, a.g.timeoutSet = d, true
	}
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
		w.opts.For = "exit"
	}
	switch w.opts.For {
	case "exit", "done", "idle":
	case "pattern":
		if w.opts.Pattern == nil {
			return w, usagef("wait: --until pattern needs --pattern")
		}
	default:
		return w, usagef("wait: unknown --until %q", w.opts.For)
	}
	return w, nil
}

// waitLine is the JSON line wait prints: the run's status plus what ended
// the wait.
type waitLine struct {
	rexclient.Status
	Until  string `json:"until"`
	Met    bool   `json:"met"`
	Closed bool   `json:"closed,omitempty"`
	Match  string `json:"match,omitempty"`
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
	c, t, err := a.findThread(lookupCtx, w.bot, w.run)
	lookupCancel()
	if err != nil {
		return err
	}
	defer c.Close()
	res, err := c.Wait(ctx, t, w.opts)
	if err == nil || errors.Is(err, rexclient.ErrTimeout) {
		st := res.Status
		if st.BlockID == "" {
			st = rexclient.Status{SessionID: t.Session.ID, SessionLabel: t.Session.Label, BlockID: t.Block.ID, Label: t.Block.Label}
		}
		a.printLine(waitLine{Status: st, Until: w.opts.For, Met: err == nil, Closed: res.Closed, Match: res.Match})
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
	var signal string
	var grace time.Duration
	fs := newFlags("stop", &a.g)
	fs.StringVar(&signal, "signal", "", "\"int\" sends Ctrl-C and waits --grace before closing")
	fs.DurationVar(&grace, "grace", 5*time.Second, "how long to wait after Ctrl-C")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	bot, run, _, err := botRun("stop", pos, 2, 2)
	if err != nil {
		return err
	}
	if signal != "" && signal != "int" {
		return usagef("stop: --signal supports only \"int\"")
	}
	ctx, cancel := a.withTimeout(ctx, false)
	defer cancel()
	c, t, err := a.findThread(ctx, bot, run)
	if err != nil {
		return err
	}
	defer c.Close()
	res, err := c.StopWindow(ctx, t, signal == "int", grace)
	if err != nil {
		return err
	}
	if a.g.json {
		return a.printJSON(map[string]any{"session_id": t.Session.ID, "block_id": t.Block.ID, "stopped": true, "window_closed": res.WindowClosed, "session_closed": res.SessionClosed})
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
