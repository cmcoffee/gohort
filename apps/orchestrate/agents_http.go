package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// agentList is GET /api/agents: the caller's agents, or with
// role=dispatch-target the targets an agent may dispatch to.
func (T *OrchestrateApp) agentList(c *jsonapi.Call) (any, error) {
	r, user, udb := c.R, c.User, c.UDB
	agents := listAgents(udb, user)
	// Hidden app agents and clone-only templates are app/framework internals
	// on EVERY form of this endpoint — the bare list feeds the channel
	// re-point dropdown, and offering the Servitor Investigator there let a
	// channel be pointed at an agent no user is meant to reach.
	{
		kept := agents[:0]
		for _, a := range agents {
			if hiddenAppAgent(a.ID) {
				continue
			}
			kept = append(kept, a)
		}
		agents = kept
	}
	// role=dispatch-target scopes the list to agents that can actually be
	// dispatch TARGETS — for the editor's "Dispatch target list" picker.
	// Drops what agents(action="run") would refuse anyway: Builder (never
	// dispatchable), retired framework seeds (seed-chat), and the agent
	// being edited (self-dispatch is impossible). Listing them let a user
	// pick a target the dispatch gate then silently ignores.
	if strings.EqualFold(r.URL.Query().Get("role"), "dispatch-target") {
		self := strings.TrimSpace(r.URL.Query().Get("self"))
		kept := agents[:0]
		for _, a := range agents {
			if a.ID == self || isBuilderAgent(a.ID) || isFleetRetiredSeed(a.ID) || isRetiringArchetypeSeed(a.ID) {
				continue
			}
			kept = append(kept, a)
		}
		agents = kept
		// Pipelines are dispatch targets too, so they belong in the list
		// that decides which targets are reachable. Offering only agents is
		// what made that list unable to express a pipeline grant — and a
		// policy the UI cannot state is a policy the gate cannot enforce.
		// Encoded to the three fields the picker reads (id / name /
		// description), with the kind said out loud in the description so
		// a reader can tell what they are ticking.
		// group: the picker sections them (GroupByField), so an app's agent
		// is not offered as one of the person's own.
		out := make([]map[string]any, 0, len(agents))
		for _, a := range agents {
			if !isAppAgent(a.ID) {
				out = append(out, map[string]any{"id": a.ID, "name": a.Name, "description": a.Description, "group": "Agents"})
			}
		}
		for _, d := range ListPipelineDefs(udb, user) {
			desc := "Pipeline"
			if s := strings.TrimSpace(d.Description); s != "" {
				desc += " - " + s
			}
			out = append(out, map[string]any{"id": d.ID, "name": d.Name, "description": desc, "group": "Pipelines"})
		}
		// Machines that RUN, for the same reason: they are dispatch
		// targets, so a list that decides which targets are reachable has
		// to be able to name them. A machine that CONVERSES is left out —
		// dispatch refuses it whatever the list says, and offering it would
		// be a tick that changes nothing.
		for _, d := range ListMachineDefs(udb, user) {
			if !d.Unattended {
				continue
			}
			desc := "Machine"
			if s := strings.TrimSpace(d.Description); s != "" {
				desc += " - " + s
			}
			out = append(out, map[string]any{"id": d.ID, "name": d.Name, "description": desc, "group": "Machines"})
		}
		// App agents last, under their app.
		var apps []map[string]any
		for _, a := range agents {
			if g, ok := agentAppGroup(a); ok {
				apps = append(apps, map[string]any{"id": a.ID, "name": a.Name, "description": a.Description, "group": g})
			}
		}
		sort.SliceStable(apps, func(i, j int) bool { return apps[i]["group"].(string) < apps[j]["group"].(string) })
		out = append(out, apps...)
		return out, nil
	}
	// The bare list feeds the channel re-point dropdown, which sections
	// it by group: the person's agents, then each app's.
	type listed struct {
		AgentRecord
		Group string `json:"group,omitempty"`
	}
	rows := make([]listed, 0, len(agents))
	for _, a := range agents {
		rows = append(rows, listed{AgentRecord: a, Group: agentGroupFor(a, "Your agents")})
	}
	sort.SliceStable(rows, func(i, j int) bool { return appGroupsLast(rows[i].Group, rows[j].Group) })
	return rows, nil
}

