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
	if out := c.ok("start", "--dry-run", "--slug", "demo", "--", "sh"); !strings.Contains(out, "session.create") {
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
	json.Unmarshal([]byte(c.ok("start", "--json", "--session", "cli", "--label", "main", "--cwd", "/tmp")), &started)
	if started.SessionID == "" || started.BlockID == "" || !started.Created {
		t.Fatalf("start: %+v", started)
	}
	c.ok("send", "main", "echo", "CLI_$((2*21))", "--enter")
	c.ok("wait", "main", "--pattern", `CLI_42`, "--timeout", "10")
	if out := c.ok("read", "main", "--lines", "3"); !strings.Contains(out, "CLI_42") {
		t.Fatalf("read: %q", out)
	}
	// stdin text and named keys.
	if code, out, errb := c.run("echo FROM_STDIN\n", "send", "main", "-"); code != 0 {
		t.Fatalf("send stdin: %d %s %s", code, out, errb)
	}
	c.ok("key", "main", "C-u", "e", "c", "h", "o", "Space", "K", "E", "Y", "S", "Enter")
	c.ok("wait", "main", "--pattern", `(?m)^KEYS$`, "--timeout", "10")
	if out := c.ok("read", "--json", "main"); !strings.Contains(out, "FROM_STDIN") {
		t.Fatalf("read json: %s", out)
	}

	// A job thread: wait for exit and propagate its status.
	c.ok("start", "--session", "cli", "--label", "job", "--", "/bin/sh", "-c", "echo JOB; exit 5")
	code, out, _ := c.run("", "wait", "job", "--for", "exit", "--exit-status", "--timeout", "10")
	if code != 5 || !strings.Contains(out, "exited") {
		t.Fatalf("wait --exit-status: %d %s", code, out)
	}
	var st struct {
		State    string `json:"state"`
		ExitCode *int   `json:"exit_code"`
	}
	json.Unmarshal([]byte(c.ok("status", "--json", "job")), &st)
	if st.State != "exited" || st.ExitCode == nil || *st.ExitCode != 5 {
		t.Fatalf("status job: %+v", st)
	}
	var all []map[string]any
	json.Unmarshal([]byte(c.ok("status", "--json", "cli")), &all)
	if len(all) != 2 {
		t.Fatalf("status session: %v", all)
	}

	// Timeouts exit 124; unknown names exit 3.
	if code, _, _ := c.run("", "wait", "main", "--pattern", "NEVER_SEEN", "--timeout", "0.3"); code != exitTimeout {
		t.Fatalf("wait timeout exit %d", code)
	}
	if code, _, _ := c.run("", "send", "nosuch", "x"); code != exitNotFound {
		t.Fatalf("missing thread exit %d", code)
	}

	if out := c.ok("ls", "cli"); !strings.Contains(out, "main") || !strings.Contains(out, "job") {
		t.Fatalf("ls session: %s", out)
	}
	c.ok("stop", "job")
	c.ok("stop", "--signal", "int", "--grace", "1s", "main")
	var threads []any
	json.Unmarshal([]byte(c.ok("ls", "--json", "cli")), &threads)
	if len(threads) != 0 {
		t.Fatalf("threads after stop: %v", threads)
	}
	c.ok("close", "cli")
	if code, _, _ := c.run("", "close", "cli"); code != exitNotFound {
		t.Fatalf("close twice exit %d", code)
	}
}
