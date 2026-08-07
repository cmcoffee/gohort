// Kept images: the durable half of the image space.
//
// The ring next door (image_space.go) is deliberately disposable — it holds the
// last few pictures so "edit the one you just made" resolves, and prunes on
// every write. That is the right lifetime for working material and the wrong
// one for a reference: a brand mark, a style sample, a chart the agent will be
// asked to match again next month. Those need a name that means the same thing
// in six weeks, which a positional ref by construction cannot be.
//
// So an agent can promote a ring entry to a KEPT image under a name it chooses.
// Kept images live in their own directory, are never pruned, and are addressed
// as image#<name> — the same prefix as the ring, because to the model these are
// one idea ("a picture I can point at"), and one namespace is one thing to
// learn. Numeric names are refused precisely so the two forms can't collide.
//
// CAPTIONS ARE THE POINT. A kept image is captioned once, at keep time, by a
// vision pass. Everything downstream — the manifest, and later the memory layer
// — reads the caption, not the pixels. That keeps recall at the cost of a line
// of text and pays for vision exactly once per image, instead of every time the
// agent wonders what it saved. It is also what lets a kept image be referenced
// from a text memory without teaching the memory system about images at all.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// keptImageLimit caps how many images one agent may keep. Generous next to the
// ring's 10 — these are chosen deliberately, one at a time — but bounded, so a
// loop that keeps on every turn hits a wall it can report rather than a disk it
// has filled.
const keptImageLimit = 50

// maxKeptImageBytes matches the per-file ceiling on app assets. A reference
// image is a reference, not an archive master.
const maxKeptImageBytes = 8 << 20 // 8 MiB

// KeptImage is one durable entry.
type KeptImage struct {
	Name    string // "brand_mark" — stable, chosen by the agent
	Ref     string // "image#brand_mark" — what to pass to any images= param
	Note    string // why it was kept, in the agent's words
	Caption string // one line, for listings — enough to tell saved images apart
	// Description is the detailed one, and it is what memory stores — so that a
	// question months later can FIND this picture in a text-only vector space.
	//
	// It is not a stand-in for the picture. Rendering from these words produces
	// something that matches the description rather than the thing described,
	// which for a face or a logo is simply a different face or logo. It earns
	// its place by answering "which picture did they mean" and by sparing a
	// vision call for questions ABOUT the image; when the picture is what is
	// needed, the reference is passed instead.
	Description string
	// Origin is where the picture came from, carried over from the ring at keep
	// time. It decides whether this entry may be offered as a REFERENCE — the
	// agent's own output is not evidence of what anything really looks like —
	// and it is not derivable later, since Note below is the agent's own words.
	Origin ImageOrigin
	When   time.Time // when it was kept
	// Owner is the agent whose library holds it, and Inherited marks the ones
	// that came from an ancestor rather than this agent. The distinction is
	// not cosmetic: an inherited image can be USED but not forgotten here, and
	// telling the model otherwise would have it "delete" things that stay.
	Owner     string
	Inherited bool
	// Subject is who or what the picture is OF, when the agent said. A person
	// subject is what makes "use the picture of Rory" resolvable instead of a
	// guess across filenames. See image_subject.go.
	Subject ImageSubject
	path    string // absolute file path
}

// KeptImageRemember and KeptImageForgetMemory bridge the library into whatever
// memory layer the host app runs. Hooks rather than a direct call because the
// memory layer belongs to the app (core has no opinion about which one), and
// because the RIGHT layer is the pull-only one: a kept image is reference
// material an agent needs when a specific question raises it, not a standing
// instruction worth prompt tokens on every turn forever. Both are best-effort
// and nil until wired — a keep must not fail because memory was unavailable.
var (
	KeptImageRemember     func(sess *ToolSession, k KeptImage)
	KeptImageForgetMemory func(sess *ToolSession, name string)
)

// KeptImageParentAgent resolves an agent's parent, for the inheritance walk
// below. A hook because the parent relationship belongs to the app's agent
// model, which core doesn't have. Nil (or "") means no parent, which is the
// single-agent case and needs no wiring at all.
var KeptImageParentAgent func(sess *ToolSession, agentID string) string