// agentSave is POST /api/agents: the editor's whole-record save, new or
// existing. The protected fields keep their stored values (see below).
func (T *OrchestrateApp) agentSave(c *jsonapi.Call, raw json.RawMessage) (any, error) {
	r, user, udb := c.R, c.User, c.UDB
	// Read once, decode twice. The record decode is what saves; the
	// key probe is what tells a field the caller OMITTED apart from a
	// field they deliberately CLEARED. Go's zero values can't express
	// that difference, and for `machine` it is the difference between
	// "the Rules modal posted a record that never mentioned it" and
	// "the user picked None in the dropdown".
	var req AgentRecord
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, jsonapi.BadRequest("bad request")
	}
	var sent map[string]json.RawMessage
	_ = json.Unmarshal(raw, &sent)
	_, sentMachine := sent["machine"]
	_, sentBulletins := sent["bulletins"]
	applyEditorChoices(RootDB, &req, sent)
	req.Owner = user
	// Seed-IDs are saved in place as a per-user shadow record;
	// the in-code seed stays untouched and surfaces back if the
	// user later deletes the shadow (= revert). Non-seed IDs
	// must already belong to the caller to mutate; unknown IDs
	// fall through and saveAgent treats them as new.
	if req.ID != "" && !isSeedID(req.ID) {
		existing, ok := loadAgent(udb, req.ID)
		if !ok {
			req.ID = "" // treat as new
		} else if existing.Owner != user {
			return nil, jsonapi.Forbidden("not your agent")
		} else {
			// Where the agent lives changes only through the move
			// endpoint, which checks the destination. A whole-record
			// save echoes it at best and re-parents unchecked at worst.
			req.OwnedBy = existing.OwnedBy
			// Guardrails are owned by the dedicated guardrails endpoint
			// (handleAgentGuardrails), never the whole-record form — a
			// wholesale-replace save must NOT be able to weaken or clear
			// them, which is the entire point of them being un-rewritable
			// by the agent's own edit paths. Preserve from the stored copy.
			req.Guardrails = existing.Guardrails
			req.GuardrailHooks = existing.GuardrailHooks
			req.GuardrailFailClosed = existing.GuardrailFailClosed
			req.GuardrailDepth = existing.GuardrailDepth
			req.GuardrailDeclines = existing.GuardrailDeclines
			req.GuardrailsDisabled = existing.GuardrailsDisabled
			req.AuthorizedIdentities = existing.AuthorizedIdentities
			req.GuardrailExceptions = existing.GuardrailExceptions
			// Scan scope is owner-only for the same reason and preserved the
			// same way: an agent that can widen its own scan scope, or turn
			// the scanner off, has no scanner.
			req.ScanToolResults = existing.ScanToolResults
			req.ScanToolsAdd = existing.ScanToolsAdd
			req.ScanToolsSkip = existing.ScanToolsSkip
			req.ScanAction = existing.ScanAction
			req.ScanBlockTools = existing.ScanBlockTools
			req.ScanAppealable = existing.ScanAppealable
			req.ScanTightenDisabled = existing.ScanTightenDisabled
			req.ScanTrustedSources = existing.ScanTrustedSources
			// The machine picker only renders when the user HAS
			// machines (machineSelectField hides itself otherwise), and
			// modals post records built from other forms entirely — so
			// a body that never mentioned `machine` must not clear it,
			// while one that sent "" must. Hence the key probe above.
			if !sentMachine {
				req.Machine = existing.Machine
			}
			// The same for the boards it follows: the Bulletins pills
			// save them without this form, and a form posted from
			// elsewhere must not unsubscribe the agent.
			if !sentBulletins {
				req.Bulletins = existing.Bulletins
			}
		}
	} else if isSeedID(req.ID) {
		// Seeds save as a per-user shadow. The form carries no `locked`
		// field (the icon owns it), so preserve the stored lock from the
		// existing shadow or it would clear on every save.
		if existing, ok := loadAgent(udb, req.ID); ok {
			// (Locked: preserved by saveAgent itself now.)
			req.Guardrails = existing.Guardrails
			req.GuardrailHooks = existing.GuardrailHooks
			req.GuardrailFailClosed = existing.GuardrailFailClosed
			req.GuardrailDepth = existing.GuardrailDepth
			req.GuardrailDeclines = existing.GuardrailDeclines
			req.GuardrailsDisabled = existing.GuardrailsDisabled
			req.AuthorizedIdentities = existing.AuthorizedIdentities
			req.GuardrailExceptions = existing.GuardrailExceptions
			req.ScanToolResults = existing.ScanToolResults
			req.ScanToolsAdd = existing.ScanToolsAdd
			req.ScanToolsSkip = existing.ScanToolsSkip
			req.ScanAction = existing.ScanAction
			req.ScanBlockTools = existing.ScanBlockTools
			req.ScanAppealable = existing.ScanAppealable
			req.ScanTightenDisabled = existing.ScanTightenDisabled
			req.ScanTrustedSources = existing.ScanTrustedSources
			if !sentMachine {
				req.Machine = existing.Machine // see above
			}
			if !sentBulletins {
				req.Bulletins = existing.Bulletins // see above
			}
		}
	}
	// Only the Tools modal may recompute tool curation. Its save is the only
	// payload whose AllowedTools carries the CHECKED temp tools; every other
	// whole-record saver (the Rules modal, the editor form) round-trips the
	// stored list, which never contains them — so folding on those saves
	// re-denied every shared temp tool on the seed. From the owner's side:
	// "every time I enable it, it gets disabled", by a save on a page with
	// no tool checkboxes on it. Same preservation pattern as the guardrail
	// fields above: a form that doesn't show a control must not rewrite it.
	fromToolsModal := r.URL.Query().Get("tools_modal") == "1"
	if isSeedID(req.ID) && !fromToolsModal {
		if existing, ok := loadAgent(udb, req.ID); ok {
			req.DisabledPersistentTools = existing.DisabledPersistentTools
		}
	}
	if fromToolsModal {
		// Names the modal had no checkbox for survive its save; only an
		// explicit Remove (api/agents/<id>/missing) takes one off.
		if existing, ok := loadAgent(udb, req.ID); ok && req.ID != "" {
			req.AllowedTools = keepUnlistedTools(existing.AllowedTools, req.AllowedTools, workerToolCatalogNames(user))
		}
		curateToolsFromModal(T.DB, user, &req)
	}
	// Reach that needs an administrator (everyone, inbound MCP) changes
	// only through the publish door, which files a promotion request for a
	// non-admin (agentPublishNeedsApproval). A whole-record save keeps the
	// stored values, and a new record starts unpublished.
	if !requestIsAdminAgent(r) {
		req.Exposed, req.Everyone, req.MCPExposed = false, false, false
		if req.ID != "" {
			if existing, ok := loadAgent(udb, req.ID); ok {
				req.Everyone, req.MCPExposed = existing.Everyone, existing.MCPExposed
			}
		}
	}
	// Flattened namespace: tools live in the unified store; the GET view
	// synthesizes them onto the record, so a full-form save must never
	// write that view back into storage.
	req.Tools = nil
	// A record with no id is a NEW agent — stamp the starting hook set so it
	// does not fall through to resolveGuardrailHooks' broader default. Only
	// when the caller sent none: the Rules modal posts the whole record, and
	// an owner who deliberately cleared every hook must stay cleared.
	if req.ID == "" && len(req.GuardrailHooks) == 0 {
		req.GuardrailHooks = defaultNewAgentGuardrailHooks()
		// Safe to set unconditionally on a NEW record: the create form
		// carries no fail-closed control, so a false here means "not
		// asked", never "deliberately open". The Rules modal owns the
		// setting afterwards and reaches it through the guardrails
		// endpoint, which this branch never runs for.
		req.GuardrailFailClosed = defaultNewAgentFailClosed
	}
	// Kept from tonight, and not part of the permission control that was
	// reverted with it: a Tools-modal save filed as a bare "update" beside
	// a persona rewrite, so a working session left a column of identical
	// rows with nothing to scan for.
	reason := "update"
	if fromToolsModal {
		reason = "changed tools"
	}
	saved, err := saveAgentAs(udb, req, reason)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return saved, nil
}

