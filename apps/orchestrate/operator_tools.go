// The Operator agent's exclusive fleet-management catalog: create / list /
// run / pause standing (scheduled) agents and read the run-ledger.
//
// Like Builder's authoring tools, these are NOT globally registered — they're
// appended at run time ONLY for orchestrator-mode agents (the gate lives in
// runner.go's catalog assembly and the dispatch paths), so no other agent
// gets them. Owner-scoped to the runtime user; reuses the shared core spine
// (standing-agent store + run-ledger). The actual execution of a standing
// agent is handled by the registered standing runner (standing_runner.go).

package orchestrate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/timing"
)

// operatorAttachMarkerRe matches the phantom [ATTACH: file] delivery marker an
// agent may embed in a message it hands to a contact. Same shape as phantom's
// own marker so the two surfaces agree. The marker text is stripped at delivery
// (DeliverMessage); here we use it to resolve the referenced file from the
// agent's workspace and carry it as a real attachment.
var operatorAttachMarkerRe = regexp.MustCompile(`\[ATTACH:\s*([^\],]+?)(?:\s*,\s*cleanup\s*=\s*(true|false))?\s*\]`)

// collectMessageAttachments gathers the base64 images to send with an
// agent-originated phantom message. Two sources, both honored: (1) files the
// agent attached via workspace(action="attach") (sess.Images), and (2) files
// referenced by an [ATTACH: name] marker in the text, resolved against the
// agent's workspace dir. Without (2), an agent that used the marker convention
// instead of workspace(attach) would leak the marker as text and send nothing.
func collectMessageAttachments(sess *ToolSession, text string) []string {
	var out []string
	if sess == nil {
		return out
	}
	out = append(out, sess.Images...)
	// No WorkspaceDir guard: a [ATTACH: media#N] marker resolves from the inbound
	// registry (no file), and resolveAttachmentRef still guards the workspace for
	// filename refs. Images-only collector, so a video ref is skipped here.
	for _, m := range operatorAttachMarkerRe.FindAllStringSubmatch(text, -1) {
		if b64, kind, ok := resolveAttachmentRef(sess, m[1], true); ok && kind != "video" {
			out = append(out, b64)
		}
	}
	return out
}

// collectMessageMedia is the type-AWARE version used by the channel-reply path:
// it splits a turn's outbound attachments into images and videos so a video
// rides out as a video, not a mislabeled (and undeliverable) oversized image.
// Sources: session Images/Videos (video tools accumulate into sess.Videos via
// AppendVideo) plus [ATTACH: file] markers, routed by file extension. Restores
// the outbound video channel the phantom outbox had before the bridges migration.
func collectMessageMedia(sess *ToolSession, text string) (images, videos []string) {
	if sess == nil {
		return nil, nil
	}
	images = append(images, sess.Images...)
	videos = append(videos, sess.Videos...)
	// This is the channel AUTO-REPLY collector — its result goes straight back to
	// the conversation the message arrived on. So it resolves ONLY produced media:
	// sess.Images/Videos and workspace-file [ATTACH: file] markers. Inbound media#N
	// is deliberately NOT resolved here (allowInbound=false), because re-attaching
	// a photo someone just posted echoes it right back to the same group (the wiwee
	// image-echo bug). Cross-recipient forwarding of an inbound item goes through
	// the explicit messaging tools, not this reply path.
	for _, m := range operatorAttachMarkerRe.FindAllStringSubmatch(text, -1) {
		b64, kind, ok := resolveAttachmentRef(sess, m[1], false)
		if !ok {
			continue
		}
		if kind == "video" {
			videos = append(videos, b64)
		} else {
			images = append(images, b64)
		}
	}
	return images, videos
}

// recoverStagedDeliverable is the phantom-delivery BACKSTOP: when a reply CLAIMS
// it delivered an image/file ("here are the pics") but nothing was actually
// attached, it returns the deliverable file THIS TURN staged — the file the
// model produced and then forgot to attach. Returns "" when the reply makes no
// delivery claim, or when the turn staged nothing deliverable.
//
// The delivery CLAIM is one gate: the model only says "here it is" for a file
// it MEANS to send, so recovering ships what it intended rather than something
// it produced and rejected.
//
// The candidate SET is the other, and it is the one that took a second attempt
// to get right. This used to scan the workspace directory for the newest
// deliverable file modified in the last ten minutes. But the workspace root is
// per USER — every session, every agent and every turn share it — so that scan
// answers "what is newest in this user's workspace", not "what did this turn
// make". Ask for a picture in one thread, then say anything picture-shaped in
// another within ten minutes, and the backstop attaches the first thread's
// photo to the second thread's reply. The symptom is an agent posting a random
// image nobody asked it for, which is worse than the missing attachment this
// exists to prevent: a silence is a bug, but a stranger's photo is a leak.
//
// So the candidates are exactly the files the turn's own tool calls created —
// the framework records them per call (ToolSession.StagedFiles) rather than
// inferring them from a clock. Staged nothing, recover nothing.
func recoverStagedDeliverable(sess *ToolSession, reply string, produced bool) string {
	if sess == nil || strings.TrimSpace(sess.WorkspaceDir) == "" || !deliveryIntended(reply, produced) {
		return ""
	}
	var newest string
	var newestMod time.Time
	for _, name := range sess.StagedFiles() {
		if strings.HasPrefix(name, "_") || !isDeliverableFile(name) {
			continue
		}
		// Still has to be there: an attach with cleanup=true removes the file
		// it delivered, and re-shipping a name that no longer resolves is how
		// a successful delivery turns into an error on the way out.
		info, err := os.Stat(filepath.Join(sess.WorkspaceDir, name))
		if err != nil || info.IsDir() {
			continue
		}
		if newest == "" || info.ModTime().After(newestMod) {
			newest, newestMod = name, info.ModTime()
		}
	}
	return newest
}

// deliveryIntended decides whether a turn MEANT to send the file it staged.
//
// Three signals, any of which is enough, and one veto that beats all of them:
//
//   - The turn ran a tool whose whole job is to produce a deliverable. This is
//     the reliable one, and reading prose was never a good substitute for it:
//     "There you go. Shazz's got a captive audience now 🚗" is a caption, and a
//     caption names what is IN the picture rather than the word "picture", so
//     every rule keyed on delivery nouns missed exactly the replies that most
//     obviously accompany an image.
//   - A delivery MARKER — the framework's own send instruction, written by the
//     model. Stronger than any sentence.
//   - Prose that claims a delivery in so many words.
//
// The veto is a reply that says the thing FAILED. It comes first because a
// refusal mentions the subject as much as a delivery does, and recovering a
// staged file on the back of one hands the user the very thing the model just
// rejected.
func deliveryIntended(reply string, produced bool) bool {
	if replyDisclaimsDelivery(reply) {
		return false
	}
	return produced || operatorAttachMarkerRe.MatchString(reply) || replyClaimsAttachment(reply)
}

