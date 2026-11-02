package rexclient

// PlainFilter reduces a VT output stream to approximate plain text for
// logging and pattern matching. It drops escape sequences and control
// characters other than newline and tab. It does not emulate the screen, so
// cursor movement and redraws are lost; use the format method for an exact
// screen. State carries across chunks.
type PlainFilter struct {
	state int
}

const (
	vtGround = iota
	vtEsc
	vtCSI
	vtString    // OSC, DCS, APC, PM, SOS: ends at BEL or ESC \
	vtStringEsc // ESC seen inside a string
	vtEscInter  // ESC followed by intermediate bytes, e.g. ESC ( B
)

func (f *PlainFilter) Write(p []byte) []byte {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		switch f.state {
		case vtGround:
			switch {
			case b == 0x1b:
				f.state = vtEsc
			case b == '\n' || b == '\t':
				out = append(out, b)
			case b < 0x20 || b == 0x7f:
				// Carriage returns and other controls are dropped; a CRLF
				// becomes a plain newline.
			default:
				out = append(out, b)
			}
		case vtEsc:
			switch {
			case b == '[':
				f.state = vtCSI
			case b == ']' || b == 'P' || b == '_' || b == '^' || b == 'X':
				f.state = vtString
			case b >= 0x20 && b <= 0x2f:
				f.state = vtEscInter
			default:
				f.state = vtGround
			}
		case vtEscInter:
			if b < 0x20 || b > 0x2f {
				f.state = vtGround
			}
		case vtCSI:
			if b >= 0x40 && b <= 0x7e {
				f.state = vtGround
			}
		case vtString:
			switch b {
			case 0x07:
				f.state = vtGround
			case 0x1b:
				f.state = vtStringEsc
			}
		case vtStringEsc:
			if b == '\\' {
				f.state = vtGround
			} else {
				f.state = vtString
			}
		}
	}
	return out
}
