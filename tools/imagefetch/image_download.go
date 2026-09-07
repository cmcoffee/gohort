package imagefetch

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	_ "golang.org/x/image/webp" // register the WebP decoder for image.Decode
)

// fetchValidImage downloads a URL and returns the image RE-ENCODED AS JPEG
// plus its pixel dimensions, only if it's a genuinely usable image. ok=false
// for a download error (403 etc.), a non-image body (an HTML "access denied"
// block page from a hotlink-protected source), or an undecodable image — the
// caller then falls back to a more accessible URL.
//
// The JPEG re-encode is the fix for "the LLM isn't seeing what's attached":
// llama.cpp's vision (stb_image) can't read WebP/AVIF — the dominant formats
// on the web — so without normalizing it'd be handed bytes it can't decode
// and would hallucinate a description for a blank/wrong image while the saved
// file (the original webp) renders fine everywhere else. Decoding (webp via
// golang.org/x/image/webp) and re-encoding JPEG guarantees the model scores
// exactly what we save and attach.
func fetchValidImage(rawURL, referer string) (data []byte, w, h int, ok bool) {
	if rawURL == "" {
		return nil, 0, 0, false
	}
	d, err := FetchImageBytes(rawURL, referer, 20)
	if err != nil {
		return nil, 0, 0, false
	}
	return normalizeToJPEG(d)
}

// normalizeToJPEG decodes image bytes (jpeg/png/gif/webp) and re-encodes them
// as JPEG — the format the vision model can actually read — returning the
// JPEG plus pixel dimensions. ok=false for a non-image body or an undecodable
// image. Shared by the plain download path and the go-rod render escalation.
func normalizeToJPEG(d []byte) (data []byte, w, h int, ok bool) {
	if !strings.HasPrefix(http.DetectContentType(d), "image/") {
		return nil, 0, 0, false
	}
	img, _, derr := image.Decode(bytes.NewReader(d))
	if derr != nil {
		return nil, 0, 0, false
	}
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return nil, 0, 0, false
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
		return nil, 0, 0, false
	}
	return buf.Bytes(), b.Dx(), b.Dy(), true
}

func downloadImageBytes(rawURL string) ([]byte, error) {
	return FetchImageBytes(rawURL, "", 30)
}

func downloadImageTo(rawURL string, sess *ToolSession) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid image URL: %s", rawURL)
	}
	data, err := FetchImageBytes(rawURL, "", 20)
	if err != nil {
		return "", err
	}
	ct := http.DetectContentType(data)
	if !strings.HasPrefix(ct, "image/") {
		return "", fmt.Errorf("URL does not appear to be an image (detected: %s)", ct)
	}
	// Save to the session workspace and return the path. Does NOT
	// auto-attach — the LLM uses workspace(action="attach", path=...)
	// to deliver when it's ready.
	wsDir, err := EnsureSessionWorkspace(sess)
	if err != nil {
		return "", fmt.Errorf("session workspace unavailable: %w", err)
	}
	ext := extForMime(ct)
	name := "fetch-" + shortID() + ext
	target := filepath.Join(wsDir, name)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", fmt.Errorf("create parent dir: %w", err)
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		return "", fmt.Errorf("save image: %w", err)
	}
	Log("[imagefetch/fetch_image] fetched %d bytes from %s → %s", len(data), rawURL, name)
	msg := fmt.Sprintf("NOT sent yet — this only SAVED the image to your workspace as %q (%s, %d bytes). It is NOT delivered, and your reply text alone will NOT include it. To actually send it, call workspace(action=\"attach\", path=%q, cleanup=true) — do that BEFORE you write a reply claiming you sent it. Skip the attach ONLY if the user just wants info about what's in it.",
		name, ct, len(data), name)
	// Downloaded images join the space as well — this is also the path a model
	// is told to use when it tries to pass a URL straight to edit.
	if ref := RecordRecentImage(sess, data, "downloaded: "+truncate(rawURL, 60), ImageFromFound); ref != "" {
		msg += editHandleHint(name, ref)
	}
	msg += showToModel(sess, data, "the image that was fetched", "Nothing has checked what is in it")
	return msg, nil
}

// showToModel puts the picture itself in front of the agent on its next round,
// and tells it that it can now look.
//
// The gap this closes: nothing ever showed the agent the image. find handed
// back a filename, a title and a source; generate and edit handed back a path.
// The agent wrote "here's the photo of him" having seen a string that said
// "Farm & Ranch" and nothing else — it could not have caught the wrong picture,
// because it was never shown one. The only way it looked at anything was
// calling workspace(view_image) unprompted, which nothing asked it to do.
//
// The bytes go through the view-image channel: injected as a synthetic user
// message for ONE round and drained, never persisted into history, and never
// delivered to the user. Costs one image in context on the round where it is
// most useful and nothing after that.
//
// Two escape hatches, because a model that cannot see must not be asked to
// pretend:
//
//   - No LLM on the session — nowhere to send it.
//   - A DETACHED call — the turn that asked ended, and there is no next round
//     to inject into. What it produced travels to the wake as an attachment
//     instead, and telling a model "look at this" when nothing will arrive is
//     an invitation to describe an image it never got.
//
// A model wired without an image modality is the remaining case, and it is why
// this only ever ADDS a chance to catch a mistake. Nothing downstream is gated
// on the agent's verdict, so a model that cannot see simply proceeds as it does
// today. Verification that must hold with no vision at all lives in the
// provenance check, which is text.
func showToModel(sess *ToolSession, data []byte, what, caveat string) string {
	if sess == nil || sess.LLM == nil || sess.Detached || len(data) == 0 {
		return ""
	}
	// what names THIS picture. The round may be showing the model several, and
	// the note that accompanies them lists these labels instead of asking it to
	// match pixels to tool calls by position.
	sess.AppendViewImageAs(data, what)
	// What the agent is asked to judge has to be scoped, or showing it the
	// picture makes things worse rather than better.
	//
	// The first version said "check it really shows what was asked for". Asked
	// for a specific person, the search returned his photo from his own site —
	// name in the page title, first candidate, a clean hit. The agent looked at
	// it, did not recognize a face it has never seen, took that as a failed
	// check, and sent nothing. A correct result, refused, because the question
	// it was handed was the one nobody can answer by looking.
	//
	// Which is the same trap the vision screen was in: identity is not visible.
	// So the instruction names both halves — what looking settles, and what it
	// cannot — and makes DELIVERING the default, so an unfamiliar face is never
	// mistaken for evidence of a wrong picture.
	return fmt.Sprintf(" LOOK AT IT: the picture is included with this result so you can see it on this round. %s."+
		" Looking settles whether it is the right KIND of thing, whether it is blank, broken or garbled, and whether an edit did what was asked."+
		" Looking does NOT settle WHO someone is: you do not know this person's face, so a face you don't recognize is not evidence of a wrong picture — where it came from is the evidence you have, and you should not overrule it from the pixels."+
		" Deliver it unless you can point at something concretely wrong; if you can, say what that is rather than presenting it as a match.", caveat)
}

