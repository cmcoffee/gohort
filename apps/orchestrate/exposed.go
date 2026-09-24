// Helpers for the public /agents/ surface (apps/agents). Orchestrate
// is admin-only; end-users consume agents that an admin has flipped
// Exposed=true on. The agents app calls into orchestrate via the
// exported lookups + handlers below, so the runtime is shared (one
// runner, one session model, one memory store) and only the URL
// surface differs.
//
// Slug rules: <PublicName or Name normalized via SnakeFromDisplay>, the
// "base" slug. Names are per-owner, so two people can each have a
// "Resume Reviewer" reachable on this surface: both published, or one
// published and one peer-shared, or two peer-shared to the same person.
// Every agent's URL has to stay unique anyway, so:
//
//   - One agent in each clashing group keeps the plain base slug and every
//     other one gets "<base>-<id fragment>" (see assignExposedSlugs). The
//     group is the whole pool, not one viewer's slice of it, so a link means
//     the same agent to whoever it is sent to.
//   - The lookup resolves only among the agents the VIEWER can reach, so an
//     agent they cannot reach never shadows one they can. A plain base slug
//     still resolves when exactly one reachable agent carries it, which keeps
//     links that predate a clash working for the people they were made for.
//   - Approving a publish request refuses a name another published agent
//     already uses (approveAgentPublish): the admin is widening reach at that
//     moment and is the right person to ask for a rename. Renames, admin
//     direct publishes and peer shares are not gated; the suffix covers them.

package orchestrate

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// DashboardCards implements core.DashboardCardSource — one card per
// exposed agent, served at /agents/<slug>/. The framework calls this
// on every dashboard render so an admin flipping Exposed in the
// editor shows up on the next refresh without restart.
//
// Description is the agent's Description field. Cards land in the
// dashboard's default sort bucket (Order 0 → falls back to 50 in
// the framework, which puts them alongside ordinary apps); admins
// can promote a specific agent by setting a per-agent dashboard
// order later if we add that field.
// ListGrantableApps surfaces every exposed agent as a grantable app
// path for the admin user-apps picker. Returns the FULL list,
// unfiltered by access — the admin needs to see all grantable paths,
// including ones nobody (including themselves as user, separately
// from their admin bit) has been granted.
func (T *OrchestrateApp) ListGrantableApps() []GrantableApp {
	entries := T.ListExposedAgents()
	out := make([]GrantableApp, 0, len(entries))
	for _, e := range entries {
		// Only PUBLISHED agents are app-grantable. A peer-shared-only agent (in the
		// pool via AllowedUsers, not Exposed) is reached through its recipient list,
		// not an admin app grant — don't offer it in the grantable-apps picker.
		if !e.ShowOnDashboard {
			continue
		}
		out = append(out, GrantableApp{
			Path: "/agents/" + e.Slug,
			Name: e.Name + " (agent app)",
		})
	}
	return out
}

func (T *OrchestrateApp) DashboardCards(r *http.Request) []DashboardCard {
	var out []DashboardCard

	// Evals get their own tile rather than hiding a rail entry inside the
	// agents app. The whole reason the primitive exists is that nobody was
	// measuring anything — a surface reachable only by somebody who already
	// went looking for it would leave that exactly as it was.
	//
	// Sorted late (Order 60) so it sits after the apps somebody opens daily.
	// It is the thing you go to after an edit, not the thing you start in.
	if udb := UserDB(T.DB, AuthCurrentUser(r)); udb != nil {
		if suites := ListEvalSuites(udb); len(suites) > 0 {
			out = append(out, DashboardCard{
				Name:  "Evals",
				Desc:  evalCardDesc(udb, suites),
				Path:  "/orchestrate/evals",
				Order: 60,
			})
		}
	}

	entries := T.ListExposedAgents()
	if len(entries) == 0 {
		return out
	}
	var shown []ExposedAgentEntry
	sameName := map[string]int{}
	for _, e := range entries {
		// Per-agent access gate — a published agent is a normal app (app-access /
		// admin), and a peer-shared agent is reachable by its AllowedUsers recipients
		// (or its owner). AgentReachableBy composes both, so a published agent nobody
		// was granted shows only for admins, and a peer-shared agent shows only for
		// its recipients.
		if !T.AgentReachableBy(r, e.Slug, e.Owner, e.AllowedUsers, e.Everyone) {
			continue
		}
		shown = append(shown, e)
		sameName[strings.ToLower(e.Name)]++
	}
	for _, e := range shown {
		name := e.Name
		// Two cards this viewer sees under one name differ only by a suffix in
		// the URL, which nobody reads. Naming the owner is what tells them
		// apart on the page.
		if sameName[strings.ToLower(e.Name)] > 1 {
			name += " (" + e.Owner + ")"
		}
		desc := strings.TrimSpace(e.Description)
		if desc == "" {
			desc = "Chat with " + e.Name + "."
		}
		out = append(out, DashboardCard{
			Name: name,
			Desc: desc,
			Path: "/agents/" + e.Slug,
		})
	}
	return out
}

// evalCardDesc says what the suites last REPORTED rather than how many there
// are. A count is a fact about the list; the reason to open it is whether
// anything moved, and a card that already answers "is anything failing" saves
// the trip that would have answered it.
func evalCardDesc(udb Database, suites []EvalSuite) string {
	var graded, failing, never int
	for _, s := range suites {
		runs := ListEvalRuns(udb, s.ID)
		if len(runs) == 0 {
			never++
			continue
		}
		graded++
		if runs[0].Passed < runs[0].Total {
			failing++
		}
	}
	switch {
	case graded == 0:
		// Written but never run is its own state, and the one most worth
		// saying: a suite nobody has run has told nobody anything.
		return fmt.Sprintf("%d suite%s, none run yet.", never, plural(never))
	case failing == 0:
		return fmt.Sprintf("%d suite%s passing.", graded, plural(graded))
	default:
		return fmt.Sprintf("%d suite%s with failures, %d passing.", failing, plural(failing), graded-failing)
	}
}

