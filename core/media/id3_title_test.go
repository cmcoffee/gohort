package media

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

// readTitle reads TIT2 back out of a tag, the way a player does.
func readTitle(t *testing.T, data []byte) (string, []string) {
	t.Helper()
	if string(data[:3]) != "ID3" {
		t.Fatalf("no tag: %q", data[:3])
	}
	ver := data[3]
	frames := data[10 : 10+int(syncsafe(data[6:10]))]
	var title string
	var ids []string
	for pos := 0; pos+10 <= len(frames) && frames[pos] != 0; {
		id := string(frames[pos : pos+4])
		n := int(binary.BigEndian.Uint32(frames[pos+4 : pos+8]))
		if ver == 4 {
			n = int(syncsafe(frames[pos+4 : pos+8]))
		}
		body := frames[pos+10 : pos+10+n]
		ids = append(ids, id)
		if id == "TIT2" {
			switch body[0] {
			case 3:
				title = string(body[1:])
			case 1:
				var u []uint16
				for i := 3; i+1 < len(body); i += 2 {
					u = append(u, uint16(body[i])|uint16(body[i+1])<<8)
				}
				title = string(utf16.Decode(u))
			}
		}
		pos += 10 + n
	}
	return title, ids
}

func TestWithID3Title(t *testing.T) {
	audio := []byte{0xFF, 0xFB, 0x90, 0x44, 1, 2, 3}

	// A song generator's mp3: a 2.4 tag holding one TXXX frame and padding.
	txxx := append([]byte("TXXX"), putSyncsafe(5)...)
	txxx = append(txxx, 0, 0, 3, 'k', 0, 'v', 0)
	frames := append(txxx, make([]byte, 20)...)
	tagged := append(append([]byte{'I', 'D', '3', 4, 0, 0}, putSyncsafe(uint32(len(frames)))...), frames...)
	tagged = append(tagged, audio...)

	out := WithID3Title(tagged, "Money in the Cloud")
	if title, ids := readTitle(t, out); title != "Money in the Cloud" || len(ids) != 2 || ids[1] != "TXXX" {
		t.Errorf("a 2.4 tag gains the title and keeps its frames: %q %v", title, ids)
	}
	if !bytes.HasSuffix(out, audio) {
		t.Error("the audio after the tag is untouched")
	}
	// Retitled, the old title goes rather than sitting beside the new one.
	again := WithID3Title(out, "Gas Prices Are Too High")
	if title, ids := readTitle(t, again); title != "Gas Prices Are Too High" || len(ids) != 2 {
		t.Errorf("one title, the new one: %q %v", title, ids)
	}

	// An mp3 with no tag gets a 2.3 one, in UTF-16.
	bare := WithID3Title(audio, "Café Drain")
	if title, _ := readTitle(t, bare); title != "Café Drain" || bare[3] != 3 {
		t.Errorf("a bare mp3 gets a 2.3 title: %q v%d", title, bare[3])
	}
	if !bytes.HasSuffix(bare, audio) {
		t.Error("the bare mp3's audio is untouched")
	}

	// Left alone: no title, not an mp3, a tag this cannot rewrite safely.
	unsync := append([]byte{'I', 'D', '3', 3, 0, 0x80, 0, 0, 0, 0}, audio...)
	for name, in := range map[string][]byte{
		"not mp3":      []byte("\x00\x00\x00\x20ftypM4A \x00\x00"),
		"unsynced tag": unsync,
	} {
		if got := WithID3Title(in, "x"); !bytes.Equal(got, in) {
			t.Errorf("%s should be left as it is", name)
		}
	}
	if got := WithID3Title(audio, "  "); !bytes.Equal(got, audio) {
		t.Error("an empty title changes nothing")
	}
}
