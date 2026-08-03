// Delivered attachments, kept so a reloaded conversation can still show them.
//
// The problem: an image an agent delivered existed only as an in-flight event.
// Chat streamed it down the live SSE connection and the browser painted it; the
// stored message recorded the text and nothing else. Reload the thread and the
// picture was gone — and a message posted while nobody was watching (a finished
// background task) had no live stream to paint to in the first place, so it was
// never visible at all.
//
// So the bytes get a home. A message stores IDs, never base64: the session
// record is loaded whole on every turn and folded into prompt history, and a
// megabyte of base64 sitting in it would be paid for on each of those.
//
// Scoped per user and served only to that user (see the app's handler). IDs are
// unguessable, but the ownership check is what enforces the boundary.
package core

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ChatAttachmentIDPrefix marks a stored attachment reference. Present so a
// caller reading a message can tell an attachment id from any other string
// without consulting the store.
const ChatAttachmentIDPrefix = "att_"

// maxChatAttachments bounds how many a single user keeps. Past it the oldest
// are evicted, so a thread old enough to fall off the end loses its pictures
// rather than the disk filling — the text of the conversation is unaffected.
const maxChatAttachments = 500

// maxChatAttachmentBytes caps one stored attachment. Matches the delivery caps
// upstream, so nothing that could be delivered is too large to keep.
const maxChatAttachmentBytes = 20 << 20 // 20 MiB

// chatAttachmentExt maps a sniffed content type to the extension the file is
// stored under. The extension is the ONLY record of the type — it is what the
// serving handler reads the content type back out of, so an unrecognized type
// is refused rather than stored as something a browser will guess at.
var chatAttachmentExt = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// chatAttachmentDir is where a user's delivered attachments live.
func chatAttachmentDir(user string) string {
	user = strings.TrimSpace(user)
	if user == "" {
		return ""
	}
	return filepath.Join(ImageDir(), "delivered", safeRecentUser(user))
}

// SaveChatAttachment stores delivered bytes for a user and returns the id a
// message should carry. Best-effort by contract: an error means the message
// still says what it said, just without a picture attached to it — never that
// the delivery itself failed.
func SaveChatAttachment(user string, data []byte) (string, error) {
	dir := chatAttachmentDir(user)
	if dir == "" {
		return "", fmt.Errorf("no user to scope the attachment to")
	}
	if len(data) == 0 {
		return "", fmt.Errorf("empty attachment")
	}
	if len(data) > maxChatAttachmentBytes {
		return "", fmt.Errorf("attachment is %s, over the %s cap", HumanSize(int64(len(data))), HumanSize(maxChatAttachmentBytes))
	}
	ext, ok := chatAttachmentExt[strings.SplitN(http.DetectContentType(data), ";", 2)[0]]
	if !ok {
		return "", fmt.Errorf("unsupported attachment type %q", http.DetectContentType(data))
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	id := ChatAttachmentIDPrefix + UUIDv4()
	if err := os.WriteFile(filepath.Join(dir, id+ext), data, 0600); err != nil {
		return "", err
	}
	pruneChatAttachments(dir)
	return id, nil
}

// LoadChatAttachment returns one stored attachment's bytes and content type.
// The id is matched against the user's OWN directory, so a valid id belonging
// to someone else simply isn't found here.
func LoadChatAttachment(user, id string) ([]byte, string, error) {
	dir := chatAttachmentDir(user)
	if dir == "" || !ValidChatAttachmentID(id) {
		return nil, "", fmt.Errorf("no such attachment")
	}
	for mime, ext := range chatAttachmentExt {
		data, err := os.ReadFile(filepath.Join(dir, id+ext))
		if err == nil {
			return data, mime, nil
		}
	}
	return nil, "", fmt.Errorf("no such attachment")
}

// ValidChatAttachmentID reports whether id is a well-formed attachment id. The
// gate on the serving path: it is what keeps a request from naming a path
// instead of an id.
func ValidChatAttachmentID(id string) bool {
	if !strings.HasPrefix(id, ChatAttachmentIDPrefix) || len(id) > 64 {
		return false
	}
	for _, r := range strings.TrimPrefix(id, ChatAttachmentIDPrefix) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return len(id) > len(ChatAttachmentIDPrefix)
}

// DecodeBase64Attachment turns one of the base64 blobs the session carries in
// its outbound-attachment channels back into bytes, tolerating a data: URI
// prefix — both forms circulate, and which one a given tool produced is not
// something a caller should have to know.
func DecodeBase64Attachment(b64 string) ([]byte, error) {
	return decodeBase64Image(b64)
}

// SaveChatAttachments stores several at once, keeping order and skipping the
// ones that fail. Returns the ids that stuck.
func SaveChatAttachments(user string, blobs [][]byte) []string {
	var out []string
	for _, b := range blobs {
		id, err := SaveChatAttachment(user, b)
		if err != nil {
			Log("[attachment] could not keep a delivered attachment for %s: %v", user, err)
			continue
		}
		out = append(out, id)
	}
	return out
}

// pruneChatAttachments evicts the oldest past the cap. Oldest-first because a
// recent conversation is the one likely to be reopened.
func pruneChatAttachments(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= maxChatAttachments {
		return
	}
	type aged struct {
		name string
		mod  int64
	}
	files := make([]aged, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		files = append(files, aged{e.Name(), info.ModTime().UnixNano()})
	}
	if len(files) <= maxChatAttachments {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod })
	for _, f := range files[:len(files)-maxChatAttachments] {
		os.Remove(filepath.Join(dir, f.name))
	}
}
