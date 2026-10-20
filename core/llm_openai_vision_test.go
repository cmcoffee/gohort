package core

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"
)

// An image the model cannot read is left out with a note in its place, not
// sent as a JPEG it is not: vLLM fails the whole turn over it.
func TestAnUnreadableImageIsLeftOutAndNamed(t *testing.T) {
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	heic := append([]byte{0, 0, 0, 24}, []byte("ftypheic\x00\x00\x00\x00mif1heic")...)
	heic = append(heic, make([]byte, 64)...)

	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(oaiVisionContent("what is this?", [][]byte{heic, pngBuf.Bytes()}), &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("want the text and the one readable image, got %d parts", len(parts))
	}
	if !strings.Contains(parts[0].Text, "HEIC") || !strings.HasSuffix(parts[0].Text, "what is this?") {
		t.Errorf("the note should name the format ahead of the message: %q", parts[0].Text)
	}
	if parts[1].ImageURL == nil || !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("the readable image should go as itself: %+v", parts[1])
	}

	// All readable: no note.
	json.Unmarshal(oaiVisionContent("hi", [][]byte{pngBuf.Bytes()}), &parts)
	if parts[0].Text != "hi" {
		t.Errorf("no note without an unreadable image: %q", parts[0].Text)
	}
}
