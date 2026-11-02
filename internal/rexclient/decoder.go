package rexclient

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strconv"
)

// Ghostty snapshot framing, as produced by the pinned libghostty build and
// observed on the wire:
//
//	"GHOSTSNP" | version u16 | records...
//	record = type u16 | length u32 | checksum u32 | payload[length]
//
// All integers are little-endian. The snapshot ends with an empty record of
// type 6. rexctl never decodes record payloads; it only needs to find where
// the snapshot stops and terminal output begins. WebSocket frame boundaries
// do not delimit the snapshot, so the decoder is incremental.
const (
	snapshotMagic        = "GHOSTSNP"
	snapshotEnvelope     = len(snapshotMagic) + 2
	snapshotRecordHeader = 10
	snapshotEndRecord    = 6
	// Record types are small integers. A larger value means the bytes are not
	// a record header, so the snapshot ended without an end record and the
	// decoder stops skipping rather than discarding output.
	snapshotMaxRecordType = 0xff
)

// apcMaxLen bounds a buffered APC string. Rex markers are small JSON objects;
// anything longer is passed through as output instead of buffered forever.
const apcMaxLen = 64 << 10

// Marker is an out-of-band update carried in the data stream as
// ESC _ rex1;<nonce>;<json> ESC \.
type Marker struct {
	Type     string          `json:"type"`
	Cols     int             `json:"cols,omitempty"`
	Rows     int             `json:"rows,omitempty"`
	ExitCode *int            `json:"exit_code,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

// Decoder turns data stream payload bytes into terminal output and markers.
type Decoder struct {
	nonce uint64

	// snapshot state
	snapDone  bool
	snapBuf   []byte // bytes buffered while reading magic/version/headers
	skipLeft  uint64 // payload bytes still to discard
	gotHeader bool   // magic and version consumed

	// APC state
	pending []byte // partial escape sequence held across chunks
}

// NewDecoder returns a decoder for a fresh stream. Markers must carry nonce;
// APC strings with another nonce are program output and pass through.
func NewDecoder(nonce uint64) *Decoder { return &Decoder{nonce: nonce} }

// NewResumeDecoder returns a decoder for a resumed stream, which carries no
// snapshot.
func NewResumeDecoder(nonce uint64) *Decoder { return &Decoder{nonce: nonce, snapDone: true} }

// SnapshotDone reports whether the snapshot has been fully skipped.
func (d *Decoder) SnapshotDone() bool { return d.snapDone }

// Feed consumes a chunk and returns terminal output with markers removed.
func (d *Decoder) Feed(p []byte) (out []byte, markers []Marker) {
	if !d.snapDone {
		p = d.skipSnapshot(p)
		if !d.snapDone {
			return nil, nil
		}
	}
	return d.filterAPC(p)
}

// Flush returns any output held while waiting for an escape sequence to
// complete. Call it when the stream ends.
func (d *Decoder) Flush() []byte {
	out := d.pending
	d.pending = nil
	return out
}

func (d *Decoder) skipSnapshot(p []byte) []byte {
	for !d.snapDone {
		if d.skipLeft > 0 {
			n := uint64(len(p))
			if n > d.skipLeft {
				n = d.skipLeft
			}
			d.skipLeft -= n
			p = p[n:]
			if len(p) == 0 {
				return nil
			}
			continue
		}
		need := snapshotRecordHeader
		if !d.gotHeader {
			need = snapshotEnvelope
		}
		take := need - len(d.snapBuf)
		if take > len(p) {
			take = len(p)
		}
		d.snapBuf = append(d.snapBuf, p[:take]...)
		p = p[take:]
		if !d.gotHeader && !bytes.HasPrefix([]byte(snapshotMagic), d.snapBuf[:min(len(d.snapBuf), len(snapshotMagic))]) {
			// No snapshot at all: hand everything back as output.
			d.snapDone = true
			rest := append(d.snapBuf, p...)
			d.snapBuf = nil
			return rest
		}
		if d.gotHeader && len(d.snapBuf) >= 2 && binary.LittleEndian.Uint16(d.snapBuf[0:2]) > snapshotMaxRecordType {
			// Checked on the first two bytes so short output is not held
			// back waiting for a full header that will never come.
			d.snapDone = true
			rest := append(d.snapBuf, p...)
			d.snapBuf = nil
			return rest
		}
		if len(d.snapBuf) < need {
			return nil
		}
		if !d.gotHeader {
			d.gotHeader = true
			d.snapBuf = d.snapBuf[:0]
			continue
		}
		kind := binary.LittleEndian.Uint16(d.snapBuf[0:2])
		length := binary.LittleEndian.Uint32(d.snapBuf[2:6])
		d.snapBuf = d.snapBuf[:0]
		d.skipLeft = uint64(length)
		if kind == snapshotEndRecord && length == 0 {
			d.snapDone = true
			d.snapBuf = nil
		}
	}
	return p
}

func (d *Decoder) filterAPC(p []byte) (out []byte, markers []Marker) {
	if len(d.pending) > 0 {
		p = append(d.pending, p...)
		d.pending = nil
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, 0x1b)
		if i < 0 {
			out = append(out, p...)
			break
		}
		out = append(out, p[:i]...)
		p = p[i:]
		if len(p) < 2 {
			d.pending = append([]byte(nil), p...)
			break
		}
		if p[1] != '_' {
			out = append(out, p[0])
			p = p[1:]
			continue
		}
		end := bytes.Index(p[2:], []byte{0x1b, '\\'})
		if end < 0 {
			if len(p) > apcMaxLen {
				out = append(out, p...)
			} else {
				d.pending = append([]byte(nil), p...)
			}
			break
		}
		seq := p[:2+end+2]
		body := p[2 : 2+end]
		p = p[len(seq):]
		if m, ok := d.parseMarker(body); ok {
			markers = append(markers, m)
		} else {
			out = append(out, seq...)
		}
	}
	return out, markers
}

func (d *Decoder) parseMarker(body []byte) (Marker, bool) {
	rest, ok := bytes.CutPrefix(body, []byte("rex1;"))
	if !ok {
		return Marker{}, false
	}
	nonceText, payload, ok := bytes.Cut(rest, []byte{';'})
	if !ok {
		return Marker{}, false
	}
	nonce, err := strconv.ParseUint(string(nonceText), 10, 64)
	if err != nil || nonce != d.nonce {
		return Marker{}, false
	}
	var m Marker
	if json.Unmarshal(payload, &m) != nil {
		return Marker{}, false
	}
	m.Raw = append(json.RawMessage(nil), payload...)
	return m, true
}