// keptImageAncestry is the depth an inheritance walk will go. Fleets nest a
// couple of levels in practice; the bound is what stops a mis-set parent from
// walking forever, alongside the cycle guard.
const keptImageAncestry = 4

// keptImageChain is the agent ids a session may READ kept images from, nearest
// first: its own, then its parent's, and so on. Inheritance is read-only and
// one-directional — a sub-agent sees the reference material its parent kept
// (a fleet sharing one brand mark is the whole motivating case) but can never
// write to or delete from a library that isn't its own.
func keptImageChain(sess *ToolSession) []string {
	if sess == nil {
		return nil
	}
	agent := strings.TrimSpace(sess.AgentID)
	if agent == "" {
		agent = "_shared"
	}
	chain := []string{agent}
	if KeptImageParentAgent == nil {
		return chain
	}
	seen := map[string]bool{agent: true}
	for cur := agent; len(chain) < keptImageAncestry; {
		parent := strings.TrimSpace(KeptImageParentAgent(sess, cur))
		if parent == "" || seen[parent] {
			break // no parent, or a cycle someone mis-wired
		}
		seen[parent] = true
		chain = append(chain, parent)
		cur = parent
	}
	return chain
}

// keptImageMeta is the on-disk sidecar, same shape of decision as the ring's:
// next to the file, so the library survives a restart with no database.
type keptImageMeta struct {
	Note        string    `json:"note"`
	Caption     string    `json:"caption"`
	Description string    `json:"description,omitempty"`
	Mime        string    `json:"mime"`
	When        time.Time `json:"when"`
	// Origin is omitempty: an entry kept before origins existed reads back
	// unknown, and unknown is treated as reference-eligible rather than
	// silently dropped from a library somebody deliberately built.
	Origin ImageOrigin `json:"origin,omitempty"`
	// Subject is who or what it depicts. omitempty so a library built before
	// subjects existed reads back unsubjected rather than as a picture of
	// nobody, which the people section would then have to filter out.
	Subject ImageSubject `json:"subject,omitempty"`
}

// keptImageDir is where an agent's library lives — a sibling of the ring, so
// pruneRecentImages can never reach it. Scoped per user AND per agent for the
// same reason the ring is: a reference one agent kept is not an answer another
// agent should be handed.
func keptImageDir(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	agent := strings.TrimSpace(sess.AgentID)
	if agent == "" {
		agent = "_shared"
	}
	return keptImageDirFor(sess, agent)
}

// keptImageDirFor is one named agent's library, under the SESSION's user. The
// user never varies across the walk: inheritance crosses agents, never people.
func keptImageDirFor(sess *ToolSession, agentID string) string {
	if sess == nil {
		return ""
	}
	user := strings.TrimSpace(sess.Username)
	if user == "" || strings.TrimSpace(agentID) == "" {
		return ""
	}
	return filepath.Join(ImageDir(), "kept", safeRecentUser(user), safeRecentUser(agentID))
}

// safeKeptName reduces a requested name to a safe single path element, or ""
// when nothing usable survives. An all-digits name is refused rather than
// mangled: image#3 already means "third newest", and a kept image answering to
// it would make every positional reference ambiguous.
func safeKeptName(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		case r == ' ':
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_-")
	if out == "" {
		return ""
	}
	if len(out) > 48 {
		out = out[:48]
	}
	if _, err := strconv.Atoi(out); err == nil {
		return ""
	}
	return out
}

// KeepImage promotes a picture out of the ring into the durable library under
// name. ref is anything ResolveRecentImage accepts, so an agent keeps what it
// just made without needing a filename.
func KeepImage(sess *ToolSession, ref, name, note string) (KeptImage, error) {
	return KeepImageOf(sess, ref, name, note, ImageSubject{})
}

