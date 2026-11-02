package rexclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// ShellFlavor is the terminal block flavor both servers provide.
const ShellFlavor = "com.superlogical.terminal.shell"

const terminalMethodPrefix = "com.superlogical.terminal."

type Session struct {
	ID    string `json:"session_id"`
	Label string `json:"label"`
}

type Block struct {
	ID        string `json:"block_id"`
	Label     string `json:"label"`
	Flavor    string `json:"flavor,omitempty"`
	Placement string `json:"placement,omitempty"`
	WindowID  string `json:"window_id,omitempty"`
}

func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	var out struct {
		Sessions []Session `json:"sessions"`
	}
	err := c.Call(ctx, "session.list", nil, &out)
	return out.Sessions, err
}

// Attach subscribes this stream to a session. Both servers require it
// before session and block methods, and it routes that session's events here.
func (c *Client) Attach(ctx context.Context, sid string) error {
	return c.Call(ctx, "session.attach", map[string]any{"session_id": sid}, nil)
}

func (c *Client) ListBlocks(ctx context.Context, sid string) ([]Block, error) {
	var out struct {
		Blocks []Block `json:"blocks"`
	}
	err := c.Call(ctx, "session.list_blocks", map[string]any{"session_id": sid}, &out)
	return out.Blocks, err
}

// BlockSpec describes a terminal block placed alone in a window layout.
type BlockSpec struct {
	Label   string
	Options map[string]any
	// WindowLabel names the window that holds the block. It defaults to
	// Label.
	WindowLabel string
}

func (b BlockSpec) windowLabel() string {
	if b.WindowLabel != "" {
		return b.WindowLabel
	}
	return b.Label
}

func (b BlockSpec) layout() map[string]any {
	block := map[string]any{"flavor": ShellFlavor}
	if b.Label != "" {
		block["label"] = b.Label
	}
	if len(b.Options) > 0 {
		block["options"] = b.Options
	}
	return map[string]any{"block": block}
}

// CreateSessionRequest builds a session.create payload. With a block, the
// session starts with one window holding it, so no stray default shell is
// created.
func CreateSessionRequest(label string, block *BlockSpec) map[string]any {
	p := map[string]any{"label": label}
	if block != nil {
		w := map[string]any{"layout": block.layout()}
		if l := block.windowLabel(); l != "" {
			w["window_label"] = l
		}
		p["initial_windows"] = []any{w}
	}
	return p
}

// NewWindowRequest builds a session.new_window payload for one block.
func NewWindowRequest(sid string, block BlockSpec) map[string]any {
	p := map[string]any{"session_id": sid, "layout": block.layout(), "focus": false}
	if l := block.windowLabel(); l != "" {
		p["window_label"] = l
	}
	return p
}

// CreateSession returns the new session ID and, when block was given, its ID.
func (c *Client) CreateSession(ctx context.Context, label string, block *BlockSpec) (string, string, error) {
	var out struct {
		SessionID      string `json:"session_id"`
		InitialWindows []struct {
			BlockIDs []string `json:"block_ids"`
		} `json:"initial_windows"`
	}
	if err := c.Call(ctx, "session.create", CreateSessionRequest(label, block), &out); err != nil {
		return "", "", err
	}
	bid := ""
	if len(out.InitialWindows) > 0 && len(out.InitialWindows[0].BlockIDs) > 0 {
		bid = out.InitialWindows[0].BlockIDs[0]
	}
	return out.SessionID, bid, nil
}

// NewWindow creates a window holding one block and returns the block ID.
func (c *Client) NewWindow(ctx context.Context, sid string, block BlockSpec) (string, error) {
	var out struct {
		BlockIDs []string `json:"block_ids"`
	}
	if err := c.Call(ctx, "session.new_window", NewWindowRequest(sid, block), &out); err != nil {
		return "", err
	}
	if len(out.BlockIDs) == 0 {
		return "", fmt.Errorf("session.new_window returned no block")
	}
	return out.BlockIDs[0], nil
}

func (c *Client) DestroySession(ctx context.Context, sid string) error {
	return c.Call(ctx, "session.destroy", map[string]any{"session_id": sid}, nil)
}

// CloseWindow closes a window and every block in it.
func (c *Client) CloseWindow(ctx context.Context, sid, wid string) error {
	return c.Call(ctx, "session.close_window", map[string]any{"session_id": sid, "window_id": wid}, nil)
}

// WindowCount returns how many windows the session has.
func (c *Client) WindowCount(ctx context.Context, sid string) (int, error) {
	var out struct {
		Windows []json.RawMessage `json:"windows"`
	}
	err := c.Call(ctx, "session.view", map[string]any{"session_id": sid}, &out)
	return len(out.Windows), err
}

// CloseBlock removes a block. The server hangs up the block's process group.
func (c *Client) CloseBlock(ctx context.Context, sid, bid string) error {
	return c.Call(ctx, "block.close", map[string]any{"session_id": sid, "block_id": bid}, nil)
}

// Terminal invokes a com.superlogical.terminal.* block method.
func (c *Client) Terminal(ctx context.Context, sid, bid, method string, args map[string]any, out any) error {
	if args == nil {
		args = map[string]any{}
	}
	return c.Call(ctx, terminalMethodPrefix+method, map[string]any{"session_id": sid, "block_id": bid, "args": args}, out)
}

// Write sends bytes to the block's PTY.
func (c *Client) Write(ctx context.Context, sid, bid string, data string) error {
	return c.Terminal(ctx, sid, bid, "write", map[string]any{"data": data}, nil)
}

// Format returns the terminal contents. format is "plain" or "vt"; vt
// output can be written to a terminal to repaint the screen.
func (c *Client) Format(ctx context.Context, sid, bid, format string, trim bool) (string, error) {
	args := map[string]any{"trim": trim}
	if format != "" {
		args["format"] = format
	}
	var out struct {
		Content string `json:"content"`
	}
	err := c.Terminal(ctx, sid, bid, "format", args, &out)
	return out.Content, err
}

// Resize sets the terminal size. mode is "", "advisory", "take" or "release".
func (c *Client) Resize(ctx context.Context, sid, bid string, cols, rows int, mode string) error {
	args := map[string]any{"columns": cols, "rows": rows}
	if mode != "" {
		args["mode"] = mode
	}
	return c.Terminal(ctx, sid, bid, "resize", args, nil)
}

// DataTicket requests a single-use terminal data ticket.
func (c *Client) DataTicket(ctx context.Context, sid, bid string) ([]byte, error) {
	var out struct {
		Ticket json.RawMessage `json:"ticket"`
	}
	err := c.Call(ctx, "data.connect", map[string]any{"session_id": sid, "block_id": bid, "data_ref_id": "terminal", "subprotocol": terminalProtocol}, &out)
	if err != nil {
		return nil, err
	}
	return decodeTicket(out.Ticket)
}

// decodeTicket accepts both encodings seen in the wild: Tuxosaurus returns a
// base64url string, while the published schema describes a 16-element byte
// array.
func decodeTicket(raw json.RawMessage) ([]byte, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			b, err = base64.URLEncoding.DecodeString(s)
		}
		if err != nil {
			return nil, fmt.Errorf("ticket: %w", err)
		}
		return b, nil
	}
	var ints []int
	if err := json.Unmarshal(raw, &ints); err != nil {
		return nil, fmt.Errorf("ticket: unrecognized encoding %s", raw)
	}
	b := make([]byte, len(ints))
	for i, v := range ints {
		if v < 0 || v > 255 {
			return nil, fmt.Errorf("ticket: byte %d out of range", v)
		}
		b[i] = byte(v)
	}
	return b, nil
}
