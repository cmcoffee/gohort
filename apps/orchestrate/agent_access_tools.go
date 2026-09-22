// The agent's ACTUAL toolset, resolved rather than derived.
//
// The access page listed tools out of rec.AllowedTools. That field is empty on
// a default-pool agent, where empty means "every catalog tool", so the one
// surface built to answer "what can this thing do" showed nothing for the
// commonest kind of agent. It is the same root cause that took down the tool
// permission ladder: the control derived its own answer instead of asking the
// thing that decides.
//
// So ask it. resolveWorkerTools is what the runner calls to build a turn's
// catalog, and it already runs off-turn in two other places
// (inheritableParentTools, the eval harness) against a minimal chatTurn. This
// is the third, and the list is exact by construction because it is the list.

package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// accessToolRow is one tool this agent can actually call, with the standing
// decisions that govern it.
type accessToolRow struct {
	Name string `json:"name"`
	// Origin is where the tool comes from, because that decides what can be
	// done about it: a framework tool has no record to carry a flag, one in
	// the owner's pool does.
	Origin string `json:"origin"`
	Detail string `json:"detail,omitempty"`
	// Asks is the ask-before-every-call flag (in chat). Only a tool with a
	// record behind it can hold one.
	Asks bool `json:"asks"`
	// Governable says whether the controls apply at all, so the UI renders a
	// row it cannot act on differently from one set to "no". A framework tool
	// has no record to carry a flag; offering it a switch would be offering
	// one wired to nothing.
	Governable bool `json:"governable"`
	// Unattended is the standing decision for scheduled and standing runs:
	// allow, ask, or block. The GATE's answer where one is recorded, and
	// "allow" where none is, which is what the gate does with no record.
	Unattended string `json:"unattended,omitempty"`
	// Actions are a grouped tool's sub-actions, and Withheld the ones switched
	// off for this agent.
	Actions  []string `json:"actions,omitempty"`
	Withheld []string `json:"withheld,omitempty"`
}

// resolvedAgentTools returns what this agent's worker would actually be handed.
//
// forOrchestrator=true so the Fleet block is included: those are the
// consequential tools, and a page for securing an agent that omitted exactly
// the tools worth securing would be worse than no page.
//
// A resolution error yields nil, not a partial list. Half a toolset presented
// as the toolset is the failure this file exists to end, and the caller says
// so rather than rendering an empty section that reads as "no tools".
func (T *OrchestrateApp) resolvedAgentTools(ctx context.Context, udb Database, user string, rec AgentRecord) ([]accessToolRow, error) {
	sess := &ToolSession{Username: user, DB: udb, Ctx: ctx}
	turn := &chatTurn{
		app:     T,
		agent:   rec,
		user:    user,
		udb:     udb,
		ctx:     ctx,
		network: netgate.NewNetworkConnector(false),
	}
	defs, _, err := turn.resolveWorkerTools(sess, true)
	if err != nil {
		return nil, err
	}
	// BOTH feeds, because resolveWorkerTools is REGISTERED chat tools only:
	// the user's own tools reach a real turn by a separate path and would
	// otherwise be missing from the one page that lists what an agent holds.
	// They are also the tools most worth securing, being the ones somebody
	// wrote rather than the ones that shipped.
	turn.loadAgentTempTools(sess, user, udb)
	defs = append(defs, temptool.BuildAgentToolDefs(sess)...)
	// The owner's pool, read once: it answers both "can this tool carry a
	// flag" and "is the flag set", and a lookup per row would re-read it for
	// every tool in the catalog.
	pool := map[string]*TempTool{}
	for _, p := range LoadPersistentTempTools(AuthDB(), user) {
		t := p.Tool
		pool[t.Name] = &t
	}
	// The standing unattended decisions for THIS agent, read once. A per-row
	// lookup would walk the whole list again for every tool in the catalog.
	unattended := map[string]string{}
	for _, p := range listAutoToolPolicies(RootDB, user) {
		if p.AgentID == rec.ID {
			unattended[p.Tool] = p.Policy
		}
	}
	withheld := map[string][]string{}
	for _, pair := range rec.DisabledToolActions {
		if tool, action, ok := strings.Cut(strings.TrimSpace(pair), "/"); ok {
			withheld[tool] = append(withheld[tool], action)
		}
	}
	// Sub-actions come from the REGISTERED tool, not from the def: AgentToolDef
	// carries a flat core.Tool, so a grouped tool's actions are not on it and a
	// type assertion there does not compile, let alone answer.
	grouped := map[string][]string{}
	for _, ct := range RegisteredChatTools() {
		if g, ok := ct.(*GroupedTool); ok {
			grouped[g.Name()] = g.ActionNames()
		}
	}
	seen := map[string]bool{}
	out := []accessToolRow{}
	for _, d := range defs {
		name := strings.TrimSpace(d.Tool.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		row := accessToolRow{
			Name:     name,
			Origin:   "framework",
			Detail:   firstLine(d.Tool.Description),
			Withheld: withheld[name],
		}
		if p, ok := unattended[name]; ok {
			row.Unattended = p
		} else {
			// No record means the gate allows it, so say allow rather than
			// leaving the control blank. A segmented control with nothing
			// selected reads as "unset", and there is no such state.
			row.Unattended = PolicyAllow
		}
		if tt, ok := pool[name]; ok {
			row.Origin = "your tools"
			row.Asks = tt.ConfirmInChat
			row.Governable = true
			if c := strings.TrimSpace(tt.Credential); c != "" && !strings.EqualFold(c, "no_auth") {
				row.Origin = "credential: " + c
			}
		}
		row.Actions = grouped[name]
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		// Governable rows first: they are the ones somebody came here to act
		// on, and burying them under the framework catalog is how a control
		// surface becomes a list nobody scrolls.
		if out[i].Governable != out[j].Governable {
			return out[i].Governable
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// handleAgentAccessTool writes one tool's standing decisions from the access
// page. POST ?agent=<id>&name=<tool>, body {"asks":bool} or
// {"unattended":"allow|ask|block"}.
//
// A thin handler over the SAME setters the Permissions page calls, not a
// second way to store the decision. Two surfaces that write the same fact
// through different code is how they start disagreeing, and this pair already
// has to agree: a tool set to ask here must read as asking there.
//
// The ask flag is keyed by TOOL and the unattended policy by (agent, tool),
// which is not an inconsistency: a tool's riskiness in chat is a property of
// the tool, while whether it may run with nobody watching is a property of the
// agent doing the running.
func (T *OrchestrateApp) handleAgentAccessTool(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rec, found := findAgentByNameOrID(udb, user, strings.TrimSpace(r.URL.Query().Get("agent")))
	if !found {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	var body struct {
		Asks       *bool   `json:"asks"`
		Unattended *string `json:"unattended"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch {
	case body.Asks != nil:
		// Refused rather than silently ignored when the tool has no record to
		// hold the flag. A control that accepts a click and stores nothing is
		// worse than one that is not offered: the row would show the new state
		// until the next reload and then quietly revert.
		if !SetUserToolConfirmInChat(AuthDB(), user, name, *body.Asks) {
			http.Error(w, "that tool has no record of its own to carry the flag", http.StatusNotFound)
			return
		}
	case body.Unattended != nil:
		v := strings.TrimSpace(*body.Unattended)
		if v != PolicyAllow && v != PolicyAsk && v != PolicyBlock {
			http.Error(w, "unattended must be allow, ask or block", http.StatusBadRequest)
			return
		}
		setAutoToolPolicy(RootDB, udb, user, rec.ID, name, v)
	default:
		http.Error(w, "nothing to set", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