// patchAgentFields is the allowlist of keys PATCH may set.
//
// An allowlist, not "everything except a denylist". PATCH merges onto the
// STORED record, so a key it accepts is a key that survives; the safety
// question is what we vouch for, not what we happened to think of. The fields
// with their own protected endpoints are absent BY NAME so a partial save can
// never reach them:
//
//   - guardrails / guardrail_hooks / guardrail_fail_closed / guardrail_declines
//     — owner-only, and the whole point is that no ordinary agent edit path can
//     weaken the rule it is about to be checked against
//   - scan_tool_results / scan_tools_add / scan_tools_skip / scan_action /
//     scan_block_tools / scan_appealable / scan_tighten_disabled /
//     scan_trusted_sources — same reasoning one
//     layer out: a partial save that could turn the injection scanner off, or
//     add a tool to its skip list, is a partial save that can arrange to be
//     unwatched
//   - owned_by — moving an agent is /api/agents/<id>/move, which checks where
//   - id / owner / created — identity, not settings
var patchAgentFields = map[string]bool{
	"name": true, "description": true, "category": true, "orchestrator_prompt": true,
	"plan_guidance": true, "rules": true, "triggers": true,
	"allowed_tools": true, "auto_approve_tools": true, "allowed_skills": true,
	"attached_collections": true, "attached_pipelines": true,
	// The boards it follows, set from the Knowledge dialog beside its
	// collections. A board's post is read, and a follower gains no reach.
	"bulletins": true,
	// The machine it runs under: the partial save machines_http.go names
	// for attaching one, and a field the whole-record form takes too.
	"machine":                  true,
	"allowed_dispatch_targets": true, "allowed_users": true,
	// How a share reaches its recipients, beside the list of who they are.
	// These widen what a recipient can READ and never what they can change,
	// which is why they sit here and not with the owner-only names above.
	"share_hold_cortex": true, "share_hold_reference": true,
	"share_memory_explicit": true, "share_no_uploads": true,
	"max_plan_steps": true, "max_worker_rounds": true, "think": true,
	"think_budget": true, "effort": true, "context_depth": true, "gap_check": true,
	"action_quotas": true, "daily_spend_usd": true,
	// lead_plans and lead_review with lead_model: the editor's lead_use
	// writes all of them (and consult_lead), and leaving one off refused
	// that choice with a 400.
	"lead_model": true, "lead_plans": true, "lead_review": true, "memory_mode": true, "session_recall": true, "disable_explicit": true,
	"disable_inferred": true, "disable_compaction": true, "recall_hints": true, "keeps_memory": true,
	"capture_prompt": true,
	"allow_explorer": true, "explorer_hard_cap": true,
	"channel": true, "fleet": true, "tag_name": true,
	"exposed": true, "mcp_exposed": true, "show_on_dashboard": true, "public_name": true,
	"allow_private_mode": true, "force_private": true, "hidden": true,
	"workspace_no_network": true, "workspace_network": true,
	"share_cortex": true, "share_reference": true, "share_notes": true, "share_uploads": true, "disabled_tool_actions": true,
	"allow_builder_dispatch": true, "dispatch_mode": true,
	"inbound_mode": true, "allowed_callers": true, "consult_lead": true,
	"intake_form": true, "work_plan": true,
	"run_priority": true, // validated below: only the two named levels
}

