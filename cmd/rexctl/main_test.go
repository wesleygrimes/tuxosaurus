package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	s, err := parseStart(a, []string{"wes-bot", "fix-login", "/src/app", "--label", "Fix login", "--no-mise", "--", "claude", "-p", "fix it"})
	if err != nil {
		t.Fatal(err)
	}
	if s.bot != "wes-bot" || s.run != "fix-login" || s.cwd != "/src/app" || s.label != "Fix login" || !s.noMise || !reflect.DeepEqual(s.command, []string{"claude", "-p", "fix it"}) {
		t.Fatalf("%+v", s)
	}
	o := s.options()
	if o.Session != "wes-bot" || o.Label != "fix-login" || o.WindowLabel != "Fix login" || o.Cwd != "/src/app" || !reflect.DeepEqual(o.Command, s.command) {
		t.Fatalf("options: %+v", o)
	}
	s, err = parseStart(a, []string{"bot", "run", "rel"})
	wd, _ := os.Getwd()
	if err != nil || s.cwd != filepath.Join(wd, "rel") || len(s.command) != 0 {
		t.Fatalf("relative cwd: %+v %v", s, err)
	}
	for _, bad := range [][]string{nil, {"bot"}, {"bot", "run"}, {"bot", "run", "/x", "--slug", "s"}, {"bot", "", "/x"}, {"bot", "run", "/x", "--worktree", "wes/a", "--slug", "a/b"}} {
		if _, err := parseStart(a, bad); !errors.As(err, new(usageError)) {
			t.Errorf("parseStart(%q): %v", bad, err)
		}
	}
}

func TestStartWorktree(t *testing.T) {
	a, _, _ := testApp(nil)
	s, err := parseStart(a, []string{"bot", "run", "/src/app", "--worktree", "wes/fix/login", "--", "make", "test"})
	if err != nil || s.slug != "fix-login" {
		t.Fatalf("%+v %v", s, err)
	}
	o := s.options()
	want := []string{"/bin/sh", "-c", worktreeScript, "rexctl-worktree", "/src/app", "/src/app-fix-login", "wes/fix/login", "make", "test"}
	if !reflect.DeepEqual(o.Command, want) || o.Cwd != "/src/app" {
		t.Fatalf("command %q cwd %q", o.Command, o.Cwd)
	}
	s, _ = parseStart(a, []string{"bot", "run", "/src/app", "--worktree", "feature", "--slug", "x"})
	if got := s.options().Command; got[5] != "/src/app-x" || len(got) != 7 {
		t.Fatalf("slug: %q", got)
	}

	// Run the script for real: it adds the worktree once, then reuses it.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "app")
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %q: %v\n%s", args, err, out)
		}
	}
	gitRun("init", "-q", repo)
	gitRun("-C", repo, "commit", "-q", "--allow-empty", "-m", "init")
	argv := func() []string {
		s, _ := parseStart(a, []string{"bot", "run", repo, "--worktree", "wes/topic", "--", "pwd"})
		return s.options().Command
	}()
	for i := 0; i < 2; i++ {
		out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		if err != nil || !strings.HasSuffix(strings.TrimSpace(string(out)), "/app-topic") {
			t.Fatalf("run %d: %v\n%s", i, err, out)
		}
	}
}

func TestParseWait(t *testing.T) {
	a, _, _ := testApp(nil)
	w, err := parseWait(a, []string{"bot", "run", "--for", "30m"})
	if err != nil || w.opts.For != "exit" || w.bot != "bot" || w.run != "run" || a.g.timeout != 30*time.Minute || !a.g.timeoutSet {
		t.Fatalf("%+v %v", w, err)
	}
	a, _, _ = testApp(nil)
	w, err = parseWait(a, []string{"bot", "run", "--pattern", `\$ $`, "--timeout", "90"})
	if err != nil || w.opts.For != "pattern" || w.opts.Pattern == nil || a.g.timeout != 90*time.Second {
		t.Fatalf("%+v %v", w, err)
	}
	w, err = parseWait(a, []string{"bot", "run", "--pattern", `^42$`})
	if err != nil || !w.opts.Pattern.MatchString(">>> print(6*7)\n42\n>>> ") {
		t.Fatalf("pattern should match per line: %+v %v", w, err)
	}
	w, err = parseWait(a, []string{"--until", "idle", "--idle", "2.5", "bot", "run"})
	if err != nil || w.opts.For != "idle" || w.opts.Idle != 2500*time.Millisecond {
		t.Fatalf("%+v %v", w, err)
	}
	for _, bad := range [][]string{{}, {"run"}, {"b", "r", "x"}, {"b", "r", "--for", "exit"}, {"b", "r", "--until", "pattern"}, {"b", "r", "--until", "nope"}, {"b", "r", "--pattern", "("}} {
		if _, err := parseWait(a, bad); !errors.As(err, new(usageError)) {
			t.Errorf("parseWait(%q): %v", bad, err)
		}
	}
}

func TestParseReadAndAttach(t *testing.T) {
	a, _, _ := testApp(nil)
	r, err := parseRead(a, []string{"b", "r", "--tail", "5", "--follow", "--vt"})
	if err != nil || r.lines != 5 || !r.follow || !r.vt || r.bot != "b" || r.run != "r" {
		t.Fatalf("%+v %v", r, err)
	}
	if r, err := parseRead(a, []string{"b", "r", "--lines", "7"}); err != nil || r.lines != 7 {
		t.Fatalf("--lines alias: %+v %v", r, err)
	}
	if _, err := parseRead(a, []string{"b", "r", "--tail", "-1"}); err == nil {
		t.Fatal("negative --tail accepted")
	}
	if _, err := parseRead(a, []string{"r"}); !errors.As(err, new(usageError)) {
		t.Fatalf("read with one positional: %v", err)
	}
	at, err := parseAttach(a, []string{"b", "r"})
	if err != nil || at.detach != 0x1c || at.resize != "take" || at.bot != "b" || at.run != "r" {
		t.Fatalf("%+v %v", at, err)
	}
	at, err = parseAttach(a, []string{"--detach-key", "C-]", "--resize", "off", "b", "r"})
	if err != nil || at.detach != 0x1d || at.resize != "off" {
		t.Fatalf("%+v %v", at, err)
	}
	if _, err := parseAttach(a, []string{"--detach-key", "Up", "b", "r"}); err == nil {
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
	if code := a.main([]string{"key", "b", "t", "NoSuchKey"}); code != exitUsage {
		t.Fatalf("bad key: %d", code)
	}
	a, _, _ = testApp(nil)
	if code := a.main([]string{"send"}); code != exitUsage {
		t.Fatalf("send without args: %d", code)
	}
	a, _, _ = testApp(nil)
	if code := a.main([]string{"send", "b", "r", "two", "words"}); code != exitUsage {
		t.Fatalf("send with unquoted words: %d", code)
	}
}