// jsonEncode + jsonDecode are tiny adapters so PublicHandleSessionOne
// doesn't repeat the encoding/json boilerplate. Local to this file —
// the orchestrate routes already use encoding/json directly elsewhere.
func jsonEncode(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }
func jsonDecode(r io.Reader, v any) error { return json.NewDecoder(r).Decode(v) }

// ExposedSlug returns the public URL slug for an agent. Derived
// from PublicName if set (so admins can rebrand the app face
// without renaming the internal agent), else from Name. NOT
// stable across renames — admin renames the slug too, breaking
// bookmarks.
func ExposedSlug(a AgentRecord) string {
	if a.PublicName != "" {
		return SnakeFromDisplay(a.PublicName)
	}
	return SnakeFromDisplay(a.Name)
}

// ExposedDisplayName returns the public-facing name for an agent
// (PublicName if set, else Name). Used for directory cards, the
// chat page title, and the agents-app placeholder copy.
func ExposedDisplayName(a AgentRecord) string {
	if a.PublicName != "" {
		return a.PublicName
	}
	return a.Name
}

// ExposedAgentEntry is one row in the public directory: minimal
// metadata the directory page needs, plus enough hooks to route
// the user to the right chat surface.
type ExposedAgentEntry struct {
	Slug            string // unique across the pool; the URL to link
	BaseSlug        string // the name-derived slug before any clash suffix
	Name            string
	Description     string
	Owner           string
	AgentID         string
	Everyone        bool     // the REACH: every signed-in user may use it
	ShowOnDashboard bool     // PRESENTATION: a card, for whoever can already use it
	AllowedUsers    []string // peer-share recipients (empty when published-only)
}

// ListExposedAgents walks every authenticated user's orchestrate
// sub-store and returns the agents with Exposed=true. Sorted by
// display name for stable ordering.
//
// DB layout note: the USER LIST lives in the root auth-db
// (AuthDB()), but the per-user AGENT sub-stores live under THIS
// app's own bucket (T.DB.Sub("user:<uid>")). Don't conflate the
// two — passing the wrong DB silently returns empty results.
//
// Performance: O(users × agents) per call. Fine at gohort scale
// (<100 users, <20 agents/user); add a deployment-wide index if a
// scan becomes noticeable.
// publiclyExposable reports whether an agent may be served on the public
// /agents/ surface. The ONLY gate is the Exposed flag — Publish means
// published. Earlier this also refused Fleet agents (their delegation /
// standing-agent / event-monitor tools reach owner-only endpoints), but that
// silently dropped the whole publish when you checked the box. The owner-only
// concern is now handled where it belongs: the Fleet toolset is attached only
// when the RUNTIME USER IS THE OWNER (see runner.go's Fleet block), so a public
// visitor never gets those tools even on a Fleet agent — no reason to refuse
// the publish. Cortex agents publish too (each visitor gets their own
// per-(user, agent) home thread).
func publiclyExposable(a AgentRecord) bool {
	return agentSurfaceEligible(a) && a.ShowOnDashboard
}

// agentSurfaceEligible is the read-side guard shared by the "published"
// (publiclyExposable) and "reachable on /agents/" (reachableAgent) gates: an agent
// may appear on the public /agents/ surface at all only when it's neither a
// framework seed nor an internal Hidden app-agent — regardless of any stale
// Exposed flag a past save (or the old ships-Exposed Research default) may
// have set.
func agentSurfaceEligible(a AgentRecord) bool {
	// App Agents are an app's own surface — publishable unless registered
	// Hidden (Servitor Investigator, Guide Author), which are reached only
	// through their owning app, never as a standalone surface.
	if spec, ok := appagents.AppAgentByID(a.ID); ok {
		return !spec.Hidden
	}
	// Any other seed — the framework's own agents, clone-only templates
	// included — is never a dashboard app. Users get the crafted seeds as
	// wizard TEMPLATES (clone-your-own), not as published surfaces.
	if isSeedID(a.ID) {
		return false
	}
	return true
}

// reachableAgent reports whether an agent is served on the /agents/ surface for
// SOMEONE: either it's published to app-access users (Exposed) OR it's peer-shared
// to specific users (AllowedUsers). It's the directory/lookup pool; WHO may
// actually see or run it is the separate per-user gate AgentReachableBy.
func reachableAgent(a AgentRecord) bool {
	return agentSurfaceEligible(a) && (a.Everyone || a.ShowOnDashboard || len(a.AllowedUsers) > 0)
}

