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
	"fmt"
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
	media.GovernedUploadFunc = func(ctx context.Context, up media.UploadRequest) (string, error) {
		out, err := mediaUpload(ctx, up, mediaUploadBearer(ctx, up))
		if err != nil {
			return "", err
		}
		// A peer that refused the credential is the one failure worth a second
		// attempt: this side cannot otherwise learn that a token it believes in
		// has died over there. Same recovery the peer transport performs for
		// every other capability — it cannot be reused literally here because a
		// governed upload goes out through SecureAPI dispatch rather than an
		// http.Client, so the refusal arrives as text rather than a status.
		if mediaUploadRefused(out) {
			if cred, ok := renewRefusedPeerUpload(ctx, up); ok {
				out, err = mediaUpload(ctx, up, cred)
				if err != nil {
					return "", err
				}
			}
		}
		return mediaUploadBody(out)
	}
}

// mediaUpload performs one governed upload with the given bearer.
func mediaUpload(ctx context.Context, up media.UploadRequest, bearer string) (string, error) {
	scoped := SecureCredential{
		Name:              mediaUploadCredential,
		Type:              SecureCredNone,
		AllowedURLPattern: imageHostPattern(up.URL),
		Description:       "Unauthenticated media upload, scoped to the configured endpoint's host.",
	}
	// An authenticated endpoint — real OpenAI, or a peer instance — gets its
	// key. Carried on the synthesized credential rather than as a request
	// header, because dispatch strips caller-supplied Authorization on purpose;
	// and inline rather than stored, because a key the operator typed into the
	// STT form has no business appearing in the shared credential store under a
	// name they never chose.
	if bearer = strings.TrimSpace(bearer); bearer != "" {
		scoped.Type = SecureCredBearer
		scoped.inlineSecret = bearer
		scoped.Description = "Authenticated media upload, scoped to the configured endpoint's host."
	}
	args := map[string]any{
		"url":    up.URL,
		"method": "POST",
		secureUploadArg: &FileUpload{
			Reader:    bytes.NewReader(up.Body),
			FieldName: up.FieldName,
			FileName:  up.FileName,
			Fields:    up.Fields,
		},
	}
	return Secure().dispatch(scoped, args, sessionForMediaUpload(ctx))
}

// mediaUploadBearer picks the credential this upload presents.
//
// For a PEER it is resolved now, from the peer's name, blocking for a token
// exchange if there is no live one — the same rule the peer transport follows,
// and for the same reason: up.Bearer is whatever was live when the transcription
// config was READ, and reads happen far from sends. For anything else it is the
// configured key, unchanged.
func mediaUploadBearer(ctx context.Context, up media.UploadRequest) string {
	if p, ok := peerForProvider(up.Provider); ok {
		if cred := strings.TrimSpace(PeerCredentialNow(ctx, p)); cred != "" {
			return cred
		}
	}
	return up.Bearer
}

// mediaUploadRefused reports whether a dispatch result is a 401. dispatch
// writes for a model — it reports a failed status in a leading "HTTP 401 ..."
// line rather than as an error — so the refusal has to be read out of the text.
func mediaUploadRefused(out string) bool {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return strings.HasPrefix(line, "HTTP 401")
}

// renewRefusedPeerUpload drops the access token a peer refused and exchanges a
// new one, reporting whether the retry is worth making.
func renewRefusedPeerUpload(ctx context.Context, up media.UploadRequest) (string, bool) {
	p, ok := peerForProvider(up.Provider)
	if !ok || !p.UseTokens {
		return "", false
	}
	InvalidatePeerAccessToken(p.Name)
	InvalidatePeerResolution()
	if fresh, ok := GetRemotePeer(p.Name); ok {
		p = fresh
	}
	cred := strings.TrimSpace(PeerCredentialNow(ctx, p))
	if cred == "" || cred == strings.TrimSpace(p.Key) {
		return "", false
	}
	Log("[peer] %q refused our credential on a media upload — exchanged a new one and retrying", p.Name)
	return cred, true
}

// peerForProvider resolves a "peer:<name>" provider string to its record.
func peerForProvider(provider string) (RemotePeer, bool) {
	name := peerNameFromProvider(provider)
	if name == "" {
		return RemotePeer{}, false
	}
	return lookupPeerCached(name)
}

// mediaUploadBody turns a governed dispatch's LLM-facing result into the plain
// response body the media package expects.
//
// dispatch writes for a model: it prefixes "HTTP 200 OK\n" and reports a failed
// status in that line rather than as an error, because a model needs to READ
// the failure. Handed straight back, that made every transcript in the build
// begin with a junk status line — the transcribe tool then framed "HTTP 200
// OK\nthe quick brown fox" as the spoken content — and turned an HTTP 500 into
// a successful transcription whose text was the error page.
//
// So the seam that adapts a model-facing result into a machine-consumed one has
// to undo both: strip the line, and promote a failing status back to an error.
func mediaUploadBody(out string) (string, error) {
	line, rest, found := strings.Cut(out, "\n")
	if !found || !strings.HasPrefix(line, "HTTP ") {
		// No status line to strip. Either the format changed or something else
		// answered; hand it back untouched rather than eating a first line that
		// might be content.
		return out, nil
	}
	var code int
	if _, err := fmt.Sscanf(line, "HTTP %d", &code); err == nil && code >= 400 {
		return "", fmt.Errorf("%s: %s", strings.TrimSpace(line), strings.TrimSpace(truncateUploadError(rest)))
	}
	return rest, nil
}

// truncateUploadError keeps a failing body short enough to be an error message.
func truncateUploadError(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
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
