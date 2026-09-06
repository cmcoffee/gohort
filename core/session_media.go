package core

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"runtime"
	"strings"
)

// AppendImage appends a base64-encoded image to the session image list.
func (s *ToolSession) AppendImage(b64 string) {
	if s == nil || b64 == "" {
		return
	}
	s.mu.Lock()
	// Content-dedup: if the same b64 is already queued for this
	// turn's delivery, skip the duplicate. Catches "LLM emitted
	// three workspace(attach) calls with the same path in one
	// batch" — they all hash the same file content, only the
	// first gets queued, the rest no-op. Cheaper than maintaining
	// a separate fingerprint set since we already hold the lock
	// and the slice is bounded per-turn.
	for _, existing := range s.Images {
		if existing == b64 {
			s.mu.Unlock()
			Debug("[sess.AppendImage] skipped duplicate (content already attached this turn); caller=%s", callerSummary(2))
			return
		}
	}
	s.Images = append(s.Images, b64)
	n := len(s.Images)
	s.mu.Unlock()
	// Debug log — captures the call site + a content hash (md5
	// of full b64) so duplicate vs distinct appends are visible
	// at a glance. The earlier "first 16 chars" fingerprint was
	// misleading for JPEGs because they all share the JFIF
	// header prefix; md5 of full content disambiguates.
	sum := md5.Sum([]byte(b64))
	fp := hex.EncodeToString(sum[:4]) // 8 hex chars — plenty unique for visual scan
	Debug("[sess.AppendImage] now %d image(s); size=%dB; md5_8=%s; caller=%s", n, len(b64), fp, callerSummary(2))
}

// callerSummary returns a short "file:line func" string from N frames
// up the stack. Used by sess.AppendImage's debug log to point at
// what tool / handler actually appended an image, so duplicate
// appends can be traced to their source. Skip=2 names the function
// that called AppendImage (skip=0 is callerSummary itself, skip=1
// is AppendImage, skip=2 is the caller).
func callerSummary(skip int) string {
	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "?"
	}
	fn := runtime.FuncForPC(pc)
	name := "?"
	if fn != nil {
		name = fn.Name()
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
	}
	// File path → basename for readability.
	if i := strings.LastIndexByte(file, '/'); i >= 0 {
		file = file[i+1:]
	}
	return fmt.Sprintf("%s:%d %s", file, line, name)
}

// AppendVideo appends a base64-encoded video to the session video list.
// Consumed by apps that deliver outbound attachments (phantom iMessage
// repost path); ignored by apps that don't. Same content-dedup as
// AppendImage — duplicate-content appends are skipped to defend
// against LLMs that emit redundant workspace(attach) calls in one
// batch with the same path.
func (s *ToolSession) AppendVideo(b64 string) {
	if s == nil || b64 == "" {
		return
	}
	s.mu.Lock()
	for _, existing := range s.Videos {
		if existing == b64 {
			s.mu.Unlock()
			Debug("[sess.AppendVideo] skipped duplicate (content already attached this turn); caller=%s", callerSummary(2))
			return
		}
	}
	s.Videos = append(s.Videos, b64)
	s.mu.Unlock()
}

// InboundMediaItem is one addressable piece of media that arrived on THIS turn
// (a contact's photo or clip on a channel). It carries the bytes inline because,
// unlike produced media, inbound media has no workspace file to read back at
// delivery time. The ID (media#1, media#2, …) is the handle exposed to the model
// via the media manifest so it can post a SPECIFIC inbound item back BY ID.
type InboundMediaItem struct {
	ID     string // "media#1" — the handle shown to the model
	Kind   string // "image" | "video"
	B64    string // base64-encoded bytes, ready to attach outbound
	Sender string // display name of who sent it, for the manifest
}

