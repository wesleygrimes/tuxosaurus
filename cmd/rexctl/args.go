package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

// usageError makes rexctl exit with status 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// globals are accepted before the command name and after it.
type globals struct {
	server     string
	json       bool
	timeout    time.Duration
	timeoutSet bool
}

func (g *globals) register(fs *flag.FlagSet) {
	fs.StringVar(&g.server, "server", g.server, "server URL or config alias (env REX_SERVER)")
	fs.BoolVar(&g.json, "json", g.json, "print JSON")
	fs.Func("timeout", "overall time limit, e.g. 30s or 5m (default 30s; none for wait, read --follow and attach)", func(s string) error {
		d, err := parseDuration(s)
		if err != nil {
			return err
		}
		g.timeout, g.timeoutSet = d, true
		return nil
	})
}

// parseDuration accepts Go durations and plain seconds ("90").
func parseDuration(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	var secs float64
	if _, err := fmt.Sscanf(s, "%g", &secs); err != nil || secs < 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

func newFlags(name string, g *globals) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	g.register(fs)
	return fs
}

// parse lets flags and positional arguments appear in any order, as bots
// often append flags at the end. Everything after "--" is positional.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, usageError{commandHelp(fs)}
			}
			return nil, usagef("%s: %v", fs.Name(), err)
		}
		rest := fs.Args()
		// flag stops at "--" after consuming it, or at the first positional.
		consumed := len(args) - len(rest)
		if consumed > 0 && args[consumed-1] == "--" {
			return append(pos, rest...), nil
		}
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func commandHelp(fs *flag.FlagSet) string {
	var b strings.Builder
	fs.SetOutput(&b)
	fmt.Fprintf(&b, "usage: rexctl %s\n", commands[fs.Name()].usage)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	return b.String()
}