// replyDisclaimsDelivery reports whether the reply says the work did not come
// off — the one signal that outranks every claim that it did.
func replyDisclaimsDelivery(reply string) bool {
	r := strings.ToLower(reply)
	for _, no := range []string{
		"couldn't", "could not", "wasn't able", "was not able", "unable to",
		"didn't find", "did not find", "no luck", "doesn't match", "does not match",
		"nothing that", "not what you", "failed to", "went wrong",
	} {
		if strings.Contains(r, no) {
			return true
		}
	}
	return false
}

// deliverableProducers are the tools that exist to PRODUCE something for the
// user, mapped to the KIND of thing each one makes. A turn that ran one and
// attached nothing is the case this whole backstop is for; the kind is how the
// framework names what went missing when there is no filename to name.
//
// Inspection tools are deliberately absent: a screenshot taken to read a page,
// or a view_image used to check a file, produces an image the user never asked
// to receive.
var deliverableProducers = map[string]string{
	"image": "image", "find_image": "image", "generate_image": "image", "fetch_image": "image",
	"video": "video", "download_video": "video",
}

// turnDeliverableKind reports what this turn set out to make ("image"/"video"),
// or "" when no producer ran.
func turnDeliverableKind(calls []PersistedToolCall) string {
	for _, c := range calls {
		if kind := deliverableProducers[c.Name]; kind != "" {
			return kind
		}
	}
	return ""
}

// turnProducedDeliverable reports whether any call this turn was to a producer.
func turnProducedDeliverable(calls []PersistedToolCall) bool {
	return turnDeliverableKind(calls) != ""
}

// deliveryCues are the phrasings a model uses to PRESENT something it believes
// it has just handed over.
var deliveryCues = []string{
	"here's ", "here are ", "here is ", "here you go", "attached", "i've attached",
	"there you go", "there's ", "there is ", "there are ", "there ya go",
	"sending you", "sent you", "take a look", "check out", "sharing ",
	// A model announcing what it MADE is claiming delivery just as much as
	// one saying "here you go" — and these are the phrasings that actually
	// come back. Without them the backstop sat out the common case: "Done —
	// your haunted house is ready!" and no picture anywhere.
	"i made", "i've made", "i created", "i've created", "i generated",
	"i've generated", "made you", "is ready", "are ready", "all set",
}

// replyPresentsDelivery reports whether the reply is WRITTEN as a hand-over —
// the cue half of a delivery claim, with no attachment noun required.
//
// On its own this is far too loose to act on: "Here's what I think" is a cue
// and delivers nothing, which is why replyClaimsAttachment also demands a noun.
// It is split out for the one case where the evidence comes from somewhere
// better than the prose — a turn that actually RAN an image tool. There, the
// noun rule is the thing that misses, because a caption names what is IN the
// picture and never the picture: "Here's you, wasting away in the garage like
// Craig ordered" is a delivery claim with no delivery noun in it anywhere. See
// deliveryIntended, which documents the same blind spot.
func replyPresentsDelivery(reply string) bool {
	if replyDisclaimsDelivery(reply) {
		return false
	}
	r := strings.ToLower(reply)
	for _, cue := range deliveryCues {
		if strings.Contains(r, cue) {
			return true
		}
	}
	return false
}

// replyClaimsAttachment reports whether the reply asserts it delivered an
// image/file — the signal that the model THINKS it attached something. Requires
// a delivery cue AND an attachment noun so the backstop stays scoped to phantom
// deliveries instead of firing on any staged file or any casual "here's".
func replyClaimsAttachment(reply string) bool {
	if !replyPresentsDelivery(reply) {
		return false
	}
	r := strings.ToLower(reply)
	for _, noun := range []string{"photo", "picture", " pic", "image", "shot", "meme", "gif", "screenshot", "attachment", "file", "pdf", "doc", "video", "clip"} {
		if strings.Contains(r, noun) {
			return true
		}
	}
	return false
}

// frameworkNoteTag marks turn-scoped reference material the framework appended
// to the user's message. Deliberately not the correction tag: nothing went
// wrong here, this is context the model would otherwise go hunting for. Says
// plainly that the user neither wrote it nor can see it, so the model doesn't
// thank them for it or quote it back at them.
const frameworkNoteTag = "[AUTOMATED FRAMEWORK NOTE: reference material, not written by the user, who cannot see it. Do not mention it or reply to it; just use it.]\n"

// pictureWordRe matches a user asking about a picture. Word-bounded, because
// "pic" inside "epic" and "gif" inside "gifted" are not requests for an image.
//
// Nouns AND the verbs that only make sense against an existing picture: someone
// who says "blend these" or "photoshop him onto the hood" is pointing at images
// as surely as someone who says "photo".
var pictureWordRe = regexp.MustCompile(`(?i)\b(photos?|pictures?|pics?|images?|memes?|gifs?|screenshots?|selfies?|selfie|portraits?|drawings?|artwork|blend|blended|composite|photoshop|retouch)\b`)

// imageSpaceNote is the proactive half of the image space: the manifest, handed
// over on a turn that is going to want it, instead of after a failure.
//
// The space could never live in the tool schema — it changes on every image
// operation and schemas sit at the front of the prompt, so publishing it there
// re-pays cold prefill every turn. That constraint is real, but it only rules
// out the SCHEMA. The newest user turn is the volatile tail that never hits
// cache anyway, which is exactly why the date stamp lives there, and the
// manifest costs nothing in the same spot.
//
// Two gates, and they are the same gate twice: is this turn about pictures?
//
//   - The user named one. A request to change an existing image has to say
//     what to change ("add Alex to the picture of me in the garage"), so unlike
//     a model writing a caption, the user's own words are reliable here.
//   - The space is non-empty. It is scoped per user AND per agent and fills only
//     when image tools run, so anything in it means this agent has been working
//     with pictures. Empty renders "" and this returns "".
//
// What it prevents: the model reaching for workspace(ls), getting a directory
// of edit-<id>.png names with nothing to tell them apart, and guessing. Its own
// words for that were "there are too many files".
func imageSpaceNote(sess *ToolSession, userMessage string) string {
	if sess == nil || !pictureWordRe.MatchString(userMessage) {
		return ""
	}
	m := RecentImageManifest(sess)
	if m == "" {
		return ""
	}
	// Tagged as framework context so it reads as the system speaking, not as
	// something the user typed — the ids are the framework's, not theirs.
	return frameworkNoteTag + m
}

// deliveryWatch remembers, across a turn's rounds, whether a tool that exists to
// PRODUCE something for the user has run — and what kind of thing it makes.
//
// The phantom-delivery guard needs this DURING the loop: a false delivery claim
// is cheap to correct on the round it is written and impossible to correct once
// the reply has shipped. The after-the-fact backstop reads the turn's persisted
// transcript for the same signal, but mid-loop that transcript does not exist
// yet. Written from the loop's OnStep hook, read from the guard's closure.
type deliveryWatch struct {
	mu   sync.Mutex
	kind string
}

