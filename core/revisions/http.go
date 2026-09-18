package revisions

// The HTTP half: one surface, four kinds.
//
// The list, the read-only preview and the restore are the same three responses
// whatever is being versioned — only the store, the key, what "restore" does,
// and the noun in the empty state change. Those are the fields of Surface;
// everything else is here once. Copying this per kind is how four surfaces
// drift into four slightly different answers to the same question.
//
// The payload is core/ui FormPanel.HistoryURL's contract. That component knows
// nothing about versions: it renders entries and follows the urls they carry,
// so the shape below IS the coupling between the two, and it lives on this side
// because this side is the one with something to say.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Surface serves one definition's revisions. The caller has already decided
// who is asking and whether they may: this renders and applies, it does not
// authorize.
type Surface struct {
	// Store and Kind/Key name the ring. Key carries the owner wherever the
	// store is shared between users (see ringKey).
	Store Store
	Kind  string
	Key   string

	// Noun names the thing in the sentences a person reads ("agent",
	// "machine"). Lowercase; it appears mid-sentence.
	Noun string

	// Current is the live record, marshaled to compare a kept version against.
	// Nil is allowed and simply means a preview cannot say what moved.
	Current any

	// Ignore lists JSON fields to leave out of "what changed", for values that
	// move on every save (a timestamp) or that hold a copy of another version
	// (a one-deep undo snapshot).
	Ignore []string

	// Restore applies the named revision. Nil means this surface is read-only
	// and a restore is refused as not allowed.
	Restore func(ref string) error

	// LockedReason, when non-empty, refuses a restore and says why. Reading
	// history stays open: a lock is about changing something, not about
	// knowing what it used to say.
	LockedReason string
}

// Serve answers one of the three routes. action is "" for the list, "preview"
// or "restore"; anything else is a 404. Restore requires POST.
func (s Surface) Serve(w http.ResponseWriter, r *http.Request, action string) {
	switch action {
	case "":
		s.serveList(w)
	case "preview":
		s.servePreview(w, r)
	case "restore":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.serveRestore(w, r.URL.Query().Get("rev"))
	default:
		http.NotFound(w, r)
	}
}

