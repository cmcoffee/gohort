package admin

import (
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// llmSections is the llm part of the admin page: Worker LLM, Lead LLM, Model Privacy, LLM Routing, Ollama Proxy, Agent Loop Tuning, Local Model Scheduler.
func (a *AdminApp) llmSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "Worker LLM",
			Subtitle: "The primary, local model most work runs on.",
			Detail:   "Applies immediately on save, because the live LLM is rebuilt and nothing restarts. The API key is stored encrypted; leave it blank to keep the current one.",
			Body: ui.FormPanel{
				Source: "api/worker-llm",
				Fields: []ui.FormField{
					{Field: "provider", Label: "Provider", Type: "select", Options: LLMProviderOptions(false),
						Help:   "Local providers, ollama or llama.cpp, are the usual worker.",
						Detail: "A peer offering inference appears here too, and its GPU runs the turns."},
					// Shown only when there is something to say: a peer's model is
					// tuned on that peer, and its wording governs here.
					{Field: "_peer_wording", Type: "readonly", Label: "Prompt wording", ShowWhen: "_peer_wording",
						Help: "A peer's model is tuned on that peer, and its wording is what this machine sends it."},
					{Field: "model", Label: "Model", Type: "text", Placeholder: "e.g. qwen3.6-27b",
						Help:   "Blank uses the provider default.",
						Detail: "On AWS Bedrock, many accounts require a region-prefixed inference profile (us.anthropic.claude-opus-4-8) and deny the bare id."},
					{Field: "api_key", Label: "API key", Type: "password", Placeholder: "(leave blank to keep current)",
						ShowWhen: "provider:anthropic|openai|gemini|bedrock|llama.cpp",
						Help:     "Stored encrypted. Ollama ignores it; llama.cpp sends it only when the server requires one (started with --api-key)."},
					{Field: "endpoint", Label: "Endpoint", Type: "text", Placeholder: "http://localhost:8080/v1",
						ShowWhen: "provider:ollama|llama.cpp|bedrock",
						Help:     "For local / self-hosted providers; blank = provider default.",
						Presets: []ui.FieldPreset{
							{Label: "Ollama", Value: "http://localhost:11434"},
							{Label: "llama.cpp", Value: "http://localhost:8080/v1"}}},
					{Field: "aws_region", Label: "AWS region", Type: "text", Placeholder: "us-east-1",
						ShowWhen: "provider:bedrock",
						Help:     "AWS Bedrock only. Blank uses $AWS_REGION, then us-east-1.",
						Detail: "This tier has its OWN region. Setting it on the Worker does not set it on the Lead, and a blank one here falls through to $AWS_REGION on the gohort host and then to us-east-1 - which is why a tier can keep using us-east-1 while another tier's box reads us-west-2. The debug log names the region and where it came from as each client is built.\n\n" +
							"Not every region AWS lists for Bedrock has a Messages-API endpoint. us-west-1 does not; use us-west-2.",
						Presets: bedrockRegionPresets()},
					{Field: "bedrock_api", Label: "Bedrock API", Type: "select", ShowWhen: "provider:bedrock",
						Options: []ui.SelectOption{
							{Value: "", Label: "Messages API (bedrock-mantle)"},
							{Value: "invoke", Label: "InvokeModel (bedrock-runtime)"}},
						Help:   "Which Bedrock API your AWS role may call. Both stream.",
						Detail: "The Messages API needs bedrock-mantle:CreateInference. InvokeModel needs bedrock:InvokeModel, and is what most AI-tooling permission sets grant. A 403 on CreateInference means switch to InvokeModel."},
					{Field: "aws_profile", Label: "AWS profile", Type: "text", Placeholder: "(default)",
						ShowWhen: "provider:bedrock",
						Help:     "AWS Bedrock only. Blank uses $AWS_PROFILE.",
						Detail:   "Credentials are never stored here. For SSO, run `aws sso login` on the gohort host. The API key field above is optional, and means a Bedrock bearer token instead."},
					{Field: "context_size", Label: "Context size (tokens)", Type: "number", Min: 0, Max: 1000000,
						ShowWhen: "provider:!openai|gemini",
						Help:     "0 uses the default: 65K for ollama and llama.cpp, 200K for Anthropic and Bedrock.",
						Detail:   "Local providers send this as num_ctx. For Anthropic and Bedrock it is the working cap history compaction keys on. The API accepts up to 1M, but every input token bills per turn, so keep it modest."},
					{Field: "request_timeout_seconds", Label: "Request timeout (sec)", Type: "number", Min: 0, Max: 3600,
						Help: "0 = default 300s."},
					{Field: "native_tools", Label: "Native tool calling", Type: "toggle",
						// Only ollama reads it: every hosted API and llama.cpp, and so
						// every peer, calls tools natively (ProviderHasNativeTools).
						ShowWhen: "provider:ollama",
						Help:     "Disable for models without tool-calling support (ollama)."},
					{Type: "header", Label: "Thinking"},
					{Field: "disable_thinking", Label: "Disable thinking (force think=false)", Type: "toggle",
						ShowWhen: "provider:!anthropic|openai|bedrock"},
					{Field: "thinking_budget", Label: "Thinking budget (tokens)", Type: "number", Min: 0, Max: 131072,
						ShowWhen: "provider:!anthropic|openai|bedrock|ollama",
						Help:     thinkingBudgetHelp},
					effortDefaultField(), effortMaxField(),
					{Field: "no_think_use_kwarg", Label: "No-think: send enable_thinking=false", Type: "toggle",
						ShowWhen: workerNoThink},
					{Field: "no_think_send_budget", Label: "No-think: send thinking_budget cap", Type: "toggle",
						ShowWhen: workerNoThink},
					{Field: "no_think_budget", Label: "No-think: budget value (tokens)", Type: "number", Min: 0, Max: 8192,
						ShowWhen: workerNoThink + ";no_think_send_budget",
						Help:     "0 = built-in default (512)."},
					{Field: "no_think_prepend_system", Label: "No-think: prepend /no_think to system prompt", Type: "toggle",
						ShowWhen: workerNoThink},
					{Field: "no_think_prepend_user", Label: "No-think: prepend /no_think to last user message", Type: "toggle",
						ShowWhen: workerNoThink},
				},
			},
		},
		{
			Title:    "Lead LLM",
			Subtitle: "The precision, remote model for high-stakes stages.",
			Detail:   "Routing sends \"lead\" stages here. Provider \"(use primary)\" reuses the worker. Applies immediately on save with no restart; the key is stored encrypted, and blank keeps the current one.",
			Body: ui.FormPanel{
				Source: "api/lead-llm",
				Fields: []ui.FormField{
					// What is RUNNING, above what is stored. A save writes the
					// config and then rebuilds, and a rebuild that fails leaves
					// the previous client serving every call - so the fields
					// below can read back perfectly while nothing uses them.
					{Field: "_live", Type: "readonly", Label: "In use"},
					{Field: "provider", Label: "Provider", Type: "select", Options: LLMProviderOptions(true),
						Help: "(use primary) routes lead stages to the worker model. A peer offering inference appears here too."},
					{Field: "model", Label: "Model", Type: "text", Placeholder: "e.g. claude-sonnet-5",
						ShowWhen: "provider"},
					{Field: "api_key", Label: "API key", Type: "password", Placeholder: "(leave blank to keep current)",
						ShowWhen: "provider:anthropic|openai|gemini|bedrock|llama.cpp",
						Help:     "Stored encrypted. The lead has its own key and never falls back to the worker's.",
						Detail:   "Ollama ignores it, and llama.cpp sends it only when the server requires one (started with --api-key). Leaving it blank on save keeps the current key."},
					{Field: "endpoint", Label: "Endpoint", Type: "text", Placeholder: "(provider default)",
						ShowWhen: "provider:ollama|llama.cpp|bedrock",
						Help:     "For local / self-hosted lead providers. On Bedrock this OVERRIDES the AWS region below.",
						Detail:   "Anything here is used as the host verbatim, so on Bedrock the region box stops deciding where calls go while going on displaying whatever it was set to. Leave it blank unless you are pointing at a private link or a VPC endpoint."},
					{Field: "aws_region", Label: "AWS region", Type: "text", Placeholder: "us-east-1",
						ShowWhen: "provider:bedrock",
						Help:     "AWS Bedrock only. Blank uses $AWS_REGION, then us-east-1.",
						Detail: "This tier has its OWN region. Setting it on the Worker does not set it on the Lead, and a blank one here falls through to $AWS_REGION on the gohort host and then to us-east-1 - which is why a tier can keep using us-east-1 while another tier's box reads us-west-2. The debug log names the region and where it came from as each client is built.\n\n" +
							"Not every region AWS lists for Bedrock has a Messages-API endpoint. us-west-1 does not; use us-west-2.",
						Presets: bedrockRegionPresets()},
					{Field: "bedrock_api", Label: "Bedrock API", Type: "select", ShowWhen: "provider:bedrock",
						Options: []ui.SelectOption{
							{Value: "", Label: "Messages API (bedrock-mantle)"},
							{Value: "invoke", Label: "InvokeModel (bedrock-runtime)"}},
						Help:   "Which Bedrock API your AWS role may call. Both stream.",
						Detail: "The Messages API needs bedrock-mantle:CreateInference. InvokeModel needs bedrock:InvokeModel, and is what most AI-tooling permission sets grant. A 403 on CreateInference means switch to InvokeModel."},
					{Field: "aws_profile", Label: "AWS profile", Type: "text", Placeholder: "(default)",
						ShowWhen: "provider:bedrock",
						Help:     "AWS Bedrock only. Blank uses $AWS_PROFILE.",
						Detail:   "Credentials are never stored here. For SSO, run `aws sso login` on the gohort host. The API key field above is optional, and means a Bedrock bearer token instead."},
					{Field: "context_size", Label: "Context size (tokens)", Type: "number", Min: 0, Max: 1000000,
						ShowWhen: "provider;provider:!openai|gemini",
						Help:     "0 uses the default: 200K for Anthropic and Bedrock, 65K for local providers.",
						Detail:   "For Anthropic and Bedrock this is the working cap the lead agent-loop history compaction keys on, not a hard API limit. The API accepts up to 1M, but input tokens bill on every turn, so raise it only for genuine long-context work."},
					{Field: "native_tools", Label: "Native tool calling", Type: "toggle",
						ShowWhen: "provider:ollama",
						Help:     "Disable for models without tool-calling support (ollama)."},
					{Type: "header", Label: "Thinking"},
					{Field: "disable_thinking", Label: "Disable thinking (force think=false)", Type: "toggle",
						ShowWhen: "provider;provider:!anthropic|openai|bedrock"},
					{Field: "thinking_budget", Label: "Thinking budget (tokens)", Type: "number", Min: 0, Max: 131072,
						ShowWhen: "provider;provider:!anthropic|openai|bedrock|ollama",
						Help:     thinkingBudgetHelp},
					leadOnly(effortDefaultField()), leadOnly(effortMaxField()),
					{Field: "no_think_use_kwarg", Label: "No-think: send enable_thinking=false", Type: "toggle",
						ShowWhen: leadNoThink},
					{Field: "no_think_send_budget", Label: "No-think: send thinking_budget cap", Type: "toggle",
						ShowWhen: leadNoThink},
					{Field: "no_think_budget", Label: "No-think: budget value (tokens)", Type: "number", Min: 0, Max: 8192,
						ShowWhen: leadNoThink + ";no_think_send_budget"},
					{Field: "no_think_prepend_system", Label: "No-think: prepend /no_think to system prompt", Type: "toggle",
						ShowWhen: leadNoThink},
					{Field: "no_think_prepend_user", Label: "No-think: prepend /no_think to last user message", Type: "toggle",
						ShowWhen: leadNoThink},
				},
			},
		},
		{
			Title:    "Model Privacy",
			Subtitle: "Some stages are pinned to the worker tier and cannot escalate.",
			Detail: "They handle material that must not reach a third-party model: SSH credentials, log contents, system facts." +
				"That pin exists because the lead is normally remote. If it is not, the pin costs you the better reasoner on exactly the work that needs it most.",
			Body: ui.FormPanel{
				Source: "api/llm-privacy",
				// The toggle changes which tiers a private stage may take, so
				// every routing table on the page has to re-ask.
				Invalidate: append([]string{"api/routing"}, appRoutingSources()...),
				Fields: []ui.FormField{
					{Field: "all_private", Label: "All LLMs are private", Type: "toggle",
						Help: "OFF (the default): private stages stay on the worker, always. " +
							"ON: private stages may be routed to the lead tier as well, and the lead options appear for them in the routing table below. " +
							"Turn this on only if you are certain every model above runs on hardware you control: this is your assertion, not a detected fact, " +
							"and getting it wrong sends credentials and log contents to a third party with no way to recall them."},
					{Field: "advice", Label: "What this deployment looks like", Type: "readonly",
						Help: "Judged from the configured providers. Advice only: the toggle is what takes effect."},
				},
			},
		},
		{
			Title:    "LLM Routing",
			Subtitle: "Pick which tier handles each pipeline stage, and whether it reasons.",
			Detail:   "\"lead\" uses the precision (remote) LLM; \"worker\" uses the local model; the \"(thinking)\" variant of either enables extended reasoning on that tier.\n\nTier and thinking are independent: a stage escalated to lead keeps thinking only if you pick \"lead (thinking)\". Budget caps thinking tokens for that stage, where 0 is the stage default.\n\nA private stage cannot route to lead unless Model Privacy is turned on above.",
			Body:     routingTable("", appRoutingSources()),
		},
		{
			Title:    "Ollama Proxy",
			Subtitle: "Expose gohort as a fair-queued Ollama endpoint.",
			Detail:   "Point Ollama clients at gohort's port instead of Ollama's and they share the local model scheduler.\n\nThis is a separate listener on its own port. It is not behind the dashboard login, the admin IP allowlist, or TLS, so what it is bound to is what decides who can reach it. Changing the port or interface requires a restart.",
			Body: ui.FormPanel{
				Source: "api/settings",
				Method: settingsSaveMethod,
				Fields: []ui.FormField{
					{Field: "ollama_proxy_enabled", Label: "Enable Ollama Proxy", Type: "toggle"},
					{Field: "ollama_proxy_port", Label: "Proxy port", Type: "number",
						Min: 1024, Max: 65535,
						Help:        "TCP port the proxy listens on. Default suggestion: 11435.",
						ShowWhen:    "ollama_proxy_enabled",
						Placeholder: "11435"},
					{Field: "ollama_proxy_bind", Label: "Reachable from", Type: "select",
						Options: []ui.SelectOption{
							{Value: "127.0.0.1", Label: "This machine only (127.0.0.1)"},
							{Value: "0.0.0.0", Label: "Any machine on the network (0.0.0.0)",
								Confirm: "Expose the Ollama proxy on every network interface? It is not behind the dashboard login. Off-box requests will be refused without a personal access token, but the port becomes reachable."},
						},
						Help:     "This machine only is the default, and needs no credential.",
						Detail:   "That is the same trust gohort extends to anything else running on the box. Choosing the network means every request from off-box must carry a personal access token in X-API-Key or Authorization: Bearer. Check your Ollama client can send a header before switching, because most cannot.",
						ShowWhen: "ollama_proxy_enabled"},
				},
			},
		},
		{
			Title:    "Agent Loop Tuning",
			Subtitle: "Per-round behavior of the agent loop.",
			Detail:   "Lower the history budget when long sessions push prefill latency or thrash the LLM's prompt cache; raise it when you need the model to remember more context across rounds.",
			Body: ui.FormPanel{
				Source: "api/agent-loop-tuning",
				Fields: []ui.FormField{
					{Field: "history_budget_percent", Label: "History budget (% of context window)",
						Type: "number", Min: 25, Max: 90, Placeholder: "50",
						Help:   "Steady-state cap on per-round history, as a percent of the LLM's context window.",
						Detail: "Default 50, so a 200K-window worker targets about 100K of history. Lower means faster prefill and more aggressive elision of old tool results; higher means more retained context and slower prefill. Clamped to 25-90."},
				},
			},
		},
		{
			Title:    "Local Model Scheduler",
			Subtitle: "Concurrent-request caps for local LLM backends. Default 1, strictly serial.",
			Detail:   "Raise it only when the backend supports parallel requests. Applies immediately on save, because the live LLM is rebuilt.",
			Body: ui.FormPanel{
				Source: "api/local-scheduler",
				Fields: []ui.FormField{
					{Field: "ollama_max_parallel", Label: "Ollama max parallel", Type: "number",
						Min: 1, Max: 16},
					{Field: "llamacpp_max_parallel", Label: "llama.cpp max parallel (also caps peer-backed tiers)", Type: "number",
						Min: 1, Max: 16,
						Help: "A tier running on a peer goes through the llama.cpp client, so this limit applies to it too."},
				},
			},
		},
	}
}

