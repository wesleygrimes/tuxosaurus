package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/wesleygrimes/tuxosaurus/internal/rexclient/rexclienttest"
)

// TestAttach drives attach through a pseudo-terminal: typed input reaches the
// remote shell, its output comes back, and the detach key leaves the thread
// running.
func TestAttach(t *testing.T) {
	if *serverBinary == "" {
		t.Skip("requires -server-binary")
	}
	c := cli{t: t, server: rexclienttest.StartServer(t, *serverBinary)}
	c.ok("start", "--session", "att", "--label", "sh")

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	defer tty.Close()
	pty.Setsize(ptmx, &pty.Winsize{Cols: 100, Rows: 30})

	var errb bytes.Buffer
	a := &app{stdin: tty, stdout: tty, stderr: &errb, getenv: env(map[string]string{"REX_SERVER": c.server})}
	done := make(chan int, 1)
	go func() { done <- a.main([]string{"attach", "sh"}) }()

	output := make(chan string, 64)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				output <- string(buf[:n])
			}
			if err != nil {
				close(output)
				return
			}
		}
	}()
	waitFor := func(want string) {
		t.Helper()
		var seen strings.Builder
		deadline := time.After(10 * time.Second)
		for !strings.Contains(seen.String(), want) {
			select {
			case s, ok := <-output:
				if !ok {
					t.Fatalf("pty closed; seen %q", seen.String())
				}
				seen.WriteString(s)
			case <-deadline:
				t.Fatalf("did not see %q; seen %q", want, seen.String())
			}
		}
	}
	// The repaint clears the screen first.
	waitFor("\x1b[2J")
	ptmx.WriteString("echo ATTACHED_$((3*3))\r")
	waitFor("ATTACHED_9")
	ptmx.Write([]byte{0x1c})
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("attach exit %d: %s", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("detach key did not detach")
	}
	if !strings.Contains(errb.String(), "detached") {
		t.Fatalf("detach message: %q", errb.String())
	}
	// The resize to the pty's size took effect and the thread survived.
	if out := c.ok("status", "sh"); !strings.Contains(out, "waiting") {
		t.Fatalf("thread after detach: %s", out)
	}
	if out := c.ok("read", "sh"); !strings.Contains(out, "ATTACHED_9") {
		t.Fatalf("screen after detach: %s", out)
	}
}