// AgentReachableBy reports whether the request's user may see + run the agent at
// /agents/<slug>: the framework app-access grant (Exposed → published + granted;
// admins auto), the agent's peer-share recipient list (AllowedUsers), or its
// owner. This composes the "published to app-access users" and "shared to specific
// users" access models on the one public surface.
func (T *OrchestrateApp) AgentReachableBy(r *http.Request, slug, owner string, allowedUsers []string, published bool) bool {
	u := AuthCurrentUser(r)
	if u == "" {
		return false
	}
	if u == owner {
		return true
	}
	if !published {
		// Not published: the owner's list is the whole answer, and nobody
		// else's opinion is involved. Handing an agent to two colleagues is
		// the owner's business, as it is for every other kind.
		return containsString(allowedUsers, u)
	}
	// Published: two gates, and it takes BOTH.
	//
	// The admin's grant of /agents/<slug> is the CEILING — who this deployment
	// will let near the agent at all. The owner's list narrows inside it: they
	// know who the agent is for, they are answerable for it (their tools,
	// their documents, their key lent into it), and making them file a ticket
	// to add a teammate is how admins end up granting broadly to stop being
	// asked, which is the worse position.
	//
	// It used to be an OR, which meant the owner's list could reach somebody
	// the admin had not granted — a narrowing that widened.
	//
	// An admin is admitted regardless of the narrowing. They can already read
	// the record, revoke the share and un-publish it; locking them out of the
	// thing they govern would be a gate that protects nothing and confuses
	// whoever is debugging it.
	if !UserHasAppAccess(r, "/agents/"+slug) {
		return false
	}
	if RequestIsAdmin(r) {
		return true
	}
	// Empty means the owner has not narrowed it: everybody the admin allowed.
	// The same reading AllowedUsers has on a credential and on a shared tool,
	// and the alternative — empty means nobody — would have made every agent
	// published before this unreachable overnight.
	return len(allowedUsers) == 0 || containsString(allowedUsers, u)
}

// CortexSessionID exposes a channel agent's pinned home-thread session id so
// the public agents app can pin each visitor to the SAME id the runner
// compacts against (agent.Cortex && sess.ID == cortexSessionID(agent.ID) in
// runner.go). Per-(user, agent) scoping means the id resolves to a different
// physical thread for every visitor without the id itself differing.
func CortexSessionID(agentID string) string { return cortexSessionID(agentID) }

// ListExposedAgents is the pool as directory rows: every agent the surface can
// serve, each once, each with its own slug, sorted by display name. WHO may see
// a row is the caller's AgentReachableBy check, made with the row's Slug.
func (T *OrchestrateApp) ListExposedAgents() []ExposedAgentEntry {
	pool := T.exposedPool()
	out := make([]ExposedAgentEntry, 0, len(pool))
	for _, e := range pool {
		out = append(out, e.ExposedAgentEntry)
	}
	return out
}

// exposedPoolEntry is a directory row plus the record it summarizes, so the
// slug lookup can hand back the record without loading it a second time.
type exposedPoolEntry struct {
	ExposedAgentEntry
	rec AgentRecord
}

// exposedPool builds every agent the /agents/ surface can serve, each with a
// slug no other agent in the pool shares, in a fixed order. The directory and
// the lookup both read this one list, which is what keeps the link a card
// shows and the agent that link opens the same agent.
func (T *OrchestrateApp) exposedPool() []exposedPoolEntry {
	if T.DB == nil || AuthDB == nil {
		return nil
	}
	authDB := AuthDB()
	if authDB == nil {
		return nil
	}
	// Dedup by AgentID, preferring user shadows over in-code seeds. Without
	// it a single seed agent surfaces twice when one user has shadowed it
	// (with PublicName, etc.) and another user still sees the default: they
	// produce different slugs but represent the same record.
	byID := map[string]exposedPoolEntry{}
	idIsShadow := map[string]bool{} // true if the stored entry came from a shadowing user
	for _, u := range AuthListUsers(authDB) {
		udb := UserDB(T.DB, u.Username)
		for _, a := range listAgents(udb, u.Username) {
			if !reachableAgent(a) {
				continue
			}
			slug := ExposedSlug(a)
			if slug == "" {
				continue
			}
			// A user's record is a "shadow" when its Owner matches
			// that user; the in-code seed defaults travel with
			// Owner=seedOwner. Shadow wins over seed.
			isShadow := a.Owner == u.Username
			if _, exists := byID[a.ID]; exists && (idIsShadow[a.ID] || !isShadow) {
				continue
			}
			byID[a.ID] = exposedPoolEntry{
				ExposedAgentEntry: ExposedAgentEntry{
					Slug:            slug,
					BaseSlug:        slug,
					Name:            ExposedDisplayName(a),
					Description:     a.Description,
					Owner:           u.Username,
					AgentID:         a.ID,
					Everyone:        a.Everyone,
					ShowOnDashboard: a.ShowOnDashboard,
					AllowedUsers:    a.AllowedUsers,
				},
				rec: a,
			}
			idIsShadow[a.ID] = isShadow
		}
	}
	pool := make([]exposedPoolEntry, 0, len(byID))
	for _, e := range byID {
		pool = append(pool, e)
	}
	assignExposedSlugs(pool)
	// Map iteration order is random, and this list is what the dashboard
	// renders: sorting here is what keeps the directory the same from one
	// refresh to the next. Slug last breaks a tie between same-named agents,
	// and slugs are unique by now.
	sort.Slice(pool, func(i, j int) bool {
		ni, nj := strings.ToLower(pool[i].Name), strings.ToLower(pool[j].Name)
		if ni != nj {
			return ni < nj
		}
		return pool[i].Slug < pool[j].Slug
	})
	return pool
}

// exposedSlugFragmentMin is the shortest id fragment a clash suffix uses: long
// enough that two UUIDs rarely need more, short enough to read in a URL.
const exposedSlugFragmentMin = 6