// note records the producers in one round's tool calls. First kind wins — the
// question is what the turn set out to make, not the last thing it touched.
func (d *deliveryWatch) note(calls []ToolCall) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.kind != "" {
		return
	}
	for _, c := range calls {
		if kind := deliverableProducers[c.Name]; kind != "" {
			d.kind = kind
			return
		}
	}
}

// producedKind is what the turn set out to make so far, "" if nothing yet.
func (d *deliveryWatch) producedKind() string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.kind
}

// unresolvedAttachMarkers returns the marker targets in a reply that resolve to
// nothing. Silence here was the whole problem: an unresolvable marker was
// skipped without a word, so the only visible symptom was a generic apology
// with no way to tell it apart from a model that said nothing at all.
func unresolvedAttachMarkers(sess *ToolSession, text string) []string {
	var missing []string
	for _, m := range operatorAttachMarkerRe.FindAllStringSubmatch(text, -1) {
		if _, _, ok := resolveAttachmentRef(sess, m[1], false); !ok {
			missing = append(missing, m[1])
		}
	}
	return missing
}

// phantomDeliveryRefs answers the loop's question: what does this reply claim to
// be sending that does not exist? Each entry names one missing thing — a
// filename when the reply named one, otherwise the kind of thing the turn was
// making ("the image").
//
// producedKind is what the turn has run a producer FOR so far, "" if none.
//
// Deliberately narrow, because every ref returned costs a correction round:
//
//   - Something was already attached this turn → not a phantom. The delivery
//     happened; a marker naming a file consumed by an earlier cleanup=true
//     attach is a duplicate reference, not a lie.
//   - A staged file is recoverable → not a phantom. The backstop will ship it,
//     so correcting the model would spend a round fixing something already fixed.
//   - A marker resolving to nothing with nothing produced to deliver is a
//     promise about a file that was never made.
//   - So is a turn that RAN an image tool, has nothing to show for it, and is
//     writing the reply as a hand-over anyway. This one needs no marker and no
//     delivery noun, because it had neither: "Here's you, wasting away in the
//     garage like Craig ordered" arrived with no picture, no [ATTACH:], and
//     nothing in it that a rule about the word "picture" could catch. The tool
//     call is the evidence — asking for a caption to describe itself is not.
func phantomDeliveryRefs(sess *ToolSession, reply, producedKind string) []string {
	if sess == nil {
		return nil
	}
	if len(sess.Images) > 0 || len(sess.Videos) > 0 || len(sess.Files) > 0 {
		return nil
	}
	missing := unresolvedAttachMarkers(sess, reply)
	if len(missing) == 0 {
		// No marker to go on. Prose alone is not enough to call a reply a lie,
		// so this fires only when a producer ran and left nothing behind.
		if producedKind == "" || !replyPresentsDelivery(reply) {
			return nil
		}
		missing = []string{"the " + producedKind}
	}
	if recoverStagedDeliverable(sess, reply, true) != "" {
		return nil
	}
	return missing
}

// isDeliverableFile reports whether a workspace filename looks like something
// meant to be SENT (image / doc / video), not a scratch or source file.
func isDeliverableFile(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".pdf", ".docx", ".mp4", ".mov", ".webm", ".m4v":
		return true
	}
	return false
}

// isVideoAttachment classifies a workspace file path as a video or audio file
// by extension, so a [ATTACH: clip.mp4] or song.mp3 routes to the video channel,
// which delivers it as a file, instead of the image channel.
func isVideoAttachment(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".mp4", ".mov", ".webm", ".mkv", ".avi", ".m4v",
		// Audio rides the video list: the transport sends it as a file. On
		// the image list a song went out named .jpg.
		".mp3", ".m4a", ".aac", ".wav", ".ogg", ".oga", ".opus", ".flac":
		return true
	}
	return false
}

// resolveWorkspaceImages reads each workspace-relative path and returns its
// base64 bytes, skipping anything empty, escaping the workspace, or unreadable.
// Lets a channel agent deliver a file straight to its channel via send_message's
// `attachments` param WITHOUT workspace(action="attach") — which stages the file
// onto the agent's REPLY (the caller), not the channel. That conflation is why a
// dispatched channel agent kept "sending the image back" instead of out.
func resolveWorkspaceImages(sess *ToolSession, paths []string) []string {
	var out []string
	if sess == nil || strings.TrimSpace(sess.WorkspaceDir) == "" {
		return out
	}
	for _, name := range paths {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		// Resolved, not joined: the sandbox writes this directory, so a
		// symlink planted in it would otherwise have the host read (and
		// send out) whatever it points at.
		full, rerr := ResolveWorkspacePath(sess.WorkspaceDir, name)
		if rerr != nil {
			continue // never escape the workspace
		}
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		out = append(out, base64.StdEncoding.EncodeToString(b))
	}
	return out
}

// resolveAttachmentRef resolves ONE attachment reference the model supplied (a
// [ATTACH: …] marker name or an attachments[] entry) to its base64 bytes and
// media kind ("image"/"video"). A media-id ref ("media#2") is looked up in the
// session's inbound registry — post-by-id, the only handle inbound media has
// since it owns no workspace file. Anything else is treated as a workspace-
// relative filename and read from disk (the existing produced-media path). ok is
// false when neither resolves, so callers skip it cleanly.
//
// allowInbound gates the inbound-registry lookup. It MUST be false on the channel
// auto-reply path (collectMessageMedia): that reply always goes back to the room
// the media arrived on, so resolving an inbound media#N there just echoes the
// photo straight back to the group — the "wiwee re-posts the picture" bug. The
// explicit cross-recipient tools (message_contact / notify_owner / send_message)
// pass true, because forwarding "the photo Henry sent" to a DIFFERENT recipient
// is the feature the inbound registry exists for.
func resolveAttachmentRef(sess *ToolSession, ref string, allowInbound bool) (b64, kind string, ok bool) {
	ref = strings.TrimSpace(ref)
	if sess == nil || ref == "" {
		return "", "", false
	}
	if allowInbound {
		if b, k, found := sess.ResolveInboundMedia(ref); found {
			if strings.TrimSpace(k) == "" {
				k = "image"
			}
			return b, k, true
		}
	}
	// A kept image id. Renderable all along — ResolveRecentImage falls through
	// to the library — but never deliverable, so "work from this picture"
	// succeeded and "send me this picture" had no route at all.
	//
	// KEPT ONLY, never a ring position, and not gated on allowInbound. A kept
	// entry is something the agent deliberately filed; the ring also holds the
	// photo the user just sent, and resolving image#1 here would reopen the
	// echo the inbound guard closed — the same picture posted straight back to
	// the room it arrived in.
	if data, found := ResolveKeptImage(sess, ref); found && len(data) > 0 {
		return base64.StdEncoding.EncodeToString(data), "image", true
	}
	imgs := resolveWorkspaceImages(sess, []string{ref})
	if len(imgs) == 0 {
		// Not a workspace file or inbound media — try a remote http(s) image/video
		// URL (a meme, a fetched picture). Without this, an attachment given as a
		// URL is silently dropped and the message sends text-only.
		if b64, kind, ok := fetchAttachmentURL(ref); ok {
			return b64, kind, true
		}
		return "", "", false
	}
	k := "image"
	if isVideoAttachment(ref) {
		k = "video"
	}
	return imgs[0], k, true
}

