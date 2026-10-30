package imsg

import (
	"encoding/binary"
	"testing"
)

// tag is an ID3 tag of version ver holding one TIT2 frame with body.
func tag(ver byte, body []byte) []byte {
	frame := []byte("TIT2")
	if ver == 4 {
		n := uint32(len(body))
		frame = append(frame, byte(n>>21)&0x7F, byte(n>>14)&0x7F, byte(n>>7)&0x7F, byte(n)&0x7F)
	} else {
		frame = binary.BigEndian.AppendUint32(frame, uint32(len(body)))
	}
	frame = append(append(frame, 0, 0), body...)
	n := uint32(len(frame))
	out := []byte{'I', 'D', '3', ver, 0, 0, byte(n>>21) & 0x7F, byte(n>>14) & 0x7F, byte(n>>7) & 0x7F, byte(n) & 0x7F}
	return append(append(out, frame...), 0xFF, 0xFB, 0x90, 0x44)
}

// A song is named by the title in its tag, in each encoding a tag writes; a
// clip with none by the agent that sent it and the item, never a temp name.
// Songs arrived as phantom-vid-XXXX.mp3.
func TestVideoClipName(t *testing.T) {
	utf16le := []byte{1, 0xFF, 0xFE}
	for _, r := range "Café Drain" {
		utf16le = append(utf16le, byte(r), byte(r>>8))
	}
	for name, data := range map[string][]byte{
		"2.4 UTF-8":      tag(4, append([]byte{3}, "Café Drain"...)),
		"2.3 UTF-16 BOM": tag(3, utf16le),
		"2.3 Latin-1":    tag(3, append([]byte{0}, "Caf\xe9 Drain\x00"...)),
		"2.4 terminated": tag(4, append([]byte{3}, "Café Drain\x00"...)),
	} {
		if got := videoClipName("WiWee", "8701d742654bc7fe", data, 0); got != "Café Drain" {
			t.Errorf("%s: %q", name, got)
		}
	}

	untitled := []byte{0xFF, 0xFB, 0x90, 0x44}
	cases := []struct {
		agent, id string
		n         int
		want      string
	}{
		{"WiWee", "8701d742654bc7fe", 0, "WiWee-8701d742"},
		{"", "8701d742654bc7fe", 0, "gohort-8701d742"}, // unsigned: no name it would not show
		{"WiWee", "8701d742654bc7fe", 1, "WiWee-8701d742-2"},
		{"WiWee", "", 0, "WiWee"},
	}
	for _, c := range cases {
		if got := videoClipName(c.agent, c.id, untitled, c.n); got != c.want {
			t.Errorf("videoClipName(%q, %q, n=%d) = %q, want %q", c.agent, c.id, c.n, got, c.want)
		}
	}
	// A tag that does not hold together names nothing rather than garbage.
	broken := tag(4, append([]byte{3}, "Café Drain"...))
	broken[9] = 0x7F
	if got := id3Title(broken); got != "" {
		t.Errorf("a tag longer than the file: %q", got)
	}
}
