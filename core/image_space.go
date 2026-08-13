// The image space: a small ring of the images a user recently produced or
// received, addressable as image#1 (newest), image#2, and so on.
//
// The problem it solves: every image tool saved into the session workspace and
// then told the model to attach with cleanup=true. That made the MODEL
// responsible for filesystem hygiene — it had to remember to delete, and when it
// didn't, the workspace filled with gen-<uuid>.png files it could no longer tell
// apart. And once a file was cleaned up, "edit the picture you just made" had
// nothing to point at.
//
// So the framework keeps the last few itself and prunes on every write. Nothing
// to delete, stable names, and a previously produced image stays referenceable
// for an edit on a later turn.
//
// The ring is deliberately NOT advertised in the tool schema. It changes every
// time an image is produced, and tool schemas sit at the front of the prompt —
// listing it there would invalidate the prefix cache on every image operation.
// Refs are handed back in tool RESULTS (end of context) and enumerated on
// demand by the `image` tool's help action.
package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// recentImageLimit is how many of the AGENT'S OWN pictures the space holds per
// user. Enough to cover "the one before that" in normal conversation; small
// enough that the pruned directory stays trivial to scan.
const recentImageLimit = 10

// sourceImageLimit is the separate allowance for pictures the agent was GIVEN
// or found. Separate because one flat queue made the agent's output evict the
// user's originals: a render is added on every generate and every edit, so a
// handful of attempts silently pushed out the photo they were attempts AT, and
// the agent had to ask for it again. A render costs a prompt to reproduce; the
// only copy of somebody's selfie costs them the conversation.
//
// So the two are counted apart. Nothing the agent makes can ever displace
// something it was given, whatever it renders in between.
const sourceImageLimit = 10

// RecentImageRefPrefix is the reference form the model uses: image#1 is newest.
const RecentImageRefPrefix = "image#"

// ringIDPrefix marks a STABLE ring reference, as opposed to the positional
// image#N or a kept image#<name>.
//
// The dot is load-bearing. safeKeptName strips every character outside
// [a-z0-9-_], so a kept name can never contain one — which makes a dotted id
// impossible to confuse with a name a user chose, without reserving any word.
const ringIDPrefix = "r."

// ringIDFromBase derives a picture's stable id from its filename.
//
// Files are already named <unixnano>-<uuid>, unique and never rewritten, so
// the identity exists on disk and was simply never exposed. The model was
// handed image#N — documented at its own definition as "NOT stable across
// writes" — and then told to hold onto it, which is how an edit landed on a
// picture the user never mentioned: saving a render pushes the result to
// image#1 and slides everything else down.
func ringIDFromBase(base string) string {
	base = strings.TrimSuffix(base, ".png")
	// The uuid half, shortened. Collisions inside a pruned ring of a few dozen
	// entries are not a practical concern, and a long id is a long thing for a
	// model to copy exactly.
	if i := strings.Index(base, "-"); i >= 0 && i+1 < len(base) {
		u := strings.ReplaceAll(base[i+1:], "-", "")
		if len(u) > 8 {
			u = u[:8]
		}
		if u != "" {
			return ringIDPrefix + u
		}
	}
	return ""
}

// isRingID reports whether a ref names a stable ring entry rather than a
// position or a kept name.
func isRingID(ref string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(ref)), ringIDPrefix)
}

// RecentImage is one entry in the space.
type RecentImage struct {
	Ref  string    // "image#1" — position, newest first; NOT stable across writes
	ID   string    // "image#r.7a3f9c2b" — STABLE for this picture's whole life in the ring
	Note string    // why it exists ("generated: a cat on a bike", "edited image#2")
	When time.Time // when it entered the space
	// Caption is what the picture LOOKS like, from the vision pass at record
	// time; Description is the long form, carried so a later keep promotes it
	// instead of paying for the image a second time. Both may be empty — the
	// pass is best-effort and may not have finished, or may not have run.
	Caption     string
	Description string
	// Origin decides whether this picture may stand as a reference. See
	// ImageOrigin: an agent's own output is not evidence of anything.
	Origin ImageOrigin
	path   string // absolute file path
}