// patchAgent merges a partial update into an existing agent.
//
// Exists so ONE record can be edited from several forms. A FormPanel POSTs the
// fields IT holds as the whole record, so splitting a long editor across
// page-level sections used to mean each section's save wiped every field it
// didn't carry. PATCH sends just what changed and merges it onto the stored
// copy, which makes that split safe.
//
// Round-trips through JSON rather than reflecting over the struct: the field
// names are the ones the form already speaks, and re-marshalling the stored
// record means every field the caller did NOT send keeps exactly the value it
// had, including ones no form knows about.
// pathID is the agent named in the URL PATH, for the RESTful spelling
// (PATCH /api/agents/<id>). Empty falls back to the query then the body, which
// is what the collection route has always done.
func (T *OrchestrateApp) patchAgent(c *jsonapi.Call, patch map[string]any, pathID string) (any, error) {
	r, user, udb := c.R, c.User, c.UDB
	// The id may come in the PATH, the query, or the body. A FormPanel's PATCH
	// body is exactly {changed_field: value} with no record id in it, so a form
	// names its target in the URL instead.
	id := strings.TrimSpace(pathID)
	if id == "" {
		id = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	if id == "" && patch["id"] != nil {
		id = strings.TrimSpace(fmt.Sprint(patch["id"]))
	}
	if id == "" {
		return nil, jsonapi.BadRequest("id is required for PATCH (in the body or as ?id=)")
	}
	existing, ok := loadAgent(udb, id)
	if !ok {
		return nil, jsonapi.NotFound("agent not found")
	}
	if existing.Owner != "" && existing.Owner != user && existing.Owner != seedOwner {
		return nil, jsonapi.Forbidden("not your agent")
	}
	// Merge: start from the stored record's own JSON so untouched fields keep
	// their exact values, then overlay only allowlisted keys.
	blob, err := json.Marshal(existing)
	if err != nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "encode failed")
	}
	var merged map[string]any
	if err := json.Unmarshal(blob, &merged); err != nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "decode failed")
	}
	// lead_use is the editor's one choice over two stored fields: translated
	// into them here, so the loop below applies it like any other field.
	if v, ok := patch["lead_use"]; ok {
		delete(patch, "lead_use")
		choice := existing
		applyLeadUse(RootDB, &choice, fmt.Sprint(v))
		patch["lead_model"] = choice.LeadModel
		patch["lead_plans"] = choice.LeadPlans
		patch["lead_review"] = choice.LeadReview
		patch["consult_lead"] = choice.ConsultLead
	}
	// reasoning is the same over Think and Effort.
	if v, ok := patch["reasoning"]; ok {
		delete(patch, "reasoning")
		choice := existing
		applyReasoning(&choice, fmt.Sprint(v))
		patch["think"] = choice.Think
		patch["effort"] = choice.Effort
	}
	applied := make([]string, 0, len(patch))
	var refused, requested []string
	for k, v := range patch {
		if k == "id" {
			continue
		}
		if !patchAgentFields[k] {
			// Refuse an attempt to CHANGE a protected field, not an echo of
			// what it already holds.
			//
			// A FormPanel loads the record and sends back everything it
			// loaded, not only the fields it draws, so a panel owning four
			// toggles PATCHes the whole agent - protected keys included, at
			// their current values. Refusing those made every panel on the
			// Security page fail with a list of fields nobody had touched.
			//
			// Unchanged is a no-op, and a door that exists to stop a field
			// being set through the wrong route has nothing to stop when
			// nothing is being set. An actual change is still refused, which
			// is the case the guard was written for.
			if sameJSONValue(merged[k], v) {
				continue
			}
			refused = append(refused, k)
			continue
		}
		// The two REACH flags are an administrator's to grant, wherever they
		// are written. This door applied them directly, so an owner who could
		// not flip the toggle on the privileges card could publish to the
		// whole deployment by PATCHing the same field — one rule with one
		// enforcement point and one way round it.
		//
		// Turning either OFF stays direct here as it does there: nobody needs
		// permission to stop sharing.
		if k == "exposed" || k == "mcp_exposed" {
			// Read tolerantly and then STORE what was read. A select sends
			// "true" where a toggle sends true, and a strict v == true made
			// that string read as OFF - so a control meaning "publish this"
			// would have quietly unpublished instead, and the merged map
			// would then have carried a string into a bool field and failed
			// the whole patch on re-decode.
			//
			// Anything unrecognised reads as OFF, which is the narrowing
			// direction: a value nobody can parse must not grant reach.
			on := truthyPatchValue(v)
			v = on
			already := existing.Everyone
			if k == "mcp_exposed" {
				already = existing.MCPExposed
			}
			if T.agentPublishNeedsApproval(r, user, existing.ID, k, on, already) {
				requested = append(requested, k)
				continue
			}
		}
		if k == "run_priority" {
			switch p := strings.TrimSpace(fmt.Sprint(v)); p {
			case "", "lowest", "urgent":
				v = p
			default:
				return nil, jsonapi.BadRequest("run_priority must be empty, \"lowest\" or \"urgent\"")
			}
		}
		merged[k] = v
		applied = append(applied, k)
	}
	if len(refused) > 0 {
		sort.Strings(refused)
		return nil, jsonapi.BadRequest("these fields cannot be set through PATCH (they have their own protected endpoints): " + strings.Join(refused, ", "))
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return nil, jsonapi.Status(http.StatusInternalServerError, "encode failed")
	}
	var rec AgentRecord
	if err := json.Unmarshal(out, &rec); err != nil {
		return nil, jsonapi.BadRequest("bad field value: " + err.Error())
	}
	rec.ID = existing.ID
	rec.Owner = existing.Owner
	if rec.Owner == "" || rec.Owner == seedOwner {
		rec.Owner = user
	}
	saved, err := saveAgent(udb, rec)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	sort.Strings(applied)
	if len(requested) > 0 {
		sort.Strings(requested)
		Log("[orchestrate.agents] PATCH agent=%s filed a publish request for %v", id, requested)
	}
	Log("[orchestrate.agents] PATCH agent=%s fields=%v", id, applied)
	return saved, nil
}