// attachmentHTTP fetches remote attachment URLs; short timeout, no redirects to
// non-public hosts (Get follows redirects but each hop's host is re-checked by the
// transport-free guard below is best-effort — the final body is still capped).
var attachmentHTTP = &http.Client{Timeout: 20 * time.Second}

// maxAttachmentBytes caps a fetched attachment so a huge or malicious URL can't
// balloon memory or the outbound payload.
const maxAttachmentBytes = 20 << 20 // 20 MB

// fetchAttachmentURL downloads a remote http(s) image/video (e.g. a meme URL) so
// it can ride along on an outbound message. SSRF-guarded (IsNonPublicHost),
// size-capped, and restricted to image/* or video/* content — arbitrary remote
// content is never attached. Returns base64 + kind ("image"/"video") on success.
func fetchAttachmentURL(ref string) (b64, kind string, ok bool) {
	if !strings.HasPrefix(ref, "http://") && !strings.HasPrefix(ref, "https://") {
		return "", "", false
	}
	u, err := url.Parse(ref)
	if err != nil || IsNonPublicHost(u.Hostname()) {
		return "", "", false
	}
	resp, err := attachmentHTTP.Get(ref)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", "", false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxAttachmentBytes {
		return "", "", false
	}
	ct := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	if ct = strings.TrimSpace(ct); ct == "" {
		ct = http.DetectContentType(data)
	}
	switch {
	case strings.HasPrefix(ct, "image/"):
		return base64.StdEncoding.EncodeToString(data), "image", true
	case strings.HasPrefix(ct, "video/"):
		return base64.StdEncoding.EncodeToString(data), "video", true
	}
	return "", "", false // not an image/video — don't attach arbitrary content
}

// attachmentsParamDesc is the shared description for the messaging tools' explicit
// image/file param. One self-contained call ("send THIS image to X") instead of
// the implicit, easily-skipped workspace(action="attach")-first convention.
const attachmentsParamDesc = "Optional attachment reference(s) to send WITH this message. Any of: a workspace file path from image(action=\"find\"/\"generate\") e.g. [\"find-djbk.jpg\"]; an inbound media id from the media manifest e.g. [\"media#1\"] to re-send a photo someone sent you; or a direct http(s) image/video URL e.g. [\"https://i.redd.it/abc.jpg\"] (fetched + attached; image/video only, size-capped). The items ride out to the recipient in one call. Prefer this over a separate workspace(action=\"attach\") step."

// messageMedia gathers what rides an outbound message, split by how the
// transport delivers it: images, and videos (which carries audio too, as
// files). The explicit `attachments` refs (the steered, self-contained path)
// PLUS the implicit sess.Images / sess.Videos / [ATTACH:] markers, deduped.
// One place so send_message, message_contact and notify_owner behave
// identically: the fragmented "did the model remember to attach first?"
// failure mode is why images were silently dropped. It used to gather images
// only, so a video ref was dropped and a song went out as a .jpg.
func messageMedia(sess *ToolSession, args map[string]any, text string) (images, videos []string) {
	images = collectMessageAttachments(sess, text)
	if sess != nil {
		videos = append(videos, sess.Videos...)
	}
	seen := map[string]bool{}
	for _, m := range append(append([]string(nil), images...), videos...) {
		seen[m] = true
	}
	// Collect refs from BOTH `attachments` (the canonical plural array) AND
	// `attachment` (the singular alias LLMs reach for), each tolerating either an
	// array OR a bare string: models pass whichever shape, and a dropped image on
	// a name/shape mismatch is silent + confusing (observed: attachment="x.png"
	// ignored because the handler only read attachments[]).
	add := func(ref string) {
		if strings.TrimSpace(ref) == "" {
			return
		}
		b64, kind, ok := resolveAttachmentRef(sess, ref, true)
		if !ok || seen[b64] {
			return
		}
		seen[b64] = true
		if kind == "video" {
			videos = append(videos, b64)
		} else {
			images = append(images, b64)
		}
	}
	for _, key := range []string{"attachments", "attachment"} {
		switch v := args[key].(type) {
		case []any:
			for _, e := range v {
				if s, ok := e.(string); ok {
					add(s)
				}
			}
		case string:
			add(v)
		}
	}
	return images, videos
}

// isReplyToActiveInbound reports whether recip is the very conversation this run
// is replying to (a channel inbound). Replying to whoever just messaged you is
// in-thread, not a proactive reach-out, so it skips the approval queue. False on
// web / dispatch runs (ReplyAuthorizedKey empty), leaving the gate unchanged there.
func isReplyToActiveInbound(sess *ToolSession, recip string) bool {
	return sess != nil && sess.ReplyAuthorizedKey != "" && recip != "" && recip == sess.ReplyAuthorizedKey
}

// replyInThread makes a message to the conversation this turn is answering
// the turn's reply: held, with what it carries, and sent once when the turn
// ends (ToolSession.HoldInThreadReply). Sent at once, it went out and then the
// turn's own reply followed it, two messages saying the same thing. Both doors
// a turn can write to its own conversation through come here: send_message
// and message_contact.
func replyInThread(sess *ToolSession, label, text string, images, videos []string) string {
	sess.HoldInThreadReply(text)
	for _, b := range images {
		sess.AppendImage(b)
	}
	for _, b := range videos {
		sess.AppendVideo(b)
	}
	return fmt.Sprintf("That is your reply to %s: it goes out, with what it carries, as the one message this turn sends when you finish. End the turn now and do not repeat it.", label)
}

// resolveCheckAgent maps a user-supplied poll-monitor checker reference to a
// real agent id. Tries: exact id, then case-insensitive display-name match,
// then falls back to the creating channel agent (always valid). This prevents
// a monitor from being saved with a checker that doesn't exist — the failure
// the LLM hits when it sets check_agent to a conversational nickname.
func resolveCheckAgent(sess *ToolSession, owner, want, fallback string) string {
	if sess == nil || sess.DB == nil {
		return fallback
	}
	if _, ok := loadAgent(sess.DB, want); ok {
		return want
	}
	for _, a := range listAgents(sess.DB, owner) {
		if strings.EqualFold(strings.TrimSpace(a.Name), want) {
			return a.ID
		}
	}
	return fallback
}

// operatorRecipientKey is the stable identity used for pre-authorization and
// display when the Operator messages someone via phantom: the chat_id when
// targeting a known conversation or group, else the raw handle. Pre-auth keys
// on this so "Always allow" whitelists exactly the recipient that was approved
// (a group's chat_id, or an individual's handle) — never a different person.
func operatorRecipientKey(chatID, handle string) string {
	if c := strings.TrimSpace(chatID); c != "" {
		return c
	}
	return strings.TrimSpace(handle)
}

