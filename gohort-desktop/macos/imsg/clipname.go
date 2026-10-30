package imsg

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
)

// videoClipName is what a clip on the video list (a song, a video) is called
// when Messages shows it: the title in its own ID3 tag, which the server
// writes for a song it knows the name of, or else <agent>-<id> from the item
// it came in. The agent is the one the server named only when that agent
// signs its messages, so a clip says no more than the text does; unsigned, it
// is "gohort". n numbers the second clip of one item on.
//
// Before, every one was a temp file, and arrived as phantom-vid-XXXX.mp3.
func videoClipName(agent, itemID string, data []byte, n int) string {
	name := id3Title(data)
	if name == "" {
		agent = strings.TrimSpace(agent)
		if agent == "" {
			agent = "gohort"
		}
		id := itemID
		if len(id) > 8 {
			id = id[:8]
		}
		name = agent
		if id != "" {
			name += "-" + id
		}
	}
	if n > 0 {
		name += fmt.Sprintf("-%d", n+1)
	}
	return name
}

// id3Title is the title (TIT2) in an mp3's ID3v2.3 or 2.4 tag, or "".
func id3Title(data []byte) string {
	if len(data) < 10 || string(data[:3]) != "ID3" {
		return ""
	}
	ver := data[3]
	if (ver != 3 && ver != 4) || data[5]&0xC0 != 0 {
		return "" // unsynchronised, or an extended header: not read
	}
	end := 10 + int(syncsafe(data[6:10]))
	if end > len(data) {
		return ""
	}
	frames := data[10:end]
	for pos := 0; pos+10 <= len(frames) && frames[pos] != 0; {
		n := int(binary.BigEndian.Uint32(frames[pos+4 : pos+8]))
		if ver == 4 {
			n = int(syncsafe(frames[pos+4 : pos+8]))
		}
		if n < 1 || pos+10+n > len(frames) {
			return ""
		}
		if string(frames[pos:pos+4]) == "TIT2" {
			return strings.TrimSpace(id3Text(frames[pos+10 : pos+10+n]))
		}
		pos += 10 + n
	}
	return ""
}

// id3Text decodes a text frame's body: its first byte says the encoding.
func id3Text(body []byte) string {
	enc, b := body[0], body[1:]
	switch enc {
	case 0: // Latin-1
		r := make([]rune, 0, len(b))
		for _, c := range b {
			if c == 0 {
				break
			}
			r = append(r, rune(c))
		}
		return string(r)
	case 1, 2: // UTF-16, with a byte order mark (1) or big-endian (2)
		big := enc == 2
		if enc == 1 && len(b) >= 2 {
			big = b[0] == 0xFE && b[1] == 0xFF
			b = b[2:]
		}
		var u []uint16
		for i := 0; i+1 < len(b); i += 2 {
			v := uint16(b[i]) | uint16(b[i+1])<<8
			if big {
				v = uint16(b[i])<<8 | uint16(b[i+1])
			}
			if v == 0 {
				break
			}
			u = append(u, v)
		}
		return string(utf16.Decode(u))
	case 3: // UTF-8
		s, _, _ := strings.Cut(string(b), "\x00")
		return s
	}
	return ""
}

func syncsafe(b []byte) uint32 {
	return uint32(b[0]&0x7F)<<21 | uint32(b[1]&0x7F)<<14 | uint32(b[2]&0x7F)<<7 | uint32(b[3]&0x7F)
}