// registerAgentRoutes mounts one agent's routes: the collection, the record,
// each action, and the Memory pane (registerMemoryRoutes).
func (T *OrchestrateApp) registerAgentRoutes() {
	T.HandleFunc("/api/agents", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.agentList),
		"POST": jsonapi.Post(T.DB, T.agentSave),
		"PATCH": jsonapi.Patch(T.DB, func(c *jsonapi.Call, patch map[string]any) (any, error) {
			return T.patchAgent(c, patch, "")
		}),
	}.Serve)
	// PATCH /api/agents/<id> is the RESTful spelling of the collection's
	// PATCH /api/agents?id=<id>, and the one any client writes first: the
	// agent assistant's Apply button shipped asking for it. A single-field
	// surface may POST just its field here too. Both are the SAME patchAgent,
	// so the same patchAgentFields allowlist applies: guardrails and the
	// injection-scan settings stay absent by name, and a second spelling of
	// an operation must never be a second set of rules for it. (POST here used
	// to decode straight onto the stored record with no allowlist.)
	patchOne := func(c *jsonapi.Call, patch map[string]any) (any, error) {
		return T.patchAgent(c, patch, c.R.PathValue("id"))
	}
	T.HandleFunc("/api/agents/{id}", jsonapi.Methods{
		"GET":    jsonapi.Get(T.DB, T.agentGet),
		"POST":   jsonapi.Post(T.DB, patchOne),
		"PATCH":  jsonapi.Patch(T.DB, patchOne),
		"DELETE": jsonapi.Delete(T.DB, T.agentDelete),
	}.Serve)
	T.HandleFunc("/api/agents/{id}/clone", jsonapi.Post(T.DB, T.agentClone, jsonapi.EmptyBodyOK))
	T.HandleFunc("/api/agents/{id}/detach", jsonapi.Post(T.DB, T.agentDetach))
	T.HandleFunc("/api/agents/{id}/move", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.agentMoveOptions),
		"POST": jsonapi.Post(T.DB, T.agentMove),
	}.Serve)
	// What deleting this agent would park, for its delete confirmation, and
	// what runs through it (agent_used_by.go).
	T.HandleFunc("/api/agents/{id}/schedules", jsonapi.Get(T.DB, T.agentSchedules))
	T.HandleFunc("/api/agents/{id}/used-by", jsonapi.Get(T.DB, T.agentUses))
	T.HandleFunc("/api/agents/{id}/reach", jsonapi.Get(T.DB, T.agentReach))
	T.HandleFunc("/api/agents/{id}/reach/credential", jsonapi.Post(T.DB, T.agentCredentialDecision))
	T.HandleFunc("/api/agents/{id}/missing", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.agentMissing),
		"POST": jsonapi.Post(T.DB, T.agentMissingFix),
	}.Serve)
	T.HandleFunc("/api/agents/{id}/guardrails", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.agentGuardrails),
		"POST": jsonapi.Post(T.DB, T.agentGuardrailsSave),
	}.Serve)
	T.HandleFunc("/api/agents/{id}/guardrail-depth", jsonapi.Methods{
		"GET":  jsonapi.Get(T.DB, T.agentGuardrailDepth),
		"POST": jsonapi.Post(T.DB, T.agentGuardrailDepthSave),
	}.Serve)
	T.HandleFunc("/api/agents/{id}/guardrail-test", jsonapi.Post(T.DB, T.agentGuardrailTest, jsonapi.EmptyBodyOK))
	T.HandleFunc("/api/agents/{id}/decline-suggest", jsonapi.Post(T.DB, T.agentDeclineSuggest))
	// Not JSON in and out: a download, an upload, the ✨ assist and the
	// revisions surface. They resolve the caller themselves.
	T.HandleFunc("/api/agents/{id}/export", T.signedIn(T.handleAgentExport))
	T.HandleFunc("/api/agents/{id}/knowledge/upload", T.signedIn(func(w http.ResponseWriter, r *http.Request, user string, _ Database) {
		T.handleAgentKnowledgeUpload(w, r, user, r.PathValue("id"))
	}))
	T.HandleFunc("/api/agents/{id}/assist", T.signedIn(func(w http.ResponseWriter, r *http.Request, user string, udb Database) {
		T.handleAgentAssist(w, r, user, udb, r.PathValue("id"))
	}))
	// Revisions: the list, one version read-only, and the way back.
	revisions := T.signedIn(func(w http.ResponseWriter, r *http.Request, user string, _ Database) {
		T.handleAgentRevisions(w, r, user, r.PathValue("id"), r.PathValue("action"))
	})
	T.HandleFunc("/api/agents/{id}/revisions", revisions)
	T.HandleFunc("/api/agents/{id}/revisions/{action}", revisions)
	T.registerMemoryRoutes()
}

