package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerLLMRoutes wires the llm API under the admin sub-mux.
func (a *AdminApp) registerLLMRoutes(sub *http.ServeMux) {
	// Live connectivity check for the Worker LLM form — POSTs the form's
	// current, possibly-unsaved values and actually talks to the provider.
	sub.HandleFunc("/api/worker-llm/test", a.handleWorkerLLMTest)

	// LLM routing: GET returns all stages + current values, POST updates one.
	sub.HandleFunc("/api/routing", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		type stageEntry struct {
			Key           string `json:"key"`
			Label         string `json:"label"`
			Value         string `json:"value"`
			Default       string `json:"default"`
			ThinkBudget   int    `json:"think_budget"`
			DefaultBudget int    `json:"default_budget"`
			Group         string `json:"group"`
			Private       bool   `json:"private"`
		}
		if r.Method == http.MethodPost {
			var req struct {
				Key         string `json:"key"`
				Value       string `json:"value"`
				ThinkBudget int    `json:"think_budget"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			allowed := map[string]bool{}
			for _, v := range RouteValues() {
				allowed[v] = true
			}
			if !allowed[req.Value] {
				http.Error(w, "invalid value", http.StatusBadRequest)
				return
			}
			// Private stages can't route to lead, but allow worker ↔ worker
			// (thinking). Tested by TIER, not by one literal, so a new lead
			// value can't slip past this guard.
			if PrivateStageEnforced(req.Key) && RouteValueIsLead(req.Value) {
				http.Error(w, "private stage — cannot route to lead", http.StatusForbidden)
				return
			}
			if a.db != nil {
				a.db.Set(RoutingTable, req.Key, req.Value)
				if req.ThinkBudget > 0 {
					a.db.Set(RoutingTable, req.Key+".think_budget", req.ThinkBudget)
				} else {
					a.db.Unset(RoutingTable, req.Key+".think_budget")
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		stages := ListRouteStages()
		out := make([]stageEntry, len(stages))
		for i, s := range stages {
			val := ""
			if a.db != nil {
				a.db.Get(RoutingTable, s.Key, &val)
			}
			if val == "" {
				val = s.Default
			}
			if val == "" {
				val = "lead"
			}
			def := s.Default
			if def == "" {
				def = "lead"
			}
			var thinkBudget int
			if a.db != nil {
				a.db.Get(RoutingTable, s.Key+".think_budget", &thinkBudget)
			}
			group := s.Group
			if group == "" {
				parts := strings.SplitN(s.Key, ".", 2)
				group = strings.Title(parts[0])
			}
			// PrivateStageEnforced, not s.Private: the row's private flag drives
			// which options the picker offers, so it has to mean "the server
			// would refuse lead here" rather than "this stage is registered
			// private". With the all-private toggle on, those differ, and a
			// picker hiding an option the server would accept reads as broken.
			out[i] = stageEntry{Key: s.Key, Label: s.Label, Value: val, Default: def, ThinkBudget: thinkBudget, DefaultBudget: s.DefaultBudget, Group: group, Private: PrivateStageEnforced(s.Key)}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})

	// Model privacy: whether every LLM this deployment uses stays under the
	// operator's control, and therefore whether a private stage may escalate.
	sub.HandleFunc("/api/llm-privacy", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				AllPrivate bool `json:"all_private"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			SetAllLLMsPrivate(req.AllPrivate)
			// Logged loudly because of what it unlocks: with this on, stages
			// holding SSH credentials and system data may reach the lead tier.
			// An audit asking "when did servitor start using the lead model"
			// needs to find an answer.
			Log("[admin] user %q set all-LLMs-private=%v — private stages %s escalate to the lead tier",
				AuthCurrentUser(r), req.AllPrivate,
				map[bool]string{true: "MAY now", false: "may no longer"}[req.AllPrivate])
			w.WriteHeader(http.StatusNoContent)
			return
		}
		recommended, verdicts := RecommendAllLLMsPrivate()
		var lines []string
		for _, v := range verdicts {
			p := "NOT private"
			if v.Private {
				p = "private"
			}
			where := v.Provider
			if where == "" {
				where = "(unset)"
			}
			if v.Endpoint != "" {
				where += " at " + v.Endpoint
			}
			lines = append(lines, v.Tier+": "+where+" — "+p+" ("+v.Reason+")")
		}
		advice := strings.Join(lines, "\n")
		if recommended {
			advice += "\n\nBoth tiers look local, so turning this on is consistent with how the deployment is configured."
		} else {
			advice += "\n\nAt least one tier reaches a third party. Turning this on anyway would send private-stage data — " +
				"including SSH credentials and log contents — to it."
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"all_private": AllLLMsPrivate(),
			"recommended": recommended,
			"advice":      advice,
		})
		return
	})

	// Worker LLM thinking defaults: GET returns current settings, POST updates.
	sub.HandleFunc("/api/worker-thinking", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				Enabled bool `json:"enabled"`
				Budget  int  `json:"budget"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if a.db != nil {
				a.db.Set(LLMTable, "disable_thinking", !req.Enabled)
				if req.Budget > 0 {
					a.db.Set(LLMTable, "thinking_budget", req.Budget)
				} else {
					a.db.Unset(LLMTable, "thinking_budget")
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var disabled bool
		var budget int
		if a.db != nil {
			a.db.Get(LLMTable, "disable_thinking", &disabled)
			a.db.Get(LLMTable, "thinking_budget", &budget)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"enabled": !disabled,
			"budget":  budget,
		})
	})

	// Worker LLM (primary / local) connection + thinking config. Mirrors the
	// --setup "Primary Provider" step. Writes llm_config. The API key is
	// CryptSet only when a new value is typed (blank = keep existing) and is
	// never returned by GET. Parallel-request caps live in the separate Local
	// Model Scheduler section. Takes effect on restart (the shared LLM is built
	// once at startup).
	sub.HandleFunc("/api/worker-llm", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleLLMConfig(w, r, LLMTable, true)
	})

	// Lead LLM (precision / remote) connection + thinking config. Mirrors the
	// --setup "Precision LLM" step. Writes lead_llm_config; an empty provider
	// means "use the primary worker". Same key-masking + restart semantics.
	sub.HandleFunc("/api/lead-llm", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleLLMConfig(w, r, LeadLLMTable, false)
	})

	// Local model scheduler: GET returns max parallel for Ollama and llama.cpp,
	// POST updates both values. Requires restart to apply.
	sub.HandleFunc("/api/local-scheduler", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				OllamaMaxParallel   int `json:"ollama_max_parallel"`
				LlamacppMaxParallel int `json:"llamacpp_max_parallel"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if a.db != nil {
				if req.OllamaMaxParallel < 1 {
					req.OllamaMaxParallel = 1
				}
				if req.LlamacppMaxParallel < 1 {
					req.LlamacppMaxParallel = 1
				}
				a.db.Set(LLMTable, "ollama_max_parallel", req.OllamaMaxParallel)
				a.db.Set(LLMTable, "llamacpp_max_parallel", req.LlamacppMaxParallel)
			}
			// Parallel caps are read when the LLM client is built, so a reload
			// re-applies them live (no restart).
			if err := ReloadLLMs(); err != nil {
				Log("[admin] LLM reload after scheduler save failed: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var ollamaMP, llamacppMP int
		if a.db != nil {
			a.db.Get(LLMTable, "ollama_max_parallel", &ollamaMP)
			a.db.Get(LLMTable, "llamacpp_max_parallel", &llamacppMP)
		}
		if ollamaMP < 1 {
			ollamaMP = 1
		}
		if llamacppMP < 1 {
			llamacppMP = 1
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ollama_max_parallel":   ollamaMP,
			"llamacpp_max_parallel": llamacppMP,
		})
	})

}