// thinkingBudgetHelp is shared by both tiers: what 0 means, and whether the
// value caps per-call budgets, depends on the provider reading it.
const thinkingBudgetHelp = "Default 4096. On llama.cpp and peers, 0 = unlimited and this is the ceiling for per-agent and per-route budgets. On Gemini, 0 = 16384 and per-call budgets are not capped."

// workerNoThink and leadNoThink show the no-think signal settings only for
// providers that send them (llama.cpp and peers). A "!" list also matches an
// empty provider, so the Lead form first requires one to be picked, which
// excludes "(use primary)".
const (
	workerNoThink = "provider:!ollama|anthropic|openai|gemini|bedrock"
	leadNoThink   = "provider;" + workerNoThink
)

// leadOnly gates a shared tier field on the Lead form, where a blank provider
// means "(use primary)" and the worker's own settings apply instead.
func leadOnly(f ui.FormField) ui.FormField {
	f.ShowWhen = "provider"
	return f
}

// effortDefaultField and effortMaxField are the per-tier effort settings,
// identical on the Worker and Lead forms because each tier resolves effort
// the same way; only the stored values differ.
func effortDefaultField() ui.FormField {
	return ui.FormField{Field: "default_effort", Label: "Default effort", Type: "select",
		Options: []ui.SelectOption{
			{Value: "", Label: "Model default"},
			{Value: "off", Label: "Off"},
			{Value: "low", Label: "Low"},
			{Value: "medium", Label: "Medium"},
			{Value: "high", Label: "High"}},
		Help: "How hard this tier reasons when a call or agent names no effort.",
		Detail: "Each provider gets its own dial: Claude's effort setting (or a thinking budget on older Claude models), OpenAI's reasoning effort, a thinking budget for llama.cpp and Gemini, on/off for Ollama. " +
			"A token budget set on an agent or a route overrides it, and a stage routed without thinking stays without it."}
}

