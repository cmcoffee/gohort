// Wiring the media package's outbound file transfer onto the governed dispatch.
//
// core imports core/media, so media cannot import core — it exposes a function
// variable and this file fills it in. Same seam as GeminiKeyFunc.
//
// Before this, Transcribe built its own http.Client and POSTed user audio
// straight out: no URL allow-list, no audit entry, no rate limit, and no
// Private-mode gate. Every other outbound call in the framework goes through
// SecureAPI; this one didn't, because there was no way to stream a file through
// it. FileUpload is that way.
package core

import (
	"bytes"
	"context"
	"strings"

	"github.com/cmcoffee/gohort/core/media"
)

// mediaUploadCredential is the credential the media package's uploads ride.
// Transcription usually points at a local whisper.cpp with no auth, so this
// synthesizes an unauthenticated credential scoped to the endpoint's own host
// — the same treatment a LAN ComfyUI gets, and for the same reason: reaching
// one local box must not widen the shared no_auth credential to all of http.
const mediaUploadCredential = "media_local"

func init() {
	media.GovernedUploadFunc = func(ctx context.Context, url, field, filename string, body []byte, fields map[string]string) (string, error) {
		scoped := SecureCredential{
			Name:              mediaUploadCredential,
			Type:              SecureCredNone,
			AllowedURLPattern: imageHostPattern(url),
			Description:       "Unauthenticated media upload, scoped to the configured endpoint's host.",
		}
		return Secure().dispatch(scoped, map[string]any{
			"url":    url,
			"method": "POST",
			secureUploadArg: &FileUpload{
				Reader:    bytes.NewReader(body),
				FieldName: field,
				FileName:  filename,
				Fields:    fields,
			},
		}, sessionForMediaUpload(ctx))
	}
}

// sessionForMediaUpload carries the caller's context onto the dispatch so a
// cancelled turn cancels the upload. Transcription runs on ingest paths that
// have a context but no ToolSession, so this is the minimum that makes the
// dispatch's cancellation and Private-mode checks meaningful.
func sessionForMediaUpload(ctx context.Context) *ToolSession {
	if ctx == nil {
		return nil
	}
	return &ToolSession{Ctx: ctx}
}

// MediaUploadHostPattern exposes the allow-list pattern a media upload runs
// under, so an operator can see what the synthesized credential permits.
func MediaUploadHostPattern(endpoint string) string {
	return imageHostPattern(strings.TrimSpace(endpoint))
}
