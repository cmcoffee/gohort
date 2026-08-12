// toolset.go — the Type=="toolset" backend: an appliance whose target is a
// curated set of already-authored tools rather than a host or a clone.
//
// The instinct is that letting a servitor investigation call a network service
// loosens it. The opposite is true, and appliance_tool.go already argues it
// about run_command: the model composes a shell string, so the injection surface
// is whatever it writes, the risk classifier runs against text that did not
// exist a second ago, and the permission an owner grants is a whole risk
// category — an open set of commands nobody has read.
//
// A curated toolset inverts all three. The action space is finite and
// enumerable. Nothing has to classify a string that did not exist a second ago,
// because there is no string. And the grant is per tool, so an owner approves
// things a person has read.
//
// WHAT THE BINDING PROMISES, AND WHAT IT HAS TO PIN. An approval that names a
// tool is a promise about a NAME, and a Builder-authored tool's body can be
// edited afterwards — the approval would then carry an implementation nobody
// approved. That is the same laundering shape appliance_tool.go refuses for
// minted commands (same name, new template, old approval). So a binding stores a
// fingerprint of the tool AS APPROVED and is checked on every resolve. A tool
// that no longer matches is WITHHELD, not warned about: a tool that changed
// since approval is a tool nobody approved, and running it behind a warning is
// the same as running it.
//
// See docs/servitor-toolset-type.md.
package servitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// Postures a binding may carry. There is deliberately no "deny": an unwanted
// tool is UNBOUND, not bound-and-refused. A tool the model can see and cannot
// use produces a probe that plans around it and then reports being blocked.
const (
	PostureAllow = "allow" // runs without asking
	PostureAsk   = "ask"   // prompts the operator per call
)

// ToolBinding is one tool this appliance's investigations may call.
type ToolBinding struct {
	Name    string `json:"name"`
	Posture string `json:"posture,omitempty"`
	// BodyHash fingerprints the tool as it stood when it was bound. Checked at
	// every resolve; a mismatch withholds the tool.
	BodyHash string `json:"body_hash,omitempty"`
	BoundAt  string `json:"bound_at,omitempty"`
	BoundBy  string `json:"bound_by,omitempty"`
	// Snapshot marks the one binding whose zero-argument result orients the
	// investigator before it probes (the toolset analogue of runQuickSnapshot).
	// Owner-set because servitor cannot know which tool is the cheap overview,
	// and running every zero-argument tool to find out would fire writes on any
	// toolset that contains one.
	Snapshot bool `json:"snapshot,omitempty"`
}

// normalizePosture defaults an unset or unrecognized posture to "ask". The
// default is the CAUTIOUS one: a binding written by hand, or by an older
// version of this code, should prompt rather than silently run.
func normalizePosture(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), PostureAllow) {
		return PostureAllow
	}
	return PostureAsk
}

