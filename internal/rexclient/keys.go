package rexclient

import (
	"fmt"
	"strings"
)

// namedKeys maps tmux-style key names to the bytes a terminal sends. Cursor
// keys use normal-mode sequences (ESC [ A); readline, shells and most
// full-screen programs accept them in either cursor mode.
var namedKeys = map[string]string{
	"enter": "\r", "return": "\r", "tab": "\t", "btab": "\x1b[Z", "s-tab": "\x1b[Z",
	"esc": "\x1b", "escape": "\x1b", "space": " ",
	"bspace": "\x7f", "backspace": "\x7f",
	"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
	"home": "\x1b[H", "end": "\x1b[F",
	"pageup": "\x1b[5~", "ppage": "\x1b[5~", "pgup": "\x1b[5~",
	"pagedown": "\x1b[6~", "npage": "\x1b[6~", "pgdn": "\x1b[6~",
	"insert": "\x1b[2~", "ic": "\x1b[2~", "delete": "\x1b[3~", "dc": "\x1b[3~",
	"f1": "\x1bOP", "f2": "\x1bOQ", "f3": "\x1bOR", "f4": "\x1bOS",
	"f5": "\x1b[15~", "f6": "\x1b[17~", "f7": "\x1b[18~", "f8": "\x1b[19~",
	"f9": "\x1b[20~", "f10": "\x1b[21~", "f11": "\x1b[23~", "f12": "\x1b[24~",
}

// KeyBytes converts one key name to bytes. It accepts the names above
// (case-insensitive), C-<char> for control characters, M-<key> for an
// Escape prefix, and any single character as itself.
func KeyBytes(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty key name")
	}
	if alias, ok := keyAlias(name); ok {
		return KeyBytes(alias)
	}
	if len(name) > 2 && (name[:2] == "M-" || name[:2] == "m-") {
		rest, err := KeyBytes(name[2:])
		if err != nil {
			return "", err
		}
		return "\x1b" + rest, nil
	}
	if len(name) > 2 && (name[:2] == "C-" || name[:2] == "c-") {
		return controlKey(name[2:])
	}
	if s, ok := namedKeys[strings.ToLower(name)]; ok {
		return s, nil
	}
	if len([]rune(name)) == 1 {
		return name, nil
	}
	return "", fmt.Errorf("unknown key %q", name)
}

// keyAlias rewrites modifier spellings such as ctrl+c, Ctrl-C, alt+x and
// shift+tab into the tmux forms C-c, M-x and BTab.
func keyAlias(name string) (string, bool) {
	lower := strings.ToLower(name)
	if lower == "shift+tab" || lower == "shift-tab" {
		return "BTab", true
	}
	for _, m := range []struct{ prefix, tmux string }{
		{"ctrl+", "C-"}, {"ctrl-", "C-"}, {"control+", "C-"}, {"control-", "C-"},
		{"alt+", "M-"}, {"alt-", "M-"}, {"meta+", "M-"}, {"meta-", "M-"}, {"option+", "M-"},
	} {
		if len(lower) > len(m.prefix) && strings.HasPrefix(lower, m.prefix) {
			return m.tmux + name[len(m.prefix):], true
		}
	}
	return "", false
}

func controlKey(rest string) (string, error) {
	if strings.EqualFold(rest, "space") {
		return "\x00", nil
	}
	if len(rest) != 1 {
		return "", fmt.Errorf("unknown key C-%s", rest)
	}
	c := rest[0]
	switch {
	case c >= 'a' && c <= 'z':
		return string(rune(c - 'a' + 1)), nil
	case c >= '@' && c <= '_':
		// Covers C-@ (NUL), C-A..C-Z, C-[ (ESC), C-\, C-], C-^ and C-_.
		return string(rune(c - '@')), nil
	case c == '?':
		return "\x7f", nil
	case c == ' ':
		return "\x00", nil
	}
	return "", fmt.Errorf("unknown key C-%s", rest)
}

// KeysBytes converts a list of key names and concatenates the result.
func KeysBytes(names []string) (string, error) {
	var b strings.Builder
	for _, n := range names {
		s, err := KeyBytes(n)
		if err != nil {
			return "", err
		}
		b.WriteString(s)
	}
	return b.String(), nil
}
