package mcpserver

// ask_agent produced pictures and returned a string. The envelope was
// text-only, so an agent could generate an image, attach it, see it in its own
// thread — and the caller received a description of something that never
// travelled. MCP content blocks are the channel; these cover the wrapping.

import (
	"encoding/base64"
	"strings"
	"testing"
)

// onePixelPNG is a real PNG so the mime sniff has something to read.
func onePixelPNG(t *testing.T) string {
	t.Helper()
	const b64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	if _, err := base64.StdEncoding.DecodeString(b64); err != nil {
		t.Fatal(err)
	}
	return b64
}

func TestImagesRideBackAsContentBlocks(t *testing.T) {
	img := onePixelPNG(t)
	res := toolResult("here is the render", []string{img})
	content, _ := res["content"].([]map[string]any)
	if len(content) != 2 {
		t.Fatalf("got %d content block(s), want text + image", len(content))
	}
	if content[0]["type"] != "text" || content[0]["text"] != "here is the render" {
		t.Errorf("first block is not the reply text: %+v", content[0])
	}
	if content[1]["type"] != "image" {
		t.Fatalf("second block is not an image: %+v", content[1])
	}
	if content[1]["data"] != img {
		t.Error("the image data was altered in transit")
	}
	if mt, _ := content[1]["mimeType"].(string); !strings.HasPrefix(mt, "image/") {
		t.Errorf("mimeType = %q, want an image type sniffed from the bytes", mt)
	}
	if res["isError"] != false {
		t.Error("a successful reply must not be flagged an error")
	}
}

func TestTextOnlyReplyIsUnchanged(t *testing.T) {
	res := toolResult("just words", nil)
	content, _ := res["content"].([]map[string]any)
	if len(content) != 1 || content[0]["type"] != "text" {
		t.Fatalf("a reply with no images grew extra blocks: %+v", content)
	}
}

// What cannot travel must be SAID. A silent drop reads as the agent claiming an
// image it never sent, which is the failure this channel exists to prevent.
func TestDroppedImagesAreNamedInTheText(t *testing.T) {
	img := onePixelPNG(t)
	many := []string{img, img, img, img, img, img}
	res := toolResult("six renders", many)
	content, _ := res["content"].([]map[string]any)
	images := 0
	for _, c := range content {
		if c["type"] == "image" {
			images++
		}
	}
	if images != maxMCPImages {
		t.Fatalf("sent %d image(s), want the %d cap", images, maxMCPImages)
	}
	text, _ := content[0]["text"].(string)
	if !strings.Contains(text, "not included") {
		t.Errorf("the held-back images are not mentioned: %q", text)
	}
	// Undecodable input is skipped, not sent as a broken block.
	bad := toolResult("one bad", []string{"!!!not base64!!!"})
	bc, _ := bad["content"].([]map[string]any)
	for _, c := range bc {
		if c["type"] == "image" {
			t.Error("an undecodable image was sent as a content block")
		}
	}
}