// recentImageMeta is the on-disk sidecar. Kept next to the image so the space
// survives a restart with its captions intact and needs no database.
type recentImageMeta struct {
	Note string `json:"note"`
	Mime string `json:"mime"`
	// Caption and Description are filled in AFTER the write, by the vision
	// pass below. Both may be empty: the pass is best-effort and the entry is
	// useful without it.
	Caption     string `json:"caption,omitempty"`
	Description string `json:"description,omitempty"`
	// Origin is omitempty, so a sidecar written before this existed simply has
	// no field and reads back as unknown — where originFromNote classifies it
	// from the note the framework wrote.
	Origin ImageOrigin `json:"origin,omitempty"`
}

// recentImageDir is where a user's ring lives. Empty when there's no session or
// no username to scope it to — the space is per-user, so an anonymous caller
// simply doesn't get one.
func recentImageDir(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	user := strings.TrimSpace(sess.Username)
	if user == "" {
		return ""
	}
	// Per AGENT, not just per user. The refs are positional — image#1 is
	// whatever is newest — so one shared ring across a fleet means an agent
	// asking for "the picture you just made" can be handed one another agent
	// made seconds earlier, and it has no way to tell. Silent, plausible, and
	// wrong: the failure mode of an unanchored reference.
	//
	// The agent-less case gets its own folder rather than the parent, so no
	// directory ever holds both a ring and other agents' rings.
	agent := strings.TrimSpace(sess.AgentID)
	if agent == "" {
		agent = "_shared"
	}
	return filepath.Join(ImageDir(), "recent", safeRecentUser(user), safeRecentUser(agent))
}