// assignExposedSlugs makes every Slug in the pool unique. Agents whose base
// slug nobody else has keep it. In a clashing group ONE agent keeps the plain
// slug and the rest become "<base>-<id fragment>".
//
// Who keeps it: a published (Everyone) agent before a peer-shared one, because
// an admin's app grant names the published agent's path ("/agents/<slug>") and
// moving it would silently revoke every grant made for it. Then the oldest
// agent, since it is the one whose links are already out there. Then the id,
// so the answer never depends on the order the stores were read in.
//
// A base slug never contains "-" (SnakeFromDisplay emits only [a-z0-9_]), so
// a suffixed slug can never equal some other agent's plain one.
func assignExposedSlugs(pool []exposedPoolEntry) {
	groups := map[string][]int{}
	for i := range pool {
		groups[pool[i].BaseSlug] = append(groups[pool[i].BaseSlug], i)
	}
	for base, idx := range groups {
		if len(idx) < 2 {
			continue
		}
		sort.Slice(idx, func(a, b int) bool {
			x, y := pool[idx[a]], pool[idx[b]]
			if x.Everyone != y.Everyone {
				return x.Everyone
			}
			if !x.rec.Created.Equal(y.rec.Created) {
				return x.rec.Created.Before(y.rec.Created)
			}
			return x.AgentID < y.AgentID
		})
		// Grow the fragment until it tells the whole group apart, the
		// keeper included, so a stale suffixed link for the keeper can never
		// also match a sibling.
		n := exposedSlugFragmentMin
		for ; n < 64; n++ {
			seen := map[string]bool{}
			clash := false
			for _, i := range idx {
				f := slugIDFragment(pool[i].AgentID, n)
				if seen[f] {
					clash = true
					break
				}
				seen[f] = true
			}
			if !clash {
				break
			}
		}
		for _, i := range idx[1:] {
			pool[i].Slug = base + "-" + slugIDFragment(pool[i].AgentID, n)
		}
	}
}

// slugIDFragment is the first n URL-safe characters of an agent id. Agent ids
// are UUIDs, whose leading hex is already safe; the filter is for app-agent ids,
// which are plain strings.
func slugIDFragment(id string, n int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() == n {
				break
			}
		}
	}
	return b.String()
}

// matchesSlugFragment reports whether slug is "<base>-<fragment>" for this
// agent at ANY fragment length from the minimum up. A suffixed link stays
// valid after its clash is gone and the agent is back on the plain slug, and
// after a later clash grew the fragment, so a link someone saved keeps opening
// what it opened.
func (e exposedPoolEntry) matchesSlugFragment(slug string) bool {
	rest, ok := strings.CutPrefix(slug, e.BaseSlug+"-")
	if !ok || len(rest) < exposedSlugFragmentMin {
		return false
	}
	return slugIDFragment(e.AgentID, len(rest)) == rest
}

// LookupAppAgent resolves an agent by ID or name within an owner's store, for
// a data-driven app (customapps) that binds an agent to power its chat
// surface. The name fallback matters for imported apps: a bundle's custom-app
// recipe carries the agent reference normalized to a NAME (an imported agent
// is reborn under a fresh ID), so the binding must dispatch either form —
// same rule as pipeline stage dispatch. Unlike LookupExposedAgent this does
// NOT require Exposed=true — an app's agent is reached through the app, not
// published on its own. Returns (zero, false) when the owner or agent doesn't
// resolve.
func (T *OrchestrateApp) LookupAppAgent(owner, agentID string) (AgentRecord, bool) {
	if T.DB == nil || owner == "" || agentID == "" {
		return AgentRecord{}, false
	}
	udb := UserDB(T.DB, owner)
	if udb == nil {
		return AgentRecord{}, false
	}
	return findAgentByNameOrID(udb, owner, agentID)
}

// LookupAppPipeline resolves a pipeline by ID or name within an owner's store,
// for a data-driven app whose `pipeline` section runs it. Name fallback for the
// same reason LookupAppAgent has one: an imported bundle carries the reference
// as a NAME because the pipeline is reborn under a fresh ID on import, so a
// binding that only understood IDs would break every imported app.
func (T *OrchestrateApp) LookupAppPipeline(owner, pipelineID string) (PipelineDef, bool) {
	if T == nil || T.DB == nil || owner == "" || strings.TrimSpace(pipelineID) == "" {
		return PipelineDef{}, false
	}
	udb := UserDB(T.DB, owner)
	if udb == nil {
		return PipelineDef{}, false
	}
	if def, ok := LoadPipelineDef(udb, owner, pipelineID); ok {
		return def, true
	}
	want := SnakeFromDisplay(strings.TrimSpace(pipelineID))
	for _, d := range ListPipelineDefs(udb, owner) {
		if SnakeFromDisplay(d.Name) == want {
			return d, true
		}
	}
	return PipelineDef{}, false
}

// PublicHandlePipeline serves the PipelinePanel protocol for a pipeline a HOST
// app has bound — the pipeline counterpart of PublicHandleSend and friends.
// sub is the path after "pipeline/": "stream" | "sessions" | "sessions/<id>".
//
// The split that makes a shared app work: the DEFINITION is the owner's (the
// caller resolved it via LookupAppPipeline), while the run transcripts are
// stored and listed under the CALLING user — so every user of a shared app
// runs the same recipe and sees only their own history. Same shape as the
// records store, where the definition is shared and the data is not.
func (T *OrchestrateApp) PublicHandlePipeline(w http.ResponseWriter, r *http.Request, def PipelineDef, sub string) {
	T.PublicHandlePipelineLive(w, r, def, sub, RunLiveInfo{})
}

// PublicHandlePipelineLive is PublicHandlePipeline for a host that can say
// WHERE its runs live.
//
// A run outlives the request that started it, so it has to be reachable from
// somewhere other than the tab it was started in. Core knows a run is going;
// only the host knows the page it belongs to, because core never learns the
// path its surface was mounted under.
func (T *OrchestrateApp) PublicHandlePipelineLive(w http.ResponseWriter, r *http.Request, def PipelineDef, sub string, live RunLiveInfo) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	surface := T.pipelineRunSurface(r.Context(), user, def)
	surface.Live = live
	T.ServePipelineRuns(w, r, surface, sub)
}