// stableRefNote offers the durable reference for a picture that was just
// saved, so the model has something safe to carry instead of a position.
//
// The position is only correct until the next save, and a render saves its own
// result — so an agent told to "use image#1 to edit it later" edits whatever
// happened to be saved most recently by the time it gets there. That is the
// mix-up where a change lands on a picture nobody mentioned.
func stableRefNote(stable string) string {
	if strings.TrimSpace(stable) == "" {
		return " If you will need it after any other image call, keep it under a name first: image(action=\"keep\", name=\"…\")."
	}
	return fmt.Sprintf(" Use %s instead whenever you refer to it later — that one always means THIS picture, however many others are saved after it."+
		" (For a name you will recognise months from now, image(action=\"keep\", name=\"…\") still gives image#<name>.)", stable)
}

// editHandleHint names the handle to EDIT this picture by, and says plainly
// that the image#N one moves.
//
// Both find and fetch used to end with "it is also kept as image#1", which is
// true for exactly as long as it is the newest picture. Search for two people
// to blend and both results say image#1 — the first quietly became image#2 the
// moment the second arrived, and nothing said so. What the model does with two
// identical handles for two different pictures is invent a third naming scheme
// and pass ids that resolve to nothing.
//
// So the filename leads: it means this picture and no other, for as long as the
// file is there. image#N follows, with what it actually means.
func editHandleHint(name, ref string) string {
	return fmt.Sprintf(" To change or blend it rather than send it, pass %q in images on your next image call — that filename keeps meaning THIS picture. It also entered your recent images as %s, but those are positional: whatever is saved next becomes image#1 and this one moves down, so don't hold onto that number across another image call.", name, ref)
}

// extForMime returns a file extension matching a mime type. Used to
// give saved files plausible suffixes so workspace tools downstream
// can mime-detect them by name when convenient. Covers the formats
// http.DetectContentType emits for common image / video / audio
// types. Unknown mimes fall back to ".bin" rather than empty — the
// filename should always have an extension so the user-facing
// attachment delivery (especially iMessage / SMS) has a meaningful
// suffix.
func extForMime(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/png"):
		return ".png"
	case strings.HasPrefix(mime, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(mime, "image/gif"):
		return ".gif"
	case strings.HasPrefix(mime, "image/webp"):
		return ".webp"
	case strings.HasPrefix(mime, "image/avif"):
		return ".avif"
	case strings.HasPrefix(mime, "image/svg"):
		return ".svg"
	case strings.HasPrefix(mime, "image/bmp"):
		return ".bmp"
	case strings.HasPrefix(mime, "image/heic"), strings.HasPrefix(mime, "image/heif"):
		return ".heic"
	case strings.HasPrefix(mime, "image/tiff"):
		return ".tiff"
	case strings.HasPrefix(mime, "image/"):
		// Unknown image subtype — generic .img fallback. Better
		// than no extension; bridge / browser will still mime-detect
		// from content. Log for diagnosis so we can extend this
		// switch when a new format surfaces in the wild.
		Log("[imagefetch] unknown image mime %q — falling back to .img", mime)
		return ".img"
	case strings.HasPrefix(mime, "video/mp4"):
		return ".mp4"
	case strings.HasPrefix(mime, "video/webm"):
		return ".webm"
	case strings.HasPrefix(mime, "video/"):
		Log("[imagefetch] unknown video mime %q — falling back to .mp4", mime)
		return ".mp4"
	case strings.HasPrefix(mime, "audio/mpeg"):
		return ".mp3"
	case strings.HasPrefix(mime, "audio/wav"), strings.HasPrefix(mime, "audio/x-wav"):
		return ".wav"
	case strings.HasPrefix(mime, "audio/"):
		Log("[imagefetch] unknown audio mime %q — falling back to .m4a", mime)
		return ".m4a"
	default:
		// Non-media content (HTML error page, plaintext error, etc.) —
		// shouldn't happen on a successful image fetch but log so the
		// path is observable when it does.
		Log("[imagefetch] non-media mime %q saved as .bin (likely a fetch error masquerading as success)", mime)
		return ".bin"
	}
}

// shortID returns a brief unique-ish identifier for filenames.
func shortID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