// operatorRecipientLabel renders a resolved recipient for user-facing messages:
// "DisplayName (handle)" when both are known, else whichever is present.
func operatorRecipientLabel(s MessagingChatSummary) string {
	switch {
	case s.DisplayName != "" && s.Handle != "":
		return s.DisplayName + " (" + s.Handle + ")"
	case s.DisplayName != "":
		return s.DisplayName
	case s.Handle != "":
		return s.Handle
	default:
		return s.ChatID
	}
}

// operatorDeliverMessage sends one message OUTBOUND via the messaging transport
// (Bridges) — addressed by chat_id (unambiguous; the ONLY correct way to reach a
// group) or handle. The agent composed the text, so it goes VERBATIM. This is
// the single delivery chokepoint for notify_owner, message_contact, and the
// approval-execution path; routing it through Bridges' outbox is what makes them
// actually deliver (phantom's outbox is no longer drained — the daemon polls
// Bridges now). Returns the text delivered.
func operatorDeliverMessage(owner, agentID, chatID, handle, text string, images []string) (string, error) {
	ct, ok := ActiveChannelThreads()
	if !ok {
		return "", fmt.Errorf("no messaging transport is available")
	}
	// Resolve the recipient's bound channel and route through its service when an
	// EXACT per-contact binding exists — so a proactive send_message goes out the
	// same channel that conversation lives on (and its per-agent name tag rides
	// along). We deliberately use ONLY an exact binding, never the whole-service
	// fallback: passing a global iMessage channel's service for a Telegram chat
	// would misroute. With no exact binding we keep the empty service, which lets
	// the transport resolve it from the conversation (Telegram stays Telegram,
	// not the iMessage default). Channel REPLIES already pass the inbound's
	// service explicitly. The name tag itself stays per-agent opt-in (TagName).
	service := exactChannelService(owner, chatID, handle)
	if err := ct.Deliver(owner, service, chatID, handle, text, agentNameTag(owner, agentID), images); err != nil {
		return "", err
	}
	return text, nil
}

// operatorDeliverMedia is operatorDeliverMessage with videos.
//
// A separate entry point because only one caller has them: a background job
// that finished a video download. The other nine send text or images, and
// widening the shared signature would make every one of them name a parameter
// it has no use for.
//
// Falls back to the images-only Deliver when the transport does not implement
// the optional capability, so a transport that cannot carry video still gets
// the text rather than nothing. The dropped-video line stays in that case,
// because a silent drop is what made this take a person noticing the file was
// missing to find.
func operatorDeliverMedia(owner, agentID, chatID, handle, text string, images, videos []string) (string, error) {
	if len(videos) == 0 {
		return operatorDeliverMessage(owner, agentID, chatID, handle, text, images)
	}
	ct, ok := ActiveChannelThreads()
	if !ok {
		return "", fmt.Errorf("no messaging transport is available")
	}
	md, ok := ct.(ChannelMediaDeliverer)
	if !ok {
		Log("[operator] transport cannot carry video; delivering %d image(s) and the text only", len(images))
		return operatorDeliverMessage(owner, agentID, chatID, handle, text, images)
	}
	service := exactChannelService(owner, chatID, handle)
	if err := md.DeliverMedia(owner, service, chatID, handle, text, agentNameTag(owner, agentID), images, videos); err != nil {
		return "", err
	}
	return text, nil
}

// exactChannelService returns the service of a channel EXACTLY bound to this
// chat_id/handle, or "" when only a whole-service ("global view") channel — or
// nothing — covers it. Unlike channelForChat, it never falls back to a
// whole-service binding, because that channel's service isn't guaranteed to be
// the transport THIS conversation uses (see operatorDeliverMessage). Empty means
// "let the transport resolve it," preserving the multi-service routing default.
func exactChannelService(owner, chatID, handle string) string {
	chatID, handle = strings.TrimSpace(chatID), strings.TrimSpace(handle)
	if owner == "" || (chatID == "" && handle == "") {
		return ""
	}
	for _, ch := range ListChannels(RootDB, owner) {
		if ch.Address == "" {
			continue // whole-service binding — doesn't pin a transport for THIS chat
		}
		if ch.Address == chatID || (handle != "" && ch.Address == handle) {
			return ch.Service
		}
	}
	return ""
}

// orchestrateBaseDB is the orchestrate app's base store, captured at Init so
// free functions can resolve per-user agents from UserDB(orchestrateBaseDB,
// owner) — the SAME store the editor writes to, NOT RootDB (a different bucket).
var orchestrateBaseDB Database

// agentNameTag returns the name to prefix on an agent's outbound message when
// that agent opted into signing its messages (AgentRecord.TagName). Returns ""
// when the agent hasn't opted in, can't be resolved, or has no name — an empty
// tag leaves the message untagged, which is the safe default.
func agentNameTag(owner, agentID string) string {
	owner = strings.TrimSpace(owner)
	agentID = strings.TrimSpace(agentID)
	if owner == "" || agentID == "" || orchestrateBaseDB == nil {
		return ""
	}
	udb := UserDB(orchestrateBaseDB, owner)
	if udb == nil {
		return ""
	}
	a, ok := findAgentByNameOrID(udb, owner, agentID)
	if !ok || !a.TagName {
		return ""
	}
	return strings.TrimSpace(a.Name)
}

// threadBindingGatekeeperRule is the wake rule stamped on an agent-requested 1:1
// thread binding. It's the shared DM default (also seeded on inbound DM
// connections in apps/bridges) — see core.DefaultDMGatekeeperRule for the rules.
const threadBindingGatekeeperRule = DefaultDMGatekeeperRule

// formatRunSteps renders a run's tool trace as a compact "steps:" block for
// inspect_run, so the model (and the audit trail) can see WHAT the run did — each
// tool, its args, and a truncated result or the error. Returns "" when the run
// captured no steps (older runs, or a fire that only produced text), so the
// output stays unchanged for those.
func formatRunSteps(steps []RunStep) string {
	if len(steps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("steps:\n")
	for i, s := range steps {
		outcome := s.Result
		if s.Err != "" {
			outcome = "ERROR: " + s.Err
		}
		outcome = strings.TrimSpace(strings.ReplaceAll(outcome, "\n", " "))
		if r := []rune(outcome); len(r) > 200 {
			outcome = string(r[:200]) + "…"
		}
		args := strings.TrimSpace(s.Args)
		if r := []rune(args); len(r) > 200 {
			args = string(r[:200]) + "…"
		}
		b.WriteString(fmt.Sprintf("  %d. %s(%s) -> %s\n", i+1, s.Name, args, outcome))
	}
	return b.String()
}

// argBool reads a boolean tool arg (accepts a real bool, or a "true"/"false"
// string which some tool bridges send), falling back to def.
func argBool(args map[string]any, key string, def bool) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return def
}

