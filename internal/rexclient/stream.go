package rexclient

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/coder/websocket"
)

// Data handshake bytes (see docs/protocol.md).
const (
	dataHello   = 0x01
	dataWelcome = 0x81
	dataReject  = 0x82
)

// Stream is an output-only terminal data stream. Input goes through the
// control API.
type Stream struct {
	conn  *websocket.Conn
	dec   *Decoder
	Nonce uint64
}

// OpenStream requests a ticket and connects a fresh terminal data stream.
// The stream begins with a snapshot that the decoder skips.
func (c *Client) OpenStream(ctx context.Context, sid, bid string) (*Stream, error) {
	ticket, err := c.DataTicket(ctx, sid, bid)
	if err != nil {
		return nil, err
	}
	conn, _, err := websocket.Dial(ctx, c.ep.WSBase+"/api/data", &websocket.DialOptions{HTTPClient: c.ep.HTTP, Subprotocols: []string{terminalProtocol}})
	if err != nil {
		return nil, fmt.Errorf("data stream: %w", err)
	}
	// The server sends up to 64 KiB per frame; leave headroom.
	conn.SetReadLimit(16 << 20)
	if err := conn.Write(ctx, websocket.MessageBinary, append([]byte{dataHello}, ticket...)); err != nil {
		conn.CloseNow()
		return nil, err
	}
	_, welcome, err := conn.Read(ctx)
	if err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("data welcome: %w", err)
	}
	if len(welcome) == 2 && welcome[0] == dataReject {
		conn.CloseNow()
		return nil, fmt.Errorf("data stream rejected (reason %d)", welcome[1])
	}
	if len(welcome) != 32 || welcome[0] != dataWelcome {
		conn.CloseNow()
		return nil, fmt.Errorf("unexpected data welcome %x", welcome)
	}
	nonce := binary.LittleEndian.Uint64(welcome[24:32])
	return &Stream{conn: conn, dec: NewDecoder(nonce), Nonce: nonce}, nil
}

// Next blocks for the next frame and returns its terminal output and markers.
// Output can be empty while the snapshot is still being skipped.
func (s *Stream) Next(ctx context.Context) ([]byte, []Marker, error) {
	_, p, err := s.conn.Read(ctx)
	if err != nil {
		return s.dec.Flush(), nil, err
	}
	out, markers := s.dec.Feed(p)
	return out, markers, nil
}

// SnapshotDone reports whether all snapshot bytes have been consumed.
func (s *Stream) SnapshotDone() bool { return s.dec.SnapshotDone() }

func (s *Stream) Close() error { return s.conn.CloseNow() }