// KeepImageOf is KeepImage with a subject attached — who or what the picture is
// of. See image_subject.go for why the subject exists and why its identity is
// the handle rather than the name.
//
// ONE CURRENT PICTURE PER SUBJECT. Keeping a new one for a subject already held
// forgets the old entry, even under a different name. The alternative — letting
// three pictures of the same person accumulate — recreates the exact problem
// the subject was added to solve, one level down: asked for "the picture of
// Rory" the agent would again be choosing between filenames it wrote itself.
func KeepImageOf(sess *ToolSession, ref, name, note string, subject ImageSubject) (KeptImage, error) {
	dir := keptImageDir(sess)
	if dir == "" {
		return KeptImage{}, fmt.Errorf("no image library available for this session")
	}
	clean := safeKeptName(name)
	if clean == "" {
		return KeptImage{}, fmt.Errorf("name %q is not usable — use letters, digits, - or _, and not a bare number (image#3 already means the third-newest picture)", name)
	}
	data, origin, ok := keepSource(sess, ref)
	if !ok {
		return KeptImage{}, fmt.Errorf("no image found for %q — call action=\"help\" to list the pictures you can point at", ref)
	}
	if len(data) > maxKeptImageBytes {
		return KeptImage{}, fmt.Errorf("that image is %s, over the %s limit for a kept reference", HumanSize(int64(len(data))), HumanSize(maxKeptImageBytes))
	}
	existing := keptImagesOf(sess, keptImageOwnAgent(sess))
	held := false
	var superseded []string
	for _, k := range existing {
		if k.Name == clean {
			held = true // a re-keep under the same name REPLACES; it doesn't count again
			continue
		}
		// A different name holding the same subject is the OLD picture of that
		// person. Collected now, deleted only once the new one is safely
		// written — losing the only headshot to a failed write would be the
		// worst possible outcome of an operation whose point is to have one.
		if subject.Named() && SameSubject(k.Subject, subject) {
			superseded = append(superseded, k.Name)
		}
	}
	if !held && len(existing) >= keptImageLimit {
		return KeptImage{}, fmt.Errorf("you are already keeping %d images, the limit — forget one first with action=\"forget\"", keptImageLimit)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return KeptImage{}, fmt.Errorf("could not open the image library: %w", err)
	}
	base := filepath.Join(dir, clean)
	if err := os.WriteFile(base+".png", data, 0600); err != nil {
		return KeptImage{}, fmt.Errorf("could not save the image: %w", err)
	}
	// Caption once, here. Best-effort: a library entry with no caption is still
	// a usable reference, and refusing to keep the picture because the vision
	// model was unreachable would be the worse trade.
	// Promotion, not a fresh look: the ring described this picture when it
	// arrived, so keeping it should cost nothing. Only an entry that was never
	// described — captioning off, the pass still in flight, or a kept image
	// being re-kept under a new name — pays for a vision call here.
	caption, description := ringDescription(sess, ref)
	if caption == "" && description == "" {
		caption, description = CaptionImage(sess, data)
	}
	kept := KeptImage{
		Subject:     subject,
		Origin:      origin,
		Name:        clean,
		Ref:         RecentImageRefPrefix + clean,
		Note:        strings.TrimSpace(note),
		Caption:     caption,
		Description: description,
		When:        time.Now(),
		path:        base + ".png",
	}
	meta, _ := json.Marshal(keptImageMeta{Note: kept.Note, Caption: kept.Caption, Description: kept.Description, Mime: "image/png", When: kept.When, Origin: kept.Origin, Subject: kept.Subject})
	if err := os.WriteFile(base+".json", meta, 0600); err != nil {
		Debug("[image_keep] write meta: %v", err)
	}
	// Tell memory it exists, so a question months later can surface it without
	// the agent having to remember to go looking. The caption does the work —
	// what gets stored and recalled is a sentence, never the picture.
	if KeptImageRemember != nil {
		KeptImageRemember(sess, kept)
	}
	// Now that the replacement exists, retire what it replaced. Best-effort:
	// a stale second picture of the same person is untidy, but failing the keep
	// over it would throw away the new one for no gain.
	for _, old := range superseded {
		if _, err := ForgetImage(sess, old); err != nil {
			Debug("[image_keep] superseding %q: %v", old, err)
			continue
		}
		Log("[image_keep] %q replaces %q as the picture of %s", clean, old, SubjectLabel(subject))
	}
	return kept, nil
}