type surfaceAction struct {
	Label   string `json:"label"`
	URL     string `json:"url"`
	Method  string `json:"method,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Confirm string `json:"confirm,omitempty"`
	Variant string `json:"variant,omitempty"`
	Done    string `json:"done,omitempty"`
}

type surfaceEntry struct {
	Title   string          `json:"title"`
	Detail  string          `json:"detail,omitempty"`
	Actions []surfaceAction `json:"actions,omitempty"`
}

func (s Surface) serveList(w http.ResponseWriter) {
	noun := s.noun()
	out := struct {
		Title    string         `json:"title"`
		Subtitle string         `json:"subtitle,omitempty"`
		Empty    string         `json:"empty,omitempty"`
		Entries  []surfaceEntry `json:"entries"`
	}{
		Title: "Kept versions",
		// Says the depth out loud. A ring is only reassuring if you know how
		// far back it goes, and the alternative is somebody assuming an edit
		// from last month is still in here.
		Subtitle: fmt.Sprintf("The last %d edits are kept. Older ones are gone.", Kept),
		Empty:    "No versions kept yet — they start once this " + noun + " is edited.",
		Entries:  []surfaceEntry{},
	}
	for _, rev := range List(s.Store, s.Kind, s.Key) {
		title := fmt.Sprintf("#%d", rev.Seq)
		if age := Age(rev.Stamp); age != "" {
			title += " · " + age
		}
		// Urls are RELATIVE to the history url, which the panel resolves them
		// against. The page rendering this decides its own depth and reaches
		// the api through its own base, so an absolute path here would have to
		// guess it, and a sibling name cannot guess wrong.
		actions := []surfaceAction{
			{Label: "Preview", URL: fmt.Sprintf("revisions/preview?rev=%d", rev.Seq), Kind: "show"},
		}
		if s.Restore != nil {
			actions = append(actions, surfaceAction{
				Label:   "Restore",
				URL:     fmt.Sprintf("revisions/restore?rev=%d", rev.Seq),
				Method:  "post",
				Variant: "danger",
				Confirm: fmt.Sprintf("Restore version #%d? The current version is kept too, so this is reversible.", rev.Seq),
				Done:    "Restored.",
			})
		}
		out.Entries = append(out.Entries, surfaceEntry{
			Title:   title,
			Detail:  rev.Reason,
			Actions: actions,
		})
	}
	writeJSON(w, out)
}

func (s Surface) servePreview(w http.ResponseWriter, r *http.Request) {
	rev, ok := Find(s.Store, s.Kind, s.Key, r.URL.Query().Get("rev"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	title := fmt.Sprintf("Version #%d", rev.Seq)
	if age := Age(rev.Stamp); age != "" {
		title += " · kept " + age
	}
	writeJSON(w, map[string]string{
		"title": title,
		"text":  s.describe(rev),
	})
}

func (s Surface) serveRestore(w http.ResponseWriter, ref string) {
	if s.Restore == nil {
		http.Error(w, "this "+s.noun()+" cannot be restored from here", http.StatusForbidden)
		return
	}
	if s.LockedReason != "" {
		http.Error(w, s.LockedReason, http.StatusForbidden)
		return
	}
	if _, ok := Find(s.Store, s.Kind, s.Key, ref); !ok {
		http.Error(w, "no such kept version", http.StatusBadRequest)
		return
	}
	if err := s.Restore(ref); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// describe renders a kept version for reading: what has moved since, then the
// kept value of each field that moved.
//
// Only the changed fields, and only their KEPT side. A definition is dozens of
// fields and printing all of them buries the three that matter; printing both
// sides doubles that to say the same thing twice, when the other side is the
// form on screen behind the modal.
func (s Surface) describe(rev Revision) string {
	var b strings.Builder
	if rev.Reason != "" {
		b.WriteString("Replaced by: " + rev.Reason + "\n\n")
	}
	kept, keptOK := blobAsMap(rev.Body, s.Ignore)
	if !keptOK {
		return b.String() + "This version could not be read."
	}
	current, currentOK := valueAsMap(s.Current, s.Ignore)
	if !currentOK {
		// No current side to compare against: show the version whole rather
		// than nothing, since the fields are still what somebody came for.
		return b.String() + dumpFields(kept, sortedKeys(kept))
	}
	changed := changedKeys(kept, current)
	if len(changed) == 0 {
		return b.String() + "Nothing differs between this version and the current one."
	}
	b.WriteString("Changed since this version: " + strings.Join(changed, ", ") + "\n")
	b.WriteString(dumpFields(kept, changed))
	return b.String()
}

func dumpFields(m map[string]any, fields []string) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString("\n--- " + f + " (this version) ---\n")
		b.WriteString(fieldText(m, f) + "\n")
	}
	return b.String()
}

// fieldText renders one field. A string prints as itself, because the field
// somebody opens this for is usually a prompt, and JSON-quoting a page of
// prose makes it unreadable.
func fieldText(m map[string]any, field string) string {
	v, present := m[field]
	if !present || v == nil {
		return "(not set)"
	}
	if str, isStr := v.(string); isStr {
		if strings.TrimSpace(str) == "" {
			return "(empty)"
		}
		return str
	}
	blob, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(blob)
}

// changedKeys names the fields whose values differ, sorted so the same pair of
// records always reads the same way.
func changedKeys(a, b map[string]any) []string {
	seen := map[string]bool{}
	var out []string
	for k := range a {
		seen[k] = true
		if !sameJSON(a[k], b[k]) {
			out = append(out, k)
		}
	}
	for k := range b {
		if !seen[k] && !sameJSON(a[k], b[k]) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sameJSON compares two decoded values by marshaling each, so map key order
// cannot read as a difference.
func sameJSON(a, b any) bool {
	ab, aerr := json.Marshal(a)
	bb, berr := json.Marshal(b)
	if aerr != nil || berr != nil {
		return false
	}
	return string(ab) == string(bb)
}

func blobAsMap(blob []byte, ignore []string) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal(blob, &m); err != nil {
		return nil, false
	}
	dropKeys(m, ignore)
	return m, true
}

func valueAsMap(v any, ignore []string) (map[string]any, bool) {
	if v == nil {
		return nil, false
	}
	blob, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	return blobAsMap(blob, ignore)
}

func dropKeys(m map[string]any, ignore []string) {
	// Always dropped: it moves on every save and would report a change on
	// every comparison while saying nothing.
	delete(m, "updated")
	for _, k := range ignore {
		delete(m, k)
	}
}

func (s Surface) noun() string {
	if n := strings.TrimSpace(s.Noun); n != "" {
		return n
	}
	return "record"
}

// Age renders how long ago a version was superseded, for a listing read at a
// glance. Empty for a stamp that cannot be parsed.
func Age(stamp string) string {
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	default:
		return plural(int(d.Hours()/24), "day")
	}
}

func plural(n int, unit string) string {
	s := fmt.Sprintf("%d %s", n, unit)
	if n != 1 {
		s += "s"
	}
	return s + " ago"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
