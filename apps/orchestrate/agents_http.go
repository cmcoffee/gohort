package orchestrate

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func (T *OrchestrateApp) handleAgentList(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		agents := listAgents(udb, user)
		// Hidden app agents and clone-only templates are app/framework internals
		// on EVERY form of this endpoint — the bare list feeds the channel
		// re-point dropdown, and offering the Servitor Investigator there let a
		// channel be pointed at an agent no user is meant to reach.
		{
			kept := agents[:0]
			for _, a := range agents {
				if hiddenAppAgent(a.ID) || isCloneOnlySeed(a.ID) {
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
			out := make([]map[string]any, 0, len(agents))
			for _, a := range agents {
				out = append(out, map[string]any{"id": a.ID, "name": a.Name, "description": a.Description})
			}
			for _, d := range ListPipelineDefs(udb, user) {
				desc := "Pipeline"
				if s := strings.TrimSpace(d.Description); s != "" {
					desc += " — " + s
				}
				out = append(out, map[string]any{"id": d.ID, "name": d.Name, "description": desc})
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
					desc += " — " + s
				}
				out = append(out, map[string]any{"id": d.ID, "name": d.Name, "description": desc})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		_ = json.NewEncoder(w).Encode(agents)
	case http.MethodPost:
		// Read once, decode twice. The record decode is what saves; the
		// key probe is what tells a field the caller OMITTED apart from a
		// field they deliberately CLEARED. Go's zero values can't express
		// that difference, and for `machine` it is the difference between
		// "the Rules modal posted a record that never mentioned it" and
		// "the user picked None in the dropdown".
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var req AgentRecord
		if err := json.Unmarshal(raw, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var sent map[string]json.RawMessage
		_ = json.Unmarshal(raw, &sent)
		_, sentMachine := sent["machine"]
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
				http.Error(w, "not your agent", http.StatusForbidden)
				return
			} else {
				// (Locked needs no restore here: saveAgent preserves the
				// stored flag for every caller. See setAgentLocked.)
				// Guardrails are owned by the dedicated guardrails endpoint
				// (handleAgentGuardrails), never the whole-record form — a
				// wholesale-replace save must NOT be able to weaken or clear
				// them, which is the entire point of them being un-rewritable
				// by the agent's own edit paths. Preserve from the stored copy.
				req.Guardrails = existing.Guardrails
				req.GuardrailHooks = existing.GuardrailHooks
				req.GuardrailFailClosed = existing.GuardrailFailClosed
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
			curateToolsFromModal(T.DB, user, &req)
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
		saved, err := saveAgent(udb, req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(saved)
	case http.MethodPatch:
		T.patchAgent(w, r, udb, user)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
//   - locked — owned by the lock icon
//   - id / owner / created — identity, not settings
var patchAgentFields = map[string]bool{
	"name": true, "description": true, "orchestrator_prompt": true,
	"plan_guidance": true, "rules": true, "triggers": true,
	"allowed_tools": true, "auto_approve_tools": true, "allowed_skills": true,
	"attached_collections": true, "attached_pipelines": true,
	"allowed_dispatch_targets": true, "allowed_users": true,
	"max_plan_steps": true, "max_worker_rounds": true, "think": true,
	"think_budget": true, "context_depth": true, "gap_check": true,
	"action_quotas": true, "daily_spend_usd": true,
	"lead_model": true, "memory_mode": true, "disable_explicit": true,
	"disable_inferred": true, "disable_compaction": true, "recall_hints": true,
	"capture_prompt": true,
	"allow_explorer": true, "explorer_hard_cap": true,
	"channel": true, "fleet": true, "author": true, "tag_name": true,
	"exposed": true, "mcp_exposed": true, "public_name": true,
	"allow_private_mode": true, "force_private": true, "hidden": true,
	"allow_builder_dispatch": true, "dispatch_mode": true,
	"evals": true, "intake_form": true, "owned_by": true,
	"work_plan": true,
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
func (T *OrchestrateApp) patchAgent(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// The id may come in the body OR the query. A FormPanel's PATCH body is
	// exactly {changed_field: value} with no record id in it, so a form names
	// its target in the URL instead.
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" && patch["id"] != nil {
		id = strings.TrimSpace(fmt.Sprint(patch["id"]))
	}
	if id == "" {
		http.Error(w, "id is required for PATCH (in the body or as ?id=)", http.StatusBadRequest)
		return
	}
	existing, ok := loadAgent(udb, id)
	if !ok {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	if existing.Owner != "" && existing.Owner != user && existing.Owner != seedOwner {
		http.Error(w, "not your agent", http.StatusForbidden)
		return
	}
	if existing.Locked {
		http.Error(w, "this agent is locked — unlock it (the 🔒 icon) before editing", http.StatusConflict)
		return
	}
	// Merge: start from the stored record's own JSON so untouched fields keep
	// their exact values, then overlay only allowlisted keys.
	blob, err := json.Marshal(existing)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	var merged map[string]any
	if err := json.Unmarshal(blob, &merged); err != nil {
		http.Error(w, "decode failed", http.StatusInternalServerError)
		return
	}
	applied := make([]string, 0, len(patch))
	var refused []string
	for k, v := range patch {
		if k == "id" {
			continue
		}
		if !patchAgentFields[k] {
			refused = append(refused, k)
			continue
		}
		merged[k] = v
		applied = append(applied, k)
	}
	if len(refused) > 0 {
		sort.Strings(refused)
		http.Error(w, "these fields cannot be set through PATCH (they have their own protected endpoints): "+strings.Join(refused, ", "), http.StatusBadRequest)
		return
	}
	out, err := json.Marshal(merged)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	var rec AgentRecord
	if err := json.Unmarshal(out, &rec); err != nil {
		http.Error(w, "bad field value: "+err.Error(), http.StatusBadRequest)
		return
	}
	rec.ID = existing.ID
	rec.Owner = existing.Owner
	if rec.Owner == "" || rec.Owner == seedOwner {
		rec.Owner = user
	}
	saved, err := saveAgent(udb, rec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sort.Strings(applied)
	Log("[orchestrate.agents] PATCH agent=%s fields=%v", id, applied)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(saved)
}

func (T *OrchestrateApp) handleAgentOne(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	// Path: /api/agents/<id>  or  /api/agents/<id>/clone
	rest := strings.TrimPrefix(r.URL.Path, "/api/agents/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	var id, action string
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		id = rest[:slash]
		action = rest[slash+1:]
	} else {
		id = rest
	}
	if id == "" {
		http.NotFound(w, r)
		return
	}

	if action == "clone" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Name    string `json:"name,omitempty"`
			Promote bool   `json:"promote,omitempty"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		clone, err := cloneAgent(udb, id, user, body.Name, body.Promote)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(clone)
		return
	}
	if action == "assist" {
		T.handleAgentAssist(w, r, user, udb, id)
		return
	}
	if action == "detach" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rec, err := detachAgentFromShape(udb, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rec)
		return
	}
	if action == "facts" {
		T.handleAgentFacts(w, r, user, id)
		return
	}
	if action == "notes" {
		T.handleAgentNotes(w, r, user, id)
		return
	}
	if action == "memaudit" {
		T.handleAgentMemoryAudit(w, r, user, id)
		return
	}
	if action == "memsearch" {
		T.handleAgentMemorySearch(w, r, user, id)
		return
	}
	if action == "guardrails" {
		T.handleAgentGuardrails(w, r, user, id)
		return
	}
	if action == "decline-suggest" {
		T.handleAgentDeclineSuggest(w, r, user, id)
		return
	}
	if action == "guardrail-test" {
		T.handleAgentGuardrailTest(w, r, user, id)
		return
	}
	if action == "inferred" {
		T.handleAgentInferredList(w, r, user, id)
		return
	}
	if strings.HasPrefix(action, "inferred/") {
		chunkID := strings.TrimPrefix(action, "inferred/")
		T.handleAgentInferredDelete(w, r, user, id, chunkID)
		return
	}
	if action == "graph" {
		T.handleAgentGraph(w, r, user, id)
		return
	}
	if strings.HasPrefix(action, "graph/entity/") {
		rest := strings.TrimPrefix(action, "graph/entity/")
		// Entity IDs are "<kind>:<slug>" — never contain a slash — so a
		// trailing /attr or /alias unambiguously selects the sub-action.
		switch {
		case strings.HasSuffix(rest, "/attr"):
			T.handleAgentGraphAttrDelete(w, r, user, id, strings.TrimSuffix(rest, "/attr"))
		case strings.HasSuffix(rest, "/alias"):
			T.handleAgentGraphAliasDelete(w, r, user, id, strings.TrimSuffix(rest, "/alias"))
		default:
			T.handleAgentGraphEntityDelete(w, r, user, id, rest)
		}
		return
	}
	if action == "graph/edge" {
		T.handleAgentGraphEdgeDelete(w, r, user, id)
		return
	}
	if action == "knowledge" {
		T.handleAgentKnowledge(w, r, user, id)
		return
	}
	if action == "phantom-sessions" {
		T.handleAgentPhantomSessions(w, r, user, id)
		return
	}
	if strings.HasPrefix(action, "phantom-sessions/") {
		// /api/agents/{id}/phantom-sessions/{session_id}?chat_id=<chatID>
		// reads a single phantom-owned session out of the per-chat
		// sub-store. Read-only; deletion can be added later if needed.
		sid := strings.TrimPrefix(action, "phantom-sessions/")
		T.handleAgentPhantomSessionOne(w, r, user, id, sid)
		return
	}
	if action == "lock" {
		T.handleAgentLock(w, r, user, id)
		return
	}
	if action == "knowledge/auto-inferred" {
		T.handleAgentKnowledgeAutoInferredWipe(w, r, user, id)
		return
	}
	if action == "knowledge/scaffold-collection" {
		T.handleAgentKnowledgeScaffoldCollection(w, r, user, udb, id)
		return
	}
	if action == "knowledge/upload" {
		T.handleAgentKnowledgeUpload(w, r, user, id)
		return
	}
	if action == "knowledge/sources" {
		T.handleAgentKnowledgeSources(w, r, user, id)
		return
	}
	if strings.HasPrefix(action, "knowledge/sources/") {
		reportID := strings.TrimPrefix(action, "knowledge/sources/")
		T.handleAgentKnowledgeSourceDelete(w, r, user, id, reportID)
		return
	}
	if action == "eval-suite" {
		// Lift the agent's inline cases into a standalone suite, which is
		// where a history and a per-run fingerprint become possible. The
		// agent's own field is left exactly as it was.
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		agent, ok := findAgentByNameOrID(UserDB(T.DB, user), user, id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		suite, err := EvalSuiteFromAgent(agent)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		suite.Owner = user
		saved, err := SaveEvalSuite(UserDB(T.DB, user), suite)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "id": saved.ID, "cases": len(saved.Cases),
			"message": fmt.Sprintf("Created %q with %d case(s). The agent's own cases are unchanged.", saved.Name, len(saved.Cases)),
		})
		return
	}
	if action == "eval" {
		// Dispatch into the eval-harness handler via a synthetic
		// path so handleAgentEval's TrimPrefix logic still works.
		r.URL.Path = "/api/agents/" + id + "/eval"
		_ = user // (used implicitly by handleAgentEval via RequireUser)
		T.handleAgentEval(w, r)
		return
	}
	if action == "export" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		payload, ok := buildAgentExport(udb, id, user)
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
		return
	}
	if action != "" {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		a, ok := loadAgent(udb, id)
		if !ok || (a.Owner != user && a.Owner != seedOwner) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Flattened namespace: the record stores no tools; the GET response
		// synthesizes the `tools` array from the unified store (rows scoped to
		// this agent) as a VIEW — the Tools modal renders it unchanged. A fork
		// between a pool copy and a record copy is structurally impossible
		// now, so the old pool_diverged_tools computation is gone. The POST
		// below strips Tools before save, so the fetch-modify-post round-trip
		// can't write the view back into storage.
		a.Tools = toolsOfScoped(AgentScopedTools(udb, user, a.ID))
		_ = json.NewEncoder(w).Encode(a)
	case http.MethodPost:
		// PARTIAL update of one existing agent. The full edit form posts the
		// whole record to /api/agents (handleAgentList); single-field surfaces
		// like the dispatch-allowlist ChipPicker POST just their field HERE.
		// Without this case the POST fell to default → 405, so the
		// allowed_dispatch_targets picker silently never saved (the dispatch
		// allowlist "didn't work"). Decoding the posted body INTO the loaded
		// record merges: present fields overwrite, absent fields keep their
		// stored value — and Locked (owned by the lock icon) is preserved since
		// the partial body never carries it.
		existing, ok := loadAgent(udb, id)
		if !ok || (existing.Owner != "" && existing.Owner != user && existing.Owner != seedOwner) {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&existing); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		existing.ID = id
		existing.Owner = user
		// Flattened namespace: tools live in the unified store, and the GET
		// view synthesizes them onto the record — never write that view back.
		existing.Tools = nil
		saved, err := saveAgent(udb, existing)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(saved)
	case http.MethodDelete:
		if err := deleteAgent(udb, id, user); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
		t.TrialSince = time.Time{}
		return t
	}
	return reflect.DeepEqual(neutralize(a), neutralize(b))
}

// handleAgentLock toggles the per-agent edit/delete lock — POST /api/agents/{id}/lock
// {locked}. This is the HUMAN control (the editor's lock icon), so it's owner-
// gated only; the agent-CRUD tools enforce the lock, they don't set it. Locked
// is changed ONLY here — the main agent save preserves the stored value — so the
// icon is the single source of truth and a form save can't clobber it.
func (T *OrchestrateApp) handleAgentLock(w http.ResponseWriter, r *http.Request, user, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	udb := UserDB(T.DB, user)
	a, ok := loadAgent(udb, id)
	// Seeds load with an empty Owner until first shadowed; treat that as the
	// caller's own. A non-seed must already belong to the caller.
	if !ok || (a.Owner != "" && a.Owner != user) {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Locked bool `json:"locked"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	a.Owner = user
	// The RETURNED record, not the local one: setAgentLocked takes a by value,
	// so the caller's copy still carries the old flag and would report it.
	saved, err := setAgentLocked(udb, a, req.Locked)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"locked": saved.Locked})
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
