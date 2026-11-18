// Package apptools is an app's own tool space (docs/design/app-history.md,
// stage 2, piece B).
//
// An app's tools are kept apart from the person's pool, in a store of their
// own keyed by the owner and the app, so an app's get_weather and the pool's
// are two tools that never collide, and nothing that reads the pool (the
// Tools pages, the shared catalog, adoption) ever sees one. Nothing stored
// moves: a space starts empty. Only Builder and Anvil change it (appEdit, as
// for an app's agents), and each change is the app's change, recorded in its
// history (core.AppOwnedChanged).
//
// A tool that reaches no further than gohort's own tools (sandboxed, the
// public web through gohort) loads as soon as it is saved: whoever built or
// installed the app chose it. One that reaches further (Reach: a credential,
// raw network) loads only once an administrator approved it as it is now;
// the approval is per space, one request in the approvals queue listing each
// such tool, and is asked again only for a tool that changed. An
// administrator may also stop one tool; both are kept on the space's review
// record, never on the tool, which only Builder and Anvil write.
package apptools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
)

const (
	toolsTable   = "app_tools"
	reviewsTable = "app_tool_reviews"
)

var mu sync.Mutex

// Tool is one tool of an app's space.
type Tool struct {
	Tool    core.TempTool `json:"tool"`
	SavedAt time.Time     `json:"saved_at"`
	// SavedBy is what last changed it: "Builder", "Anvil" or "import".
	SavedBy string `json:"saved_by,omitempty"`
	// CopiedFrom is the person's tool this one was copied from (CopyIn);
	// nil for one made for the app.
	CopiedFrom *core.CopiedFrom `json:"copied_from,omitempty"`
}

// Review is what administrators decided about an app's space.
type Review struct {
	// Approved maps a tool's name to the fingerprint (Fingerprint) of
	// the definition an administrator approved, and By/At say who and when.
	Approved map[string]string `json:"approved,omitempty"`
	By       string            `json:"by,omitempty"`
	At       time.Time         `json:"at,omitempty"`
	// Stopped maps a tool's name to the administrator who switched it off.
	Stopped map[string]string `json:"stopped,omitempty"`
	// Requested is what the open request in the approvals queue asks for,
	// each tool by the fingerprint it had when the request was made: the
	// administrator approves what the request showed them, and a tool
	// changed since asks again.
	Requested map[string]string `json:"requested,omitempty"`
}

// RequestKind is the approvals-queue kind of a review of an app's
// tool space; its name is the app's slug.
const RequestKind = "app_tools"

func init() {
	promotion.RegisterApprover(RequestKind, func(owner, slug string) error {
		r := LoadReview(owner, slug)
		if len(r.Requested) == 0 {
			return errors.New("nothing of this app's tools waits for approval")
		}
		Approve(owner, slug, "an administrator", r.Requested)
		return nil
	})
}

// askReview puts the review of owner's app slug's space in the
// approvals queue, listing each tool that waits and what it reaches, or
// takes it out when none waits.
func askReview(owner, slug string) {
	if core.AuthDB == nil || core.AuthDB() == nil {
		return
	}
	waiting := AwaitingReview(owner, slug)
	if len(waiting) == 0 {
		promotion.WithdrawPromotionRequest(core.AuthDB(), owner, RequestKind, slug)
		return
	}
	asked := map[string]string{}
	lines := make([]string, 0, len(waiting))
	for _, at := range waiting {
		asked[at.Tool.Name] = Fingerprint(at.Tool)
		lines = append(lines, at.Tool.Name+" reaches "+strings.Join(Reach(at.Tool), " and "))
	}
	if db := core.RootDB; db != nil {
		mu.Lock()
		var r Review
		db.Get(reviewsTable, key(owner, slug), &r)
		r.Requested = asked
		db.Set(reviewsTable, key(owner, slug), r)
		mu.Unlock()
	}
	note := "The app's own tools reach further than gohort's own tools: " + strings.Join(lines, "; ") +
		". Approving lets the app's agents use them as they are now; a change to one asks again."
	if err := core.CreatePromotionRequest(core.AuthDB(), owner, RequestKind, slug, note); err != nil {
		core.Log("[app-tools] %s/%s: the review could not be asked for: %v", owner, slug, err)
	}
}

func key(owner, slug string) string { return owner + "/" + slug }

// Load is owner's app slug's tool space, by name.
func Load(owner, slug string) []Tool {
	db := core.RootDB
	if db == nil || owner == "" || slug == "" {
		return nil
	}
	var out []Tool
	db.Get(toolsTable, key(owner, slug), &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Tool.Name < out[j].Tool.Name })
	return out
}