// signedIn is a raw route's sign-in: h runs for a signed-in caller, with
// their store.
func (T *OrchestrateApp) signedIn(h func(w http.ResponseWriter, r *http.Request, user string, udb Database)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, udb, ok := RequireUser(w, r, T.DB)
		if !ok {
			return
		}
		h(w, r, user, udb)
	}
}

// agentGet is the record the editor loads, the caller's own or a seed.
func (T *OrchestrateApp) agentGet(c *jsonapi.Call) (any, error) {
	a, ok := loadAgent(c.UDB, c.R.PathValue("id"))
	if !ok || (a.Owner != c.User && a.Owner != seedOwner) {
		return nil, errNotFound
	}
	// Flattened namespace: the record stores no tools; the GET response
	// synthesizes the `tools` array from the unified store (rows scoped to
	// this agent) as a VIEW — the Tools modal renders it unchanged. A fork
	// between a pool copy and a record copy is structurally impossible
	// now, so the old pool_diverged_tools computation is gone. patchAgent
	// and agentSave strip Tools before save, so the fetch-modify-post
	// round-trip can't write the view back into storage.
	a.Tools = toolsOfScoped(AgentScopedTools(c.UDB, c.User, a.ID))
	// With lead_use and reasoning: the editor's one choice over
	// LeadModel + ConsultLead, and over Think + Effort.
	return withEditorChoices(RootDB, a), nil
}

