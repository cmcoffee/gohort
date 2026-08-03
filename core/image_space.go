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
	"sort"
	"strconv"
	"strings"
	"time"
)

// recentImageLimit is how many images the space holds per user. Enough to cover
// "the one before that" in normal conversation; small enough that the pruned
// directory stays trivial to scan.
const recentImageLimit = 10

// RecentImageRefPrefix is the reference form the model uses: image#1 is newest.
const RecentImageRefPrefix = "image#"

// RecentImage is one entry in the space.
type RecentImage struct {
	Ref  string    // "image#1" — position, newest first; NOT stable across writes
	Note string    // what it is ("generated: a cat on a bike", "edited image#2")
	When time.Time // when it entered the space
	path string    // absolute file path
}

// recentImageMeta is the on-disk sidecar. Kept next to the image so the space
// survives a restart with its captions intact and needs no database.
type recentImageMeta struct {
	Note string `json:"note"`
	Mime string `json:"mime"`
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

// RecordRecentImage adds an image to the space and prunes it back to the limit,
// returning the ref the caller should show the model ("image#1"). Best-effort:
// an empty ref means the space is unavailable (no session, no username, or an
// unwritable directory) and the caller carries on without it — the space is a
// convenience, never a dependency.
func RecordRecentImage(sess *ToolSession, data []byte, note string) string {
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
	meta, _ := json.Marshal(recentImageMeta{Note: strings.TrimSpace(note), Mime: "image/png"})
	if err := os.WriteFile(filepath.Join(dir, base+".json"), meta, 0600); err != nil {
		Debug("[image_space] write meta: %v", err)
	}
	pruneRecentImages(dir)
	return RecentImageRefPrefix + "1"
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
		if ts, err := strconv.ParseInt(strings.SplitN(f, "-", 2)[0], 10, 64); err == nil {
			r.When = time.Unix(0, ts)
		}
		var meta recentImageMeta
		if raw, err := os.ReadFile(strings.TrimSuffix(abs, ".png") + ".json"); err == nil {
			_ = json.Unmarshal(raw, &meta)
		}
		r.Note = meta.Note
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
	n, err := strconv.Atoi(strings.TrimSpace(ref[len(RecentImageRefPrefix):]))
	if err != nil || n < 1 {
		return nil, false
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

// pruneRecentImages is the garbage collection the model used to be asked to do:
// everything past the limit goes, image and sidecar together.
func pruneRecentImages(dir string) {
	names := recentImageFiles(dir)
	if len(names) <= recentImageLimit {
		return
	}
	for _, name := range names[recentImageLimit:] {
		abs := filepath.Join(dir, name)
		if err := os.Remove(abs); err != nil {
			Debug("[image_space] prune %s: %v", abs, err)
		}
		_ = os.Remove(strings.TrimSuffix(abs, ".png") + ".json")
	}
}

// RecentImageManifest renders the space for the model — the answer to "which
// picture do you mean?". Empty string when the space holds nothing, so callers
// can append it unconditionally.
func RecentImageManifest(sess *ToolSession) string {
	all := RecentImages(sess)
	if len(all) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Recent images you can reference by id (newest first):\n")
	for _, r := range all {
		note := r.Note
		if note == "" {
			note = "image"
		}
		fmt.Fprintf(&b, "- %s — %s\n", r.Ref, note)
	}
	b.WriteString("Pass one of these ids to image(action=\"edit\", images=[...]) to change it. They are kept automatically; you never need to delete them.")
	return b.String()
}