// safeRecentUser reduces a username to something safe as a single path element,
// so a username can never escape the directory or collide with a sibling.
func safeRecentUser(user string) string {
	var b strings.Builder
	for _, r := range user {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "user"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// transientImageRefPattern finds picture handles that do NOT survive: a
// positional ring id (image#3) and a turn-scoped inbound id (media#2). A KEPT
// image is image#<name> — letters, not digits — and is deliberately not matched,
// because that one lasts.
var transientImageRefPattern = regexp.MustCompile(`(?i)\b(image|media)#\d+`)

// TransientImageRefs returns the picture handles in a text that will stop
// resolving, in order and deduplicated.
//
// They break for different reasons and on different clocks. image#N is a
// POSITION in the ring, so it silently comes to mean a different picture as new
// ones arrive; media#N belongs to one turn and is gone by the next. Written
// into anything durable — a pinned note, a stored fact — both are wrong within
// a few turns, and the note reads as authoritative the whole time.
func TransientImageRefs(text string) []string {
	found := transientImageRefPattern.FindAllString(text, -1)
	if len(found) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(found))
	for _, f := range found {
		k := strings.ToLower(f)
		if !seen[k] {
			seen[k] = true
			out = append(out, f)
		}
	}
	return out
}

// SnapshotImageRefs freezes what image#1, image#2 … mean for the length of one
// tool round, and is why a positional ref cannot come to mean a different
// picture between the model writing it and the call running.
//
// The model chooses a ref from what it was last shown — the previous round's
// results and manifest. Every save then renumbers the ring, INCLUDING a save
// the same round performs on its own output: ask for a render and an attach in
// one response and the render slides into image#1 before the attach resolves,
// so the attach delivers the render instead of the picture the model meant.
// Same for a detached call, which runs after the turn that wrote it has ended.
// The symptom is a reply carrying two pictures, one of them from an earlier
// request entirely.
//
// So the snapshot is taken BEFORE a round's tools run, and holds until the next
// round is taken — which is exactly when the model has seen fresh results and
// its idea of the positions is current again. Nothing here resolves bytes: it
// rewrites the ref to the stable id that meant the same picture at snapshot
// time, and normal resolution proceeds from there.
//
// Sessions with no snapshot (a CLI path that never calls this) keep the old
// behavior — positions resolve live.
func SnapshotImageRefs(sess *ToolSession) {
	if sess == nil {
		return
	}
	all := RecentImages(sess)
	frozen := make([]string, len(all))
	for i, r := range all {
		frozen[i] = strings.TrimSpace(r.ID) // "" for a picture with no stable id
	}
	sess.mu.Lock()
	sess.imageRefsFrozen = frozen
	sess.mu.Unlock()
}

// FrozenImageRef returns the stable ref a positional one meant at snapshot
// time. Reports false for anything else — a stable id, a kept name, an
// out-of-range position, or a picture too old to have an id — so callers pass
// the original through untouched.
func FrozenImageRef(sess *ToolSession, ref string) (string, bool) {
	if sess == nil {
		return "", false
	}
	body := strings.TrimSpace(ref)
	if !strings.HasPrefix(strings.ToLower(body), RecentImageRefPrefix) {
		return "", false
	}
	n, err := strconv.Atoi(strings.TrimSpace(body[len(RecentImageRefPrefix):]))
	if err != nil || n < 1 {
		return "", false
	}
	sess.mu.Lock()
	frozen := sess.imageRefsFrozen
	sess.mu.Unlock()
	if n > len(frozen) || frozen[n-1] == "" {
		return "", false
	}
	return frozen[n-1], true
}

// FreezeImageRefs rewrites the positional image refs in one tool call's
// arguments to the stable ids they named at snapshot time, and returns how many
// it rewrote.
//
// Only a value that is ENTIRELY a ref is rewritten, in a string or in a list of
// them — the two shapes an argument names a picture in. Prose is left alone: a
// prompt that happens to mention image#2 is the model describing the picture,
// not addressing it, and rewriting inside a sentence would put a machine id in
// front of a user for no gain.
func FreezeImageRefs(sess *ToolSession, args map[string]any) int {
	if sess == nil || len(args) == 0 {
		return 0
	}
	n := 0
	swap := func(v any) (any, bool) {
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		stable, ok := FrozenImageRef(sess, s)
		if !ok || strings.EqualFold(stable, strings.TrimSpace(s)) {
			return nil, false
		}
		n++
		return stable, true
	}
	for k, v := range args {
		if out, ok := swap(v); ok {
			args[k] = out
			continue
		}
		list, ok := v.([]any)
		if !ok {
			continue
		}
		for i, item := range list {
			if out, ok := swap(item); ok {
				list[i] = out
			}
		}
	}
	return n
}

// ImageOrigin says WHERE a picture came from, which decides whether it can
// serve as a reference. An agent's own output is not evidence of anything: a
// generated subject is invented, so treating it as a reference for that subject
// compounds the invention every time it is reused, and the thing depicted
// drifts away from the real one it was supposed to depict.
//
// Recorded structurally rather than parsed back out of the note, because the
// note that reaches a KEPT image is the agent's own words about why it kept it,
// not the framework's record of where it came from.
type ImageOrigin string

const (
	// ImageFromUser arrived as an attachment. The strongest reference there is:
	// somebody chose this picture and sent it.
	ImageFromUser ImageOrigin = "user"
	// ImageFromFound was searched for or downloaded from a URL. A photograph of
	// a real thing, so it stands as a reference.
	ImageFromFound ImageOrigin = "found"
	// ImageFromGenerated was drawn from a text prompt. Invented, and therefore
	// not a reference for anything real.
	ImageFromGenerated ImageOrigin = "generated"
	// ImageFromEdited is derived output. It carries some of its source, but it
	// is what the agent produced rather than what it was given.
	ImageFromEdited ImageOrigin = "edited"
	// ImageOriginUnknown is an entry written before origins were recorded.
	ImageOriginUnknown ImageOrigin = ""
)

// AgentMade reports whether the deployment POSITIVELY knows this picture is the
// agent's own output. Unknown is deliberately not agent-made: the same
// allowlist direction the workspace reaper uses. A missed classification here
// leaves a stale reference in a list, which is visible and correctable; the
// other direction silently drops a reference somebody deliberately kept, and
// they find out when the agent stops using it and cannot say why.
func (o ImageOrigin) AgentMade() bool {
	return o == ImageFromGenerated || o == ImageFromEdited
}

// originFromNote classifies an entry written before Origin existed, from the
// note the framework itself wrote. Only ring notes are framework-authored, so
// this is never applied to a kept image's note.
func originFromNote(note string) ImageOrigin {
	switch n := strings.ToLower(strings.TrimSpace(note)); {
	case strings.HasPrefix(n, "generated:"):
		return ImageFromGenerated
	case strings.HasPrefix(n, "edited "), strings.HasPrefix(n, "edited:"):
		// Both spellings: the framework writes "edited <refs>: …", but a note
		// with no refs collapses to "edited:" — and an unmatched render falls
		// to unknown, which lists it among the pictures you were GIVEN. Wrong
		// in the direction that matters.
		return ImageFromEdited
	case strings.HasPrefix(n, "received"):
		return ImageFromUser
	case strings.HasPrefix(n, "found:"), strings.HasPrefix(n, "downloaded:"):
		return ImageFromFound
	}
	return ImageOriginUnknown
}

// RecordRecentImage adds an image to the space and prunes it back to the limit,
// returning the ref the caller should show the model ("image#1"). Best-effort:
// an empty ref means the space is unavailable (no session, no username, or an
// unwritable directory) and the caller carries on without it — the space is a
// convenience, never a dependency.
func RecordRecentImage(sess *ToolSession, data []byte, note string, origin ImageOrigin) string {
	return recordRecentImage(sess, data, note, origin, CaptionOnRecord)
}

// recordRecentImage is the implementation. describe=false is for an image the
// TURN is already looking at — see the inbound-attachment call site — where a
// second, concurrent vision call for the same picture buys nothing and costs
// the user's own request its LLM slot.
func recordRecentImage(sess *ToolSession, data []byte, note string, origin ImageOrigin, describe bool) string {
	dir := recentImageDir(sess)
	if dir == "" || len(data) == 0 {
		return ""
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		Debug("[image_space] mkdir %s: %v", dir, err)
		return ""
	}
	// Nanosecond prefix so a lexical sort IS a chronological sort — the whole
	// ordering model, with no index to keep in step with the files.
	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	base := stamp + "-" + UUIDv4()
	if err := os.WriteFile(filepath.Join(dir, base+".png"), data, 0600); err != nil {
		Debug("[image_space] write: %v", err)
		return ""
	}
	meta, _ := json.Marshal(recentImageMeta{Note: strings.TrimSpace(note), Mime: "image/png", Origin: origin})
	if err := os.WriteFile(filepath.Join(dir, base+".json"), meta, 0600); err != nil {
		Debug("[image_space] write meta: %v", err)
	}
	pruneRecentImages(dir)
	// Describe it, in the background. On record rather than only on keep, so
	// the ring's own listing can say what each picture IS rather than only the
	// note it was saved under — "image#3 — six navy bars on a light grid" is
	// the answer to "which one did you mean", and the note alone is not.
	//
	// Never on the caller's path: this is a vision call, and every image
	// operation would otherwise wait seconds for a description nothing has
	// asked for yet. The entry is complete and usable the moment the file
	// lands; the caption catches up.
	if describe {
		captionRunner(func() { captionRecentImage(filepath.Join(dir, base), sess, data) })
	}
	return RecentImageRefPrefix + "1"
}

// RecordRecentImageStable records a picture and returns BOTH references to it:
// the positional one it currently occupies, and the stable id that keeps
// meaning this picture after other pictures are saved.
//
// Two returns rather than replacing the positional one: "it is image#1 right
// now" is still the useful thing to say about a freshly saved picture, and
// several callers print it. What was missing was anything durable to offer
// alongside it.
func RecordRecentImageStable(sess *ToolSession, data []byte, note string, origin ImageOrigin) (positional, stable string) {
	positional = RecordRecentImage(sess, data, note, origin)
	if positional == "" {
		return "", ""
	}
	// The newest entry is the one just written.
	if all := RecentImages(sess); len(all) > 0 {
		stable = all[0].ID
	}
	return positional, stable
}

// CaptionOnRecord gates the describe-on-record pass. On by default; an operator
// paying per vision call on a deployment that never searches its images can
// turn it off and still get captions on keep, where they matter most.
var CaptionOnRecord = true

// captionRunner decides where the vision pass runs. A var so tests can make it
// synchronous — an async caption is otherwise racing every assertion about it.
//
// The recover is not defensive padding: this goroutine has no caller and no
// request handler above it, so a panic anywhere under it — a driver, an image
// codec, a nil LLM behind a non-nil interface — takes down the whole server
// instead of one description.
var captionRunner = func(f func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				Debug("[image_space] caption panicked: %v", r)
			}
		}()
		f()
	}()
}