// RegisterInboundMedia records an inbound attachment and returns its assigned id
// (media#1, media#2, …). Order-stable so the id matches the position the media
// manifest shows the model. Raw bytes in, base64 stored. The gap this closes:
// produced media (image action=find/generate) is already postable by its
// workspace filename, but inbound media had NO handle, so the model could only
// describe a photo someone sent, never re-send it. Turn-scoped: dispatch mints a
// fresh session per turn, so ids never point at stale bytes.
func (s *ToolSession) RegisterInboundMedia(kind string, raw []byte, sender string) string {
	if s == nil || len(raw) == 0 {
		return ""
	}
	if strings.TrimSpace(kind) == "" {
		kind = "image"
	}
	s.mu.Lock()
	id := fmt.Sprintf("media#%d", len(s.InboundMedia)+1)
	s.InboundMedia = append(s.InboundMedia, InboundMediaItem{
		ID:     id,
		Kind:   kind,
		B64:    base64.StdEncoding.EncodeToString(raw),
		Sender: strings.TrimSpace(sender),
	})
	s.mu.Unlock()
	// An arriving photo joins the image space too, so it stays editable after
	// this turn ends. media#N is turn-scoped and vanishes; without this, "blend
	// the two photos I sent" worked only while they were still in the live
	// message, and a later "now make that one snowy" had nothing to point at.
	if kind == "image" {
		who := strings.TrimSpace(sender)
		note := "received"
		if who != "" {
			note += " from " + who
		}
		// Recorded WITHOUT a description. The model is about to be handed this
		// very image as part of the turn, so describing it here would be a
		// second vision call for the same picture — racing the user's own
		// request for the one slot a local backend has, which is how "attach a
		// photo and ask what it is" turns into a wait. It gets described if it
		// is ever kept, where nothing is competing with it.
		recordRecentImage(s, raw, note, ImageFromUser, false)
	}
	return id
}

// InboundMediaCount is how many attachments arrived with this turn's message.
// Lets a failed media#N lookup say whether the id was out of range or whether
// the namespace was wrong entirely — two mistakes that need opposite advice.
func (s *ToolSession) InboundMediaCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.InboundMedia)
}

// ResolveInboundMedia maps a media-id reference ("media#2") to its base64 bytes
// and kind. Match is space- and case-tolerant (see normalizeMediaID). ok is
// false when ref is not a known inbound id, so the outbound collector falls
// through to workspace-filename resolution (the produced-media path).
func (s *ToolSession) ResolveInboundMedia(ref string) (b64, kind string, ok bool) {
	if s == nil {
		return "", "", false
	}
	want := normalizeMediaID(ref)
	if want == "" {
		return "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.InboundMedia {
		if normalizeMediaID(m.ID) == want {
			return m.B64, m.Kind, true
		}
	}
	return "", "", false
}

// normalizeMediaID canonicalizes a media-id reference so "media#2", "media #2",
// and "MEDIA#2" all match the stored "media#2". Returns "" when ref is not a
// media id at all (e.g. a workspace filename), which is the signal for callers
// to fall through to file resolution. Strict on the tail: only digits follow the
// hash, so a filename that merely starts with "media" never false-matches.
func normalizeMediaID(ref string) string {
	r := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(ref)), " ", "")
	if !strings.HasPrefix(r, "media#") {
		return ""
	}
	num := strings.TrimPrefix(r, "media#")
	if num == "" {
		return ""
	}
	for _, c := range num {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return "media#" + num
}

// ViewImage is one image queued for the model to look at, with a label naming
// what it is.
//
// The label is the anchor, and it exists because position is not one. Several
// image-producing tools can run in the SAME round — that is deliberate, a user
// asking for three ducks gets three parallel renders — and they all append to
// this one queue from separate goroutines. Arrival order is decided by whichever
// backend answered first, so it matches neither the order the model called the
// tools in nor the order their results are listed. Handing the model a bare pile
// of pixels and telling it the preceding tool result explains them asks it to
// re-derive an alignment that was never preserved, and the failure is silent:
// it describes duck two as duck one and nothing contradicts it.
type ViewImage struct {
	Data  []byte
	Label string // e.g. "the render for image#r.7a3f9c2b" — what THIS picture is
}