// PublicLatestPipelineRun returns a user's most recent run of a pipeline —
// what it produced, and the per-stage transcript.
//
// For a host app whose action scripts need the run that just finished. Without
// it, the only way to get a completed run into an app's record store was to
// guess at an environment variable the framework never set, which is exactly
// what happened: a "save this debate to history" button read `pipeline_output`,
// found nothing, and reported "run a debate first" after every debate. The
// script ran and printed valid JSON, so every check passed.
//
// Scoped to the CALLING user, like the run store itself: a shared app's users
// each see their own last run.
func (T *OrchestrateApp) PublicLatestPipelineRun(user, pipelineID string) (PipelineRun, bool) {
	if T == nil {
		return PipelineRun{}, false
	}
	return LatestPipelineRun(T.DB, user, pipelineID)
}

// LookupExposedAgent resolves a URL slug to the agent it opens FOR THIS
// VIEWER, plus its directory row (the owner, used to scope memory/sessions in
// the right sub-store, and the canonical Slug the access gate is checked
// against). Returns ok=false when no agent the viewer can reach answers to it.
//
// Only reachable agents are candidates. Resolving across the whole pool first
// and gating second is what used to 404 a recipient: the first same-named
// agent in user-listing order won, it was somebody else's, and the gate then
// refused the viewer an agent they had not asked for.
//
// Precedence, most specific first, and each of the looser two only when it
// names exactly one reachable agent (an ambiguous link opens nothing rather
// than a guess):
//
//  1. the canonical slug, which is unique across the pool;
//  2. "<base>-<id fragment>" at any fragment length, so a saved suffixed link
//     survives its clash going away or its fragment growing;
//  3. the plain base slug, so a link made before a clash still opens the
//     agent for anyone who can reach only one agent of that name.
func (T *OrchestrateApp) LookupExposedAgent(r *http.Request, slug string) (AgentRecord, ExposedAgentEntry, bool) {
	if slug == "" {
		return AgentRecord{}, ExposedAgentEntry{}, false
	}
	var byFragment, byBase []exposedPoolEntry
	for _, e := range T.exposedPool() {
		if !T.AgentReachableBy(r, e.Slug, e.Owner, e.AllowedUsers, e.Everyone) {
			continue
		}
		switch {
		case e.Slug == slug:
			return e.rec, e.ExposedAgentEntry, true
		case e.matchesSlugFragment(slug):
			byFragment = append(byFragment, e)
		case e.BaseSlug == slug:
			byBase = append(byBase, e)
		}
	}
	for _, set := range [][]exposedPoolEntry{byFragment, byBase} {
		if len(set) == 1 {
			return set[0].rec, set[0].ExposedAgentEntry, true
		}
		if len(set) > 1 {
			break
		}
	}
	return AgentRecord{}, ExposedAgentEntry{}, false
}

// lookupReachableAgentByID finds a published or peer-shared agent by record
// id in the pool, the id-keyed twin of LookupExposedAgent. The row comes back
// so the caller can run the per-user reachability gate against the agent's
// canonical slug, which is the path an admin's grant names.
func (T *OrchestrateApp) lookupReachableAgentByID(agentID string) (AgentRecord, ExposedAgentEntry, bool) {
	if agentID == "" {
		return AgentRecord{}, ExposedAgentEntry{}, false
	}
	for _, e := range T.exposedPool() {
		if e.AgentID == agentID {
			return e.rec, e.ExposedAgentEntry, true
		}
	}
	return AgentRecord{}, ExposedAgentEntry{}, false
}

// memoryAgent resolves the agent whose memory a handler is about to serve for
// user, whose store is udb. Two shapes:
//
//   - The agent is user's own, or a seed (whose per-user shadow loadAgent's
//     fallback supplies): the record is in udb. The ordinary console case and
//     the servitor per-scope case, where user is a synthetic scope and the
//     agent a hidden template.
//   - The agent is someone else's, published or peer-shared, and user is a
//     visitor chatting it on /agents/. The record lives in the AUTHOR's store
//     and nothing ever copies it into the visitor's, so loadAgent(udb) fails —
//     yet every turn the visitor takes writes facts, notes, graph and findings
//     into udb under this agent's id (handleSend: "memory stays with whoever
//     is typing"). That memory shaped the visitor's replies and was readable by
//     nobody: the author's pane reads the author's store, and the visitor's
//     pane 404'd here. The fallback resolves the record from the author's
//     store, gated by the same AgentReachableBy that admits the visitor to the
//     chat, and the handler then reads DATA from udb exactly as before — so
//     the visitor sees and prunes their own scope, never the author's.
//
// The fallback is only for the request's own user: a scope user is never the
// session identity, so a scope lookup that misses stays a miss.
func (T *OrchestrateApp) memoryAgent(r *http.Request, udb Database, user, agentID string) (AgentRecord, bool) {
	if a, ok := loadAgent(udb, agentID); ok && (a.Owner == user || a.Owner == seedOwner) {
		return a, true
	}
	if r == nil || AuthCurrentUser(r) != user {
		return AgentRecord{}, false
	}
	a, e, ok := T.lookupReachableAgentByID(agentID)
	if !ok || !T.AgentReachableBy(r, e.Slug, e.Owner, a.AllowedUsers, a.Everyone) {
		return AgentRecord{}, false
	}
	return a, true
}

