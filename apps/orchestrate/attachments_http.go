// Serving a delivered attachment back to the thread that shows it.
//
// The store is in core (chat_attachments.go); this is the read side: one GET,
// scoped to the requesting user, returning the bytes a message id refers to.
// Ownership is the whole security model — an id is unguessable, but a request
// still only ever reads the caller's OWN directory, so a leaked id from another
// account resolves to nothing.
package orchestrate

import (
	"mime"
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
	// Only a picture, a sound or a video is shown in the page. Anything else a
	// file delivery kept (HTML, SVG, a script) is served as a download: shown
	// inline from this origin it could run with the viewer's session.
	if !inlineAttachmentType(mime) {
		mime = "application/octet-stream"
		w.Header().Set("Content-Disposition", "attachment")
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

// inlineAttachmentType reports whether a kept attachment may render in the
// page: the raster image types the store keeps, audio, and video. SVG is an
// image that can carry script, so it is not one of them.
func inlineAttachmentType(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return strings.HasPrefix(mime, "audio/") || strings.HasPrefix(mime, "video/")
}

// deliveredFile is a kept file a message delivered (a song, a video, a
// document): enough to offer it again after a reload. Kept apart from
// Attachments, which are pictures and render as pictures.
type deliveredFile struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Mime string `json:"mime,omitempty"`
	Kind string `json:"kind"` // "audio", "video" or "file"
	Size int    `json:"size,omitempty"`
}

// keepDeliveredFiles stores what a turn delivered that is not a picture: the
// session's videos (audio rides that list too) and its files. Best-effort like
// keepDeliveredAttachments: one that cannot be kept (over the size cap) is
// missing on reload, never undelivered.
func keepDeliveredFiles(user string, videos []string, files []FileAttachment) []deliveredFile {
	if strings.TrimSpace(user) == "" {
		return nil
	}
	var out []deliveredFile
	keep := func(name, mt string, data []byte) {
		id, err := SaveChatAttachment(user, data, name)
		if err != nil {
			Log("[attachment] could not keep a delivered file for %s: %v", user, err)
			return
		}
		if mt == "" {
			mt = strings.SplitN(http.DetectContentType(data), ";", 2)[0]
		}
		kind := "file"
		switch {
		case strings.HasPrefix(mt, "audio/"):
			kind = "audio"
		case strings.HasPrefix(mt, "video/"):
			kind = "video"
		}
		out = append(out, deliveredFile{ID: id, Name: name, Mime: mt, Kind: kind, Size: len(data)})
	}
	for _, b64 := range videos {
		data, err := DecodeBase64Attachment(b64)
		if err != nil || len(data) == 0 {
			continue
		}
		name, mt := "video.mp4", "video/mp4"
		if a := sniffAudio(data); a != "" {
			name, mt = "audio"+a, mime.TypeByExtension(a)
		}
		keep(name, mt, data)
	}
	for _, f := range files {
		data, err := DecodeBase64Attachment(f.Data)
		if err != nil || len(data) == 0 {
			continue
		}
		keep(f.Name, strings.SplitN(f.MimeType, ";", 2)[0], data)
	}
	return out
}

// sniffAudio names an audio file from its first bytes (".mp3", ".wav", ...),
// or "" when it is not one. The video list carries audio, and a song kept as
// video/mp4 would not play back.
func sniffAudio(data []byte) string {
	if len(data) < 4 {
		return ""
	}
	switch {
	case data[0] == 'I' && data[1] == 'D' && data[2] == '3':
		return ".mp3"
	case data[0] == 0xFF && data[1]&0xF6 == 0xF0:
		return ".aac"
	case data[0] == 0xFF && data[1]&0xE0 == 0xE0:
		return ".mp3"
	case string(data[:4]) == "OggS":
		return ".ogg"
	case string(data[:4]) == "fLaC":
		return ".flac"
	case string(data[:4]) == "RIFF" && len(data) >= 12 && string(data[8:12]) == "WAVE":
		return ".wav"
	case len(data) >= 12 && string(data[4:8]) == "ftyp" && string(data[8:12]) == "M4A ":
		return ".m4a"
	}
	return ""
}
