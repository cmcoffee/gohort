package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/flows"
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
	if !strings.Contains(parts[0].Text, "System Hooks") {
		t.Errorf("the note should say where an administrator fixes it: %q", parts[0].Text)
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

// adaptImages gives conversions a folder to run in, outside /tmp (which the
// sandbox replaces), and an adapted image conversion under test; both go
// when the test ends.
func adaptImages(t *testing.T, name, command string) {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no folder outside /tmp to run a conversion in")
	}
	base, err := os.MkdirTemp(cache, "gohort-core-convert-test-")
	if err != nil {
		t.Skip(err)
	}
	flows.SetWorkBase(base)
	if err := flows.SetAdapted(name, flows.Adapted{Takes: []string{flows.Any}, Becomes: flows.KindImage,
		Command: command, TimeoutSecs: 20}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		flows.RemoveAdapted(name)
		flows.SetWorkBase("")
		os.RemoveAll(base)
	})
}

// An image the model cannot read is converted when the admin has adapted a
// conversion for it, and reaches the model as what the conversion gave.
func TestAnUnreadableImageIsConverted(t *testing.T) {
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	src := append([]byte("GOHORTX!"), pngBuf.Bytes()...)
	adaptImages(t, "test-dd", "dd if={in} of={out} bs=8 skip=1")
	if _, err := flows.Convert(context.Background(), src, "", flows.KindImage); err != nil && strings.Contains(err.Error(), "sandbox") {
		t.Skipf("no sandbox here: %v", err)
	}

	var parts []struct {
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(oaiVisionContent("look", [][]byte{src}), &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(pngBuf.Bytes()) {
		t.Fatalf("want the text and the hook's PNG, got %+v", parts)
	}
	if parts[0].Text != "look" {
		t.Errorf("no note when it is converted: %q", parts[0].Text)
	}
}

// A conversion that fails is kept as failed, so the loop's later rounds do
// not run a broken command again.
func TestAFailedConversionIsNotRunEveryRound(t *testing.T) {
	src := append([]byte("GOHORTY!"), make([]byte, 64)...)
	adaptImages(t, "test-fails", "sh -c 'exit 3' {in} {out}")
	resizeImage(src)
	key := fmt.Sprintf("%x:%d:%d", sha256.Sum256(src), visionMaxDim(), visionJQQual())
	if _, ok := resizedImages.get(key); !ok {
		t.Error("the failed run should be kept")
	}
}
