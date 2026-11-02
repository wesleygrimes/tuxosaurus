package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/wesleygrimes/tuxosaurus/internal/rexclient"
)

// detachReset undoes terminal modes a remote program may have enabled:
// mouse reporting, bracketed paste, the alternate screen, hidden cursor and
// text attributes.
const detachReset = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l\x1b[?1049l\x1b[?25h\x1b[0m"

type attachArgs struct {
	bot, run string
	detach   byte
	resize   string
}

func parseAttach(a *app, args []string) (attachArgs, error) {
	var at attachArgs
	var detachKey string
	fs := newFlags("attach", &a.g)
	fs.StringVar(&detachKey, "detach-key", `C-\`, "key that detaches, as a single-byte key name")
	fs.StringVar(&at.resize, "resize", "take", "terminal size policy: take (resize to this window), advisory (resize only if no one else owns the size), off")
	pos, err := parse(fs, args)
	if err != nil {
		return at, err
	}
	if at.bot, at.run, _, err = botRun("attach", pos, 2, 2); err != nil {
		return at, err
	}
	key, err := rexclient.KeyBytes(detachKey)
	if err != nil || len(key) != 1 {
		return at, usagef("attach: --detach-key must be a single-byte key such as C-\\ or C-]")
	}
	at.detach = key[0]
	switch at.resize {
	case "take", "advisory", "off":
	default:
		return at, usagef("attach: --resize must be take, advisory or off")
	}
	return at, nil
}

func cmdAttach(ctx context.Context, a *app, args []string) error {
	at, err := parseAttach(a, args)
	if err != nil {
		return err
	}
	in, inOK := a.stdin.(*os.File)
	out, outOK := a.stdout.(*os.File)
	if !inOK || !outOK || !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return errors.New("attach: stdin and stdout must be a terminal")
	}
	ctx, cancel := a.withTimeout(ctx, true)
	defer cancel()
	lookupCtx, lookupCancel := context.WithTimeout(ctx, defaultTimeout)
	c, t, err := a.findThread(lookupCtx, at.bot, at.run)
	lookupCancel()
	if err != nil {
		return err
	}
	defer c.Close()
	sid, bid := t.Session.ID, t.Block.ID

	resize := func() {
		if at.resize == "off" {
			return
		}
		cols, rows, err := term.GetSize(int(out.Fd()))
		if err != nil || cols <= 0 || rows <= 0 {
			return
		}
		if c.Resize(ctx, sid, bid, cols, rows, at.resize) != nil && at.resize == "take" {
			// Older servers may not accept mode; plain resize means take.
			c.Resize(ctx, sid, bid, cols, rows, "")
		}
	}
	resize()

	stream, err := c.OpenStream(ctx, sid, bid)
	if err != nil {
		return err
	}
	defer stream.Close()

	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return err
	}
	defer func() {
		out.WriteString(detachReset)
		term.Restore(int(in.Fd()), state)
	}()

	// Repaint from the server's screen state, then follow live output.
	// Output arriving between the two may repeat once.
	screen, err := repaint(ctx, c, sid, bid)
	if err == nil {
		out.WriteString("\x1b[H\x1b[2J" + screen)
	}

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	done := make(chan string, 2)
	go func() {
		for {
			data, markers, err := stream.Next(ctx)
			out.Write(data)
			for _, m := range markers {
				if m.Type == "child_exited" {
					code := "?"
					if m.ExitCode != nil {
						code = fmt.Sprint(*m.ExitCode)
					}
					done <- "process exited with status " + code
					return
				}
			}
			if err != nil {
				done <- "stream closed"
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				detach := false
				for i, b := range chunk {
					if b == at.detach {
						chunk, detach = chunk[:i], true
						break
					}
				}
				if len(chunk) > 0 {
					if werr := c.Write(ctx, sid, bid, string(chunk)); werr != nil {
						done <- "write failed: " + werr.Error()
						return
					}
				}
				if detach {
					done <- "detached"
					return
				}
			}
			if err != nil {
				done <- "stdin closed"
				return
			}
		}
	}()

	var reason string
loop:
	for {
		select {
		case reason = <-done:
			break loop
		case <-winch:
			resize()
		case <-ctx.Done():
			reason = "interrupted"
			break loop
		}
	}
	if at.resize != "off" {
		// Hand the size back so another client's preference applies again.
		relCtx, relCancel := context.WithTimeout(context.Background(), 2*time.Second)
		c.Resize(relCtx, sid, bid, 0, 0, "release")
		relCancel()
	}
	fmt.Fprintf(a.stderr, "\r\n[rexctl: %s; thread %s keeps running]\r\n", reason, t.Block.Label)
	return nil
}

// repaint asks for the screen as VT with cursor, modes and style so the local
// terminal matches the server's. A server that rejects the extra options gets
// a plain VT request.
func repaint(ctx context.Context, c *rexclient.Client, sid, bid string) (string, error) {
	var res struct {
		Content string `json:"content"`
	}
	args := map[string]any{"format": "vt", "extra_cursor": true, "extra_modes": true, "extra_style": true, "extra_scrolling_region": true, "extra_keyboard": true}
	if err := c.Terminal(ctx, sid, bid, "format", args, &res); err == nil {
		return res.Content, nil
	}
	return c.Format(ctx, sid, bid, "vt", false)
}
