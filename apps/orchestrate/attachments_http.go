// Serving a delivered attachment back to the thread that shows it.
//
// The store is in core (chat_attachments.go); this is the read side: one GET,
// scoped to the requesting user, returning the bytes a message id refers to.
// Ownership is the whole security model — an id is unguessable, but a request
// still only ever reads the caller's OWN directory, so a leaked id from another
// account resolves to nothing.
package orchestrate

import (
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handleAttachment serves one stored attachment: /api/attachment?id=att_…
func (T *OrchestrateApp) handleAttachment(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if !ValidChatAttachmentID(id) {
		http.Error(w, "bad attachment id", http.StatusBadRequest)
		return
	}
	data, mime, err := LoadChatAttachment(user, id)
	if err != nil {
		// A thread older than the retention cap has lost its pictures; its text
		// is intact. 404 so the <img> falls back to its alt text rather than the
		// page reporting an error it can do nothing about.
		http.Error(w, "attachment not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	// Immutable: an id names one set of bytes forever.
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
}

// keepDeliveredAttachments stores what a turn attached and returns the ids for
// the message that delivered them. Best-effort by design: a failure here costs
// the picture on RELOAD, never the delivery itself, which has already happened
// through the live stream or the channel.
func keepDeliveredAttachments(user string, images []string) []string {
	if strings.TrimSpace(user) == "" || len(images) == 0 {
		return nil
	}
	blobs := make([][]byte, 0, len(images))
	for _, b64 := range images {
		if data, err := DecodeBase64Attachment(b64); err == nil {
			blobs = append(blobs, data)
		}
	}
	return SaveChatAttachments(user, blobs)
}