// captionMu serializes the pass. A fan-out that produces six images at once
// would otherwise put six vision calls in flight against a backend that
// schedules one at a time, and every one of them is ahead of whatever the user
// asks next.
var captionMu sync.Mutex

// captionRecentImage describes one ring entry and merges the result into its
// sidecar. Re-reads the sidecar rather than holding the earlier value, because
// the entry may have been pruned or rewritten while the vision call was out —
// and writes nothing if the image is gone.
func captionRecentImage(base string, sess *ToolSession, data []byte) {
	captionMu.Lock()
	defer captionMu.Unlock()
	// Re-check after the wait: the entry may have been pruned while this call
	// was queued behind another description.
	if _, err := os.Stat(base + ".png"); err != nil {
		return
	}
	caption, description := CaptionImage(sess, data)
	if caption == "" && description == "" {
		return
	}
	if _, err := os.Stat(base + ".png"); err != nil {
		return // pruned while we were describing it
	}
	var meta recentImageMeta
	if raw, err := os.ReadFile(base + ".json"); err == nil {
		_ = json.Unmarshal(raw, &meta)
	}
	meta.Caption, meta.Description = caption, description
	if out, err := json.Marshal(meta); err == nil {
		if err := os.WriteFile(base+".json", out, 0600); err != nil {
			Debug("[image_space] caption write: %v", err)
		}
	}
}

