package rexclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	controlProtocol  = "rex.control.v1"
	terminalProtocol = "rex.data.terminal.v1"
	// The server caps control messages at 1 MiB.
	controlReadLimit = 1 << 20
)

// APIError is an error response from the server. Status is HTTP-style.
type APIError struct {
	Method  string
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s (%s)", e.Method, e.Message, e.Code)
}

// NotFound reports whether err is a server-side "not found" response.
func NotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && (e.Status == 404 || e.Code == "not_found")
}

// Event is a control stream event. Kind is the outer type such as
// "session_event"; Payload is that event's payload.
type Event struct {
	Kind    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// BlockEvent is a session_event wrapping a block_event, the form used for
// child_exited, program_status_changed, title_changed and similar.
type BlockEvent struct {
	SessionID string
	BlockID   string
	Name      string
	Payload   json.RawMessage
}

// AsBlockEvent unwraps a session_event/block_event pair.
func (e Event) AsBlockEvent() (BlockEvent, bool) {
	if e.Kind != "session_event" {
		return BlockEvent{}, false
	}
	var outer struct {
		SessionID string `json:"session_id"`
		Event     struct {
			Type    string `json:"type"`
			Payload struct {
				BlockID string          `json:"block_id"`
				Name    string          `json:"name"`
				Payload json.RawMessage `json:"payload"`
			} `json:"payload"`
		} `json:"event"`
	}
	if json.Unmarshal(e.Payload, &outer) != nil || outer.Event.Type != "block_event" {
		return BlockEvent{}, false
	}
	p := outer.Event.Payload
	return BlockEvent{SessionID: outer.SessionID, BlockID: p.BlockID, Name: p.Name, Payload: p.Payload}, true
}

// SessionEventType returns the nested session event type (for example
// "block_closed") and its payload.
func (e Event) SessionEventType() (sessionID, kind string, payload json.RawMessage) {
	if e.Kind != "session_event" {
		return "", "", nil
	}
	var outer struct {
		SessionID string `json:"session_id"`
		Event     struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		} `json:"event"`
	}
	json.Unmarshal(e.Payload, &outer)
	return outer.SessionID, outer.Event.Type, outer.Event.Payload
}

type frame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
	Error   *APIError       `json:"error"`
}

// Client is one control stream. Requests may be issued concurrently; the
// server can answer them out of order, so responses are matched by ID.
type Client struct {
	ep   *Endpoint
	conn *websocket.Conn

	mu      sync.Mutex
	next    int
	pending map[string]chan frame
	subs    map[chan Event]struct{}
	err     error
	done    chan struct{}
}

// Dial opens a control stream and completes the hello/welcome exchange.
func Dial(ctx context.Context, ep *Endpoint, name string) (*Client, error) {
	conn, _, err := websocket.Dial(ctx, ep.WSBase+"/api/control", &websocket.DialOptions{HTTPClient: ep.HTTP, Subprotocols: []string{controlProtocol}})
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", ep.Raw, err)
	}
	conn.SetReadLimit(controlReadLimit)
	hello := map[string]any{"type": "hello", "client": map[string]any{"kind": "cli", "name": name}}
	if err := wsjson.Write(ctx, conn, hello); err != nil {
		conn.CloseNow()
		return nil, err
	}
	var welcome struct {
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
	}
	if err := wsjson.Read(ctx, conn, &welcome); err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("welcome: %w", err)
	}
	if welcome.Type != "welcome" || welcome.Protocol != controlProtocol {
		conn.CloseNow()
		return nil, fmt.Errorf("unexpected welcome %q/%q", welcome.Type, welcome.Protocol)
	}
	c := &Client{ep: ep, conn: conn, pending: map[string]chan frame{}, subs: map[chan Event]struct{}{}, done: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

// Endpoint returns the endpoint this client is connected to.
func (c *Client) Endpoint() *Endpoint { return c.ep }

func (c *Client) readLoop() {
	defer close(c.done)
	for {
		var f frame
		err := wsjson.Read(context.Background(), c.conn, &f)
		if err != nil {
			c.mu.Lock()
			c.err = err
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			for ch := range c.subs {
				close(ch)
				delete(c.subs, ch)
			}
			c.mu.Unlock()
			return
		}
		c.mu.Lock()
		switch f.Type {
		case "response":
			if ch := c.pending[f.ID]; ch != nil {
				delete(c.pending, f.ID)
				ch <- f
			}
		case "event":
			var ev Event
			if json.Unmarshal(f.Payload, &ev) == nil {
				for ch := range c.subs {
					// A slow subscriber must not stall responses. Buffers are
					// large enough for the bursts rexctl waits on.
					select {
					case ch <- ev:
					default:
					}
				}
			}
		}
		c.mu.Unlock()
	}
}

// Subscribe returns a channel of events and a function that ends the
// subscription. The channel closes when the connection ends.
func (c *Client) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 4096)
	c.mu.Lock()
	if c.err != nil {
		close(ch)
	} else {
		c.subs[ch] = struct{}{}
	}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		if _, ok := c.subs[ch]; ok {
			delete(c.subs, ch)
			close(ch)
		}
		c.mu.Unlock()
	}
}

// Call sends a request and decodes the response payload into out, which
// may be nil.
func (c *Client) Call(ctx context.Context, method string, payload any, out any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	ch := make(chan frame, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return fmt.Errorf("%s: connection closed: %w", method, c.err)
	}
	c.next++
	id := strconv.Itoa(c.next)
	c.pending[id] = ch
	c.mu.Unlock()
	req := map[string]any{"type": "request", "id": id, "method": method, "payload": payload}
	if err := wsjson.Write(ctx, c.conn, req); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case f, ok := <-ch:
		if !ok {
			return fmt.Errorf("%s: connection closed", method)
		}
		if f.Error != nil {
			f.Error.Method = method
			return f.Error
		}
		if out != nil && len(f.Payload) > 0 {
			if err := json.Unmarshal(f.Payload, out); err != nil {
				return fmt.Errorf("%s: decode response: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: %w", method, ctx.Err())
	}
}

// Close ends the control stream. Sessions and blocks keep running.
func (c *Client) Close() error {
	err := c.conn.Close(websocket.StatusNormalClosure, "")
	<-c.done
	return err
}
