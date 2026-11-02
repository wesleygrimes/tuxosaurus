package rexclient

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestKeyBytes(t *testing.T) {
	cases := map[string]string{
		"Enter": "\r", "enter": "\r", "Tab": "\t", "Esc": "\x1b", "Escape": "\x1b",
		"BSpace": "\x7f", "Backspace": "\x7f", "Space": " ",
		"Up": "\x1b[A", "Down": "\x1b[B", "Right": "\x1b[C", "Left": "\x1b[D",
		"Home": "\x1b[H", "End": "\x1b[F", "PageUp": "\x1b[5~", "NPage": "\x1b[6~",
		"DC": "\x1b[3~", "F1": "\x1bOP", "F12": "\x1b[24~", "BTab": "\x1b[Z",
		"C-c": "\x03", "C-C": "\x03", "C-d": "\x04", "C-z": "\x1a", "C-l": "\x0c",
		"C-a": "\x01", "C-[": "\x1b", `C-\`: "\x1c", "C-]": "\x1d", "C-_": "\x1f",
		"C-@": "\x00", "C-Space": "\x00", "C-?": "\x7f",
		"M-x": "\x1bx", "M-Enter": "\x1b\r", "M-C-c": "\x1b\x03",
		"y": "y", "é": "é",
	}
	for name, want := range cases {
		got, err := KeyBytes(name)
		if err != nil || got != want {
			t.Errorf("KeyBytes(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
	for _, bad := range []string{"", "Nope", "C-", "C-ab", "M-", "C-1"} {
		if _, err := KeyBytes(bad); err == nil {
			t.Errorf("KeyBytes(%q) succeeded", bad)
		}
	}
	got, err := KeysBytes([]string{"C-c", "Up", "Enter"})
	if err != nil || got != "\x03\x1b[A\r" {
		t.Fatalf("KeysBytes = %q, %v", got, err)
	}
}

func TestParseEndpoint(t *testing.T) {
	cases := map[string]string{
		"https://host.ts.net":     "wss://host.ts.net",
		"https://host.ts.net/":    "wss://host.ts.net",
		"http://127.0.0.1:51433":  "ws://127.0.0.1:51433",
		"host.ts.net":             "wss://host.ts.net",
		"unix:///tmp/server.sock": "ws://rex",
	}
	for raw, want := range cases {
		ep, err := ParseEndpoint(raw)
		if err != nil || ep.WSBase != want {
			t.Errorf("ParseEndpoint(%q) = %+v, %v; want %s", raw, ep, err, want)
		}
	}
	for _, bad := range []string{"", "unix://", "ftp://x", "https://"} {
		if _, err := ParseEndpoint(bad); err == nil {
			t.Errorf("ParseEndpoint(%q) succeeded", bad)
		}
	}
}

func TestDecodeTicket(t *testing.T) {
	want := []byte{1, 2, 3, 250, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255}
	for _, raw := range []string{`"AQID-gAAAAAAAAAAAAAA_w"`, `[1,2,3,250,0,0,0,0,0,0,0,0,0,0,0,255]`} {
		got, err := decodeTicket(json.RawMessage(raw))
		if err != nil || string(got) != string(want) {
			t.Errorf("decodeTicket(%s) = %v, %v", raw, got, err)
		}
	}
	if _, err := decodeTicket(json.RawMessage(`[256]`)); err == nil {
		t.Error("out-of-range byte accepted")
	}
}

func TestMatchSessionAndThread(t *testing.T) {
	sessions := []Session{
		{ID: "session:aaaa1111", Label: "work"},
		{ID: "session:aaaa2222", Label: "play"},
		{ID: "session:bbbb3333", Label: "work"},
	}
	if s, err := MatchSession(sessions, "play"); err != nil || s.ID != "session:aaaa2222" {
		t.Fatalf("by label: %v %v", s, err)
	}
	if s, err := MatchSession(sessions, "session:bbbb3333"); err != nil || s.Label != "work" {
		t.Fatalf("by id: %v %v", s, err)
	}
	if s, err := MatchSession(sessions, "bbbb"); err != nil || s.ID != "session:bbbb3333" {
		t.Fatalf("by bare prefix: %v %v", s, err)
	}
	if s, err := MatchSession(sessions, "session:aaaa1"); err != nil || s.Label != "work" {
		t.Fatalf("by full prefix: %v %v", s, err)
	}
	var amb *AmbiguousError
	if _, err := MatchSession(sessions, "work"); !errors.As(err, &amb) || len(amb.Matches) != 2 {
		t.Fatalf("duplicate label: %v", err)
	}
	if _, err := MatchSession(sessions, "aaaa"); !errors.As(err, &amb) {
		t.Fatalf("ambiguous prefix: %v", err)
	}
	if _, err := MatchSession(sessions, "zzz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// A label that looks like another thread's ID prefix still resolves by
	// label first.
	threads := []Thread{
		{Block: Block{ID: "block:cafe0001", Label: "cafe"}},
		{Block: Block{ID: "block:cafe0002", Label: "other"}},
	}
	if th, err := MatchThread(threads, "cafe"); err != nil || th.Block.ID != "block:cafe0001" {
		t.Fatalf("label precedence: %v %v", th, err)
	}
	if th, err := MatchThread(threads, "cafe0002"); err != nil || th.Block.Label != "other" {
		t.Fatalf("prefix: %v %v", th, err)
	}
}

func TestDeriveState(t *testing.T) {
	zero, one := 0, 1
	shell := &ProcessInfo{PID: 10, Name: "bash"}
	cases := []struct {
		name string
		st   Status
		want string
	}{
		{"exit wins", Status{ExitCode: &one, Program: &ProgramRecord{State: "working"}}, StateExited},
		{"exit zero", Status{ExitCode: &zero}, StateExited},
		{"working", Status{Program: &ProgramRecord{State: "working"}, Foreground: shell, Child: shell}, StateRunning},
		{"blocked", Status{Program: &ProgramRecord{State: "blocked", Kind: "permission"}}, StateWaiting},
		{"idle", Status{Program: &ProgramRecord{State: "idle"}}, StateWaiting},
		{"done", Status{Program: &ProgramRecord{State: "done"}}, StateDone},
		{"error", Status{Program: &ProgramRecord{State: "error"}}, StateError},
		{"shell prompt", Status{Foreground: shell, Child: shell}, StateWaiting},
		{"login shell", Status{Foreground: &ProcessInfo{PID: 10, Name: "-zsh"}, Child: shell}, StateWaiting},
		{"program running", Status{Foreground: &ProcessInfo{PID: 11, Name: "make"}, Child: shell}, StateRunning},
		{"exec'd program", Status{Foreground: &ProcessInfo{PID: 10, Name: "claude"}, Child: &ProcessInfo{PID: 10, Name: "claude"}}, StateRunning},
		{"nothing", Status{}, StateRunning},
	}
	for _, c := range cases {
		if got, _ := deriveState(c.st); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	latest := latestProgram([]ProgramRecord{
		{State: "done", UpdatedAt: "2026-01-01T00:00:02Z"},
		{State: "working", UpdatedAt: "2026-01-01T00:00:01Z"},
	})
	if latest.State != "done" {
		t.Fatalf("latest program record: %+v", latest)
	}
}

func TestStartPlan(t *testing.T) {
	o := StartOptions{Session: "proj", Label: "fix", Cwd: "/src", Command: []string{"claude", "-p", "hi"}, KeepOnExit: true}
	plan := StartPlan(nil, o)
	if len(plan) != 2 || plan[0].Method != "session.create" {
		t.Fatalf("new session plan: %+v", plan)
	}
	b, _ := json.Marshal(plan[0].Payload)
	want := `{"initial_windows":[{"layout":{"block":{"flavor":"com.superlogical.terminal.shell","label":"fix","options":{"command":["claude","-p","hi"],"cwd":"/src","exit":{"on_completion":false}}}},"window_label":"fix"}],"label":"proj"}`
	if string(b) != want {
		t.Fatalf("session.create payload:\n%s\nwant\n%s", b, want)
	}
	plan = StartPlan([]Session{{ID: "session:1", Label: "proj"}}, StartOptions{Session: "proj"})
	if len(plan) != 2 || plan[1].Method != "session.new_window" {
		t.Fatalf("existing session plan: %+v", plan)
	}
	b, _ = json.Marshal(plan[1].Payload)
	if string(b) != `{"focus":false,"layout":{"block":{"flavor":"com.superlogical.terminal.shell"}},"session_id":"session:1"}` {
		t.Fatalf("session.new_window payload: %s", b)
	}
}

func TestEventUnwrap(t *testing.T) {
	ev := Event{Kind: "session_event", Payload: json.RawMessage(`{"session_id":"s","event":{"type":"block_event","payload":{"block_id":"b","creator_name":"com.superlogical.terminal","name":"child_exited","payload":{"exit_code":7}}}}`)}
	be, ok := ev.AsBlockEvent()
	if !ok || be.SessionID != "s" || be.BlockID != "b" || be.Name != "child_exited" || string(be.Payload) != `{"exit_code":7}` {
		t.Fatalf("block event: %+v %v", be, ok)
	}
	closed := Event{Kind: "session_event", Payload: json.RawMessage(`{"session_id":"s","event":{"type":"block_closed","payload":{"block_id":"b"}}}`)}
	if _, ok := closed.AsBlockEvent(); ok {
		t.Fatal("block_closed parsed as block_event")
	}
	if sid, kind, payload := closed.SessionEventType(); sid != "s" || kind != "block_closed" || blockIDIn(payload) != "b" {
		t.Fatalf("session event: %s %s %s", sid, kind, payload)
	}
}
