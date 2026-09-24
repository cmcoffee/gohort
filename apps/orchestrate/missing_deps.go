package orchestrate

// What an agent references and can no longer reach.
//
// An agent names its tools, skills and knowledge by reference: a tool by NAME
// (through the owner's adoption list and its own AllowedTools), a skill or a
// collection by ID. When whoever shared one of those takes it back, unpublishes
// it or deletes it, the reference stays on the agent and simply stops
// resolving. Every loader here skips what does not resolve, which is the right
// thing for the run and the wrong thing for everybody around it: the model
// still has instructions describing the capability, the owner sees nothing in
// the editor (the Tools modal even dropped the name on its next save), and the
// person who ran it gets a confident answer from an agent missing a piece.
//
// This file is the one place that answers "what is missing" and the one place
// that says so, on every run path (web turn, worker step, channel, dispatch,
// scheduled fire): one model-facing note on the newest user turn, one
// breadcrumb per session, and the owner told when somebody else's run found
// the gap. The editor asks the same function, so what it shows as gone is what
// the runtime skips.

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/shareledger"
)

// missingRef is one reference an agent carries that no longer resolves.
type missingRef struct {
	Kind string `json:"kind"` // "tool", "skill" or "collection"
	ID   string `json:"id"`
	// Name is the last name it was known by, or the id when nothing
	// remembers one. A deleted collection's UUID tells its owner nothing.
	Name string `json:"name"`
	// Recreatable is set, for a tool, when the user can have it back as their
	// own copy (core.RecreateLostTool): it was withdrawn or deleted, not taken
	// from them in particular. Filled by the editor's GET only; the per-turn
	// check does not pay for it.
	Recreatable bool `json:"recreatable,omitempty"`
}

// missingKindPhrase is how the model and the owner are told what kind of thing
// went missing, with its article.
func missingKindPhrase(kind string) string {
	switch kind {
	case "tool":
		return "a tool"
	case "skill":
		return "a skill"
	case "collection":
		return "a knowledge collection"
	}
	return "something"
}

// missingKindWord is the short form, for a list.
func missingKindWord(kind string) string {
	if kind == "collection" {
		return "knowledge"
	}
	return kind
}

// agentLoadsPoolTool reports whether this agent loads a user-wide tool of this
// name when one resolves: the gates loadAgentTempTools applies to an unscoped
// row, stated once so the runtime, the editor and the dependents finder agree.
func agentLoadsPoolTool(a AgentRecord, name string) bool {
	if isNoToolsSentinel(a.AllowedTools) || namedIn(a.DisabledPersistentTools, name) {
		return false
	}
	_, seedBacked := seedAgentByID(a.ID)
	if a.Owner == seedOwner || seedBacked || len(a.AllowedTools) == 0 {
		return true
	}
	for _, n := range a.AllowedTools {
		if canonicalToolName(n) == name {
			return true
		}
	}
	return false
}

// agentReferences reports whether agent a references kind/id.
func agentReferences(a AgentRecord, kind, id string) bool {
	switch kind {
	case "tool":
		return !isBuilderAgent(a.ID) && agentLoadsPoolTool(a, id)
	case "skill":
		return !a.DisableSkills && namedIn(a.AllowedSkills, id)
	case "collection":
		return namedIn(a.AttachedCollections, id)
	}
	return false
}

// toolResolution is what the owner's tool names resolve to for this agent:
// own is every name a tool of their own holds for it (a scoped row counts only
// on the agents it is scoped to), resolved every adopted name that still loads.
func toolResolution(a AgentRecord, poolDB Database, owner string) (own, resolved map[string]bool) {
	own, resolved = map[string]bool{}, map[string]bool{}
	for _, p := range LoadPersistentTempTools(poolDB, owner) {
		if len(p.ScopeAgents) > 0 && !p.ScopedToAgent(a.ID) {
			continue
		}
		own[p.Tool.Name] = true
	}
	for _, p := range AdoptedToolsFor(poolDB, owner) {
		resolved[p.Tool.Name] = true
	}
	return own, resolved
}

