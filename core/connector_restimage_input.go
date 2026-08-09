// Source photos for an image-EDIT backend: resolving what the caller named into
// bytes, and getting those bytes onto the backend before the graph runs.
//
// Two halves:
//
//   - resolveInputImages turns the references a model can hold — a media id for
//     something the user just sent, a workspace-relative path for something a
//     tool produced — into verified image bytes.
//   - uploadInputImages puts them on the backend. ComfyUI's graph references an
//     input by SERVER-SIDE filename, so the bytes go to /upload/image first and
//     the returned name is what lands in the LoadImage node. Backends that take
//     images inline (the {image} token) skip the upload entirely.
package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// maxInputImageBytes caps one source photo. Generous for a phone photo, small
// enough that a mistaken reference to a video or an archive fails fast instead
// of streaming megabytes into a multipart body.
const maxInputImageBytes = 8 << 20 // 8 MB

// inputImage is one caller-supplied source photo, already read and verified.
type inputImage struct {
	name string // a filename for the upload part; cosmetic on the server
	data []byte
	mime string
}

// resolveInputImages turns caller references into verified image bytes, in the
// ORDER given — the first reference is the base/subject for a compose backend,
// so the sequence is meaningful and must be preserved.
//
// Four reference forms, and one deliberate omission:
//
//   - "image#1" — the image space (image_space.go): pictures this user recently
//     produced or received, kept and pruned by the framework. This is what
//     "edit the one you just made" resolves through, including across turns.
//   - "media#1" — media that arrived on THIS turn. Makes "change this photo"
//     work the moment the user attaches one, with no file anywhere.
//   - "edited.png" — a workspace-relative path, for something a tool produced.
//     ResolveWorkspacePath rejects absolute paths and `..`.
//   - an http(s) URL — REFUSED. Fetching arbitrary URLs here would be SSRF
//     through a dispatch scoped to the backend's own host. The model already
//     has a tool for this: fetch the URL first, pass the workspace path.
func resolveInputImages(sess *ToolSession, refs []string, max int) ([]inputImage, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if max > 0 && len(refs) > max {
		// Say what NOT to do. Given a bare limit the model retried with fewer
		// pictures and reported the blend as done — so the user who asked for
		// three images combined got one, and was told it had worked.
		return nil, fmt.Errorf("this backend takes at most %d image(s) and %d were given. Do NOT retry with fewer and present it as the blend that was asked for — a composite missing pictures is not the picture requested. Tell the user this backend can combine only %d at a time",
			max, len(refs), max)
	}
	out := make([]inputImage, 0, len(refs))
	for _, raw := range refs {
		img, err := resolveInputImage(sess, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	return out, nil
}

// --- cascade planning -------------------------------------------------------

// maxCascadeStages bounds a cascade. Each stage is a full render — on a local
// GPU that is tens of seconds with a model load in front of it — so an
// unbounded chain is a request that never visibly finishes. Six stages is 7
// images on a 2-input backend and 26 on a 6-input one, past which "combine
// these" is not really the operation being asked for.
const maxCascadeStages = 6

// planImageCascade splits n source images into per-stage counts for a backend
// that takes max at once.
//
// The first stage takes a full load. Every stage after that carries the
// previous stage's OUTPUT in one of its input slots — that is what makes it a
// cascade rather than n unrelated renders — so it has max-1 slots left for new
// images. Five images into a 3-input backend is [3, 2]: edit the first three,
// then blend that result with the remaining two.
//
// Returns nil when n doesn't fit: max < 2 leaves no slot to carry a result
// forward, and past maxCascadeStages the chain is too long to be worth running.
func planImageCascade(n, max int) []int {
	if n <= 0 || max <= 0 {
		return nil
	}
	if n <= max {
		return []int{n} // fits in one call — the ordinary path
	}
	if max < 2 {
		return nil // a 1-input backend has no slot for the carried result
	}
	stages := []int{max}
	for left := n - max; left > 0; left -= max - 1 {
		take := max - 1
		if left < take {
			take = left
		}
		stages = append(stages, take)
		if len(stages) > maxCascadeStages {
			return nil
		}
	}
	return stages
}

// cascadeCapacity is the most images a backend can handle across a full
// cascade — what callers should validate against instead of the per-call max.
func cascadeCapacity(max int) int {
	if max < 2 {
		return max
	}
	return max + (maxCascadeStages-1)*(max-1)
}

// CheckImageInputs answers "would this edit's source references resolve, right
// now?" without rendering anything — the preflight an image edit runs before it
// is allowed to detach.
//
// It resolves rather than pattern-matches, because every way a reference goes
// wrong is a lookup: a media id for media that never arrived, a workspace file
// that was already consumed by an attach, bytes that turn out to be a PDF. The
// bytes it reads are thrown away; the call re-reads them when it runs. That
// double read costs a few megabytes of I/O and buys the model the chance to
// correct itself in the same turn instead of a minute later in a wake.
func CheckImageInputs(sess *ToolSession, backend string, refs []string, mask string) error {
	if len(refs) == 0 && strings.TrimSpace(mask) == "" {
		return nil
	}
	s, err := resolveImageConnector(backend)
	if err != nil {
		return err
	}
	// The cap is the CASCADE's, not one call's: more images than the backend
	// takes at once is run as stages, so the preflight must not refuse what the
	// render would go on to accept.
	if _, err := resolveInputImages(sess, refs, cascadeCapacity(s.MaxImages())); err != nil {
		return err
	}
	if m := strings.TrimSpace(mask); m != "" {
		if _, err := resolveInputImage(sess, m); err != nil {
			return err
		}
	}
	return nil
}

func resolveInputImage(sess *ToolSession, ref string) (inputImage, error) {
	var out inputImage
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return out, fmt.Errorf("empty image reference")
	}
	if u, err := url.Parse(ref); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return out, fmt.Errorf("a URL can't be used as a source image directly — download it first (image action=\"fetch\", url=%q), then pass the saved workspace path", ref)
	}
	// The image space first: "image#1" is the framework's own ring of recently
	// produced/received pictures, which is what "edit the one you just made"
	// resolves through.
	if data, ok := ResolveRecentImage(sess, ref); ok {
		return verifyInputImage(strings.ReplaceAll(ref, "#", "")+".png", data)
	}
	if strings.HasPrefix(strings.ToLower(ref), RecentImageRefPrefix) {
		// Two different lists wear the same prefix, and saying "recent images"
		// for both sent a model that had mistyped a KEPT name off to look at
		// ring positions — twice, before it thought to call help.
		//
		// A numeric suffix is a ring position; anything else is a name from the
		// library. Say which one is missing, and for a name say what IS there,
		// since the answer is short and the alternative is another round trip.
		if suffix := strings.TrimSpace(ref[len(RecentImageRefPrefix):]); !isAllDigits(suffix) {
			if names := keptImageNames(sess); len(names) > 0 {
				return out, fmt.Errorf("you have no kept image called %q. What you have kept: %s. "+
					"These are exact names, not filenames — no extension, and they do not shift", suffix, strings.Join(names, ", "))
			}
			return out, fmt.Errorf("you have no kept image called %q, and nothing is kept under any name yet. "+
				"image#<name> only works after action=\"keep\"; for a picture from this conversation use image#1 (most recent) or a media id", suffix)
		}
		return out, fmt.Errorf("%s isn't in the recent images — call image(action=\"help\") to see what's there", ref)
	}
	if b64, kind, ok := sess.ResolveInboundMedia(ref); ok {
		if kind != "" && kind != "image" {
			return out, fmt.Errorf("%s is a %s, not an image", ref, kind)
		}
		data, err := decodeBase64Image(b64)
		if err != nil {
			return out, fmt.Errorf("%s: %w", ref, err)
		}
		return verifyInputImage(strings.ReplaceAll(ref, "#", "")+".png", data)
	}
	if strings.HasPrefix(ref, "media#") {
		// Two very different failures wear the same prefix, and answering both
		// with "it expired" taught the model to report an expiry that never
		// happened — including for pictures it had just downloaded itself,
		// seconds earlier, which had never been media#N to begin with.
		//
		// media#N names a photo the USER attached. Nothing a tool produces is
		// ever one. So: none attached at all is a naming mistake, and the model
		// needs to be told which namespace it wanted. Some attached but N is
		// past the end is an off-by-one, and the count is the useful fact.
		var lead string
		if n := sess.InboundMediaCount(); n == 0 {
			lead = fmt.Sprintf("%s doesn't exist. media#N only ever names a photo the USER attached to a message, and nothing was attached here. Pictures you found, downloaded or generated are NOT media#N — they are the workspace filename the tool handed back, or image#N", ref)
		} else {
			lead = fmt.Sprintf("%s is past the end — %d item(s) came in with this message, so the ids stop at media#%d", ref, n, n)
		}
		// Whichever mistake it was, the picture it wanted is usually sitting in
		// the space already. Handing over the manifest is what keeps the
		// conversation going instead of ending it with "send it to me again".
		if m := RecentImageManifest(sess); m != "" {
			return out, fmt.Errorf("%s. Use one of these lasting handles instead:\n%s", lead, m)
		}
		return out, fmt.Errorf("%s. Pass the workspace filename the tool handed you instead — or, if the user really did send a photo, ask them to re-attach it", lead)
	}
	if sess == nil || strings.TrimSpace(sess.WorkspaceDir) == "" {
		return out, fmt.Errorf("no workspace available to read %q from", ref)
	}
	abs, err := ResolveWorkspacePath(sess.WorkspaceDir, ref)
	if err != nil {
		return out, fmt.Errorf("%q is not a readable workspace path: %w", ref, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		// The dead end that sent a turn guessing, and the only branch in this
		// function that had one. A URL is told to fetch first; an image#N past
		// the end is told to call help; a media#N in the wrong namespace gets
		// the whole manifest. A stale workspace filename — the handle the tool
		// description calls the most direct there is, and the one likeliest to
		// go stale, because the framework prunes the ring behind it — said only
		// that it wasn't there.
		//
		// Observed: an edit reached for a filename from an earlier turn, got
		// this, and could not tell a pruned picture from a misremembered one
		// ("it might have been cleaned up or I'm misremembering"). It listed the
		// workspace, found dozens of edit-<id>.png names with nothing to tell
		// them apart, said "there are too many files", guessed, failed again,
		// and ended the turn promising work it never did.
		//
		// Naming the pruning is half the fix: without it there is no rule to
		// learn, only a filename that used to work.
		if m := RecentImageManifest(sess); m != "" {
			return out, fmt.Errorf("no file %q in your workspace. Produced images are kept as a small rolling set, so an older filename gets pruned while the picture itself is usually still here under an id. Do NOT go hunting through the workspace for it — pick it from this list:\n%s", ref, m)
		}
		return out, fmt.Errorf("no file %q in your workspace, and your recent images are empty too, so this picture is gone. Make it again or ask the user to re-send it — do NOT guess at other filenames", ref)
	}
	if info.Size() > maxInputImageBytes {
		return out, fmt.Errorf("%q is %s — the limit for a source image is %s", ref, HumanSize(info.Size()), HumanSize(maxInputImageBytes))
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return out, fmt.Errorf("read %q: %w", ref, err)
	}
	return verifyInputImage(filepath.Base(ref), data)
}

// verifyInputImage confirms the bytes really decode as an image. A text file
// renamed .png would otherwise upload cleanly and fail deep inside the backend
// with something unreadable.
func verifyInputImage(name string, data []byte) (inputImage, error) {
	var out inputImage
	if len(data) == 0 {
		return out, fmt.Errorf("%q is empty", name)
	}
	if len(data) > maxInputImageBytes {
		return out, fmt.Errorf("%q is %s — the limit for a source image is %s", name, HumanSize(int64(len(data))), HumanSize(maxInputImageBytes))
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return out, fmt.Errorf("%q isn't a readable image (%v) — pass a png/jpg/webp", name, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return out, fmt.Errorf("%q has no dimensions", name)
	}
	return inputImage{name: name, data: data, mime: "image/" + format}, nil
}

// uniqueUploadName keeps the caller's extension (backends sniff format from it)
// but makes the stored key collision-proof.
func uniqueUploadName(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = ".png"
	}
	return "gohort-" + UUIDv4() + ext
}

func decodeBase64Image(b64 string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(stripDataURIPrefix(b64))
	if err != nil {
		return nil, fmt.Errorf("not decodable image data: %w", err)
	}
	return data, nil
}

// uploadInputImages puts every source photo where the backend expects it and
// returns the references the graph will carry. A backend with no UploadURL
// takes its images inline (the {image} token), so this is a no-op for it —
// p.images still reaches the token substitution.
func (s RestImageSpec) uploadInputImages(sess *ToolSession, p restImageParams) ([]ComfyUploadedImage, *ComfyUploadedImage, error) {
	if len(p.images) == 0 && p.mask == nil {
		return nil, nil, nil
	}
	if strings.TrimSpace(s.UploadURL) == "" {
		return nil, nil, nil // inline shape — nothing to upload
	}
	out := make([]ComfyUploadedImage, 0, len(p.images))
	for _, img := range p.images {
		up, err := s.uploadImage(sess, img)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, up)
	}
	var mask *ComfyUploadedImage
	if p.mask != nil {
		up, err := s.uploadImage(sess, *p.mask)
		if err != nil {
			return nil, nil, err
		}
		mask = &up
	}
	return out, mask, nil
}