// PublicHandleSend dispatches a /api/send for an exposed agent. The
// caller (apps/agents) has already resolved the slug + checked
// Exposed=true; we just bypass the admin gate that wraps the
// orchestrate-mounted variant and call the runner with the active
// END-USER's identity (not the agent owner's). That way each user's
// sessions/memory/knowledge live in their own per-(user, agent)
// scope — admin builds the agent, end-users accumulate their own
// timeline against it.
func (T *OrchestrateApp) PublicHandleSend(w http.ResponseWriter, r *http.Request, agent AgentRecord) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleSend(w, r, udb, user, agent)
}

// PublicHandleSendWithAppTools is PublicHandleSend plus host-app tools injected
// into the agent's catalog for this run. A data-driven app (customapps) passes a
// co-author tool — a closure over its own record store — so the bound agent can
// write into the open document. The tools are built by the caller (with its own
// data access); orchestrate just runs them, staying ignorant of app storage.
func (T *OrchestrateApp) PublicHandleSendWithAppTools(w http.ResponseWriter, r *http.Request, agent AgentRecord, appTools []AgentToolDef) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleSendWithAppTools(w, r, udb, user, agent, appTools)
}

// PublicHandleCancel mirrors PublicHandleSend's bypass for cancel.
func (T *OrchestrateApp) PublicHandleCancel(w http.ResponseWriter, r *http.Request, agent AgentRecord) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleCancel(w, r, user, agent)
}

// PublicHandleRunsActive / PublicHandleRunsDispatch expose the run-stream
// reconnect endpoints on the published /agents/ surface so a long turn whose
// live /api/send socket drops can be resumed from the run buffer — the same
// resilience the admin console has. The underlying handlers already RequireUser
// and refuse a run whose UserID != the caller, so a leaked run id stays private.
func (T *OrchestrateApp) PublicHandleRunsActive(w http.ResponseWriter, r *http.Request) {
	T.handleRunsActive(w, r)
}

func (T *OrchestrateApp) PublicHandleRunsDispatch(w http.ResponseWriter, r *http.Request) {
	T.handleRunsDispatch(w, r)
}

// PublicHandleChannelClear wipes the calling end-user's channel home thread
// (conversation + rolling summary / fold cursor) for an exposed channel agent
// — the per-visitor equivalent of the owner's "Clear channel" console action.
// Per-(user, agent) scoped via RequireUser, so a visitor only ever resets
// their OWN thread. agentID comes from the slug-resolved record, not the
// request, so a crafted ?agent= can't redirect the wipe.
func (T *OrchestrateApp) PublicHandleChannelClear(w http.ResponseWriter, r *http.Request, agentID string) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sid := cortexSessionID(agentID)
	deleteChatSession(udb, agentID, sid) // includes the compact state
	w.WriteHeader(http.StatusNoContent)
}

// (PublicHandleAgentMemory removed — the auto-notes Memory layer
// it served is gone. End-users' per-(user, agent) state now lives
// entirely in Explicit Memory facts and Reference Memory chunks,
// both managed through their respective tools / endpoints.)

// PublicHandlePrivateModeGet / Set expose the per-user Private-
// mode toggle without the admin gate. Reuses the AuthDB-backed
// pref the orchestrate (and legacy chat) surface uses, so a
// user's toggle applies everywhere private-mode lands in send
// body.
func (T *OrchestrateApp) PublicHandlePrivateModeGet(w http.ResponseWriter, r *http.Request) {
	T.handlePrivateModeGet(w, r)
}
func (T *OrchestrateApp) PublicHandlePrivateModeSet(w http.ResponseWriter, r *http.Request) {
	T.handlePrivateModeSet(w, r)
}

// PublicHandleMemoryModeGet / Set expose the per-user Reference Memory
// suppression toggle for the public agents surface. Same per-user
// preference the admin orchestrate surface uses, so toggling either
// flips the bit globally for that user.
func (T *OrchestrateApp) PublicHandleMemoryModeGet(w http.ResponseWriter, r *http.Request) {
	T.handleMemoryModeGet(w, r)
}
func (T *OrchestrateApp) PublicHandleMemoryModeSet(w http.ResponseWriter, r *http.Request) {
	T.handleMemoryModeSet(w, r)
}

// PublicHandleAgentFacts exposes the per-(user, agent) facts list
// (store_fact entries) without the admin gate. Mirrors
// handleAgentFacts for the public surface.
func (T *OrchestrateApp) PublicHandleAgentFacts(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentFacts(w, r, user, agentID)
}

// PublicHandleAgentNotes exposes the Working notes block — the memory layer
// the model rewrites on its own and the owner previously could not see.
func (T *OrchestrateApp) PublicHandleAgentNotes(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentNotes(w, r, user, agentID)
}

// PublicHandleAgentMemoryAudit exposes the Memory pane's "Needs attention"
// findings — memory entries that reference something no longer there.
func (T *OrchestrateApp) PublicHandleAgentMemoryAudit(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentMemoryAudit(w, r, user, agentID)
}

// PublicHandleAgentMemorySearch exposes the Memory modal's search
// (grep + recall-preview + per-hit delete) on the public agents surface,
// scoped to the logged-in user like the facts handler above.
func (T *OrchestrateApp) PublicHandleAgentMemorySearch(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentMemorySearch(w, r, user, agentID)
}