// agentMissingRefs is everything agent a references that its owner can no
// longer reach. own and resolved are toolResolution's answer, passed in by a
// caller that already computed them (the tool loader) and nil otherwise.
//
// Tools are judged by the adoption list, not by AllowedTools alone. A name on
// the allow-list that resolves to nothing could be a framework tool that is
// off in this deployment, a connector's, an MCP proxy's; telling the model its
// owner withdrew it would be a claim nobody checked. A name the owner TOOK from
// somebody is a claim somebody made, and it is exactly what a revoke leaves
// behind.
func agentMissingRefs(a AgentRecord, poolDB Database, owner string, own, resolved map[string]bool) []missingRef {
	var out []missingRef
	if strings.TrimSpace(owner) == "" || isBuilderAgent(a.ID) {
		return nil
	}
	if adopted := LoadAdoptedGlobalTools(poolDB, owner); len(adopted) > 0 {
		if own == nil || resolved == nil {
			own, resolved = toolResolution(a, poolDB, owner)
		}
		names := make([]string, 0, len(adopted))
		for name := range adopted {
			if !resolved[name] && !own[name] && agentLoadsPoolTool(a, name) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, missingRef{Kind: "tool", ID: n, Name: n})
		}
	}
	if !a.DisableSkills && len(a.AllowedSkills) > 0 {
		have := map[string]bool{}
		for _, s := range AvailableSkills(poolDB, owner) {
			have[s.ID] = true
		}
		for _, id := range a.AllowedSkills {
			if id = strings.TrimSpace(id); id != "" && !have[id] {
				out = append(out, missingRef{Kind: "skill", ID: id, Name: missingRefName("skill", id)})
			}
		}
	}
	if len(a.AttachedCollections) > 0 {
		// The retrieval gate itself, so a collection is missing here exactly
		// when the search path leaves it out.
		_, withheld := agentCorpusSourceSet(owner, owner, a.AttachedCollections, nil)
		for _, id := range withheld {
			out = append(out, missingRef{Kind: "collection", ID: id, Name: missingRefName("collection", id)})
		}
	}
	return out
}

// ----------------------------------------------------------------------
// The runtime: one note per turn, one breadcrumb per session
// ----------------------------------------------------------------------

// pendingMissing is what a run found missing, waiting for the loop to start.
//
// Found where the tools are loaded, because that is the one step every run
// path shares; said where the loop starts (TurnNotes), because the note has to
// ride the newest user turn and the breadcrumb needs the session a dispatch
// path only names after the catalog is built.
type pendingMissing struct {
	note   string
	report func() // the breadcrumb and owner notice; run once, then nil
}

var missingByRun = struct {
	sync.Mutex
	m map[string]*pendingMissing
}{m: map[string]*pendingMissing{}}

// missingRunKey is who is running which agent. The answer depends only on the
// agent and its owner, and the owner follows from the two.
func missingRunKey(runtimeUser, agentID string) string {
	return runtimeUser + "\x00" + agentID
}

// trackMissingDependencies records what this run's agent references and cannot
// reach, for dependencyTurnNote to deliver. An empty answer clears the entry,
// so a reference fixed between turns stops being reported on the next one.
//
// Only for a session that names its agent: a page listing an agent's tools
// builds a session to do it, and is not a run anybody should hear about.
func (t *chatTurn) trackMissingDependencies(sess *ToolSession, owner string, poolDB Database, own, resolved map[string]bool) {
	if t == nil || sess == nil || strings.TrimSpace(sess.AgentID) == "" {
		return
	}
	refs := agentMissingRefs(t.agent, poolDB, owner, own, resolved)
	key := missingRunKey(sess.Username, sess.AgentID)
	missingByRun.Lock()
	defer missingByRun.Unlock()
	if len(refs) == 0 {
		delete(missingByRun.m, key)
		return
	}
	// Debug, not Log: this runs on every tool load, several times a turn. The
	// durable record is the session's one breadcrumb.
	Debug("[orchestrate.deps] agent=%s (owner %s) references %d thing(s) it can no longer reach: %s",
		t.agent.ID, owner, len(refs), missingRefList(refs))
	missingByRun.m[key] = &pendingMissing{
		note:   missingDepsNote(refs),
		report: func() { t.reportMissingDependencies(owner, refs) },
	}
}

// dependencyTurnNote is the TurnNotes piece: the note for this run's agent, and
// on its first delivery the breadcrumb that goes with it.
func dependencyTurnNote(sess *ToolSession) string {
	if sess == nil {
		return ""
	}
	missingByRun.Lock()
	p := missingByRun.m[missingRunKey(sess.Username, sess.AgentID)]
	var report func()
	note := ""
	if p != nil {
		note, report = p.note, p.report
		p.report = nil // the entry holds the turn only until it has reported
	}
	missingByRun.Unlock()
	if report != nil {
		report()
	}
	return note
}

