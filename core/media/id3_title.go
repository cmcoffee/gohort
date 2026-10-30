package media

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// WithID3Title is an mp3 with title as its ID3 title (TIT2), or data
// unchanged when it is not an mp3, title is empty, or its tag is one this
// cannot rewrite safely (unsynchronised, an extended header, a footer).
//
// A song travels to a messaging channel as bytes, with no name beside it at
// any hop, and the connector writes it to a temp file Messages then shows by
// that name. In the file, the title reaches the connector whatever path the
// song took, and a player shows it too.
//
// An existing tag keeps its version and every other frame; a file with none
// gets an ID3v2.3 tag, the version every player reads.
func WithID3Title(data []byte, title string) []byte {
	title = strings.Join(strings.Fields(title), " ")
	if title == "" || len(data) < 4 {
		return data
	}
	if string(data[:3]) != "ID3" {
		if data[0] != 0xFF || data[1]&0xE0 != 0xE0 {
			return data // not an mp3 frame either
		}
		tag := id3Tag(3, id3TitleFrame(3, title))
		return append(tag, data...)
	}
	if len(data) < 10 {
		return data
	}
	ver, flags := data[3], data[5]
	if (ver != 3 && ver != 4) || flags != 0 {
		return data
	}
	size := int(syncsafe(data[6:10]))
	if 10+size > len(data) {
		return data
	}
	frames := data[10 : 10+size]
	kept := id3TitleFrame(ver, title)
	for pos := 0; pos+10 <= len(frames); {
		id := string(frames[pos : pos+4])
		if frames[pos] == 0 {
			break // padding
		}
		var n int
		if ver == 4 {
			n = int(syncsafe(frames[pos+4 : pos+8]))
		} else {
			n = int(binary.BigEndian.Uint32(frames[pos+4 : pos+8]))
		}
		end := pos + 10 + n
		if n < 0 || end > len(frames) {
			return data // a frame runs past its tag
		}
		if id != "TIT2" {
			kept = append(kept, frames[pos:end]...)
		}
		pos = end
	}
	return append(id3Tag(ver, kept), data[10+size:]...)
}

// id3Tag is an ID3v2 header of version ver around frames.
func id3Tag(ver byte, frames []byte) []byte {
	out := []byte{'I', 'D', '3', ver, 0, 0}
	out = append(out, putSyncsafe(uint32(len(frames)))...)
	return append(out, frames...)
}

// id3TitleFrame is a TIT2 frame for version ver: UTF-8 in 2.4, UTF-16 with a
// byte order mark in 2.3, which has no UTF-8.
func id3TitleFrame(ver byte, title string) []byte {
	var body []byte
	if ver == 4 {
		body = append([]byte{3}, title...)
	} else {
		body = []byte{1, 0xFF, 0xFE}
		for _, u := range utf16.Encode([]rune(title)) {
			body = append(body, byte(u), byte(u>>8))
		}
	}
	frame := []byte("TIT2")
	if ver == 4 {
		frame = append(frame, putSyncsafe(uint32(len(body)))...)
	} else {
		frame = binary.BigEndian.AppendUint32(frame, uint32(len(body)))
	}
	frame = append(frame, 0, 0)
	return append(frame, body...)
}

func syncsafe(b []byte) uint32 {
	return uint32(b[0]&0x7F)<<21 | uint32(b[1]&0x7F)<<14 | uint32(b[2]&0x7F)<<7 | uint32(b[3]&0x7F)
}

func putSyncsafe(n uint32) []byte {
	return []byte{byte(n>>21) & 0x7F, byte(n>>14) & 0x7F, byte(n>>7) & 0x7F, byte(n) & 0x7F}
}