// keepSource resolves what a keep should store, and where it came from.
//
// image#N and image#name are the ring and the library. media#N is the photo the
// user attached to THIS turn — a separate namespace that ResolveRecentImage
// does not answer for, so "keep the picture I just sent you" failed on the one
// ref the model had been handed for it. The edit path already accepted media#N;
// keep did not, and a rule on one side of that symmetry is a bug.
//
// A media ref is user-origin by definition: nothing a tool produces is ever
// one.
func keepSource(sess *ToolSession, ref string) ([]byte, ImageOrigin, bool) {
	if data, ok := ResolveRecentImage(sess, ref); ok {
		return data, ringOrigin(sess, ref), true
	}
	if b64, kind, ok := sess.ResolveInboundMedia(ref); ok && (kind == "" || kind == "image") {
		if data, err := decodeBase64Image(b64); err == nil && len(data) > 0 {
			return data, ImageFromUser, true
		}
	}
	return nil, ImageOriginUnknown, false
}

// ringOrigin reports where the ring entry behind a ref came from. A ref that
// names an already-KEPT image carries that entry's origin forward, so
// re-keeping under a new name cannot launder generated output into a reference.
func ringOrigin(sess *ToolSession, ref string) ImageOrigin {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(strings.ToLower(ref), RecentImageRefPrefix) {
		if n, err := strconv.Atoi(strings.TrimSpace(ref[len(RecentImageRefPrefix):])); err == nil && n >= 1 {
			if all := RecentImages(sess); n <= len(all) {
				return all[n-1].Origin
			}
		}
	}
	for _, k := range KeptImages(sess) {
		if strings.EqualFold(k.Ref, ref) {
			return k.Origin
		}
	}
	return ImageOriginUnknown
}

// ringDescription returns what the ring already knows about a ref, or empty
// when it knows nothing (or the ref names a kept image rather than a position).
func ringDescription(sess *ToolSession, ref string) (caption, description string) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(strings.ToLower(ref), RecentImageRefPrefix) {
		return "", ""
	}
	n, err := strconv.Atoi(strings.TrimSpace(ref[len(RecentImageRefPrefix):]))
	if err != nil || n < 1 {
		return "", ""
	}
	all := RecentImages(sess)
	if n > len(all) {
		return "", ""
	}
	return all[n-1].Caption, all[n-1].Description
}

// ForgetImage drops a kept image. Reports whether there was one to drop, so the
// caller can tell the model "there wasn't one" instead of implying it deleted
// something.
func ForgetImage(sess *ToolSession, name string) (bool, error) {
	dir := keptImageDir(sess)
	clean := safeKeptName(name)
	if dir == "" || clean == "" {
		return false, nil
	}
	base := filepath.Join(dir, clean)
	if _, err := os.Stat(base + ".png"); err != nil {
		// Inherited images are readable but not this agent's to delete. Saying
		// "nothing to forget" would be a lie the model acts on — it would
		// report the picture gone while every future reference still resolves.
		for _, k := range KeptImages(sess) {
			if k.Name == clean && k.Inherited {
				return false, fmt.Errorf("%q belongs to %s, not to you — you can use it but only its owner can forget it. To stop using it here, keep your own image under that name instead", clean, k.Owner)
			}
		}
		return false, nil
	}
	if err := os.Remove(base + ".png"); err != nil {
		return false, fmt.Errorf("could not forget %q: %w", clean, err)
	}
	_ = os.Remove(base + ".json")
	// The memory entry outliving the image would be worse than never having
	// written one: recall would keep offering a ref that no longer resolves.
	if KeptImageForgetMemory != nil {
		KeptImageForgetMemory(sess, clean)
	}
	return true, nil
}

// KeptImages lists the library, oldest name-sorted (stable ordering — these are
// addressed by name, so recency carries no meaning here the way it does in the
// ring).
func KeptImages(sess *ToolSession) []KeptImage {
	chain := keptImageChain(sess)
	seen := map[string]bool{}
	var out []KeptImage
	for depth, agentID := range chain {
		for _, k := range keptImagesOf(sess, agentID) {
			if seen[k.Name] {
				continue // a nearer library already answered this name
			}
			seen[k.Name] = true
			k.Inherited = depth > 0
			out = append(out, k)
		}
	}
	return out
}

