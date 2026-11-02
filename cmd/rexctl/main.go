// Command rexctl drives Rex-compatible terminal servers (the official Rex
// server and Tuxosaurus) from scripts and bots. See docs/client.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/wesleygrimes/tuxosaurus/internal/rexclient"
)

// Exit statuses.
const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitNotFound = 3
	exitTimeout  = 124
)

const defaultTimeout = 30 * time.Second

// app holds process-level inputs so commands can be tested without a real
// terminal or environment.
type app struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string
	g      globals
	// exitCode lets a command choose a non-error exit status, as
	// wait --exit-status does.
	exitCode int
}

type command struct {
	run   func(ctx context.Context, a *app, args []string) error
	usage string
	// streaming commands run without the default timeout.
	streaming bool
}

// commands is filled in init to avoid an initialization cycle: command
// functions print their own usage from this table.
var commands map[string]command

func init() { commands = commandTable() }

func commandTable() map[string]command {
	return map[string]command{
		"start":  {run: cmdStart, usage: "start BOT RUN CWD [--label L] [--worktree wes/BRANCH [--slug S]] [--no-mise] [--dry-run] [--close-on-exit] [-- CMD...]"},
		"send":   {run: cmdSend, usage: "send BOT RUN TEXT [--enter] [--enter-delay DUR]   (TEXT - reads stdin)"},
		"key":    {run: cmdKey, usage: "key BOT RUN KEY...   (Enter, C-c, ctrl+c, escape, alt+x, shift+tab, ...)"},
		"read":   {run: cmdRead, usage: "read BOT RUN [--tail N] [--vt] [--follow] [--no-trim]", streaming: true},
		"status": {run: cmdStatus, usage: "status BOT RUN"},
		"wait":   {run: cmdWait, usage: "wait BOT RUN [--for DUR] [--until exit|done|idle|pattern] [--pattern RE] [--idle SECS] [--exit-status]", streaming: true},
		"stop":   {run: cmdStop, usage: "stop BOT RUN [--signal int] [--grace DUR]"},
		"ls":     {run: cmdLs, usage: "ls [BOT]"},
		"close":  {run: cmdClose, usage: "close BOT"},
		"attach": {run: cmdAttach, usage: "attach BOT RUN [--detach-key KEY] [--resize take|advisory|off]", streaming: true},
	}
}

func main() {
	a := &app{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv}
	os.Exit(a.main(os.Args[1:]))
}

func usage() string {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("usage: rexctl [--server URL|ALIAS] [--json] [--timeout DUR] COMMAND [ARGS]\n\ncommands:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  rexctl %s\n", commands[n].usage)
	}
	b.WriteString("\nexit status: 0 ok, 1 error, 2 usage, 3 not found, 124 timeout\n")
	return b.String()
}

func (a *app) main(args []string) int {
	fs := newFlags("rexctl", &a.g)
	if err := fs.Parse(args); err != nil {
		if strings.Contains(err.Error(), "help requested") {
			fmt.Fprint(a.stdout, usage())
			return exitOK
		}
		fmt.Fprintf(a.stderr, "rexctl: %v\n%s", err, usage())
		return exitUsage
	}
	rest := fs.Args()
	if len(rest) == 0 || rest[0] == "help" {
		fmt.Fprint(a.stdout, usage())
		if len(rest) == 0 {
			return exitUsage
		}
		return exitOK
	}
	cmd, ok := commands[rest[0]]
	if !ok {
		fmt.Fprintf(a.stderr, "rexctl: unknown command %q\n%s", rest[0], usage())
		return exitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := cmd.run(ctx, a, rest[1:])
	return a.report(err)
}

// withTimeout applies --timeout, or the default for non-streaming commands.
func (a *app) withTimeout(ctx context.Context, streaming bool) (context.Context, context.CancelFunc) {
	switch {
	case a.g.timeoutSet && a.g.timeout > 0:
		return context.WithTimeout(ctx, a.g.timeout)
	case !a.g.timeoutSet && !streaming:
		return context.WithTimeout(ctx, defaultTimeout)
	}
	return context.WithCancel(ctx)
}

func (a *app) report(err error) int {
	if err == nil {
		return a.exitCode
	}
	var ue usageError
	var amb *rexclient.AmbiguousError
	switch {
	case errors.As(err, &ue):
		fmt.Fprintf(a.stderr, "rexctl: %v\n", err)
		return exitUsage
	case errors.Is(err, rexclient.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintf(a.stderr, "rexctl: %v\n", err)
		return exitTimeout
	case errors.Is(err, rexclient.ErrNotFound), rexclient.NotFound(err):
		fmt.Fprintf(a.stderr, "rexctl: %v\n", err)
		return exitNotFound
	case errors.As(err, &amb):
		fmt.Fprintf(a.stderr, "rexctl: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(a.stderr, "rexctl: %v\n", err)
	return exitError
}

func (a *app) server() (string, error) {
	cfg, err := loadConfig(configPath(a.getenv))
	if err != nil {
		return "", err
	}
	return resolveServer(a.g.server, a.getenv, cfg), nil
}

func (a *app) connect(ctx context.Context) (*rexclient.Client, error) {
	url, err := a.server()
	if err != nil {
		return nil, err
	}
	ep, err := rexclient.ParseEndpoint(url)
	if err != nil {
		return nil, usageError{err.Error()}
	}
	return rexclient.Dial(ctx, ep, "rexctl")
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// printLine prints v as one compact JSON line.
func (a *app) printLine(v any) error {
	return json.NewEncoder(a.stdout).Encode(v)
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