// --- per-SCOPE memory handlers (the agent-as-template display) ----------------
//
// These serve an agent's facts / graph / knowledge / Reference Memory for an
// EXPLICIT scope (e.g. a servitor appliance instance, "app:servitor:<id>") rather
// than the logged-in user. They require a valid session (the auth gate) but read
// the data from scopeUser; the CALLER is responsible for authorizing that the
// session user may view that scope (servitor checks the appliance is theirs
// before calling). This lets servitor mount the SAME editable Memory surface
// orchestrate uses, pointed at a per-appliance scope. agentID is the template's
// id; loadAgent's seed fallback keeps the ownership gate satisfied for it.

func (T *OrchestrateApp) PublicHandleAgentFactsForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentFacts(w, r, scopeUser, agentID)
}

func (T *OrchestrateApp) PublicHandleAgentNotesForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentNotes(w, r, scopeUser, agentID)
}

func (T *OrchestrateApp) PublicHandleAgentMemoryAuditForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentMemoryAudit(w, r, scopeUser, agentID)
}

func (T *OrchestrateApp) PublicHandleAgentGraphForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentGraph(w, r, scopeUser, agentID)
}

func (T *OrchestrateApp) PublicHandleAgentGraphEntityDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID, entityID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentGraphEntityDelete(w, r, scopeUser, agentID, entityID)
}

func (T *OrchestrateApp) PublicHandleAgentGraphAttrDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID, entityID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentGraphAttrDelete(w, r, scopeUser, agentID, entityID)
}

func (T *OrchestrateApp) PublicHandleAgentGraphAliasDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID, entityID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentGraphAliasDelete(w, r, scopeUser, agentID, entityID)
}

func (T *OrchestrateApp) PublicHandleAgentGraphEdgeDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentGraphEdgeDelete(w, r, scopeUser, agentID)
}

func (T *OrchestrateApp) PublicHandleAgentKnowledgeForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentKnowledge(w, r, scopeUser, agentID)
}

func (T *OrchestrateApp) PublicHandleAgentInferredListForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentInferredList(w, r, scopeUser, agentID)
}

func (T *OrchestrateApp) PublicHandleAgentInferredDeleteForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID, chunkID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentInferredDelete(w, r, scopeUser, agentID, chunkID)
}

// PublicHandleAgentMemorySearchForScope mounts the memory search on an
// explicit scope (e.g. a servitor appliance) — session gate proves a
// logged-in user; the CALLER authorizes the scope, same contract as the
// other ForScope handlers.
func (T *OrchestrateApp) PublicHandleAgentMemorySearchForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentMemorySearch(w, r, scopeUser, agentID)
}

// PublicHandleAgentKnowledgeAutoInferredWipeForScope is the "Wipe all"
// button on the per-scope Memory modal — drops every Reference Memory
// chunk in the scope's namespace. Like the other ForScope handlers, the
// session gate proves a logged-in user; the CALLER authorizes the scope.
func (T *OrchestrateApp) PublicHandleAgentKnowledgeAutoInferredWipeForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	T.handleAgentKnowledgeAutoInferredWipe(w, r, scopeUser, agentID)
}

// PublicHandleAgentRecordForScope returns the template agent's record so
// the Memory modal can gate its sections on the disable_explicit /
// disable_inferred flags. The record is config (template-owned), so it
// loads from the scope store via loadAgent's seed fallback — the same
// path the facts handler relies on.
func (T *OrchestrateApp) PublicHandleAgentRecordForScope(w http.ResponseWriter, r *http.Request, scopeUser, agentID string) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a, ok := loadAgent(UserDB(T.DB, scopeUser), agentID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = jsonEncode(w, a)
}

// PublicHandleAgentGraph* expose the per-(user, agent) graph memory (the
// entities + relationships the agent linked about THIS visitor via
// link_entities) on the dashboard surface — read + delete only, scoped via
// RequireUser so a visitor only ever sees and prunes their OWN graph. Same
// data-hygiene posture as the Reference Memory wipe; the underlying handlers are
// already per-user (UserDB + factsNamespace), so these just supply the visitor.
func (T *OrchestrateApp) PublicHandleAgentGraph(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentGraph(w, r, user, agentID)
}
func (T *OrchestrateApp) PublicHandleAgentGraphEntityDelete(w http.ResponseWriter, r *http.Request, agentID, entityID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentGraphEntityDelete(w, r, user, agentID, entityID)
}
func (T *OrchestrateApp) PublicHandleAgentGraphAttrDelete(w http.ResponseWriter, r *http.Request, agentID, entityID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentGraphAttrDelete(w, r, user, agentID, entityID)
}
func (T *OrchestrateApp) PublicHandleAgentGraphAliasDelete(w http.ResponseWriter, r *http.Request, agentID, entityID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentGraphAliasDelete(w, r, user, agentID, entityID)
}
func (T *OrchestrateApp) PublicHandleAgentGraphEdgeDelete(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentGraphEdgeDelete(w, r, user, agentID)
}

// PublicHandleAgentKnowledge serves the per-(user, agent) vector
// knowledge chunk count + wipe without the admin gate. Mirrors
// handleAgentKnowledge.
func (T *OrchestrateApp) PublicHandleAgentKnowledge(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentKnowledge(w, r, user, agentID)
}

// PublicHandleAgentKnowledgeAutoInferredWipe mirrors the admin
// auto-inferred wipe for end-users on the public agent app.
func (T *OrchestrateApp) PublicHandleAgentKnowledgeAutoInferredWipe(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentKnowledgeAutoInferredWipe(w, r, user, agentID)
}

// PublicHandleAgentInferredList exposes the per-(user, agent)
// Reference Memory listing for the public agent app — same payload
// as the admin /inferred endpoint.
func (T *OrchestrateApp) PublicHandleAgentInferredList(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentInferredList(w, r, user, agentID)
}