// uploadImage POSTs one image as multipart/form-data and reads back the
// server-side name the graph will reference.
//
// This rides the SAME governed dispatch as every other call the backend makes —
// the credential's URL allow-list, the audit entry, and the Private-mode kill
// switch all apply. The request body is binary, which the dispatch handles
// (it writes the body through bytes.NewReader and never assumes JSON) and
// never logs.
func (s RestImageSpec) uploadImage(sess *ToolSession, img inputImage) (ComfyUploadedImage, error) {
	var out ComfyUploadedImage
	field := strings.TrimSpace(s.UploadFileField)
	if field == "" {
		field = "image"
	}
	// The form is built by mimebody as the request is written, so the photo is
	// never held twice: once in a buffer and again in the string the old
	// hand-rolled multipart handed to the dispatch.
	//
	// ComfyUI keys its input store by FILENAME. Two turns uploading "photo.png"
	// would land on the same key, and the second upload could replace the first
	// between its upload and its graph run — the first turn then renders the
	// wrong photo, silently and non-reproducibly. A unique name per upload
	// removes the shared key entirely.
	up := FileUpload{
		Reader:    bytes.NewReader(img.data),
		FieldName: field,
		FileName:  uniqueUploadName(img.name),
		Fields:    map[string]string{"subfolder": "gohort", "type": "input"},
	}

	raw, err := s.dispatchImageUpload(sess, s.UploadURL, up)
	if err != nil {
		return out, fmt.Errorf("uploading %q to the image backend failed: %w", img.name, err)
	}
	status, jsonBody := parseHTTPDispatchResult(raw)
	if status != 0 && (status < 200 || status >= 300) {
		return out, fmt.Errorf("image backend rejected the upload of %q with HTTP %d: %s", img.name, status, truncateForError(jsonBody))
	}
	var node any
	if err := json.Unmarshal([]byte(jsonBody), &node); err != nil {
		return out, fmt.Errorf("image backend's upload response was not JSON: %s", truncateForError(jsonBody))
	}
	namePath := firstNonEmpty(s.UploadNamePath, "name")
	out.Name = strings.TrimSpace(restJSONString(node, namePath))
	if out.Name == "" {
		return out, fmt.Errorf("image backend's upload response had no filename at %q: %s", namePath, truncateForError(jsonBody))
	}
	if p := strings.TrimSpace(s.UploadSubfolderPath); p != "" {
		out.Subfolder = strings.TrimSpace(restJSONString(node, p))
	}
	return out, nil
}
