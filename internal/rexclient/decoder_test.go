package rexclient

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"testing"
)

// The fixtures are fresh data streams captured from Tuxosaurus built against
// the pinned libghostty: a Ghostty snapshot followed by the initial resize
// marker. Each stream's nonce comes from its data welcome.
var streamFixtures = []struct {
	file       string
	nonce      uint64
	cols, rows int
}{
	{"testdata/stream-80x24.bin", 8778933137906247938, 80, 24},
	{"testdata/stream-120x40.bin", 16142536836468912221, 120, 40},
}

func feedChunks(d *Decoder, data []byte, size int) ([]byte, []Marker) {
	var out []byte
	var markers []Marker
	for len(data) > 0 {
		n := min(size, len(data))
		o, m := d.Feed(data[:n])
		out = append(out, o...)
		markers = append(markers, m...)
		data = data[n:]
	}
	return append(out, d.Flush()...), markers
}

func marker(nonce uint64, json string) string {
	return fmt.Sprintf("\x1b_rex1;%d;%s\x1b\\", nonce, json)
}

func TestDecoderSkipsRealSnapshot(t *testing.T) {
	for _, f := range streamFixtures {
		data, err := os.ReadFile(f.file)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(data, []byte(snapshotMagic)) {
			t.Fatalf("%s: fixture lacks snapshot magic", f.file)
		}
		// Live output after the snapshot, split across the same chunks.
		tail := "after\r\n" + marker(f.nonce, `{"type":"child_exited","exit_code":3}`) + "done"
		stream := append(append([]byte(nil), data...), tail...)
		for _, size := range []int{1, 2, 3, 7, 10, 64, 1000, len(stream)} {
			d := NewDecoder(f.nonce)
			out, markers := feedChunks(d, stream, size)
			if string(out) != "after\r\ndone" {
				t.Fatalf("%s chunk %d: output %q", f.file, size, out)
			}
			if len(markers) != 2 || markers[0].Type != "resize" || markers[0].Cols != f.cols || markers[0].Rows != f.rows {
				t.Fatalf("%s chunk %d: markers %+v", f.file, size, markers)
			}
			if markers[1].Type != "child_exited" || markers[1].ExitCode == nil || *markers[1].ExitCode != 3 {
				t.Fatalf("%s chunk %d: exit marker %+v", f.file, size, markers[1])
			}
		}
	}
}

func TestDecoderEverySplitPoint(t *testing.T) {
	f := streamFixtures[0]
	data, err := os.ReadFile(f.file)
	if err != nil {
		t.Fatal(err)
	}
	stream := append(append([]byte(nil), data...), "tail"...)
	for i := 0; i <= len(stream); i++ {
		d := NewDecoder(f.nonce)
		o1, m1 := d.Feed(stream[:i])
		o2, m2 := d.Feed(stream[i:])
		out := string(o1) + string(o2) + string(d.Flush())
		if out != "tail" || len(m1)+len(m2) != 1 {
			t.Fatalf("split %d: output %q markers %d", i, out, len(m1)+len(m2))
		}
	}
}

func TestDecoderPassesForeignAPC(t *testing.T) {
	d := NewResumeDecoder(42)
	in := "a" + marker(41, `{"type":"resize","cols":1,"rows":1}`) + "b\x1b_Gf=24;AAAA\x1b\\c\x1b[31md"
	out, markers := feedChunks(d, []byte(in), 3)
	if string(out) != in {
		t.Fatalf("output %q", out)
	}
	if len(markers) != 0 {
		t.Fatalf("a marker with the wrong nonce was accepted: %+v", markers)
	}
}

func TestDecoderWithoutSnapshot(t *testing.T) {
	d := NewDecoder(1)
	out, _ := feedChunks(d, []byte("plain output"), 4)
	if string(out) != "plain output" {
		t.Fatalf("output %q", out)
	}
}

func TestDecoderSnapshotWithoutEndRecord(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(snapshotMagic)
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint32(3))
	binary.Write(&b, binary.LittleEndian, uint32(0))
	b.WriteString("xyz")
	// No end record: VT output starts with ESC, which cannot begin a record
	// header of a plausible type.
	b.WriteString("\x1b[1mbold")
	out, _ := feedChunks(NewDecoder(1), b.Bytes(), 5)
	if string(out) != "\x1b[1mbold" {
		t.Fatalf("output %q", out)
	}
}

func TestDecoderUnterminatedAPCIsBounded(t *testing.T) {
	d := NewResumeDecoder(1)
	big := "\x1b_" + string(bytes.Repeat([]byte("x"), apcMaxLen+10))
	out, _ := feedChunks(d, []byte(big), 4096)
	if len(out) != len(big) {
		t.Fatalf("unterminated APC was swallowed: %d of %d bytes", len(out), len(big))
	}
}

func TestPlainFilter(t *testing.T) {
	var f PlainFilter
	in := "\x1b[31mred\x1b[0m\r\nnext\x1b]0;title\x07line\x1b]8;;http://x\x1b\\link\x1b(Bok\ttab\x07\n"
	var out []byte
	for i := 0; i < len(in); i++ {
		out = append(out, f.Write([]byte{in[i]})...)
	}
	if string(out) != "red\nnextlinelinkok\ttab\n" {
		t.Fatalf("plain %q", out)
	}
}