// agentDelete removes the agent, and with ?schedules=delete its schedules.
func (T *OrchestrateApp) agentDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	user, udb, id := c.User, c.UDB, c.R.PathValue("id")
	// The family is read before the delete, which removes the sub-agents
	// whose schedules go with it.
	family := agentFamilyIDs(udb, user, id)
	if err := deleteAgent(udb, id, user); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	// Asked to take its schedules with it, rather than leave them
	// parked for a replacement (parked_schedules.go).
	if c.R.URL.Query().Get("schedules") == "delete" {
		if n := deleteAgentSchedules(user, family); n > 0 {
			Log("[orchestrate.agents] deleted %d schedule(s) with agent %s", n, id)
		}
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// agentClone copies the agent under a new id, optionally renamed.
func (T *OrchestrateApp) agentClone(c *jsonapi.Call, body struct {
	Name    string `json:"name,omitempty"`
	Promote bool   `json:"promote,omitempty"`
}) (any, error) {
	clone, err := cloneAgent(c.UDB, c.R.PathValue("id"), c.User, body.Name, body.Promote)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return clone, nil
}

// agentDetach cuts the agent loose from the shape it was made from.
func (T *OrchestrateApp) agentDetach(c *jsonapi.Call, _ struct{}) (any, error) {
	rec, err := detachAgentFromShape(c.UDB, c.R.PathValue("id"))
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return rec, nil
}

// agentReach is what this agent depends on, and how far each of those goes.
// Read-only and owner-only: it reports the reach of the owner's OWN tools,
// keys and documents, which is their business and nobody else's — a
// recipient reading it would be reading somebody else's configuration.
//
// There used to be a POST here that shared every dependency the agent's
// recipients could not reach; they reach all of them now, because a
// dependency travels with the agent. The one thing that does not travel is a
// credential, and that is decided per key (reach/credential) rather than by a
// button that assumes an answer.
func (T *OrchestrateApp) agentReach(c *jsonapi.Call) (any, error) {
	agent, ok := loadAgent(c.UDB, c.R.PathValue("id"))
	if !ok || (agent.Owner != "" && agent.Owner != c.User && agent.Owner != seedOwner) {
		return nil, errNotFound
	}
	return agentReachOf(c.UDB, c.User, agent).Items, nil
}

// handleAgentExport downloads the agent as a .agent.json bundle.
func (T *OrchestrateApp) handleAgentExport(w http.ResponseWriter, r *http.Request, user string, udb Database) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload, ok := buildAgentExport(udb, r.PathValue("id"), user)
	if !ok {
		http.NotFound(w, r)
		return
	}
	filename := safeFilename(payload.Name) + ".agent.json"
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+filename+`"`)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

