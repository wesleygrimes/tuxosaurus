package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"strings"
	"testing"

	"github.com/wesleygrimes/tuxosaurus/internal/rexclient/rexclienttest"
)

var serverBinary = flag.String("server-binary", "", "Tuxosaurus executable for integration tests")

type cli struct {
	t      *testing.T
	server string
}

func (c cli) run(stdin string, args ...string) (int, string, string) {
	c.t.Helper()
	var out, errb bytes.Buffer
	a := &app{stdin: strings.NewReader(stdin), stdout: &out, stderr: &errb, getenv: env(map[string]string{"REX_SERVER": c.server})}
	code := a.main(args)
	return code, out.String(), errb.String()
}

func (c cli) ok(args ...string) string {
	c.t.Helper()
	code, out, errb := c.run("", args...)
	if code != 0 {
		c.t.Fatalf("rexctl %q: exit %d\nstdout: %s\nstderr: %s", args, code, out, errb)
	}
	return out
}

func TestCLIIntegration(t *testing.T) {
	if *serverBinary == "" {
		t.Skip("requires -server-binary")
	}
	c := cli{t: t, server: rexclienttest.StartServer(t, *serverBinary)}

	// dry-run sends nothing.
	if out := c.ok("start", "--dry-run", "demo", "sh", "/tmp", "--", "sh"); !strings.Contains(out, "session.create") {
		t.Fatalf("dry-run: %s", out)
	}
	if out := c.ok("--json", "ls"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("dry-run created something: %s", out)
	}

	var started struct {
		SessionID string `json:"session_id"`
		BlockID   string `json:"block_id"`
		Created   bool   `json:"session_created"`
	}
	json.Unmarshal([]byte(c.ok("start", "--json", "cli", "main", "/tmp", "--label", "Main shell")), &started)
	if started.SessionID == "" || started.BlockID == "" || !started.Created {
		t.Fatalf("start: %+v", started)
	}
	if code, _, errb := c.run("", "start", "cli", "main", "/tmp"); code != exitError || !strings.Contains(errb, "already exists") {
		t.Fatalf("duplicate run: %d %s", code, errb)
	}
	c.ok("send", "cli", "main", "echo CLI_$((2*21))", "--enter")
	c.ok("wait", "cli", "main", "--pattern", `CLI_42`, "--for", "10s")
	if out := c.ok("read", "cli", "main", "--tail", "3"); !strings.Contains(out, "CLI_42") {
		t.Fatalf("read: %q", out)
	}
	// stdin text and named keys in both spellings.
	if code, out, errb := c.run("echo FROM_STDIN\n", "send", "cli", "main", "-"); code != 0 {
		t.Fatalf("send stdin: %d %s %s", code, out, errb)
	}
	c.ok("key", "cli", "main", "ctrl+u", "e", "c", "h", "o", "Space", "K", "E", "Y", "S", "enter")
	c.ok("wait", "cli", "main", "--pattern", `(?m)^KEYS$`, "--for", "10")
	if out := c.ok("read", "--json", "cli", "main"); !strings.Contains(out, "FROM_STDIN") {
		t.Fatalf("read json: %s", out)
	}

	// A job run: wait (default: for exit) and propagate its status.
	c.ok("start", "cli", "job", "/tmp", "--", "/bin/sh", "-c", "echo JOB; exit 5")
	code, out, _ := c.run("", "wait", "cli", "job", "--for", "10s", "--exit-status")
	var waited struct {
		State    string `json:"state"`
		Until    string `json:"until"`
		Met      bool   `json:"met"`
		ExitCode *int   `json:"exit_code"`
	}
	if json.Unmarshal([]byte(out), &waited); code != 5 || strings.Count(out, "\n") != 1 || waited.State != "exited" || waited.Until != "exit" || !waited.Met {
		t.Fatalf("wait --exit-status: %d %s", code, out)
	}
	out = c.ok("status", "cli", "job")
	var st struct {
		State    string `json:"state"`
		Label    string `json:"label"`
		ExitCode *int   `json:"exit_code"`
	}
	if json.Unmarshal([]byte(out), &st); strings.Count(out, "\n") != 1 || st.State != "exited" || st.Label != "job" || st.ExitCode == nil || *st.ExitCode != 5 {
		t.Fatalf("status job: %q", out)
	}

	// Timeouts exit 124; unknown names exit 3.
	if code, out, _ := c.run("", "wait", "cli", "main", "--pattern", "NEVER_SEEN", "--for", "0.3"); code != exitTimeout || !strings.Contains(out, `"met":false`) {
		t.Fatalf("wait timeout exit %d %s", code, out)
	}
	if code, _, _ := c.run("", "send", "cli", "nosuch", "x"); code != exitNotFound {
		t.Fatalf("missing run exit %d", code)
	}
	if code, _, _ := c.run("", "status", "nobot", "main"); code != exitNotFound {
		t.Fatalf("missing bot exit %d", code)
	}

	if out := c.ok("ls", "cli"); !strings.Contains(out, "main") || !strings.Contains(out, "job") {
		t.Fatalf("ls session: %s", out)
	}
	var stopped struct {
		WindowClosed  bool `json:"window_closed"`
		SessionClosed bool `json:"session_closed"`
	}
	json.Unmarshal([]byte(c.ok("stop", "--json", "cli", "job")), &stopped)
	if !stopped.WindowClosed || stopped.SessionClosed {
		t.Fatalf("stop job: %+v", stopped)
	}
	var threads []any
	json.Unmarshal([]byte(c.ok("ls", "--json", "cli")), &threads)
	if len(threads) != 1 {
		t.Fatalf("threads after one stop: %v", threads)
	}
	// Stopping the last run ends the session.
	json.Unmarshal([]byte(c.ok("stop", "--json", "--signal", "int", "--grace", "1s", "cli", "main")), &stopped)
	if !stopped.WindowClosed || !stopped.SessionClosed {
		t.Fatalf("stop main: %+v", stopped)
	}
	if out := c.ok("--json", "ls"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("session left after last stop: %s", out)
	}

	// close ends a session with its runs in it.
	c.ok("start", "cli2", "a", "/tmp")
	c.ok("start", "cli2", "b", "/tmp")
	c.ok("close", "cli2")
	if code, _, _ := c.run("", "close", "cli2"); code != exitNotFound {
		t.Fatalf("close twice exit %d", code)
	}
}