// RecentImages lists the space newest-first. Refs are POSITIONAL — image#1 is
// whatever is newest right now — so they're resolved within a turn, not stored.
func RecentImages(sess *ToolSession) []RecentImage {
	dir := recentImageDir(sess)
	if dir == "" {
		return nil
	}
	files := recentImageFiles(dir)
	out := make([]RecentImage, 0, len(files))
	for i, f := range files {
		abs := filepath.Join(dir, f)
		r := RecentImage{Ref: RecentImageRefPrefix + strconv.Itoa(i+1), path: abs}
		if id := ringIDFromBase(f); id != "" {
			r.ID = RecentImageRefPrefix + id
		}
		if ts, err := strconv.ParseInt(strings.SplitN(f, "-", 2)[0], 10, 64); err == nil {
			r.When = time.Unix(0, ts)
		}
		var meta recentImageMeta
		if raw, err := os.ReadFile(strings.TrimSuffix(abs, ".png") + ".json"); err == nil {
			_ = json.Unmarshal(raw, &meta)
		}
		r.Note, r.Caption, r.Description = meta.Note, meta.Caption, meta.Description
		// An entry written before origins existed has no field; classify it
		// from the note the framework wrote, so a library that predates this
		// still knows which of its pictures the agent made.
		if r.Origin = meta.Origin; r.Origin == ImageOriginUnknown {
			r.Origin = originFromNote(meta.Note)
		}
		out = append(out, r)
	}
	return out
}

// ResolveRecentImage reads the bytes behind an "image#N" reference. Returns
// (nil, false) for any other form, so callers can fall through to their other
// reference types.
func ResolveRecentImage(sess *ToolSession, ref string) ([]byte, bool) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(strings.ToLower(ref), RecentImageRefPrefix) {
		return nil, false
	}
	body := strings.TrimSpace(ref[len(RecentImageRefPrefix):])
	n, err := strconv.Atoi(body)
	if err != nil || n < 1 {
		// A STABLE ring id (image#r.7a3f9c2b) names one picture for its whole
		// life in the ring, unlike the position, which moves every time
		// anything is saved. Checked before kept names because the dot makes
		// the two shapes disjoint — safeKeptName cannot produce one.
		if isRingID(body) {
			return resolveRingID(sess, body)
		}
		// Not a position — it may name a KEPT image (image#brand_mark). One
		// prefix covers both on purpose: to the model they're one idea, and
		// every caller that already resolves image#N gains the durable form
		// here rather than having to learn a second reference type.
		return ResolveKeptImage(sess, ref)
	}
	all := RecentImages(sess)
	if n > len(all) {
		return nil, false
	}
	data, err := os.ReadFile(all[n-1].path)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

// resolveRingID reads the bytes behind a stable ring reference.
func resolveRingID(sess *ToolSession, id string) ([]byte, bool) {
	want := RecentImageRefPrefix + strings.ToLower(strings.TrimSpace(id))
	for _, r := range RecentImages(sess) {
		if strings.EqualFold(r.ID, want) {
			data, err := os.ReadFile(r.path)
			if err != nil || len(data) == 0 {
				return nil, false
			}
			return data, true
		}
	}
	// Pruned out of the ring, or never existed. Callers read false as "no such
	// picture" and say so, rather than silently working on a different one —
	// which is the whole failure this id exists to prevent.
	return nil, false
}

// recentImageFiles returns the ring's .png names newest-first.
func recentImageFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".png") {
			names = append(names, e.Name())
		}
	}
	// Reverse lexical == newest first, given the nanosecond prefix.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names
}