// findAgentBoundChannel locates an agent's own AgentBound 1:1 channel matching
// `to` (a handle/chat_id directly, or a resolvable recipient), so set_thread_wake
// and release_thread_binding can only ever touch bindings the agent created.
func findAgentBoundChannel(owner, agentID, to string) (Channel, bool) {
	to = strings.TrimSpace(to)
	// A sub-agent's thread bindings are its top-level parent's
	// (agent_family.go), so that is where they are found.
	agentID = channelAgentFor(owner, agentID)
	for _, ch := range ListChannelsForAgent(RootDB, owner, agentID) {
		if ch.AgentBound && ch.Address == to {
			return ch, true
		}
	}
	if link, ok := ActiveMessagingLink(); ok {
		if rec, ok := link.ResolveRecipient(owner, to); ok {
			for _, ch := range ListChannelsForAgent(RootDB, owner, agentID) {
				if ch.AgentBound && (ch.Address == rec.Handle || ch.Address == rec.ChatID) {
					return ch, true
				}
			}
		}
	}
	return Channel{}, false
}

// ownerOnlyFleetTools are the operator tools that set up work which runs later
// with the owner's authority (standing jobs, monitors), change who is let in
// (sender grants, thread bindings), or read the owner's own fleet and run
// history. Someone else on a channel can ask the agent things; they cannot
// have it do these on their word (nonOwnerRequester).
var ownerOnlyFleetTools = map[string]bool{
	// schedule folds the standing-agent, monitor and timer tools; their
	// handlers run inside its own, so the gate on it covers them all.
	"schedule":                 true,
	"authorize_channel_sender": true,
	"request_thread_binding":   true, "set_thread_wake": true, "release_thread_binding": true,
	"list_runs": true, "inspect_run": true,
	// Its wake runs as the owner's, with a note written on the stranger's word.
	"await_result": true,
}

func operatorManagementTools(sess *ToolSession, agentID string) []AgentToolDef {
	return operatorManagementToolsFor(sess, agentID, nil)
}

// operatorManagementToolsFor is operatorManagementTools with the agent's own
// recurring tasks folded into `schedule` too (an author keeps them; a Fleet
// agent passes nil and schedules through the fleet only).
func operatorManagementToolsFor(sess *ToolSession, agentID string, rec recurringImpl) []AgentToolDef {
	tools := operatorManagementToolDefs(sess, agentID, rec)
	for i := range tools {
		if !ownerOnlyFleetTools[tools[i].Tool.Name] {
			continue
		}
		name, h := tools[i].Tool.Name, tools[i].Handler
		tools[i].Handler = func(ctx context.Context, args map[string]any) (string, error) {
			if nonOwnerRequester(ctx) {
				return fmt.Sprintf("Not done: %s is the owner's to use, and this request came from someone else on a channel. Tell them it needs the owner, who can do it themselves or ask you directly.", name), nil
			}
			return h(ctx, args)
		}
	}
	return tools
}

// operatorToolDefsUnfolded is the operator toolset with every scheduling
// tool separate. The model sees operatorManagementToolDefs instead, which
// folds the scheduling ones into `schedule` (operator_schedule_tool.go);
// their handlers here are that tool's implementation.
func operatorToolDefsUnfolded(sess *ToolSession, agentID string) []AgentToolDef {
	owner := ""
	if sess != nil {
		owner = sess.Username
	}
	// The channel/orchestrator agent this management surface is bound to
	// (runner passes t.agent.ID). Named apart from agentID because handlers
	// shadow agentID with the agent a call names.
	controllerAgentID := agentID
	return []AgentToolDef{
		delegateToolDef(sess, owner, controllerAgentID),
		createStandingAgentToolDef(sess, owner, agentID, controllerAgentID),
		listStandingAgentsToolDef(sess, owner),
		runStandingNowToolDef(owner),
		setStandingPausedToolDef(owner),
		deleteStandingAgentToolDef(owner),
		listRunsToolDef(owner),
		inspectRunToolDef(sess, owner),
		createEventMonitorToolDef(sess, owner, agentID),
		timerToolDef(sess, owner, agentID),
		awaitResultToolDef(sess, owner, agentID),
		// The phantom-named read tools were removed — superseded by the
		// channel-scoped chat tools (channel_tools.go) which read the live
		// Bridges threads. A Fleet agent reaches them by holding a whole-service
		// channel.
		messageContactToolDef(sess, owner, agentID, controllerAgentID),
		authorizeChannelSenderToolDef(owner),
		requestThreadBindingToolDef(owner, controllerAgentID),
		setThreadWakeToolDef(owner, controllerAgentID),
		releaseThreadBindingToolDef(owner, controllerAgentID),
		listEventMonitorsToolDef(owner, controllerAgentID),
		deleteEventMonitorToolDef(owner, controllerAgentID),
		// bridge — the friendly "connect an authenticated API to a schedule"
		// front-end over a watch monitor + call_<credential>. Same family as
		// the event-monitor tools above; defaults wake_agent to THIS agent so a
		// Fleet/Cortex agent can self-monitor a service by leaving it blank.
		ChatToolToAgentToolDefWithSession(bridgeDefTool(controllerAgentID), sess),
	}
}

// dropToolsByName removes the named tools from a parallel (tools, names) pair.
// Used to keep the generic interval scheduler ("recurring") off the Operator —
// it schedules through the fleet (create_standing_agent) instead.
// toolsHaveName reports whether a tool of that name is in the list.
func toolsHaveName(tools []AgentToolDef, name string) bool {
	for _, td := range tools {
		if td.Tool.Name == name {
			return true
		}
	}
	return false
}

func dropToolsByName(tools []AgentToolDef, names []string, drop ...string) ([]AgentToolDef, []string) {
	dropSet := map[string]bool{}
	for _, d := range drop {
		dropSet[d] = true
	}
	outT := make([]AgentToolDef, 0, len(tools))
	for _, td := range tools {
		if !dropSet[td.Tool.Name] {
			outT = append(outT, td)
		}
	}
	outN := make([]string, 0, len(names))
	for _, n := range names {
		if !dropSet[n] {
			outN = append(outN, n)
		}
	}
	return outT, outN
}

// --- arg helpers (o-prefixed to avoid collisions in this package) ------------

func oArgStr(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return v
	}
	return ""
}

func oArgBool(args map[string]any, k string) bool {
	switch v := args[k].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	}
	return false
}