func effortMaxField() ui.FormField {
	return ui.FormField{Field: "max_effort", Label: "Maximum effort", Type: "select",
		Options: []ui.SelectOption{
			{Value: "", Label: "No cap"},
			{Value: "low", Label: "Low"},
			{Value: "medium", Label: "Medium"},
			{Value: "high", Label: "High"}},
		Help:   "The most effort any call on this tier may use, whatever an agent asks for.",
		Detail: "Caps effort levels only. An explicit token budget is not an effort level; on llama.cpp the thinking budget above remains its ceiling."}
}

// routingTable is the tier-and-budget table, over every stage or over the
// stages one app has claimed.
//
// One builder for both views so they cannot drift: the LLMs tab and an app's
// pane on the Apps tab show the same control, writing the same key through the
// same POST. invalidate names the OTHER views of those rows, because a select
// does not reload anything on its own and a tier set in one place must not be
// contradicted by the other a tab-click later.
func routingTable(app string, invalidate []string) ui.Table {
	source := "api/routing"
	cols := []ui.Col{
		{Field: "label", Flex: 1},
		{Field: "group", Mute: true},
	}
	empty := "No routing stages registered."
	if app != "" {
		source = routingSourceForApp(app)
		// Every row here is this app's, so the group column would only repeat
		// the section heading.
		cols = []ui.Col{{Field: "label", Label: "Routing stage", Flex: 1}}
		empty = "This app has claimed no routing stages."
	}
	return ui.Table{
		Source:  source,
		RowKey:  "key",
		Columns: cols,
		RowActions: []ui.RowAction{
			{
				Type:   "select",
				Field:  "value",
				PostTo: "api/routing",
				Method: "POST",
				Width:  "10rem",
				Options: []ui.SelectOption{
					{Value: "lead", Label: "Lead"},
					{Value: "lead (thinking)", Label: "Lead (Thinking)"},
					{Value: "worker", Label: "Worker"},
					{Value: "worker (thinking)", Label: "Worker (Thinking)"},
				},
				// Hide the lead options when the stage is
				// private — private stages can't escalate. BOTH
				// lead values, or the private row would offer an
				// escalation the server then refuses.
				FilterOptionsIf: "private",
				FilterOptions:   "lead,lead (thinking)",
				// Mark the option matching the stage's
				// out-of-the-box Default with an asterisk
				// so operators can tell at a glance what
				// the registered default is even when
				// they've overridden it.
				DefaultField: "default",
				Invalidate:   invalidate,
			},
			{
				Type:       "number",
				Field:      "think_budget",
				Label:      "budget",
				PostTo:     "api/routing",
				Method:     "POST",
				Min:        0,
				Max:        65536,
				Width:      "7rem",
				Invalidate: invalidate,
			},
		},
		EmptyText: empty,
	}
}

// routingSourceForApp is the source of one app's routing table. Named so the
// table and the invalidation lists cannot disagree on its spelling — a source
// that differs by one character is a table that never refreshes.
func routingSourceForApp(app string) string {
	return "api/routing?app=" + app
}

// appRoutingSources lists every app routing table the Apps tab will render,
// for the views that must tell them a row changed.
func appRoutingSources() []string {
	var out []string
	for _, rw := range listableApps() {
		if len(RouteStagesForApp(rw.path)) > 0 {
			out = append(out, routingSourceForApp(rw.path))
		}
	}
	return out
}