// (poolDivergedTools is gone: under the flattened namespace a pool/record
// fork is structurally impossible — one name is one store row.)
// tempToolDefEqual compares two TempTool definitions ignoring the
// user-governance / provenance fields (lock, disable, builder-only, trial
// clock) that legitimately differ between a pool copy and a record copy of
// the same tool. Only a difference in what the tool DOES counts as a fork.
func tempToolDefEqual(a, b TempTool) bool {
	neutralize := func(t TempTool) TempTool {
		t.Locked, t.Disabled, t.BuilderOnly, t.Trial = false, false, false, false
		t.TrialSince, t.NoUnattended = time.Time{}, false
		return t
	}
	return reflect.DeepEqual(neutralize(a), neutralize(b))
}

// newlyHidden reports whether this save is the one turning Hidden ON — a new
// record arriving hidden, or an existing record whose stored copy was visible.
//
// Separates "apply a sensible default at the moment the user hides an agent"
// from "re-apply it forever", which is the difference between a default and an
// override the user cannot escape.
func newlyHidden(db Database, a AgentRecord) bool {
	if db == nil || strings.TrimSpace(a.ID) == "" {
		return true // brand-new record: the default applies
	}
	var prior AgentRecord
	if !db.Get(agentsTable, a.ID, &prior) {
		return true // no stored copy yet
	}
	return !prior.Hidden
}

// truthyPatchValue reads a reach flag from whatever a client sent it.
//
// The two reach flags are an administrator's to grant, so this errs one way:
// only the values that plainly mean yes are yes, and everything else is no.
// Reading an unknown value as ON would hand out reach on a typo.
func truthyPatchValue(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "on", "yes", "1":
			return true
		}
	case float64:
		return t != 0
	}
	return false
}

// sameJSONValue reports whether a patch value is what the record already
// holds. Compared as JSON because both sides came through it: a number is a
// float64 on one side and may be an int on the other, and a nil and a missing
// key mean the same thing here.
func sameJSONValue(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ab) == string(bb)
}