// keptImagesOf reads one agent's own library, name-sorted.
func keptImagesOf(sess *ToolSession, agentID string) []KeptImage {
	dir := keptImageDirFor(sess, agentID)
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".png") {
			names = append(names, strings.TrimSuffix(e.Name(), ".png"))
		}
	}
	sort.Strings(names)
	out := make([]KeptImage, 0, len(names))
	for _, n := range names {
		base := filepath.Join(dir, n)
		k := KeptImage{Name: n, Ref: RecentImageRefPrefix + n, Owner: agentID, path: base + ".png"}
		var meta keptImageMeta
		if raw, err := os.ReadFile(base + ".json"); err == nil {
			_ = json.Unmarshal(raw, &meta)
		}
		k.Note, k.Caption, k.Description, k.When, k.Origin = meta.Note, meta.Caption, meta.Description, meta.When, meta.Origin
		k.Subject = meta.Subject
		out = append(out, k)
	}
	return out
}

// ResolveKeptImage reads the bytes behind an "image#<name>" reference. Numeric
// suffixes are left alone — those are the ring's.
func ResolveKeptImage(sess *ToolSession, ref string) ([]byte, bool) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(strings.ToLower(ref), RecentImageRefPrefix) {
		return nil, false
	}
	clean := safeKeptName(ref[len(RecentImageRefPrefix):])
	if clean == "" {
		return nil, false
	}
	// Nearest library wins, so an agent that kept its own "house_style" gets
	// its own rather than its parent's.
	for _, agentID := range keptImageChain(sess) {
		dir := keptImageDirFor(sess, agentID)
		if dir == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, clean+".png"))
		if err == nil && len(data) > 0 {
			return data, true
		}
	}
	return nil, false
}