// PublicHandleAgentInferredDelete deletes one Reference Memory chunk
// for the end-user under their per-(user, agent) namespace.
func (T *OrchestrateApp) PublicHandleAgentInferredDelete(w http.ResponseWriter, r *http.Request, agentID, chunkID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentInferredDelete(w, r, user, agentID, chunkID)
}

// PublicHandleAgentRecord returns a read-only JSON view of the agent
// record so the public chat UI can branch on flag fields
// (disable_explicit, disable_inferred, etc.) without a separate
// per-flag endpoint.
func (T *OrchestrateApp) PublicHandleAgentRecord(w http.ResponseWriter, r *http.Request, agent AgentRecord) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = jsonEncode(w, agent)
}

// PublicHandleAgentKnowledgeUpload mirrors handleAgentKnowledgeUpload
// for the public agent app — same body shape, same per-(user, agent)
// ingest. End-users get to build their own document corpus under the
// exposed agent.
func (T *OrchestrateApp) PublicHandleAgentKnowledgeUpload(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentKnowledgeUpload(w, r, user, agentID)
}

// PublicHandleAgentKnowledgeSources mirrors handleAgentKnowledgeSources.
func (T *OrchestrateApp) PublicHandleAgentKnowledgeSources(w http.ResponseWriter, r *http.Request, agentID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentKnowledgeSources(w, r, user, agentID)
}

// PublicHandleAgentKnowledgeSourceDelete mirrors handleAgentKnowledgeSourceDelete.
func (T *OrchestrateApp) PublicHandleAgentKnowledgeSourceDelete(w http.ResponseWriter, r *http.Request, agentID, reportID string) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	T.handleAgentKnowledgeSourceDelete(w, r, user, agentID, reportID)
}

// PublicHandleSessionList writes the calling user's session
// summaries for the given exposed agent. Resolves the user from
// the request and reads from orchestrate's own DB — apps/agents
// can't pass its per-app bucket here because the sessions are
// stored under orchestrate.
// PublicHandleSessionListFor is PublicHandleSessionList narrowed to one app
// context (see ChatSession.AppContext) — the sessions about ONE document rather
// than every conversation the user has had with this agent.
//
// A session with no context set is shown in every scope rather than nowhere:
// conversations that predate the field, or came from a surface that sets none,
// stay reachable instead of disappearing the day scoping arrives. Continuing
// one files it (the send path back-fills), so the unfiled set drains rather
// than growing.
//
// An empty appContext means "no scoping" and behaves exactly like
// PublicHandleSessionList.
func (T *OrchestrateApp) PublicHandleSessionListFor(w http.ResponseWriter, r *http.Request, agentID, appContext string) {
	T.publicSessionList(w, r, agentID, appContext)
}

func (T *OrchestrateApp) PublicHandleSessionList(w http.ResponseWriter, r *http.Request, agentID string) {
	T.publicSessionList(w, r, agentID, "")
}

func (T *OrchestrateApp) publicSessionList(w http.ResponseWriter, r *http.Request, agentID, appContext string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	sessions := listChatSessions(udb, agentID)
	// The Cortex home-thread lives as a session keyed "channel:<agentID>"
	// (see cortexSessionID). Orchestrate's own UI lifts that row out of the
	// list and renders it as the pinned "Cortex" hero (AltNavFlag). The
	// exposed agent-as-an-app front deliberately opts out of that hero
	// (apps/agents/agents.go), so without this filter the same row leaks
	// into the ordinary session rail. Drop it here rather than teaching the
	// shared core/ui runtime about the "channel:" key convention.
	filtered := sessions[:0]
	for _, s := range sessions {
		if strings.HasPrefix(s.ID, "channel:") {
			continue
		}
		if appContext != "" && s.AppContext != "" && s.AppContext != appContext {
			continue
		}
		filtered = append(filtered, s)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = jsonEncode(w, filtered)
}

// PublicHandleSessionOne is GET (load) / DELETE (drop) / PATCH
// (truncate) for one session under an exposed agent. Method on
// *OrchestrateApp so the udb resolves from orchestrate's T.DB
// (same bucket the sessions were written into by PublicHandleSend).
func (T *OrchestrateApp) PublicHandleSessionOne(w http.ResponseWriter, r *http.Request, agentID, sid string) {
	// Sub-action: /api/sessions/{sid}/export — full session trace
	// download. Same behavior as the admin orchestrate surface; the
	// caller (apps/agents) has already authorized that agent is
	// exposed and the user owns the session.
	if strings.HasSuffix(sid, "/export") {
		sid = strings.TrimSuffix(sid, "/export")
		if sid == "" || strings.Contains(sid, "/") {
			http.NotFound(w, r)
			return
		}
		T.handleSessionExport(w, r, agentID, sid)
		return
	}
	if sid == "" || strings.Contains(sid, "/") {
		http.NotFound(w, r)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s, ok := loadChatSession(udb, agentID, sid)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = jsonEncode(w, s)
	case http.MethodDelete:
		deleteChatSession(udb, agentID, sid)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch:
		var body struct {
			At int `json:"at"`
		}
		if err := jsonDecode(r.Body, &body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s, ok := loadChatSession(udb, agentID, sid)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if body.At < 0 {
			body.At = 0
		}
		if body.At > len(s.Messages) {
			body.At = len(s.Messages)
		}
		s.Messages = s.Messages[:body.At]
		if _, err := saveChatSession(udb, s); err != nil {
			http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = jsonEncode(w, map[string]any{"at": body.At, "messages_remaining": len(s.Messages)})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