func oArgInt(args map[string]any, k string) int {
	switch v := args[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

// --- required-argument reporting --------------------------------------------

// reqArg is one argument a tool needs, paired with what belongs in it.
type reqArg struct {
	name  string
	value string
	hint  string // shown only when THIS argument is the one missing
}

// missingArgs reports the arguments that are actually empty, and explains only
// those. It returns nil when nothing is missing, so a validator reads as one
// line at the call site.
//
// The shape it replaces listed every required argument whenever any one of
// them was empty: "http_poll monitors need url, compare_op, and threshold".
// Live, an agent that had sent url and compare_op correctly on every attempt
// read that as a verdict on all three. It spent six rounds re-sending the same
// call, dropping arguments that were never the problem, and finally switching
// to a different monitor kind, because the message never once said which field
// was blank. An error that names the whole contract instead of the one broken
// term makes the caller debug by elimination.
func missingArgs(subject string, req ...reqArg) error {
	var names, hints []string
	for _, r := range req {
		if strings.TrimSpace(r.value) != "" {
			continue
		}
		names = append(names, r.name)
		if r.hint != "" {
			hints = append(hints, "  "+r.name+": "+r.hint)
		}
	}
	if len(names) == 0 {
		return nil
	}
	// Said explicitly, because the caller cannot see it: the rest of the call
	// was accepted. Without this the natural repair is to rebuild the whole
	// call from scratch, which is what cost those six rounds.
	msg := fmt.Sprintf("%s is missing %s: everything else you sent is fine, so resend the same call with %s filled in",
		subject, joinWords(names), pluralIt(len(names)))
	if len(hints) > 0 {
		msg += ":\n" + strings.Join(hints, "\n")
	}
	return errors.New(msg)
}

// joinWords writes a list the way a sentence would.
func joinWords(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}

// fireLimitSentence states a monitor's fire bound in the sentence that confirms
// it was created, or nothing when it has none. The confirmation is the only
// place the caller learns whether the bound it asked for was taken: before
// stop_after existed, a request to alert twice produced a cheerful "monitor
// created" and an unbounded monitor, and nothing anywhere said otherwise.
func fireLimitSentence(m EventMonitor) string {
	var parts []string
	if until := strings.TrimSpace(m.Until); until != "" {
		parts = append(parts, fmt.Sprintf("It stops itself when this is true: %s.", until))
	}
	switch {
	case m.MaxFires == 1:
		parts = append(parts, "It fires ONCE and then stops itself (kept, not deleted: resume it for another).")
	case m.MaxFires > 1:
		parts = append(parts, fmt.Sprintf("It stops itself after %d fires (kept, not deleted: resume it for another %d).", m.MaxFires, m.MaxFires))
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// scheduleHomeAgent decides which agent a schedule created in THIS turn
// belongs to: where its runs report, and which thread a monitor wakes.
//
// The default is the agent that set it up, and for a controller that is right.
// A Fleet agent schedules specialists on the user's behalf and is where the
// user reads, so its runs belong in its thread.
//
// For an AUTHORING agent it is wrong, and this is the bug it exists to fix.
// Builder holds the operator toolset deliberately, so that "build me a tool and
// run it every 30 minutes" is one job rather than a build plus a handoff. But
// Builder is a workshop, not a home: it has no ongoing thread, and its sessions
// are build sessions nobody returns to. A schedule that reported there ran
// correctly and then filed its findings where the user was never going to look
// again, which reads exactly like the schedule going to the wrong agent.
//
// So when an author wires up a schedule for a DIFFERENT agent, that agent gets
// it. The author built it; the agent owns it.
func scheduleHomeAgent(sess *ToolSession, controllerAgentID, runnerAgentID string) string {
	runner := strings.TrimSpace(runnerAgentID)
	controller := strings.TrimSpace(controllerAgentID)
	if runner == "" || runner == controller {
		return controller
	}
	if !authoringHandoffAgent(sess, controller) {
		return controller
	}
	return runner
}

// authoringHandoffAgent reports an agent that authors things for others rather
// than running them itself: it should hand a schedule to whoever will run it.
//
// Builder by identity, since its authoring catalog comes from an identity check
// rather than from any record. Any other agent that holds the authoring
// capability WITHOUT being a Fleet controller is the same case: authoring is a
// capability now, not an identity, so an agent somebody granted it to has the
// same problem for the same reason. A Fleet controller keeps its schedules,
// because delegating and then reading the results is what it is for.
func authoringHandoffAgent(sess *ToolSession, agentID string) bool {
	if agentID == "" {
		return false
	}
	if isBuilderAgent(agentID) {
		return true
	}
	if sess == nil || sess.DB == nil {
		return false
	}
	rec, ok := loadAgent(sess.DB, agentID)
	if !ok {
		return false
	}
	return agentHandsOffSchedules(rec)
}

// agentHandsOffSchedules is the same question asked of a record already in
// hand, which is what the turn-scoped schedule tools have.
func agentHandsOffSchedules(rec AgentRecord) bool {
	if isBuilderAgent(rec.ID) {
		return true
	}
	return agentCanAuthor(rec) && !rec.Fleet
}

// resolveMonitorWakeAgent decides which agent an event monitor wakes.
//
// A monitor has no run target the way a standing agent does: its entire shape
// is "wake me when this happens", so the creator is the default and usually
// the right answer. The exception is an AUTHOR setting one up for somebody
// else's agent, where the default fires the alert into a build session nobody
// reopens, which is indistinguishable from the monitor never firing.
//
// So an author must say whose monitor it is, and is told so rather than left
// to find out. The one case that needs no agent is a monitor that delivers
// straight to a person or a chat: nothing has to wake up for that to work.
func resolveMonitorWakeAgent(sess *ToolSession, creatorAgentID, requested, notify, deliverTo string) (string, error) {
	if want := strings.TrimSpace(requested); want != "" {
		if sess == nil || sess.DB == nil {
			return "", fmt.Errorf("cannot resolve wake_agent %q here", want)
		}
		target, ok := findAgentByNameOrID(sess.DB, sess.Username, want)
		if !ok {
			return "", fmt.Errorf("no agent named %q to wake; create_agent first, or leave wake_agent empty to wake yourself", want)
		}
		return target.ID, nil
	}
	if !authoringHandoffAgent(sess, creatorAgentID) {
		return creatorAgentID, nil
	}
	// Delivered rather than woken: no agent has to read this for it to work.
	if deliverTo != "" || notify == EventNotifyDirect || notify == EventNotifyText {
		return creatorAgentID, nil
	}
	return "", fmt.Errorf("say which agent this monitor should wake: pass wake_agent with the name of the agent that owns this watch. " +
		"Waking you would fire the alert into this build session, where nobody will see it again. " +
		"If the alert should go straight to the user instead, set notify=\"direct\"")
}

// notifyOwnerToolDef is the tool every agent has for telling its owner
// something out of band.
//
// A FRAMEWORK tool, not an Operator one. Anything that runs can have something
// worth saying, and the agents that need it most are the scheduled and standing
// ones, which have no live conversation to speak into and are the least likely
// to be in a fleet. It was gated while it texted the owner's phone
// unconditionally, because universal reach plus unconditional delivery is every
// agent able to interrupt somebody at will; forwarding being opt-in is what
// makes it safe to hand out, since by default this writes a notification and
// nothing leaves the machine.
//
// One definition, called from the framework catalog. It lived inside the
// Operator's toolset, and a copy for everybody else is how two tools with one
// name come to behave differently.
func notifyOwnerToolDef(sess *ToolSession, owner, agentID, controllerAgentID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "notify_owner",
			Description: "Tell the OWNER something out of band. Where it reaches them is their own setting and not something you decide or report on, so write a message that stands on its own and say only that you sent it. Use this ONLY when the user has explicitly asked to be notified, OR when a monitor, scheduled job, or long-running task is delivering a result they asked to be alerted about. Do NOT use it on greetings or ordinary chat, and do NOT volunteer unprompted status; for a normal reply, just reply in the conversation. No approval needed since it only reaches the owner. To include an image/file, pass its workspace path in `attachments`; if one cannot be delivered the reply says so, and then whatever mattered in it belongs in the text.",
			Parameters: map[string]ToolParam{
				"text":        {Type: "string", Description: "What to tell the owner. The first line becomes the notification's title, so lead with the thing itself rather than a preamble."},
				"attachments": {Type: "array", Items: &ToolParam{Type: "string"}, Description: attachmentsParamDesc},
			},
			Required: []string{"text"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			text := strings.TrimSpace(oArgStr(args, "text"))
			if text == "" {
				return "", fmt.Errorf("text is required")
			}
			// This tool telling the owner something IS a notification, and
			// there is one rule for those: they are kept, and one
			// preference decides whether they also reach a phone or an
			// inbox. It used to text unconditionally and leave nothing
			// behind, which made it both a second delivery rule and a
			// message that could be lost outright when the bridge was down.
			//
			// The preference RESOLVES rather than reading raw, so an owner
			// who has never been asked keeps the texts they were already
			// getting. See ResolveNotifyForward.
			recordAgentNotice(owner, controllerAgentID, text)
			wants := "off"
			if AuthDB != nil {
				wants = ResolveNotifyForward(AuthDB(), owner)
			}
			images, videos := messageMedia(sess, args, text)
			toPhone := wants == "phone" || wants == "both"
			if !toPhone {
				// Images cannot live in a notice, so say so rather than
				// dropping them quietly. The agent can then decide to put
				// what mattered into the text.
				if len(images)+len(videos) > 0 {
					// The ONE thing the agent has to know, because it
					// changes what it should do: the attachment did not
					// arrive, so anything that mattered in it has to be
					// said in words instead.
					return "Sent. The attachments could not be delivered, so put anything essential from them into the message text.", nil
				}
				return notifySent, nil
			}
			link, ok := ActiveMessagingLink()
			if !ok {
				return notifySent, nil
			}
			self, ok := link.OwnerHandle(owner)
			if !ok {
				return notifySent, nil
			}
			// Prefixed, like any other forwarded notice: a text arriving on
			// a phone has no other context, and this one is going out for
			// exactly the same reason the passive ones do.
			//
			// WITHOUT the agent, because this path is the agent-aware send
			// and the bridge already tags it "[<name>] " on the wire. Both
			// produced "[Wren] [Wren@Gohort] ...". The deployment name is
			// the part the tag does not carry, so that is the part this
			// adds; an owner who has turned the bridge tag off gets the
			// deployment and not the agent, which is the setting they chose.
			outbound := noticePrefix("") + " " + text
			// DeliverMessage (not SendToHandle) so attachments ride along;
			// persona is inactive for the owner's own chat, so the text
			// is sent verbatim. Empty chatID resolves the owner's thread.
			if _, err := operatorDeliverMedia(owner, agentID, "", self, outbound, images, videos); err != nil {
				// The notice is already filed, so say what is actually true
				// rather than reporting a total failure. Telling the model
				// the send failed when the owner WILL see it is how an agent
				// ends up saying the same thing three more ways.
				Log("[orchestrate.notify] forwarding %s's notification failed: %v", owner, err)
				return notifySent, nil
			}
			// The owner's channel (their phone) must see what was sent — record
			// into its cortex/session so when the owner replies, the agent knows
			// what it just told them (fixes "I sent you a joke but have no idea
			// what it was" — the reply lands over the bridge in a different
			// session than this notify_owner). Recorded by AGENT ID, not by
			// matching the owner's handle to a channel address (those rarely
			// match: SelfHandle is a phone, the channel address may be an email
			// or chat-id form, so channelForChat misses and the cortex is
			// skipped). notify_owner IS this agent notifying the owner, so it
			// belongs in this agent's cortex. No-op if the agent has no cortex.
			appendCortexObs(sess.DB, controllerAgentID, "Sent to you", cortexKindMessage, text)
			return notifySent, nil
		},
	}
}

// There is no one-off schedule, and asking for one is how a model trying
// to run something once ends up making a recurring job: point it at the
// tools that run once instead.
const standingNotOnce = "A standing agent has no one-off schedule: it runs on its schedule until deleted. To run something once now, use machine(action=\"run\", name, input) for a machine, or agents(action=\"run\") for an agent or pipeline, and delete any standing agent made only to get one run."

// agentBoundTo reports whether agent already has the 1:1 thread at chatID or
// handle bound, so it can read the replies there.
func agentBoundTo(owner, agent, chatID, handle string) bool {
	for _, ch := range ListChannelsForAgent(RootDB, owner, agent) {
		if ch.AgentBound && ch.Service == "imessage" && ((handle != "" && ch.Address == handle) || (chatID != "" && ch.Address == chatID)) {
			return true
		}
	}
	return false
}

// replyBindingPending is a request already waiting to bind that thread to
// agent: a bind_thread, or a send_message that binds on approval.
func replyBindingPending(owner, agent, chatID, handle string) (string, bool) {
	same := func(a Authorization) bool {
		return (handle != "" && a.Handle == handle) || (chatID != "" && a.ChatID == chatID)
	}
	for _, ex := range ListAuthorizations(RootDB, owner) {
		if ex.Action == "bind_thread" && ex.Agent == agent && same(ex) {
			return ex.ID, true
		}
		if ex.Action == "send_message" && ex.BindReply && approvalRequester(ex) == agent && same(ex) {
			return ex.ID, true
		}
	}
	return "", false
}

// queueReplyBinding asks, once, to bind the thread a message just went out to
// without an approval, and says so for the send's reply.
func queueReplyBinding(owner, agent, chatID, handle, label string) string {
	if id, pending := replyBindingPending(owner, agent, chatID, handle); pending {
		return fmt.Sprintf("Reading their reply is already awaiting the user's approval (id %s).", id)
	}
	a := SaveAuthorization(RootDB, Authorization{
		Owner: owner, Action: "bind_thread", Agent: agent, ChatID: chatID, Handle: handle, Brief: label,
	})
	return fmt.Sprintf("Reading their reply needs the user's approval: queued (id %s). Once approved, their reply wakes you.", a.ID)
}

// timingArg reads an optional "timing" argument (a core/timing.Spec as JSON,
// or the map a tool call carries) and validates it.
func timingArg(args map[string]any) (timing.Spec, bool, error) {
	raw, ok := args["timing"]
	if !ok || raw == nil {
		return timing.Spec{}, false, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return timing.Spec{}, false, err
	}
	var spec timing.Spec
	if err := json.Unmarshal(b, &spec); err != nil {
		return timing.Spec{}, false, fmt.Errorf("timing: %v", err)
	}
	if !spec.Set() {
		return timing.Spec{}, false, nil
	}
	if err := spec.Validate(); err != nil {
		return timing.Spec{}, false, fmt.Errorf("timing: %v", err)
	}
	return spec, true, nil
}
