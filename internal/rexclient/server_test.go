package rexclient_test

import (
	"context"
	"errors"
	"flag"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/wesleygrimes/tuxosaurus/internal/rexclient"
	"github.com/wesleygrimes/tuxosaurus/internal/rexclient/rexclienttest"
)

var serverBinary = flag.String("server-binary", "", "Tuxosaurus executable for integration tests")

func connect(t *testing.T) *rexclient.Client {
	t.Helper()
	if *serverBinary == "" {
		t.Skip("requires -server-binary")
	}
	ep, err := rexclient.ParseEndpoint(rexclienttest.StartServer(t, *serverBinary))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var c *rexclient.Client
	for {
		if c, err = rexclient.Dial(ctx, ep, "rexclient test"); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestIntegration(t *testing.T) {
	c := connect(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// start: creates the session with the thread as its only block.
	shell, created, err := c.Start(ctx, rexclient.StartOptions{Session: "itest", Label: "sh1", Cwd: "/tmp", KeepOnExit: true})
	if err != nil || !created {
		t.Fatalf("start: %v created=%v", err, created)
	}
	threads, err := c.Threads(ctx, []rexclient.Session{shell.Session})
	if err != nil || len(threads) != 1 || threads[0].Block.Label != "sh1" {
		t.Fatalf("threads after start: %+v %v", threads, err)
	}

	// send + wait for a pattern + read.
	if err := c.Write(ctx, shell.Session.ID, shell.Block.ID, "echo REX_$((6*7)); pwd\r"); err != nil {
		t.Fatal(err)
	}
	res, err := c.Wait(ctx, shell, rexclient.WaitOptions{For: "pattern", Pattern: regexp.MustCompile(`REX_\d+`)})
	if err != nil || res.Match != "REX_42" {
		t.Fatalf("wait pattern: %+v %v", res, err)
	}
	screen, err := c.Format(ctx, shell.Session.ID, shell.Block.ID, "plain", true)
	if err != nil || !strings.Contains(screen, "REX_42") || !strings.Contains(screen, "/tmp") {
		t.Fatalf("read: %q %v", screen, err)
	}
	// The shell is back at its prompt.
	var st rexclient.Status
	for i := 0; i < 50; i++ {
		if st, err = c.Status(ctx, shell); err == nil && st.State == rexclient.StateWaiting {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.State != rexclient.StateWaiting {
		t.Fatalf("status at prompt: %+v %v", st, err)
	}

	// A command thread in the existing session keeps its block after exit,
	// so the exit code and output stay readable.
	job, created, err := c.Start(ctx, rexclient.StartOptions{Session: "itest", Label: "job", Command: []string{"/bin/sh", "-c", "echo JOB_OUTPUT; sleep 0.5; exit 3"}, KeepOnExit: true})
	if err != nil || created {
		t.Fatalf("start job: %v created=%v", err, created)
	}
	res, err = c.Wait(ctx, job, rexclient.WaitOptions{For: "exit", Poll: 100 * time.Millisecond})
	if err != nil || res.Status.ExitCode == nil || *res.Status.ExitCode != 3 || res.Status.State != rexclient.StateExited {
		t.Fatalf("wait exit: %+v %v", res, err)
	}
	if screen, _ := c.Format(ctx, job.Session.ID, job.Block.ID, "plain", true); !strings.Contains(screen, "JOB_OUTPUT") {
		t.Fatalf("output after exit: %q", screen)
	}
	if st, err := c.Status(ctx, job); err != nil || st.State != rexclient.StateExited || *st.ExitCode != 3 {
		t.Fatalf("status after exit: %+v %v", st, err)
	}

	// Without KeepOnExit the server removes the block; wait still succeeds.
	gone, _, err := c.Start(ctx, rexclient.StartOptions{Session: "itest", Label: "gone", Command: []string{"/bin/sh", "-c", "sleep 0.5"}})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := c.Wait(ctx, gone, rexclient.WaitOptions{For: "done", Poll: 100 * time.Millisecond}); err != nil || res.Status.State != rexclient.StateExited {
		t.Fatalf("wait for removed block: %+v %v", res, err)
	}

	// Live output through the data stream: snapshot skipped, markers removed.
	stream, err := c.OpenStream(ctx, shell.Session.ID, shell.Block.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, shell.Session.ID, shell.Block.ID, "echo STREAM_$((1+1))\r"); err != nil {
		t.Fatal(err)
	}
	var plain rexclient.PlainFilter
	var seen strings.Builder
	for !strings.Contains(seen.String(), "STREAM_2") {
		out, _, err := stream.Next(ctx)
		if err != nil {
			t.Fatalf("stream: %v, seen %q", err, seen.String())
		}
		if strings.Contains(string(out), "GHOSTSNP") {
			t.Fatal("snapshot bytes leaked into output")
		}
		seen.Write(plain.Write(out))
	}
	if !stream.SnapshotDone() {
		t.Fatal("snapshot not finished")
	}
	stream.Close()

	// Idle: the shell prints nothing after the command finishes.
	if res, err := c.Wait(ctx, shell, rexclient.WaitOptions{For: "idle", Idle: 300 * time.Millisecond, Poll: 100 * time.Millisecond}); err != nil || res.Status.State != rexclient.StateWaiting {
		t.Fatalf("wait idle: %+v %v", res, err)
	}

	// Timeout.
	short, cancelShort := context.WithTimeout(ctx, 300*time.Millisecond)
	_, err = c.Wait(short, shell, rexclient.WaitOptions{For: "pattern", Pattern: regexp.MustCompile("NEVER_PRINTED")})
	cancelShort()
	if !errors.Is(err, rexclient.ErrTimeout) {
		t.Fatalf("wait timeout: %v", err)
	}

	// Lookup by label and ID prefix.
	if th, err := c.FindThread(ctx, "", "job"); err != nil || th.Block.ID != job.Block.ID {
		t.Fatalf("find by label: %+v %v", th, err)
	}
	bare := strings.TrimPrefix(job.Block.ID, "block:")[:8]
	if th, err := c.FindThread(ctx, "itest", bare); err != nil || th.Block.ID != job.Block.ID {
		t.Fatalf("find by prefix: %+v %v", th, err)
	}
	if _, err := c.FindThread(ctx, "", "missing"); !errors.Is(err, rexclient.ErrNotFound) {
		t.Fatalf("find missing: %v", err)
	}

	// stop with Ctrl-C: a running command is interrupted and the block closed.
	sleeper, _, err := c.Start(ctx, rexclient.StartOptions{Session: "itest", Label: "sleeper", Command: []string{"sleep", "60"}, KeepOnExit: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, sleeper, true, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FindThread(ctx, "itest", "sleeper"); !errors.Is(err, rexclient.ErrNotFound) {
		t.Fatalf("stopped thread still listed: %v", err)
	}
	if err := c.Stop(ctx, shell, false, 0); err != nil {
		t.Fatal(err)
	}

	// close: destroy the session.
	if err := c.DestroySession(ctx, shell.Session.ID); err != nil {
		t.Fatal(err)
	}
	sessions, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.ID == shell.Session.ID {
			t.Fatal("session still listed after destroy")
		}
	}
}
