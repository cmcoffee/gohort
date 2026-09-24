package media

// A decode bomb is refused from its header, before the decoder allocates for
// the size it claims.

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"testing"
)

// pngHeaderClaiming is a PNG whose IHDR declares w x h and whose pixel data
// is missing: a few dozen bytes asking for as much memory as the header says.
func pngHeaderClaiming(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // RGBA
	binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	chunk := append([]byte("IHDR"), ihdr...)
	b.Write(chunk)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func TestDecodeImageRefusesABombFromItsHeader(t *testing.T) {
	bomb := pngHeaderClaiming(100000, 100000)
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(bomb)); err != nil || cfg.Width != 100000 {
		t.Fatalf("fixture: the header should parse as 100000 wide: %+v %v", cfg, err)
	}
	if _, _, err := DecodeImage(bomb); err == nil {
		t.Fatal("a 10-gigapixel header was decoded")
	} else if !bytes.Contains([]byte(err.Error()), []byte("megapixel")) {
		t.Errorf("the refusal should name the limit: %v", err)
	}

	var ok bytes.Buffer
	png.Encode(&ok, image.NewRGBA(image.Rect(0, 0, 64, 48)))
	img, format, err := DecodeImage(ok.Bytes())
	if err != nil || format != "png" || img.Bounds().Dx() != 64 {
		t.Errorf("an ordinary image: %v %q %v", img, format, err)
	}
}

func TestCheckImageDimensions(t *testing.T) {
	for _, c := range []struct {
		w, h int
		ok   bool
	}{
		{8000, 6000, true},   // 48 MP phone sensor
		{7680, 4320, true},   // 8K frame
		{10000, 5001, false}, // just past 50 MP
		{1, 60_000_000, false},
		{0, 100, false},
	} {
		if err := CheckImageDimensions(c.w, c.h); (err == nil) != c.ok {
			t.Errorf("%dx%d: err=%v, want ok=%v", c.w, c.h, err, c.ok)
		}
	}
}
