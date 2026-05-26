package phantom

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// attachmentMetadata returns a compact human-readable description of a
// base64-encoded attachment for inclusion in history annotations.
// Decodes only the header (image.DecodeConfig) so it's cheap — does
// not load the full image into memory. kind is "image" or "video".
//
// Format: "jpg, 4032x3024, 2.4MB" — concise enough to inline in every
// history message without context bloat. The model uses this signal to
// decide whether the attachment description in conversation already
// covers a question, or whether it needs to call look_at_attachment.
func attachmentMetadata(b64 string, kind string) string {
	approxSize := (len(b64) * 3) / 4 // base64 → bytes
	sizeStr := humanSize(approxSize)

	// Decode just enough to get mime + dimensions for images.
	if kind == "image" {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err == nil && len(data) > 0 {
			mime := http.DetectContentType(data)
			mimeShort := strings.TrimPrefix(mime, "image/")
			if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
				return fmt.Sprintf("%s, %dx%d, %s", mimeShort, cfg.Width, cfg.Height, sizeStr)
			}
			return fmt.Sprintf("%s, %s", mimeShort, sizeStr)
		}
		return fmt.Sprintf("image, %s", sizeStr)
	}

	// Videos: don't try to parse container — just report mime + size.
	// Decoding video metadata via stdlib isn't cheap; ffprobe could
	// give duration/dimensions but adds a dependency. Size + mime is
	// usually enough signal for "should I look at it" decisions.
	if data, err := base64.StdEncoding.DecodeString(b64); err == nil && len(data) > 0 {
		mime := http.DetectContentType(data)
		mimeShort := strings.TrimPrefix(mime, "video/")
		return fmt.Sprintf("%s, %s", mimeShort, sizeStr)
	}
	return fmt.Sprintf("video, %s", sizeStr)
}

// humanSize formats a byte count as a short human-readable string.
func humanSize(n int) string {
	const kb, mb = 1024, 1024 * 1024
	switch {
	case n >= mb:
		return fmt.Sprintf("%.1fMB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1fKB", float64(n)/kb)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// findAttachmentByID walks a phantom message history and returns the
// base64 blob and detected kind ("image" or "video") for the given
// reference ID (e.g. "image-3", "video-1"). Returns "", "", false if
// the ID doesn't resolve.
//
// The ID counter walks forward chronologically through history so the
// IDs match the order the model saw them in its annotated content.
// This keeps lookup deterministic even when history is rebuilt across
// rounds — image-3 always means the third image in the conversation,
// regardless of when look_at_attachment is called.
func findAttachmentByID(history []PhantomMessage, id string) (string, string, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	imgCounter, vidCounter := 0, 0
	for _, m := range history {
		if m.Role != "user" {
			continue
		}
		for _, b64 := range m.Images {
			imgCounter++
			if id == fmt.Sprintf("image-%d", imgCounter) {
				return b64, "image", true
			}
		}
		for _, b64 := range m.Videos {
			vidCounter++
			if id == fmt.Sprintf("video-%d", vidCounter) {
				return b64, "video", true
			}
		}
	}
	return "", "", false
}

// buildLookAtAttachmentTool returns the tool definition for
// look_at_attachment, bound to a history-fetching closure. The closure
// lets the tool re-fetch the latest history at call time rather than
// capturing a stale snapshot at agent-loop construction.
func buildLookAtAttachmentTool(fetchHistory func() []PhantomMessage, queueImage, queueVideo func([]byte)) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "look_at_attachment",
			Description: "Re-fetch a prior image or video from this conversation by its reference ID (e.g. 'image-3', 'video-1'). " +
				"Use this when the user asks a question that requires examining visual detail beyond what the metadata in your conversation history conveys — for example: 'what color was the shirt in that earlier picture' / 'what does the receipt say' / 'how many people are in that photo'. " +
				"Each prior attachment is annotated in the conversation as `[image-N | metadata]` or `[video-N | metadata]` — pass that ID. " +
				"The attachment will be loaded into your context for the next round; you can then describe what you see. " +
				"Do NOT use this for the most recent attachment (the one in [CURRENT ATTACHMENT: ...] tag) — that one is already in your multimodal context.",
			Parameters: map[string]ToolParam{
				"id": {Type: "string", Description: "The attachment reference ID, e.g. 'image-3' or 'video-1'."},
			},
			Required: []string{"id"},
		},
		Handler: func(args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			if strings.TrimSpace(id) == "" {
				return "", fmt.Errorf("id is required (e.g. 'image-3')")
			}
			history := fetchHistory()
			b64, kind, ok := findAttachmentByID(history, id)
			if !ok {
				return fmt.Sprintf("[NOT FOUND] No attachment with id '%s' in this conversation. Valid IDs are referenced in the history as `[image-N | ...]` or `[video-N | ...]`.", id), nil
			}
			data, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return "", fmt.Errorf("attachment %s is corrupt: %w", id, err)
			}
			switch kind {
			case "image":
				queueImage(data)
			case "video":
				queueVideo(data)
			}
			return fmt.Sprintf("Loaded %s into your context. The attachment is available for examination on the next round; describe what you see in your reply.", id), nil
		},
	}
}
