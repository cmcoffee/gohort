package core

// Keeping a delivered picture. The bytes used to exist only as a live SSE
// event, so a reloaded thread showed the text that described an image and no
// image — and a message posted with nobody watching never showed one at all.

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func attachmentTestDir(t *testing.T) {
	t.Helper()
	saved := ImageDir()
	SetImageDir(t.TempDir())
	t.Cleanup(func() { SetImageDir(saved) })
}

func attachmentPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func TestDeliveredAttachmentSurvivesForTheReload(t *testing.T) {
	attachmentTestDir(t)
	want := attachmentPNG(t)

	id, err := SaveChatAttachment("alice", want)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !ValidChatAttachmentID(id) {
		t.Fatalf("id %q is not a well-formed attachment id", id)
	}
	got, mime, err := LoadChatAttachment("alice", id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("the bytes that came back are not the ones stored")
	}
	if mime != "image/png" {
		t.Errorf("mime = %q, want image/png", mime)
	}
}

func TestAnAttachmentIsReadableOnlyByItsOwner(t *testing.T) {
	// The id is unguessable, but ownership is what actually enforces the
	// boundary: a leaked id resolves against the CALLER's own directory, where
	// it does not exist.
	attachmentTestDir(t)
	id, err := SaveChatAttachment("alice", attachmentPNG(t))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, _, err := LoadChatAttachment("bob", id); err == nil {
		t.Fatal("another user must not be able to read alice's attachment")
	}
}

func TestAttachmentIDsCannotNameAPath(t *testing.T) {
	for _, bad := range []string{
		"",
		"att_",
		"../../etc/passwd",
		"att_../../etc/passwd",
		"att_a/b",
		"att_A", // uppercase isn't in the id alphabet
		"other_" + strings.Repeat("a", 10),
		"att_" + strings.Repeat("a", 100),
	} {
		if ValidChatAttachmentID(bad) {
			t.Errorf("%q must not pass as an attachment id", bad)
		}
	}
}

func TestOnlyImagesAreKept(t *testing.T) {
	attachmentTestDir(t)
	if _, err := SaveChatAttachment("alice", []byte("this is a text file, not a picture")); err == nil {
		t.Error("a non-image must be refused rather than stored as something the browser will guess at")
	}
	if _, err := SaveChatAttachment("alice", nil); err == nil {
		t.Error("empty bytes must be refused")
	}
	if _, err := SaveChatAttachment("", attachmentPNG(t)); err == nil {
		t.Error("an unscoped attachment has no owner to serve it to")
	}
}

func TestSaveChatAttachmentsKeepsOrderAndSkipsFailures(t *testing.T) {
	attachmentTestDir(t)
	ids := SaveChatAttachments("alice", [][]byte{attachmentPNG(t), []byte("not an image"), attachmentPNG(t)})
	if len(ids) != 2 {
		t.Fatalf("kept %d, want the 2 real images", len(ids))
	}
	for _, id := range ids {
		if _, _, err := LoadChatAttachment("alice", id); err != nil {
			t.Errorf("id %q did not load back: %v", id, err)
		}
	}
}

func TestDecodeBase64AttachmentTakesEitherForm(t *testing.T) {
	// Both circulate — a bare base64 payload and a data: URI — and which one a
	// given tool produced is not something a caller should have to know.
	raw := attachmentPNG(t)
	b64 := base64.StdEncoding.EncodeToString(raw)
	for _, form := range []string{b64, "data:image/png;base64," + b64} {
		got, err := DecodeBase64Attachment(form)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !bytes.Equal(got, raw) {
			t.Error("decoded bytes differ from the original")
		}
	}
}

func TestTheStoreStaysBounded(t *testing.T) {
	// Past the cap the oldest go, so a thread old enough to fall off the end
	// loses its pictures rather than the disk filling. The text is unaffected.
	attachmentTestDir(t)
	dir := chatAttachmentDir("alice")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < maxChatAttachments+5; i++ {
		if _, err := os.Create(filepath.Join(dir, ChatAttachmentIDPrefix+strings.Repeat("a", 8)+string(rune('a'+i%26))+"-"+itoaTest(i)+".png")); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	pruneChatAttachments(dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) > maxChatAttachments {
		t.Errorf("%d files remain, over the %d cap", len(entries), maxChatAttachments)
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