// ByName is the tool name of owner's app slug's space.
func ByName(owner, slug, name string) (Tool, bool) {
	for _, t := range Load(owner, slug) {
		if t.Tool.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// refusal is why a save by appEdit may not change owner's app slug's
// tools: only Builder and Anvil change them, and an import brings them. As
// for an app's agents and pipelines, appEdit is said only by their paths
// (what made the change: "Builder", "Anvil", "import"), so a save that says
// nothing is anything else's, and refused.
func refusal(owner, slug, appEdit string) error {
	if strings.TrimSpace(appEdit) != "" {
		return nil
	}
	name := slug
	if spec, ok := core.LoadAppSpec(owner, slug); ok && spec.Name != "" {
		name = spec.Name
	}
	return fmt.Errorf("this tool is part of the app %q: change it with Builder or Anvil", name)
}

// SaveBy saves t into owner's app slug's space, replacing the tool of
// its name, as appEdit's change. A save that changes nothing is not one.
func SaveBy(owner, slug string, t core.TempTool, appEdit string) error {
	if err := refusal(owner, slug, appEdit); err != nil {
		return err
	}
	t.Name = strings.TrimSpace(t.Name)
	if owner == "" || slug == "" || t.Name == "" {
		return errors.New("an app's tool needs its owner, its app and a name")
	}
	db := core.RootDB
	if db == nil {
		return errors.New("no tool store")
	}
	// Never stored (grants and an admin's mapping live with whoever made
	// them), and Trial means nothing in an app: the app chose it.
	t.GrantRead, t.GrantWrite, t.AdminMapped = nil, nil, false
	t.Trial, t.TrialSince = false, time.Time{}
	mu.Lock()
	var all []Tool
	db.Get(toolsTable, key(owner, slug), &all)
	at := -1
	for i, cur := range all {
		if cur.Tool.Name == t.Name {
			at = i
		}
	}
	if at >= 0 && storedJSON(all[at].Tool) == storedJSON(t) {
		mu.Unlock()
		return nil
	}
	rec := Tool{Tool: t, SavedAt: time.Now(), SavedBy: appEdit}
	if at >= 0 {
		all[at] = rec
	} else {
		all = append(all, rec)
	}
	db.Set(toolsTable, key(owner, slug), all)
	mu.Unlock()
	askReview(owner, slug)
	core.AppOwnedChanged(owner, slug, "tool", t.Name, appEdit)
	return nil
}

// DeleteBy removes the tool name from owner's app slug's space, as
// appEdit's change.
func DeleteBy(owner, slug, name, appEdit string) error {
	if err := refusal(owner, slug, appEdit); err != nil {
		return err
	}
	db := core.RootDB
	if db == nil {
		return errors.New("no tool store")
	}
	mu.Lock()
	var all []Tool
	db.Get(toolsTable, key(owner, slug), &all)
	kept := all[:0]
	for _, t := range all {
		if t.Tool.Name != name {
			kept = append(kept, t)
		}
	}
	if len(kept) == len(all) {
		mu.Unlock()
		return fmt.Errorf("the app has no tool %q", name)
	}
	db.Set(toolsTable, key(owner, slug), kept)
	mu.Unlock()
	askReview(owner, slug)
	core.AppOwnedChanged(owner, slug, "tool", name, appEdit)
	return nil
}

// DeleteAll removes owner's app slug's whole space and its review, for
// the app's deletion, and says which tools went.
func DeleteAll(owner, slug string) []string {
	db := core.RootDB
	if db == nil || owner == "" || slug == "" {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	var all []Tool
	db.Get(toolsTable, key(owner, slug), &all)
	db.Unset(toolsTable, key(owner, slug))
	db.Unset(reviewsTable, key(owner, slug))
	if core.AuthDB != nil && core.AuthDB() != nil {
		promotion.WithdrawPromotionRequest(core.AuthDB(), owner, RequestKind, slug)
	}
	names := make([]string, 0, len(all))
	for _, t := range all {
		names = append(names, t.Tool.Name)
	}
	sort.Strings(names)
	return names
}

// storedJSON is t as stored, to tell a save that changes nothing.
func storedJSON(t core.TempTool) string {
	b, _ := json.Marshal(t)
	return string(b)
}

// Fingerprint names what t does: two definitions with one fingerprint
// reach the same, so an approval of one holds for the other. The flags an
// owner sets on a tool (confirm in chat, never unattended) narrow it and do
// not count.
func Fingerprint(t core.TempTool) string {
	sum := sha256.Sum256([]byte(storedJSON(t.Definition())))
	return hex.EncodeToString(sum[:8])
}

// Reach says what t reaches beyond what gohort's own tools do, one phrase
// each ("the credential \"crm\"", "raw network"), nil for none: such a tool
// in an app's space waits for an administrator.
func Reach(t core.TempTool) []string {
	var out []string
	seen := map[string]bool{}
	cred := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, fmt.Sprintf("the credential %q", name))
		}
	}
	cred(t.Credential)
	for _, c := range t.HookCapabilities {
		if i := strings.IndexByte(c, ':'); i > 0 {
			switch c[:i] {
			case "secret", "fetch_via":
				cred(c[i+1:])
			}
		}
	}
	if t.RawNetwork {
		out = append(out, "raw network")
	}
	return out
}