// KeptImageManifest renders the library for the model. Captions carry it — the
// agent reads what each picture IS without any of them entering the prompt as
// pixels. Empty when the library is, so callers append it unconditionally.
func KeptImageManifest(sess *ToolSession) string {
	all := KeptImages(sess)
	if len(all) == 0 {
		return ""
	}
	// People first, and under their own heading. A picture of a person answers
	// a different question from a logo — "what does this person look like",
	// which is the question a request naming somebody actually asks — and
	// mixing the two into one alphabetical list is what left the agent
	// choosing a reference by filename.
	var people, things []KeptImage
	for _, k := range all {
		if k.Subject.Person && k.Subject.Named() {
			people = append(people, k)
			continue
		}
		things = append(things, k)
	}
	var b strings.Builder
	inherited := false
	if len(people) > 0 {
		b.WriteString("People you have a picture of — when a request names one of them, pass their id as a reference so you are working from their actual face, not a description of it:\n")
		for _, k := range people {
			if k.Inherited {
				inherited = true
			}
			fmt.Fprintf(&b, "- %s — %s\n", k.Ref, personLine(k))
		}
		b.WriteString("If a request names somebody who is NOT listed here, you do not know what they look like. Say so, or find a picture. Never render a face from a description and present it as them.\n")
		if len(things) > 0 {
			b.WriteString("\n")
		}
	}
	if len(things) > 0 {
		b.WriteString("Other images you have kept (stable — these names don't shift):\n")
		for _, k := range things {
			if k.Inherited {
				inherited = true
			}
			fmt.Fprintf(&b, "- %s — %s\n", k.Ref, keptLine(k))
		}
	}
	if inherited {
		b.WriteString("Ones marked (inherited) come from the agent that owns you: usable exactly like your own, but only their owner can forget them.\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// personLine renders one person's entry: who it is, whether they are here, and
// what they look like.
func personLine(k KeptImage) string {
	var parts []string
	label := SubjectLabel(k.Subject)
	switch {
	case k.Subject.Owner:
		parts = append(parts, label+" (the person you work for)")
	case strings.TrimSpace(k.Subject.Handle) != "":
		// The handle is shown because it is the identity — it is how the agent
		// can tell this entry belongs to the person currently talking, rather
		// than to somebody else who goes by the same name.
		parts = append(parts, label+" ("+strings.TrimSpace(k.Subject.Handle)+")")
	default:
		// Said plainly: an entry with no handle was never matched to anyone who
		// messaged in, so it is a label the agent wrote, not an identification.
		parts = append(parts, label+" (name only — never matched to a handle)")
	}
	if k.Inherited {
		parts = append(parts, "inherited")
	}
	if d := describeKept(k); d != "" {
		parts = append(parts, d)
	}
	return strings.Join(parts, " — ")
}

// keptLine renders a non-person entry the way it always read.
func keptLine(k KeptImage) string {
	out := describeKept(k)
	if out == "" {
		out = "kept image"
	}
	if k.Inherited {
		return "(inherited) " + out
	}
	return out
}

// describeKept joins the agent's own reason for keeping a picture with what the
// picture looks like. Either may be missing; both missing is possible and the
// callers each have their own word for it.
func describeKept(k KeptImage) string {
	desc := k.Note
	switch {
	case desc != "" && k.Caption != "":
		desc += " — " + k.Caption
	case desc == "":
		desc = k.Caption
	}
	// Provenance still overrides everything: the agent's own output is not
	// evidence of what anyone really looks like, and that matters MORE for a
	// person than for a logo, not less.
	if k.Origin.AgentMade() {
		return "MADE BY YOU (" + string(k.Origin) + ") — not a reference for anything real: " + desc
	}
	return desc
}

// captionImagePrompt asks for BOTH tiers in one pass, because they answer
// different questions and a second call would pay the image twice. Deliberately
// about APPEARANCE, not interpretation: an agent reading this later already
// knows what the picture is FOR from the name it chose, and needs to know what
// it LOOKS like — "navy circular mark, lowercase white wordmark" is useful
// where "the company's logo" is not.
const captionImagePrompt = "Describe this image twice, in this exact format:\n\n" +
	"First line: a short label, under 15 words — what it is.\n" +
	"Then a blank line, then a detailed description of 60 to 120 words: subject, " +
	"composition, colors, any text and its typography, and the overall style. " +
	"Enough detail that someone who cannot see it could match the look.\n\n" +
	"Describe only what is visible. No preamble, no markdown, no quotes, no headings."

// CaptionImage runs one vision pass over the bytes and returns a short
// description. Returns "" on any failure — no session LLM, no vision support,
// a timeout — because every caller treats the caption as a nicety and none of
// them should fail for want of one.
func CaptionImage(sess *ToolSession, data []byte) (caption, description string) {
	if sess == nil || sess.LLM == nil || len(data) == 0 {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(AppContext(), 45*time.Second)
	defer cancel()
	resp, err := sess.LLM.Chat(ctx, []Message{{
		Role:    "user",
		Content: captionImagePrompt,
		Images:  [][]byte{data},
	}})
	if err != nil || resp == nil {
		Debug("[image_keep] caption: %v", err)
		return "", ""
	}
	out := strings.TrimSpace(resp.Content)
	// Label is the first line; everything after it is the detail. A model that
	// answers with one line only still yields a usable label — the detail is
	// the part that degrades, and an empty one is handled everywhere.
	caption, description = out, ""
	if i := strings.IndexAny(out, "\r\n"); i >= 0 {
		caption = strings.TrimSpace(out[:i])
		description = strings.TrimSpace(out[i:])
	}
	// Bounded so a model that ignores the limits can't put a paragraph into
	// every future manifest, or a page into every memory entry.
	if len(caption) > 200 {
		caption = strings.TrimSpace(caption[:200]) + "…"
	}
	if len(description) > 1200 {
		description = strings.TrimSpace(description[:1200]) + "…"
	}
	return caption, description
}

// keptImageOwnAgent is the agent id whose library this session WRITES to —
// always its own, never an ancestor's. Inheritance is read-only.
func keptImageOwnAgent(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	if a := strings.TrimSpace(sess.AgentID); a != "" {
		return a
	}
	return "_shared"
}