// pruneRecentImages is the garbage collection the model used to be asked to do.
// Two queues, pruned independently: what the agent MADE against
// recentImageLimit, what it was GIVEN or found against sourceImageLimit. Each
// keeps its own newest and drops its own oldest, so a burst of renders expires
// only earlier renders.
//
// Unknown origin counts as a source. Same direction as everywhere else in this
// change: what is not positively recognized as the agent's own output gets the
// treatment that loses nothing.
func pruneRecentImages(dir string) {
	var made, given []string
	for _, name := range recentImageFiles(dir) { // newest first
		if readRecentOrigin(dir, name).AgentMade() {
			made = append(made, name)
			continue
		}
		given = append(given, name)
	}
	dropRecentImages(dir, made, recentImageLimit)
	dropRecentImages(dir, given, sourceImageLimit)
}

// dropRecentImages removes everything past a queue's allowance, image and
// sidecar together. names must be newest-first.
func dropRecentImages(dir string, names []string, limit int) {
	if len(names) <= limit {
		return
	}
	for _, name := range names[limit:] {
		abs := filepath.Join(dir, name)
		if err := os.Remove(abs); err != nil {
			Debug("[image_space] prune %s: %v", abs, err)
		}
		_ = os.Remove(strings.TrimSuffix(abs, ".png") + ".json")
	}
}

// readRecentOrigin reads one entry's provenance, falling back to the note for
// sidecars written before origins existed — so an old ring is partitioned the
// same way a new one is, rather than counting entirely as sources.
func readRecentOrigin(dir, name string) ImageOrigin {
	var meta recentImageMeta
	raw, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(name, ".png")+".json"))
	if err != nil {
		return ImageOriginUnknown
	}
	if json.Unmarshal(raw, &meta) != nil {
		return ImageOriginUnknown
	}
	if meta.Origin != ImageOriginUnknown {
		return meta.Origin
	}
	return originFromNote(meta.Note)
}

// RecentImageManifest renders the space for the model — the answer to "which
// picture do you mean?". Empty string when the space holds nothing, so callers
// can append it unconditionally.
func RecentImageManifest(sess *ToolSession) string {
	all := RecentImages(sess)
	if len(all) == 0 {
		return ""
	}
	// SPLIT BY PROVENANCE, not listed flat.
	//
	// The kept-image manifest has marked the agent's own output for a while;
	// this one did not, and this is where a photo somebody sent lives before
	// anyone keeps it. Listed flat, a render and a real photograph differ only
	// by the prose in their notes — so asked for a reference, the model picked
	// whatever was newest, which after two attempts is always something it made
	// itself. It loses the picture it was given without ever being told the
	// difference.
	var given, made []RecentImage
	for _, r := range all {
		if r.Origin.AgentMade() {
			made = append(made, r)
			continue
		}
		given = append(given, r)
	}
	var b strings.Builder
	// Lead with the STABLE id and show the position as a parenthetical. The
	// model copies what it is shown, and the position is only true until the
	// next save — including the save that a render performs on its own output,
	// which is how a follow-up edit lands on a picture nobody asked about.
	label := func(r RecentImage) string {
		if strings.TrimSpace(r.ID) == "" {
			return r.Ref // pre-dates stable ids, or the name did not parse
		}
		return r.ID + " (now " + r.Ref + ")"
	}
	describe := func(r RecentImage) string {
		desc := r.Note
		switch {
		case desc != "" && r.Caption != "":
			desc += " — " + r.Caption
		case desc == "":
			desc = r.Caption
		}
		if desc == "" {
			desc = "image"
		}
		return desc
	}
	if len(given) > 0 {
		// First, because these are the ones a request for a reference means.
		b.WriteString("Pictures you were GIVEN or found — these are real, and are what a request about \"the photo\" or a real person or thing refers to (newest first):\n")
		for _, r := range given {
			fmt.Fprintf(&b, "- %s — %s\n", label(r), describe(r))
		}
	}
	if len(made) > 0 {
		b.WriteString("Pictures YOU MADE — not evidence of what anything really looks like. Use one only to keep working on that same render, never as the reference for a real subject (newest first):\n")
		for _, r := range made {
			fmt.Fprintf(&b, "- %s — %s\n", label(r), describe(r))
		}
	}
	b.WriteString("Pass an id in the images list of an image call to work from it. PREFER the image#r.… form: it always means that same picture. " +
		"The image#N form is only its position right now — saving any picture, including your own render, makes that one image#1 and pushes the rest down. " +
		"They are kept automatically; you never need to delete them.")
	return b.String()
}