// AppendViewImage queues an image for the LLM to see on its next round without
// saying what it is. Prefer AppendViewImageAs: an unlabeled image is exactly the
// ambiguity ViewImage.Label exists to remove, and it is only tolerable when the
// round can produce just one.
func (s *ToolSession) AppendViewImage(data []byte) {
	s.AppendViewImageAs(data, "")
}

// AppendViewImageAs queues an image for the LLM to see on its next round, with
// a label naming what it is. Tools that want the LLM to "look at" something call
// this; the agent loop's caller drains and injects them as a synthetic user
// message before the next LLM call. These bytes never reach the user — they're
// LLM-input only.
func (s *ToolSession) AppendViewImageAs(data []byte, label string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.PendingViewImages = append(s.PendingViewImages, ViewImage{Data: data, Label: label})
	s.mu.Unlock()
}

// DrainViewImages returns and clears any pending view images. Callers
// should drain right before the next LLM call, after tool results have
// been appended to history; the frames go into a synthetic user message
// with Images set so buildMessages handles them through the standard
// vision content path.
func (s *ToolSession) DrainViewImages() []ViewImage {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.PendingViewImages) == 0 {
		return nil
	}
	out := s.PendingViewImages
	s.PendingViewImages = nil
	return out
}

// DeliverySession is where a background result belongs: the conversation the
// user is actually in, which is the sub-session id only when nothing separated
// them. Every delivery path must read this rather than ChatSessionID.
func (s *ToolSession) DeliverySession() string {
	if s == nil {
		return ""
	}
	if d := strings.TrimSpace(s.DeliverySessionID); d != "" {
		return d
	}
	return strings.TrimSpace(s.ChatSessionID)
}

// Unattended reports whether this turn is happening somewhere NOBODY can watch
// it work.
//
// A web chat shows a live pill, a progress tree and a running status line: the
// person can see the render happening and waiting reads as working. A messaging
// conversation shows none of that. The contact texted, and until a message
// arrives there is nothing on their screen at all — so a turn that quietly
// spends ninety seconds on a tool call is indistinguishable from an assistant
// that has gone away, and they ask again, or give up.
//
// That difference is why the same call deserves different answers about
// detaching. The channel path already recognises this for failures ("a
// messaging surface must not go silent — the contact texted and expects a
// reply"); this is the same rule applied to slowness.
//
// Read off the conversation rather than a flag anyone has to remember to set:
// a turn answering a channel inbound carries the conversation it must reply to.
func (s *ToolSession) Unattended() bool {
	if s == nil {
		return false
	}
	return strings.TrimSpace(s.ChannelChatID) != "" || strings.TrimSpace(s.ChannelHandle) != ""
}

// ImageRenderCount is how many renders this TURN has run, without recording
// another. Turn-scoped with the session, so unlike a declared count it cannot
// leak into the next request in the same conversation. Nil-safe.
func (s *ToolSession) ImageRenderCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.imgGenTotal
}

// NextImageAttempt records one generate_image call and returns (attempt, total),
// both 1-based. `attempt` is the CHAIN position that drives the retry budget:
// refine=true continues the chain (the model is regenerating the previous image
// to fix a checkable miss), refine=false resets it (a new, distinct subject gets
// a fresh budget). `total` is the absolute count this turn — never reset, so the
// runaway hard cap can't be evaded by mislabeling retries as new subjects. The
// counters are session-scoped, so a fresh ToolSession each turn zeroes both.
// Nil-safe.
func (s *ToolSession) NextImageAttempt(refine bool) (attempt, total int) {
	if s == nil {
		return 1, 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.imgGenTotal++
	if refine {
		s.imgGenAttempts++ // continue the retry chain
	} else {
		s.imgGenAttempts = 1 // new subject → fresh retry budget
	}
	return s.imgGenAttempts, s.imgGenTotal
}