// toolBodyHash fingerprints everything that determines what a tool DOES.
//
// The description is included deliberately. It is what the model acts on, so
// rewriting "list open merge requests" into "list and close merge requests"
// changes behavior as surely as rewriting the URL, and an approval that
// survived that edit would be worthless.
func toolBodyHash(t TempTool) string {
	h := sha256.New()
	write := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
	}
	writeParams := func(label string, params map[string]ToolParam, required []string) {
		// Stable order — a map walk would make the hash differ between two
		// runs over the same tool and re-prompt on every resolve.
		names := make([]string, 0, len(params))
		for name := range params {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			p := params[name]
			write(label+".param", name, p.Type, p.Description, strings.Join(p.Enum, "\x1f"))
		}
		req := append([]string(nil), required...)
		sort.Strings(req)
		write(label+".required", strings.Join(req, "\x1f"))
	}

	write("name", t.Name)
	write("desc", t.Description)
	write("mode", t.Mode)
	write("cred", t.Credential)
	write("cmd", t.CommandTemplate)
	write("method", t.Method)
	write("body", t.BodyTemplate)
	write("ctype", t.ContentType)
	write("upload", t.UploadParam, t.UploadFormField)
	write("pipe", t.ResponsePipe)
	writeHeaders(write, "hdr", t.Headers)
	writeParams("tool", t.Params, t.Required)

	// A toolbox's actions ARE its behavior; hashing only the parent would let
	// an action's URL change under an approved binding.
	for _, a := range t.Actions {
		write("action", a.Name, a.Description, a.URLTemplate, a.Method, a.BodyTemplate, a.ContentType, a.ResponsePipe)
		writeHeaders(write, "action.hdr", a.Headers)
		writeParams("action."+a.Name, a.Params, a.Required)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// writeHeaders folds a header map into the fingerprint in key order.
func writeHeaders(write func(...string), label string, h map[string]string) {
	if len(h) == 0 {
		return
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		write(label, k, h[k])
	}
}

// authDBOrNil returns the auth database, or nil when it is not wired — early
// startup, a deployment assembled without it, and every unit test. AuthDB is a
// FUNC VAR, so calling it unguarded panics rather than returning nil, which is
// the trap this exists to close.
func authDBOrNil() Database {
	if AuthDB == nil {
		return nil
	}
	return AuthDB()
}

// bindableTools returns the tools a user may bind to an appliance: their
// user-wide pool. Agent-scoped rows are excluded — those belong to a specific
// agent's kit, and binding one to a shared appliance would hand it to every
// investigation of that target.
func bindableTools(user string) []TempTool {
	// No auth database means we cannot read the tool pool, so we cannot verify
	// what any binding refers to. Returning nothing makes the caller withhold
	// every binding and say why, which is the right answer to "we cannot check".
	db := authDBOrNil()
	if db == nil {
		return nil
	}
	var out []TempTool
	seen := map[string]bool{}
	for _, p := range SharedUserTools(db, user) {
		if p.Tool.Name == "" || seen[p.Tool.Name] {
			continue
		}
		seen[p.Tool.Name] = true
		out = append(out, p.Tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// findBindableTool resolves one of a user's pool tools by name.
func findBindableTool(user, name string) (TempTool, bool) {
	for _, t := range bindableTools(user) {
		if t.Name == name {
			return t, true
		}
	}
	return TempTool{}, false
}

// resolvedToolset is the outcome of resolving an appliance's bindings: the
// tools an investigation may actually use, and — just as important — the ones it
// may not, with the reason.
type resolvedToolset struct {
	Defs []AgentToolDef
	// Withheld names tools that were bound but are not being handed over, with
	// why. Surfaced into the session rather than logged: an investigation that
	// quietly got quieter is indistinguishable from a target with less to say.
	Withheld []string
	// Snapshot is the binding marked as the orientation pass, if any.
	Snapshot string
}

// resolveToolset loads an appliance's bound tools in the OWNER's context and
// renders them as agent tools.
//
// Owner context, not caller context: a shared appliance runs on the owner's
// credentials and store everywhere else in servitor, and a bound tool is part of
// the target's definition rather than something each user brings with them.
func resolveToolset(ctx context.Context, owner string, a Appliance) resolvedToolset {
	var out resolvedToolset
	if len(a.Toolset) == 0 {
		return out
	}
	// One session carries the bound tools into temptool's builder, which is the
	// same path orchestrate uses — so a bound tool behaves at call time exactly
	// as it does when an agent calls it directly, rather than through a second
	// implementation that drifts.
	// DB is REQUIRED, not optional decoration: an api-mode tool resolves its
	// credential through the session's database and refuses outright without
	// one ("api tool %q requires a session with DB access",
	// dispatchAPIModeTempTool). A session missing it produces a bound tool that
	// looks correctly configured everywhere — bound, fingerprint matching,
	// listed in the prompt — and fails on every call.
	//
	// Network is left nil, which means ALLOWED (see ToolSession.NetworkAllowed).
	// Servitor's private posture pins the route stage to the worker MODEL tier;
	// it does not block outbound calls, and an api-mode tool bound to this
	// appliance is an outbound call the owner explicitly sanctioned.
	sess := &ToolSession{Username: owner, DB: authDBOrNil(), Ctx: ctx}
	// A bound tool must behave the same here as it does in the agent that
	// authored it. An agent's session carries a workspace, so a tool that
	// downloads a file, uses save_to, or takes an upload parameter works there
	// and fails here with a message about a missing workspace — the same class
	// of "looks configured, fails at call time" gap the missing DB handle was.
	// Same directory the user's agents write to; servitor gains no path of its
	// own, it just stops being the odd caller out.
	if ws, err := EnsureWorkspaceDir(owner); err == nil {
		sess.WorkspaceDir = ws
	}
	posture := map[string]string{}

	for _, b := range a.Toolset {
		name := strings.TrimSpace(b.Name)
		if name == "" {
			continue
		}
		if normalizePosture(b.Posture) == PostureAsk && drillIsReadOnly(ctx) {
			// A workspace drill answers every confirmation with a denial (see
			// memberInvestigation), so an "ask" tool here would be offered,
			// planned around, called, and refused. Withholding it up front
			// costs the same coverage and tells the worker the truth before it
			// builds a plan on a tool it cannot use.
			out.Withheld = append(out.Withheld,
				fmt.Sprintf("%s (needs per-call approval, which a workspace investigation cannot ask for — open this system directly to use it)", name))
			continue
		}
		t, ok := findBindableTool(owner, name)
		if !ok {
			out.Withheld = append(out.Withheld,
				fmt.Sprintf("%s (no longer in the owner's tool pool)", name))
			continue
		}
		if b.BodyHash != "" && toolBodyHash(t) != b.BodyHash {
			out.Withheld = append(out.Withheld,
				fmt.Sprintf("%s (changed since it was approved for this system — re-approve it in the appliance's Tools list)", name))
			continue
		}
		if b.BodyHash == "" {
			// A binding with no fingerprint cannot be verified, so it is not
			// honored. This is only reachable for a record written by hand or
			// by a future path that forgets to pin; treating it as trusted
			// would make the pin optional, which is the same as not having it.
			out.Withheld = append(out.Withheld,
				fmt.Sprintf("%s (bound without a fingerprint — re-bind it)", name))
			continue
		}
		copyTool := t
		sess.TempTools = append(sess.TempTools, &copyTool)
		posture[name] = normalizePosture(b.Posture)
		if b.Snapshot {
			out.Snapshot = name
		}
	}

	for _, def := range temptool.BuildAgentToolDefs(sess) {
		p, bound := posture[def.Tool.Name]
		if !bound {
			// An expanded toolbox yields <toolbox>_<action> defs whose names
			// are not the bound name; inherit the parent's posture by prefix
			// rather than dropping them, which would silently break a bound
			// toolbox into nothing.
			p = posturePrefixMatch(posture, def.Tool.Name)
		}
		def.NeedsConfirm = p != PostureAllow
		out.Defs = append(out.Defs, def)
	}
	return out
}

// posturePrefixMatch finds the posture of the bound tool a derived def belongs
// to. Longest prefix wins, so "gitlab_issues_close" prefers a binding on
// "gitlab_issues" over one on "gitlab".
func posturePrefixMatch(posture map[string]string, derived string) string {
	best, bestLen := PostureAsk, -1
	for name, p := range posture {
		if strings.HasPrefix(derived, name+"_") && len(name) > bestLen {
			best, bestLen = p, len(name)
		}
	}
	return best
}

// toolsetBindingNames lists an appliance's bound tool names, for the allow-list
// check. Returns the DECLARED set, not the resolved one: the guard's question is
// "was this tool sanctioned for this target", and a withheld tool never reaches
// it anyway.
func toolsetBindingNames(a Appliance) map[string]bool {
	out := map[string]bool{}
	for _, b := range a.Toolset {
		if n := strings.TrimSpace(b.Name); n != "" {
			out[n] = true
		}
	}
	return out
}

// bindToolsetTools stamps fingerprints on a submitted binding list, dropping
// entries that name nothing the owner has. Called on the appliance save path so
// a binding is never stored without the pin that makes it verifiable.
func bindToolsetTools(owner, boundBy string, in []ToolBinding) []ToolBinding {
	if len(in) == 0 {
		return nil
	}
	pool := map[string]TempTool{}
	for _, t := range bindableTools(owner) {
		pool[t.Name] = t
	}
	stamp := time.Now().Format(time.RFC3339)
	seen := map[string]bool{}
	out := make([]ToolBinding, 0, len(in))
	snapshotTaken := false
	for _, b := range in {
		name := strings.TrimSpace(b.Name)
		if name == "" || seen[name] {
			continue
		}
		t, ok := pool[name]
		if !ok {
			Log("[servitor.toolset] %s: dropping binding %q — not in the owner's tool pool", owner, name)
			continue
		}
		seen[name] = true
		nb := ToolBinding{
			Name:     name,
			Posture:  normalizePosture(b.Posture),
			BodyHash: toolBodyHash(t),
			BoundAt:  stamp,
			BoundBy:  boundBy,
		}
		// At most one snapshot binding: two orientation passes is just two
		// tool calls with extra ceremony, and the first-wins rule keeps the
		// stored record honest about which one runs.
		if b.Snapshot && !snapshotTaken {
			nb.Snapshot = true
			snapshotTaken = true
		}
		out = append(out, nb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// toolsetDisplayTarget names the target for the prompts, where the SSH prompts
// show a host.
func toolsetDisplayTarget(a Appliance) string {
	if d := strings.TrimSpace(a.Domain); d != "" {
		return d
	}
	if n := len(a.Toolset); n > 0 {
		return fmt.Sprintf("%d bound tools", n)
	}
	return "a tool-backed service"
}

// runToolsetSnapshot is the orientation pass: the result of the binding marked
// Snapshot, or "" when none is marked.
//
// Deliberately narrow. Every other appliance type has a cheap no-LLM
// orientation, and a toolset has no equivalent by construction — servitor cannot
// know which tool is the cheap overview. Running every zero-argument tool to
// find out would fire writes on any toolset containing one, and "read tools
// only" is a convention nothing enforces. So it is one owner-set flag, or
// nothing, and with nothing the investigator opens by calling tools itself.
func runToolsetSnapshot(rt resolvedToolset) string {
	if rt.Snapshot == "" {
		return ""
	}
	for _, def := range rt.Defs {
		if def.Tool.Name != rt.Snapshot || def.Handler == nil {
			continue
		}
		out, err := def.Handler(map[string]any{})
		if err != nil {
			return fmt.Sprintf("### Orientation\n\n`%s` failed: %v\n\n", rt.Snapshot, err)
		}
		out = strings.TrimSpace(out)
		if out == "" {
			return ""
		}
		if len(out) > 8000 {
			out = out[:8000] + "\n…(truncated)"
		}
		return fmt.Sprintf("### Orientation (from `%s`)\n\n```\n%s\n```\n\n", rt.Snapshot, out)
	}
	return ""
}

// firstToolset unwraps the variadic toolset context the prompt builders take.
// The parameter is variadic so the four non-toolset types' call sites do not
// have to pass an empty struct they have no use for; at most one is ever given.
func firstToolset(rt []resolvedToolset) resolvedToolset {
	if len(rt) > 0 {
		return rt[0]
	}
	return resolvedToolset{}
}

// handleBindableTools GETs the tools the caller may bind to a toolset
// appliance: their user-wide pool, each with the fingerprint it would be bound
// at and whether this appliance already binds it.
//
// The fingerprint is returned for DISPLAY only. It is recomputed server-side on
// save (see the appliance POST handler) and any value the client sends back is
// discarded — a caller-supplied fingerprint would let it bless a body nobody
// approved, which is the whole thing the pin exists to prevent.
func (T *Servitor) handleBindableTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	owner := userID
	bound := map[string]ToolBinding{}
	if id := strings.TrimSpace(r.URL.Query().Get("appliance_id")); id != "" && udb != nil {
		if a, o, _, found := T.resolveAppliance(userID, udb, id); found {
			owner = o
			for _, b := range a.Toolset {
				bound[b.Name] = b
			}
		}
	}
	type row struct {
		Name string `json:"name"`
		Desc string `json:"desc,omitempty"`
		// Group is the tool's self-declared Category, the same label the agent
		// tool picker groups by — so a user binding tools to an appliance reads
		// the same organization they already know from an agent's toolkit,
		// including the per-server "MCP: <name>" headings.
		Group    string `json:"group,omitempty"`
		Mode     string `json:"mode,omitempty"`
		Cred     string `json:"credential,omitempty"`
		Bound    bool   `json:"bound"`
		Posture  string `json:"posture,omitempty"`
		Snapshot bool   `json:"snapshot,omitempty"`
		// Changed reports a binding whose tool no longer matches what was
		// approved. Shown so an owner can see WHY an investigation lost a tool,
		// rather than discovering it as a quieter answer.
		Changed bool `json:"changed,omitempty"`
	}
	out := []row{}
	for _, t := range bindableTools(owner) {
		desc := strings.TrimSpace(t.Description)
		if i := strings.IndexByte(desc, '\n'); i > 0 {
			desc = desc[:i]
		}
		if len(desc) > 160 {
			desc = desc[:160] + "…"
		}
		b, isBound := bound[t.Name]
		group := strings.TrimSpace(t.Category)
		if group == "" {
			// Same fallback the agent picker uses for a user's own authored
			// tools, rather than a generic "Other" bucket they get lost in.
			group = "My Tools"
		}
		out = append(out, row{
			Name: t.Name, Desc: desc, Group: group, Mode: t.Mode, Cred: t.Credential,
			Bound: isBound, Posture: b.Posture, Snapshot: b.Snapshot,
			Changed: isBound && b.BodyHash != "" && b.BodyHash != toolBodyHash(t),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// drillReadOnlyKey marks a run dispatched by a workspace coordinator, where
// every confirmation is auto-denied. Carried on the context rather than
// threaded through runSession's signature, matching how the framework carries
// its other per-run facts (WithNetworkConnector, WithWorkspaceDir).
type drillReadOnlyKey struct{}

// WithReadOnlyDrill marks ctx as a read-only workspace drill.
func WithReadOnlyDrill(ctx context.Context) context.Context {
	return context.WithValue(ctx, drillReadOnlyKey{}, true)
}

// drillIsReadOnly reports whether this run is a workspace drill.
func drillIsReadOnly(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(drillReadOnlyKey{}).(bool)
	return v
}