// LoadReview is what administrators decided about owner's app slug's
// space.
func LoadReview(owner, slug string) Review {
	var r Review
	if db := core.RootDB; db != nil && owner != "" && slug != "" {
		db.Get(reviewsTable, key(owner, slug), &r)
	}
	return r
}

// Held says why the tool t of owner's app slug's space does not load,
// "" when it does: an administrator stopped it, or it reaches further than
// gohort's own tools and is not approved as it is now.
func Held(owner, slug string, t core.TempTool, r Review) string {
	if by := r.Stopped[t.Name]; by != "" {
		return "switched off by an administrator"
	}
	if len(Reach(t)) > 0 && r.Approved[t.Name] != Fingerprint(t) {
		return "waiting for an administrator to approve what it reaches (" + strings.Join(Reach(t), ", ") + ")"
	}
	return ""
}

// AwaitingReview is the tools of owner's app slug's space that
// reach further than gohort's own and are not approved as they are now.
func AwaitingReview(owner, slug string) []Tool {
	r := LoadReview(owner, slug)
	var out []Tool
	for _, t := range Load(owner, slug) {
		if len(Reach(t.Tool)) > 0 && r.Approved[t.Tool.Name] != Fingerprint(t.Tool) {
			out = append(out, t)
		}
	}
	return out
}

// Spaces lists every app space that has tools, as owner/slug pairs.
func Spaces() [][2]string {
	db := core.RootDB
	if db == nil {
		return nil
	}
	var out [][2]string
	for _, k := range db.Keys(toolsTable) {
		if i := strings.IndexByte(k, '/'); i > 0 {
			out = append(out, [2]string{k[:i], k[i+1:]})
		}
	}
	return out
}

// Approve records admin's approval of the tools of owner's app
// slug's space that the review showed them, each by the fingerprint they
// saw: a tool changed since is not approved, and waits for the next review.
// It says how many were approved.
func Approve(owner, slug, admin string, seen map[string]string) int {
	db := core.RootDB
	if db == nil {
		return 0
	}
	mu.Lock()
	defer mu.Unlock()
	var r Review
	db.Get(reviewsTable, key(owner, slug), &r)
	if r.Approved == nil {
		r.Approved = map[string]string{}
	}
	var all []Tool
	db.Get(toolsTable, key(owner, slug), &all)
	n := 0
	for _, t := range all {
		if fp := Fingerprint(t.Tool); seen[t.Tool.Name] == fp {
			r.Approved[t.Tool.Name] = fp
			n++
		}
	}
	if n > 0 {
		r.By, r.At = admin, time.Now()
		db.Set(reviewsTable, key(owner, slug), r)
	}
	return n
}

// Stop switches the tool name of owner's app slug's space off (stop)
// or back on, as admin's decision. An administrator's stop, not a change to
// the app: the tool stays as Builder or Anvil left it.
func Stop(owner, slug, name, admin string, stop bool) {
	db := core.RootDB
	if db == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	var r Review
	db.Get(reviewsTable, key(owner, slug), &r)
	if r.Stopped == nil {
		r.Stopped = map[string]string{}
	}
	if stop {
		r.Stopped[name] = admin
	} else {
		delete(r.Stopped, name)
	}
	db.Set(reviewsTable, key(owner, slug), r)
}

// CopyIn copies the person's tool t into owner's app slug's space, as
// appEdit's change, recording what it came from. A copy made within the
// deployment keeps its approval while it is unchanged: one that reaches
// further than gohort's own tools is approved as it is, and asks an
// administrator again once it is edited.
func CopyIn(owner, slug string, t core.TempTool, from core.CopiedFrom, appEdit string) error {
	if err := SaveBy(owner, slug, t, appEdit); err != nil {
		return err
	}
	db := core.RootDB
	if db == nil {
		return errors.New("no tool store")
	}
	mu.Lock()
	var all []Tool
	db.Get(toolsTable, key(owner, slug), &all)
	var saved core.TempTool
	for i := range all {
		if all[i].Tool.Name == t.Name {
			f := from
			all[i].CopiedFrom = &f
			saved = all[i].Tool
		}
	}
	db.Set(toolsTable, key(owner, slug), all)
	if len(Reach(saved)) > 0 {
		var r Review
		db.Get(reviewsTable, key(owner, slug), &r)
		if r.Approved == nil {
			r.Approved = map[string]string{}
		}
		r.Approved[saved.Name] = Fingerprint(saved)
		db.Set(reviewsTable, key(owner, slug), r)
	}
	mu.Unlock()
	askReview(owner, slug)
	return nil
}
