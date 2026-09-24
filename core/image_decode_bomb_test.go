package core

// Every place core decodes an image from outside the process refuses a header
// that claims more pixels than it should, before allocating for them.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"runtime"
	"strings"
	"testing"
)

// bombPNG is a PNG declaring w x h RGBA pixels that carries one row of them:
// tiny on the wire, and w*h*4 bytes to a decoder that trusts the header (Go's
// allocates the whole image when the pixel data starts, then runs out of it).
func bombPNG(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	writeChunk := func(kind string, data []byte) {
		binary.Write(&b, binary.BigEndian, uint32(len(data)))
		chunk := append([]byte(kind), data...)
		b.Write(chunk)
		binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6 // 8-bit RGBA
	writeChunk("IHDR", ihdr)
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(make([]byte, 1+int(w)*4)) // one row: filter byte + pixels
	zw.Close()
	writeChunk("IDAT", z.Bytes())
	return b.Bytes()
}

// allocatedBy reports how many bytes fn allocated.
func allocatedBy(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestImageDecodeSitesRefuseABombHeader(t *testing.T) {
	// 60 megapixels: past the limit, and 240 MB to a decoder that believes it,
	// which is enough to see and small enough not to hurt if a site regresses.
	bomb := bombPNG(10000, 6000)
	const ceiling = 16 << 20

	if _, err := verifyInputImage("bomb.png", bomb); err == nil || !strings.Contains(err.Error(), "megapixel") {
		t.Errorf("a source image declaring 60 MP was accepted: %v", err)
	}
	if n := allocatedBy(func() { resizeImage(bomb) }); n > ceiling {
		t.Errorf("resizeImage allocated %d MB for a 60 MP header", n>>20)
	}
	if n := allocatedBy(func() { faceFraction(inputImage{name: "bomb.png", data: bomb}) }); n > ceiling {
		t.Errorf("faceFraction allocated %d MB for a 60 MP header", n>>20)
	}
}
