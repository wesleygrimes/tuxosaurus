package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wesleygrimes/tuxosaurus/internal/rexclient"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func testApp(e map[string]string) (*app, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &app{stdin: strings.NewReader(""), stdout: &out, stderr: &errb, getenv: env(e)}, &out, &errb
}

func TestParseIntersperse(t *testing.T) {
	a, _, _ := testApp(nil)
	var enter bool
	fs := newFlags("send", &a.g)
	fs.BoolVar(&enter, "enter", false, "")
	pos, err := parse(fs, []string{"thread", "--json", "hello", "--enter", "--", "--not-a-flag", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !enter || !a.g.json || !reflect.DeepEqual(pos, []string{"thread", "hello", "--not-a-flag", "x"}) {
		t.Fatalf("enter=%v json=%v pos=%q", enter, a.g.json, pos)
	}
	if _, err := parse(newFlags("send", &a.g), []string{"--bogus"}); !errors.As(err, new(usageError)) {
		t.Fatalf("unknown flag: %v", err)
	}
}

func TestParseStart(t *testing.T) {
	a, _, _ := testApp(nil)
	s, err := parseStart(a, []string{"--slug", "fix-login", "--worktree", "/src/wt", "--no-mise", "--", "claude", "-p", "fix it"})
	if err != nil {
		t.Fatal(err)
	}
	if s.session != "fix-login" || s.label != "fix-login" || s.cwd != "/src/wt" || !reflect.DeepEqual(s.command, []string{"claude", "-p", "fix it"}) {
		t.Fatalf("%+v", s)
	}
	s, err = parseStart(a, []string{"--session", "proj", "--", "/usr/bin/codex", "exec", "x"})
	if err != nil || s.session != "proj" || s.label != "codex" {
		t.Fatalf("%+v %v", s, err)
	}
	s, err = parseStart(a, nil)
	if err != nil || s.session != defaultSession || s.label != "" || len(s.command) != 0 {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	if _, err := parseStart(a, []string{"--cwd", "/a", "--worktree", "/b"}); !errors.As(err, new(usageError)) {
		t.Fatalf("cwd and worktree: %v", err)
	}
	s, err = parseStart(a, []string{"--cwd", "rel"})
	wd, _ := os.Getwd()
	if err != nil || s.cwd != filepath.Join(wd, "rel") {
		t.Fatalf("relative cwd: %q %v", s.cwd, err)
	}
}

func TestParseWait(t *testing.T) {
	a, _, _ := testApp(nil)
	w, err := parseWait(a, []string{"t1", "--pattern", `\$ $`, "--timeout", "90"})
	if err != nil || w.opts.For != "pattern" || w.opts.Pattern == nil || w.thread != "t1" || a.g.timeout != 90*time.Second || !a.g.timeoutSet {
		t.Fatalf("%+v %v", w, err)
	}
	w, err = parseWait(a, []string{"t1", "--pattern", `^42$`})
	if err != nil || !w.opts.Pattern.MatchString(">>> print(6*7)\n42\n>>> ") {
		t.Fatalf("pattern should match per line: %+v %v", w, err)
	}
	w, err = parseWait(a, []string{"--for", "idle", "--idle", "2.5", "t1"})
	if err != nil || w.opts.For != "idle" || w.opts.Idle != 2500*time.Millisecond {
		t.Fatalf("%+v %v", w, err)
	}
	w, err = parseWait(a, []string{"t1"})
	if err != nil || w.opts.For != "done" {
		t.Fatalf("default: %+v %v", w, err)
	}
	for _, bad := range [][]string{{}, {"t1", "--for", "pattern"}, {"t1", "--for", "nope"}, {"t1", "--pattern", "("}, {"a", "b"}} {
		if _, err := parseWait(a, bad); !errors.As(err, new(usageError)) {
			t.Errorf("parseWait(%q): %v", bad, err)
		}
	}
}

func TestParseReadAndAttach(t *testing.T) {
	a, _, _ := testApp(nil)
	r, err := parseRead(a, []string{"t", "--lines", "5", "--follow", "--vt"})
	if err != nil || r.lines != 5 || !r.follow || !r.vt || r.thread != "t" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := parseRead(a, []string{"t", "--lines", "-1"}); err == nil {
		t.Fatal("negative --lines accepted")
	}
	at, err := parseAttach(a, []string{"t"})
	if err != nil || at.detach != 0x1c || at.resize != "take" {
		t.Fatalf("%+v %v", at, err)
	}
	at, err = parseAttach(a, []string{"--detach-key", "C-]", "--resize", "off", "t"})
	if err != nil || at.detach != 0x1d || at.resize != "off" {
		t.Fatalf("%+v %v", at, err)
	}
	if _, err := parseAttach(a, []string{"--detach-key", "Up", "t"}); err == nil {
		t.Fatal("multi-byte detach key accepted")
	}
}

func TestResolveServer(t *testing.T) {
	cfg := Config{Default: "mac", Servers: map[string]string{"mac": "https://mac.ts.net", "tux": "unix:///run/tux.sock"}}
	cases := []struct {
		flag string
		env  map[string]string
		cfg  Config
		want string
	}{
		{"tux", map[string]string{"REX_SERVER": "https://env"}, cfg, "unix:///run/tux.sock"},
		{"https://flag", nil, cfg, "https://flag"},
		{"", map[string]string{"REX_SERVER": "tux"}, cfg, "unix:///run/tux.sock"},
		{"", map[string]string{"REX_SERVER": "http://127.0.0.1:1"}, cfg, "http://127.0.0.1:1"},
		{"", nil, cfg, "https://mac.ts.net"},
		{"", nil, Config{}, defaultServer},
	}
	for i, c := range cases {
		if got := resolveServer(c.flag, env(c.env), c.cfg); got != c.want {
			t.Errorf("case %d: %s, want %s", i, got, c.want)
		}
	}
}

func TestConfigFile(t *testing.T) {
	dir := t.TempDir()
	if got := configPath(env(map[string]string{"HOME": "/home/u"})); got != "/home/u/.config/rexctl/config.json" {
		t.Fatal(got)
	}
	if got := configPath(env(map[string]string{"HOME": "/home/u", "XDG_CONFIG_HOME": "/x"})); got != "/x/rexctl/config.json" {
		t.Fatal(got)
	}
	path := filepath.Join(dir, "config.json")
	if got := configPath(env(map[string]string{"REXCTL_CONFIG": path})); got != path {
		t.Fatal(got)
	}
	if c, err := loadConfig(path); err != nil || c.Default != "" {
		t.Fatalf("missing file: %+v %v", c, err)
	}
	os.WriteFile(path, []byte(`{"default":"tux","servers":{"tux":"unix:///s"}}`), 0600)
	c, err := loadConfig(path)
	if err != nil || resolveServer("", env(nil), c) != "unix:///s" {
		t.Fatalf("%+v %v", c, err)
	}
	os.WriteFile(path, []byte(`{`), 0600)
	if _, err := loadConfig(path); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("a\nb\nc\n", 2); got != "b\nc\n" {
		t.Fatalf("%q", got)
	}
	if got := lastLines("a\nb\nc", 5); got != "a\nb\nc" {
		t.Fatalf("%q", got)
	}
	if got := lastLines("a\nb", 0); got != "a\nb" {
		t.Fatalf("%q", got)
	}
}

func TestExitCodes(t *testing.T) {
	a, _, _ := testApp(nil)
	cases := []struct {
		err  error
		want int
	}{
		{nil, exitOK},
		{usagef("bad"), exitUsage},
		{rexclient.ErrTimeout, exitTimeout},
		{fmt.Errorf("x: %w", context.DeadlineExceeded), exitTimeout},
		{fmt.Errorf("thread: %w", rexclient.ErrNotFound), exitNotFound},
		{&rexclient.APIError{Code: "not_found", Status: 404}, exitNotFound},
		{&rexclient.AmbiguousError{What: "thread", Query: "x"}, exitUsage},
		{errors.New("boom"), exitError},
	}
	for _, c := range cases {
		if got := a.report(c.err); got != c.want {
			t.Errorf("report(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestMainUsage(t *testing.T) {
	a, out, _ := testApp(nil)
	if code := a.main(nil); code != exitUsage || !strings.Contains(out.String(), "rexctl start") {
		t.Fatalf("no args: %d %q", code, out)
	}
	a, _, errb := testApp(nil)
	if code := a.main([]string{"frobnicate"}); code != exitUsage || !strings.Contains(errb.String(), "unknown command") {
		t.Fatalf("unknown: %d %q", code, errb)
	}
	a, _, _ = testApp(nil)
	if code := a.main([]string{"key", "t", "NoSuchKey"}); code != exitUsage {
		t.Fatalf("bad key: %d", code)
	}
	a, _, _ = testApp(nil)
	if code := a.main([]string{"send"}); code != exitUsage {
		t.Fatalf("send without args: %d", code)
	}
}