// missingDepsNote is what the model is told, every turn the gap stands: the
// note rides the turn rather than the history, so it has to be said again.
func missingDepsNote(refs []missingRef) string {
	if len(refs) == 1 {
		r := refs[0]
		return frameworkNoteTag + "\"" + r.Name + "\" (" + missingKindPhrase(r.Kind) + " this agent uses) is no longer available to you - its owner withdrew or deleted it. " +
			"Do not claim to use it; say so if the user asks for what it did."
	}
	return frameworkNoteTag + "These things this agent uses are no longer available to you - their owners withdrew or deleted them: " + missingRefList(refs) + ". " +
		"Do not claim to use them; say so if the user asks for what they did."
}

// missingRefList names refs for a sentence: "wiki" (tool), "Runbooks" (knowledge).
func missingRefList(refs []missingRef) string {
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		parts = append(parts, "\""+r.Name+"\" ("+missingKindWord(r.Kind)+")")
	}
	return strings.Join(parts, ", ")
}

// missingDiagSeen remembers which sessions have their breadcrumb, per set of
// missing things, so a gap that stands for fifty turns is one entry on the
// trail and a NEW gap in the same session is another. In memory: a restart
// costs one repeat per session, which is cheaper than a table for it.
var missingDiagSeen = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

const missingDiagSeenCap = 4096

// reportMissingDependencies leaves the session's one breadcrumb and, when
// somebody other than the owner is running the agent, tells the owner.
func (t *chatTurn) reportMissingDependencies(owner string, refs []missingRef) {
	trail := ""
	if t.session != nil {
		trail = t.session.ID
	} else {
		trail = t.diagSessionID
	}
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.Kind+":"+r.ID)
	}
	key := t.agent.ID + "\x00" + trail + "\x00" + strings.Join(ids, ",")
	missingDiagSeen.Lock()
	if missingDiagSeen.m[key] {
		missingDiagSeen.Unlock()
		return
	}
	if len(missingDiagSeen.m) >= missingDiagSeenCap {
		missingDiagSeen.m = map[string]bool{}
	}
	missingDiagSeen.m[key] = true
	missingDiagSeen.Unlock()
	t.turnDiag("dependency-missing", "This agent references "+missingRefList(refs)+
		", which it can no longer reach (withdrawn by the owner or deleted), so it runs without them. "+
		"Remove them in the agent's Tools, Skills or Knowledge, or ask whoever shared them to share them again.")
	notifyMissingDependencies(owner, t.user, t.agent.ID, refs)
}

// ----------------------------------------------------------------------
// Names for things that are gone
// ----------------------------------------------------------------------

// withdrawnNamesTable keeps the last name of a skill or collection that
// stopped reaching somebody, keyed "kind:id". Written when the owner takes it
// back (shareledger.OnWithdrawn), which is the last moment anybody knows it.
const withdrawnNamesTable = "withdrawn_names"

var missingNameCache = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

// missingRefName recovers what to call a reference that no longer resolves for
// its holder. A revoked record still exists under its owner and is found by
// id; a deleted one is known only if its withdrawal left the name behind.
// Looked up only for references already known to be missing, and cached,
// because the fallback is a walk over every user.
func missingRefName(kind, id string) string {
	if kind == "tool" {
		return id // a tool is referenced by its name
	}
	key := kind + ":" + id
	missingNameCache.Lock()
	name, ok := missingNameCache.m[key]
	missingNameCache.Unlock()
	if ok {
		return name
	}
	name = lookupMissingName(kind, id)
	if name == "" {
		name = id
	}
	missingNameCache.Lock()
	if len(missingNameCache.m) >= missingDiagSeenCap {
		missingNameCache.m = map[string]string{}
	}
	missingNameCache.m[key] = name
	missingNameCache.Unlock()
	return name
}

