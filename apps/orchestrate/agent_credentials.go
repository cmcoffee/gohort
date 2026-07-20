// agent_credentials.go — agent-centric tier-2 credential scoping, on the USER's
// own agent editor (the relocation from the admin credential page). Tier 1 (which
// USERS may use a credential) stays on the admin page as AllowedUsers; this is
// tier 2 (which of a user's OWN agents may use a credential they've been granted).
//
// It's agent-centric on purpose: the admin page's old per-agent pill was
// cred-centric (one credential -> a list of agents), which doesn't scale as the
// deployment grows. Here each user sees only their OWN fleet, one agent at a time,
// so the list stays small.
//
// Storage is the existing opt-OUT (AgentRecord.DisabledCredentials): every granted
// credential is available to a new agent by default; a user unchecks the ones a
// given agent shouldn't reach. The ChipPicker on the editor is an ALLOWLIST
// (checked = may use), so this endpoint inverts enabled <-> disabled at the edge.
package orchestrate

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// userScopableCredentials returns the credentials `user` may use and that make
// sense as per-agent scope targets: their own namespaced creds plus the global
// creds their tier-1 grant admits, minus disabled, "none"-type, and SECURED creds
// (a secured cred's access follows its tool bindings, not per-agent scope). A
// user-owned cred shadows a same-named global (first-seen wins). Sorted by name.
func userScopableCredentials(user string) []SecureCredential {
	out := []SecureCredential{}
	seen := map[string]bool{}
	emit := func(c SecureCredential) {
		if seen[c.Name] || c.Disabled || c.Secured || c.Type == SecureCredNone {
			return
		}
		seen[c.Name] = true
		out = append(out, c)
	}
	if user != "" {
		for _, c := range Secure().ListUser(user) {
			emit(c)
		}
	}
	for _, c := range Secure().List() {
		if Secure().UserMayUse(c, user) {
			emit(c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// handleAgentCredentials serves the agent editor's "Credentials this agent may
// use" picker. GET returns the candidate creds + the currently-enabled subset;
// POST {enabled_credentials:[...]} rewrites the agent's opt-out for exactly those
// candidates (opt-outs for creds NOT in the candidate set are preserved).
func (T *OrchestrateApp) handleAgentCredentials(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	a, ok := loadAgent(udb, id)
	// A user may only scope their OWN agent (seeds load with an empty Owner until
	// first shadowed; treat that as the caller's).
	if !ok || (a.Owner != "" && a.Owner != user && a.Owner != seedOwner) {
		http.NotFound(w, r)
		return
	}
	candidates := userScopableCredentials(user)

	switch r.Method {
	case http.MethodGet:
		disabled := map[string]bool{}
		for _, n := range a.DisabledCredentials {
			disabled[n] = true
		}
		type opt struct {
			Value string `json:"value"`
			Label string `json:"label"`
			Desc  string `json:"desc,omitempty"`
		}
		opts := []opt{}
		enabled := []string{}
		for _, c := range candidates {
			opts = append(opts, opt{Value: c.Name, Label: c.Name, Desc: c.Description})
			if !disabled[c.Name] {
				enabled = append(enabled, c.Name)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"credentials":         opts,
			"enabled_credentials": enabled,
		})
	case http.MethodPost:
		var body struct {
			EnabledCredentials []string `json:"enabled_credentials"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		enabled := map[string]bool{}
		for _, n := range body.EnabledCredentials {
			enabled[strings.TrimSpace(n)] = true
		}
		cand := map[string]bool{}
		for _, c := range candidates {
			cand[c.Name] = true
		}
		// Preserve opt-outs for creds not shown here; recompute the candidate ones
		// from the enabled allowlist (unchecked candidate -> disabled).
		set := map[string]bool{}
		for _, n := range a.DisabledCredentials {
			if !cand[n] {
				set[n] = true
			}
		}
		for _, c := range candidates {
			if !enabled[c.Name] {
				set[c.Name] = true
			}
		}
		out := make([]string, 0, len(set))
		for n := range set {
			out = append(out, n)
		}
		sort.Strings(out)
		a.DisabledCredentials = out
		a.Owner = user
		if _, err := saveAgent(udb, a); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
