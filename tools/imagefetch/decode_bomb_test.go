package imagefetch

// A downloaded image is whatever a remote server sent; one whose header
// claims more pixels than the limit is refused before it is allocated.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"runtime"
	"testing"
)

func TestNormalizeToJPEGRefusesABombHeader(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	writeChunk := func(kind string, data []byte) {
		binary.Write(&b, binary.BigEndian, uint32(len(data)))
		chunk := append([]byte(kind), data...)
		b.Write(chunk)
		binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 10000) // 60 MP: 240 MB to a trusting decoder
	binary.BigEndian.PutUint32(ihdr[4:], 6000)
	ihdr[8], ihdr[9] = 8, 6
	writeChunk("IHDR", ihdr)
	// One row of pixel data: Go's decoder allocates the whole image when the
	// data starts, then runs out of it.
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(make([]byte, 1+10000*4))
	zw.Close()
	writeChunk("IDAT", z.Bytes())

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, _, _, ok := normalizeToJPEG(b.Bytes())
	runtime.ReadMemStats(&after)
	if ok {
		t.Fatal("a 60 MP header with one row of data normalized")
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 16<<20 {
		t.Errorf("normalizeToJPEG allocated %d MB for a 60 MP header", n>>20)
	}
}