func lookupMissingName(kind, id string) string {
	if orchestrateBaseDB != nil {
		var name string
		if orchestrateBaseDB.Get(withdrawnNamesTable, kind+":"+id, &name) && strings.TrimSpace(name) != "" {
			return name
		}
	}
	switch kind {
	case "skill":
		for _, s := range DeploymentSkills(nil) {
			if s.ID == id {
				return s.Name
			}
		}
		for _, u := range deploymentUsers() {
			for _, s := range append(LoadSkills(nil, u), PublishedSkillsBy(nil, u)...) {
				if s.ID == id {
					return s.Name
				}
			}
		}
	case "collection":
		var c Collection
		if RootDB != nil && RootDB.Get(GlobalCollectionsTable, id, &c) && c.Name != "" {
			return c.Name
		}
		for _, u := range deploymentUsers() {
			if cdb := UserDB(CollectionsDB(), u); cdb != nil && cdb.Get(CollectionsTable, id, &c) && c.Name != "" {
				return c.Name
			}
		}
	}
	return ""
}

// rememberWithdrawnName is wired as shareledger.OnWithdrawn.
func rememberWithdrawnName(kind, owner, id, name string, _ []shareledger.Dependent) {
	if kind == "tool" || strings.TrimSpace(name) == "" || name == id || orchestrateBaseDB == nil {
		return
	}
	orchestrateBaseDB.Set(withdrawnNamesTable, kind+":"+id, name)
	missingNameCache.Lock()
	missingNameCache.m[kind+":"+id] = name
	missingNameCache.Unlock()
}

// deploymentUsers is every account, for the questions asked about everybody.
func deploymentUsers() []string {
	if AuthDB == nil {
		return nil
	}
	db := AuthDB()
	if db == nil {
		return nil
	}
	var out []string
	for _, u := range AuthListUsers(db) {
		if u.Username != "" {
			out = append(out, u.Username)
		}
	}
	sort.Strings(out)
	return out
}

// ----------------------------------------------------------------------
// Who depends on a shared thing (shareledger.FindDependents)
// ----------------------------------------------------------------------

func init() {
	shareledger.FindDependents = agentDependents
	shareledger.OnWithdrawn = rememberWithdrawnName
}

// agentDependents answers, for the sharing ledger, which of these users' own
// agents reference kind/id. Nil users means everybody.
//
// An agent here is one the user has in their picker: their own, plus the
// framework agents they can pick, since a default-pool Chat agent loads a taken
// tool as surely as one they built. Builder is left out: it loads everything by
// design, so naming it would be true of every tool and informative about none.
func agentDependents(kind, owner, id string, users []string) []shareledger.Dependent {
	if orchestrateBaseDB == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	if users == nil {
		users = deploymentUsers()
	}
	seen := map[string]bool{}
	var out []shareledger.Dependent
	for _, u := range users {
		if u = strings.TrimSpace(u); u == "" || u == owner || seen[u] {
			continue
		}
		seen[u] = true
		udb := UserDB(orchestrateBaseDB, u)
		if udb == nil {
			continue
		}
		var names []string
		for _, a := range pickerAgents(listAgents(udb, u)) {
			if isBuilderAgent(a.ID) || !agentReferences(a, kind, id) {
				continue
			}
			name := strings.TrimSpace(a.Name)
			if name == "" {
				name = a.ID
			}
			names = append(names, name)
		}
		if len(names) > 0 {
			out = append(out, shareledger.Dependent{User: u, Uses: names})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User < out[j].User })
	return out
}

// ----------------------------------------------------------------------
// The editor: GET what is missing, POST to drop one reference
// ----------------------------------------------------------------------