// handleLLMConfig is the shared GET/POST handler for the Worker (llm_config) and
// Lead (lead_llm_config) LLM sections. worker=true includes the worker-only
// fields (request timeout). The API key is CryptSet only when a
// new value is supplied (blank on the form = keep the existing key) and is
// masked — never returned — on GET. Mirrors the --setup save in config.go.
func (a *AdminApp) handleLLMConfig(w http.ResponseWriter, r *http.Request, table string, worker bool) {
	if a.db == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			Provider             string `json:"provider"`
			Model                string `json:"model"`
			APIKey               string `json:"api_key"`
			Endpoint             string `json:"endpoint"`
			AWSRegion            string `json:"aws_region"`
			AWSProfile           string `json:"aws_profile"`
			BedrockAPI           string `json:"bedrock_api"`
			ContextSize          int    `json:"context_size"`
			RequestTimeout       int    `json:"request_timeout_seconds"`
			NativeTools          bool   `json:"native_tools"`
			DisableThinking      bool   `json:"disable_thinking"`
			ThinkingBudget       int    `json:"thinking_budget"`
			NoThinkUseKwarg      bool   `json:"no_think_use_kwarg"`
			NoThinkSendBudget    bool   `json:"no_think_send_budget"`
			NoThinkBudget        int    `json:"no_think_budget"`
			NoThinkPrependSystem bool   `json:"no_think_prepend_system"`
			NoThinkPrependUser   bool   `json:"no_think_prepend_user"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		a.db.Set(table, "provider", req.Provider)
		a.db.Set(table, "model", req.Model)
		a.db.Set(table, "endpoint", req.Endpoint)
		a.db.Set(table, "aws_region", req.AWSRegion)
		a.db.Set(table, "aws_profile", req.AWSProfile)
		a.db.Set(table, "bedrock_api", req.BedrockAPI)
		a.db.Set(table, "native_tools", req.NativeTools)
		a.db.Set(table, "disable_thinking", req.DisableThinking)
		a.db.Set(table, "thinking_budget", req.ThinkingBudget)
		a.db.Set(table, "no_think_configured", true)
		a.db.Set(table, "no_think_use_kwarg", req.NoThinkUseKwarg)
		a.db.Set(table, "no_think_send_budget", req.NoThinkSendBudget)
		a.db.Set(table, "no_think_budget", req.NoThinkBudget)
		a.db.Set(table, "no_think_prepend_system", req.NoThinkPrependSystem)
		a.db.Set(table, "no_think_prepend_user", req.NoThinkPrependUser)
		// context_size applies to both tiers: num_ctx for local providers,
		// the compaction working cap for Anthropic/Bedrock leads.
		a.db.Set(table, "context_size", req.ContextSize)
		if worker {
			a.db.Set(table, "request_timeout_seconds", req.RequestTimeout)
		}
		if req.APIKey != "" {
			a.db.CryptSet(table, "api_key", req.APIKey)
		}
		// Apply live — rebuild the shared LLMs from the new config so the change
		// takes effect without a restart. Best-effort: on a bad config the prior
		// LLMs stay active and we log it (the config is still saved).
		if err := ReloadLLMs(); err != nil {
			Log("[admin] LLM reload after %s save failed (config saved; prior LLM still active): %v", table, err)
		}
		Log("[admin] user %q updated %s (provider=%q model=%q)", AuthCurrentUser(r), table, req.Provider, req.Model)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// GET — current values; api_key is masked (never returned).
	var provider, model, endpoint, awsRegion, awsProfile, bedrockAPI string
	var contextSize, reqTimeout, noThinkBudget int
	var nativeTools, disableThinking, ntPrependSys, ntPrependUser bool
	ntKwarg, ntBudget := true, true // defaults when no-think was never configured
	thinkingBudget := 4096          // default budget when unset (matches config.go)
	a.db.Get(table, "provider", &provider)
	a.db.Get(table, "model", &model)
	a.db.Get(table, "endpoint", &endpoint)
	a.db.Get(table, "aws_region", &awsRegion)
	a.db.Get(table, "aws_profile", &awsProfile)
	a.db.Get(table, "bedrock_api", &bedrockAPI)
	a.db.Get(table, "native_tools", &nativeTools)
	a.db.Get(table, "disable_thinking", &disableThinking)
	a.db.Get(table, "thinking_budget", &thinkingBudget)
	var ntConfigured bool
	a.db.Get(table, "no_think_configured", &ntConfigured)
	if ntConfigured {
		a.db.Get(table, "no_think_use_kwarg", &ntKwarg)
		a.db.Get(table, "no_think_send_budget", &ntBudget)
		a.db.Get(table, "no_think_prepend_system", &ntPrependSys)
		a.db.Get(table, "no_think_prepend_user", &ntPrependUser)
	}
	a.db.Get(table, "no_think_budget", &noThinkBudget)
	out := map[string]any{
		"provider":                provider,
		"model":                   model,
		"endpoint":                endpoint,
		"aws_region":              awsRegion,
		"aws_profile":             awsProfile,
		"bedrock_api":             bedrockAPI,
		"native_tools":            nativeTools,
		"disable_thinking":        disableThinking,
		"thinking_budget":         thinkingBudget,
		"no_think_use_kwarg":      ntKwarg,
		"no_think_send_budget":    ntBudget,
		"no_think_budget":         noThinkBudget,
		"no_think_prepend_system": ntPrependSys,
		"no_think_prepend_user":   ntPrependUser,
		"api_key":                 "", // masked; blank on the form means "keep existing"
	}
	a.db.Get(table, "context_size", &contextSize)
	out["context_size"] = contextSize
	if worker {
		a.db.Get(table, "request_timeout_seconds", &reqTimeout)
		out["request_timeout_seconds"] = reqTimeout
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