// handleAgentMissing serves /api/agents/<id>/missing.
//
// GET lists what the agent references and can no longer reach, with the last
// known names, so the Tools, Skills and Knowledge modals can show a gone thing
// as gone instead of hiding it (or showing a bare id). POST {kind, id} drops
// one reference: from the agent's own list, and for a tool that was TAKEN and
// no longer resolves, from the owner's adoption list too - that entry is what
// every one of their agents was loading it through.
func (T *OrchestrateApp) handleAgentMissing(w http.ResponseWriter, r *http.Request, user string, udb Database, id string) {
	a, ok := loadAgent(udb, id)
	if !ok || (a.Owner != "" && a.Owner != user && a.Owner != seedOwner) {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		refs := agentMissingRefs(a, udb, user, nil, nil)
		if refs == nil {
			refs = []missingRef{}
		}
		for i := range refs {
			if refs[i].Kind == "tool" {
				_, err := RecreateLostTool(udb, user, refs[i].ID, false)
				refs[i].Recreatable = err == nil
			}
		}
		labels := map[string]string{}
		for _, m := range refs {
			labels[m.ID] = m.Name
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": refs, "labels": labels})
	case http.MethodPost:
		if a.Locked {
			http.Error(w, "this agent is locked: unlock it (the 🔒 icon) before editing", http.StatusConflict)
			return
		}
		var body struct {
			Kind   string `json:"kind"`
			ID     string `json:"id"`
			Action string `json:"action"` // "" = remove the reference; "recreate" = take the tool back as one's own
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.ID) == "" {
			http.Error(w, "kind and id are required", http.StatusBadRequest)
			return
		}
		if body.Action == "recreate" {
			if strings.TrimSpace(body.Kind) != "tool" {
				http.Error(w, "only a tool can be recreated", http.StatusBadRequest)
				return
			}
			// The agent keeps naming the tool; the user's own copy now answers
			// to that name, so nothing on the agent changes.
			def, err := RecreateLostTool(udb, user, strings.TrimSpace(body.ID), true)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"recreated": def.Name})
			return
		}
		saved, err := dropAgentReference(udb, user, a, strings.TrimSpace(body.Kind), strings.TrimSpace(body.ID))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(saved)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// dropAgentReference removes one reference from the agent and saves it.
func dropAgentReference(udb Database, user string, a AgentRecord, kind, id string) (AgentRecord, error) {
	switch kind {
	case "tool":
		listed := false
		if len(a.AllowedTools) > 0 && !isNoToolsSentinel(a.AllowedTools) {
			var keep []string
			for _, n := range a.AllowedTools {
				if canonicalToolName(n) != id {
					keep = append(keep, n)
				} else {
					listed = true
				}
			}
			// An allow-list emptied by a removal must not read as "every
			// tool": the owner narrowed it, and removing the last name is
			// the narrowest it gets.
			if len(keep) == 0 {
				keep = []string{"__none__"}
			}
			a.AllowedTools = keep
		}
		// The adoption goes only when it no longer resolves. A working tool
		// taken for all of the owner's agents is not this agent's to drop.
		if _, resolved := toolResolution(a, udb, user); !resolved[id] && LoadAdoptedGlobalTools(udb, user)[id] {
			if err := SetGlobalToolAdopted(udb, user, id, "", false); err != nil {
				return a, err
			}
		}
		// Nothing on the record named it (a default-pool agent loads a taken
		// tool through the adoption alone): no save, so a framework agent does
		// not grow a stored copy for a change that was not made to it.
		if !listed {
			return a, nil
		}
	case "skill":
		a.AllowedSkills = withoutOne(a.AllowedSkills, id)
	case "collection":
		a.AttachedCollections = withoutOne(a.AttachedCollections, id)
	default:
		return a, errors.New("unknown kind " + kind)
	}
	a.Tools = nil // a synthesized view on GET; never stored
	return saveAgentAs(udb, a, "removed a missing "+missingKindWord(kind))
}

// keepUnlistedTools is the Tools modal's save held to what the modal showed.
//
// The modal draws a checkbox per catalog tool and posts the checked set. A name
// the agent's allow-list carries that has NO checkbox - a tool taken from a
// colleague, one withdrawn since, a registered tool outside the picker - was
// simply absent from what it posted, so every save quietly deleted it. The
// modal cannot have decided about a name it never showed; only an explicit
// Remove (dropAgentReference) takes one off.
func keepUnlistedTools(stored, submitted, catalog []string) []string {
	if len(stored) == 0 || isNoToolsSentinel(stored) {
		return submitted
	}
	inCatalog := make(map[string]bool, len(catalog))
	for _, n := range catalog {
		inCatalog[n] = true
	}
	var extras []string
	for _, n := range stored {
		if n = strings.TrimSpace(n); n != "" && n != "__none__" && !inCatalog[n] && !namedIn(extras, n) {
			extras = append(extras, n)
		}
	}
	if len(extras) == 0 {
		return submitted
	}
	var out []string
	switch {
	case isNoToolsSentinel(submitted):
		// Nothing in the catalog: the names outside it are still allowed.
	case len(submitted) == 0:
		// Every box checked collapses to "the default pool" only when that
		// loses nothing. With names outside the catalog, it becomes the
		// literal list, or the collapse would drop them.
		out = append(out, catalog...)
	default:
		out = append(out, submitted...)
	}
	for _, n := range extras {
		if !namedIn(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// workerToolCatalogNames is what the Tools modal draws a checkbox for.
func workerToolCatalogNames(user string) []string {
	opts := availableWorkerToolOptions(user)
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.Value)
	}
	return out
}
