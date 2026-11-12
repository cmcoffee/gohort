package admin

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// llmSections is the llm part of the admin page: Worker LLM, Lead LLM, Model Privacy, LLM Routing, Ollama Proxy, Agent Loop Tuning, Local Model Scheduler.
func (a *AdminApp) llmSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "Worker LLM",
			Subtitle: "The primary, local model most work runs on.",
			Detail:   "Applies immediately on save, because the live LLM is rebuilt and nothing restarts. The API key is kept encrypted, for the address it was given for; blank keeps the current one.",
			Body: ui.FormPanel{
				Assist: true,
				Source: "api/worker-llm",
				// Says what is actually at a local endpoint as well as whether
				// it answers: the kind of server, the model and its context,
				// and what in this form does not match it.
				TestURL:   "api/worker-llm/test",
				TestLabel: "Test connection",
				Fields: tierReach([]ui.FormField{
					{Field: "provider", Label: "Provider", Type: "select", Options: LLMProviderOptions(false),
						Help:   "Local providers, ollama, llama.cpp or vLLM, are the usual worker.",
						Detail: tierProviderDetail},
					{Field: "model", Label: "Model", Type: "text", Placeholder: "e.g. qwen3.6-27b", SaveOnBlur: true,
						SuggestionsSource: "api/worker-llm/models", SuggestionsLabel: "List models",
						Help:   "Blank uses the provider default. List models asks the provider what it offers.",
						Detail: "On AWS Bedrock, many accounts require a region-prefixed inference profile (us.anthropic.claude-opus-4-8) and deny the bare id."},
					{Field: "context_size", Label: "Context size (tokens)", Type: "number", Min: 0, Max: 1000000,
						ShowWhen: providerWhen(false, "openai", "gemini"),
						Help:     "0 uses the default: 65K for ollama, llama.cpp and vLLM, 200K for Anthropic and Bedrock.",
						Detail:   "Local providers send this as num_ctx. For Anthropic and Bedrock it is the working cap history compaction keys on. The API accepts up to 1M, but every input token bills per turn, so keep it modest. A model borrowed from a peer uses the size the peer set for it: 0 takes it, and a larger number here is lowered to it, so a conversation is trimmed before the peer's server would refuse it."},
					{Field: "request_timeout_seconds", Label: "Request timeout (sec)", Type: "number", Min: 0, Max: 3600,
						Help: "0 = default 300s."},
					{Field: "native_tools", Label: "Native tool calling", Type: "toggle",
						// Only ollama reads it: every hosted API and llama.cpp, and so
						// every peer, calls tools natively (ProviderHasNativeTools).
						ShowWhen: providerWhen(true, "ollama"),
						Help:     "Disable for models without tool-calling support (ollama)."},
					{Type: "header", Label: "Capabilities"},
					readsImagesField(""),
					{Type: "header", Label: "Thinking"},
					{Field: "disable_thinking", Label: "Let the model think", Type: "toggle", Invert: true,
						ShowWhen: providerWhen(false, "anthropic", "openai", "bedrock"),
						Help:     "Off sends every request with thinking turned off, whatever an agent asks for."},
					{Field: "thinking_budget", Label: "Thinking budget (tokens)", Type: "number", Min: 0, Max: 131072,
						ShowWhen: providerWhen(false, "anthropic", "openai", "bedrock", "ollama", "vllm"),
						Help:     thinkingBudgetHelp},
					effortDefaultField(), effortMaxField(),
					{Field: "no_think_use_kwarg", Label: "No-think: send enable_thinking=false", Type: "toggle",
						ShowWhen: workerNoThink()},
					{Field: "no_think_send_budget", Label: "No-think: send thinking_budget cap", Type: "toggle",
						ShowWhen: workerNoThinkBudget()},
					{Field: "no_think_budget", Label: "No-think: budget value (tokens)", Type: "number", Min: 0, Max: 8192,
						ShowWhen: workerNoThinkBudget() + ";no_think_send_budget",
						Help:     "0 = built-in default (512)."},
					{Field: "no_think_prepend_system", Label: "No-think: prepend /no_think to system prompt", Type: "toggle",
						ShowWhen: workerNoThink()},
					{Field: "no_think_prepend_user", Label: "No-think: prepend /no_think to last user message", Type: "toggle",
						ShowWhen: workerNoThink()},
				}),
			},
		},
		{
			Title:    "Lead LLM",
			Subtitle: "The precision, remote model for high-stakes stages.",
			Detail:   "Routing sends \"lead\" stages here. Provider \"(use primary)\" reuses the worker. Applies immediately on save with no restart; the API key is kept encrypted, for the address it was given for, and blank keeps the current one.",
			Body: ui.FormPanel{
				Assist:       true,
				Source:       "api/lead-llm",
				TestURL:      "api/lead-llm/test",
				TestLabel:    "Test connection",
				TestShowWhen: "provider", // "(use primary)" has nothing of its own to test
				Fields: tierReach([]ui.FormField{
					// What is RUNNING, above what is stored. A save writes the
					// config and then rebuilds, and a rebuild that fails leaves
					// the previous client serving every call - so the fields
					// below can read back perfectly while nothing uses them.
					{Field: "_live", Type: "readonly", Label: "In use"},
					{Field: "provider", Label: "Provider", Type: "select", Options: LLMProviderOptions(true),
						Help:   "(use primary) routes lead stages to the worker model. A peer offering inference appears here too.",
						Detail: tierProviderDetail},
					{Field: "model", Label: "Model", Type: "text", Placeholder: "e.g. claude-sonnet-5", SaveOnBlur: true,
						ShowWhen:          "provider",
						SuggestionsSource: "api/lead-llm/models", SuggestionsLabel: "List models",
						Help: "List models asks the provider what it offers."},
					{Field: "context_size", Label: "Context size (tokens)", Type: "number", Min: 0, Max: 1000000,
						ShowWhen: providerWhen(false, "openai", "gemini"),
						Help:     "0 uses the default: 200K for Anthropic and Bedrock, 65K for local providers.",
						Detail:   "For Anthropic and Bedrock this is the working cap the lead agent-loop history compaction keys on, not a hard API limit. The API accepts up to 1M, but input tokens bill on every turn, so raise it only for genuine long-context work. A model borrowed from a peer uses the size the peer set for it: 0 takes it, and a larger number here is lowered to it, so a conversation is trimmed before the peer's server would refuse it."},
					{Field: "native_tools", Label: "Native tool calling", Type: "toggle",
						ShowWhen: providerWhen(true, "ollama"),
						Help:     "Disable for models without tool-calling support (ollama)."},
					{Type: "header", Label: "Capabilities", ShowWhen: "provider"},
					leadReadsImagesField(),
					{Type: "header", Label: "Thinking"},
					{Field: "disable_thinking", Label: "Let the model think", Type: "toggle", Invert: true,
						ShowWhen: providerWhen(false, "anthropic", "openai", "bedrock"),
						Help:     "Off sends every request with thinking turned off, whatever an agent asks for."},
					{Field: "thinking_budget", Label: "Thinking budget (tokens)", Type: "number", Min: 0, Max: 131072,
						ShowWhen: providerWhen(false, "anthropic", "openai", "bedrock", "ollama", "vllm"),
						Help:     thinkingBudgetHelp},
					leadOnly(effortDefaultField()), leadOnly(effortMaxField()),
					{Field: "no_think_use_kwarg", Label: "No-think: send enable_thinking=false", Type: "toggle",
						ShowWhen: leadNoThink()},
					{Field: "no_think_send_budget", Label: "No-think: send thinking_budget cap", Type: "toggle",
						ShowWhen: leadNoThinkBudget()},
					{Field: "no_think_budget", Label: "No-think: budget value (tokens)", Type: "number", Min: 0, Max: 8192,
						ShowWhen: leadNoThinkBudget() + ";no_think_send_budget"},
					{Field: "no_think_prepend_system", Label: "No-think: prepend /no_think to system prompt", Type: "toggle",
						ShowWhen: leadNoThink()},
					{Field: "no_think_prepend_user", Label: "No-think: prepend /no_think to last user message", Type: "toggle",
						ShowWhen: leadNoThink()},
				}),
			},
		},
		{
			Title:    "Decision Offloading",
			Subtitle: "Takes gohort's own checks off the worker, so the worker spends its time on the agents.",
			Detail: "What it is for. Around every turn gohort makes short checks of its own: the guardrail checks before input, each action and each reply, the tool-result scan, the turn and grounding judges, the image source check, the memory checks. " +
				"On the worker, each one waits its turn behind the agents' rounds, and the agents' next round waits behind it. An Offload model is a second, usually smaller, model that answers the checks instead, so neither waits for the other.\n\n" +
				"Who it fits. A deployment with a second GPU or a spare machine (a small model there, such as a 4B or 9B), or a peer that lends a model: the checks move off the main model and busy turns get faster. " +
				"A deployment whose worker is a hosted API: the checks stop being paid calls, and what they read stays on your hardware. " +
				"It helps little with one GPU that the worker already fills: a second model there mostly moves the wait.\n\n" +
				"How to set it up. Run the model on its own server (vLLM suits bursts of checks best; llama.cpp and Ollama work too), give its address and model here, and Test connection. " +
				"A check goes to it once the Offload map below shows its test cases all right on it, twice in a row; until then it stays on the worker. The map tests a new Offload model on its own a few minutes after it is set. Check tests, at the foot of this section (Test the checks, beside Test connection), show each case's answers side by side; watch the log for a day too (call done in, route=).\n\n" +
				"Limits. Local or a peer only: the checks read everything the worker reads, the person's messages, tool results and replies, so a hosted model is refused. " +
				"A smaller model is a weaker reader: a check it gets wrong is not mapped to it, and one you want kept off it whatever its cases say can be switched off under Keep checks on the worker. " +
				"A check with pictures always runs on the worker: the checks the Offload model can be mapped to are text, so it needs no vision stack. The Offload model never thinks: a check that asks to think (a confirming reading, a thorough guardrail) runs on the worker, which reasons faster at length, and every other call is sent with thinking off. If it fails a call, that call and the next few minutes of checks run on the worker. Its tokens count with the worker's in usage.",
			Body: ui.Stack{Children: []ui.Component{
				// What the map may use, first: it caps the tiers below.
				ui.FormPanel{
					Source:     "api/judge-llm/mode",
					PostURL:    "api/judge-llm/mode",
					Invalidate: []string{"api/judge-llm", "api/judge-llm/map"},
					Fields: []ui.FormField{{Field: "mode", Label: "Use", Type: "select", Options: offloadModeOptions(),
						Help: "Which of the Offload model and System One checks may go to. The map below still decides check by check."}},
				},
				ui.FormPanel{
					Assist:       true,
					Source:       "api/judge-llm",
					TestURL:      "api/judge-llm/test",
					TestLabel:    "Test connection",
					TestShowWhen: "provider",
					TestLink:     &ui.FormLink{Label: "Test the checks ↓", Href: "#" + checkTestsAnchor},
					Invalidate:   []string{"api/judge-llm/calls", "api/judge-llm/map"},
					Fields: tierReach([]ui.FormField{
						{Field: "_live", Type: "readonly", Label: "In use"},
						{Field: "_path", Type: "readonly", Label: "Decision path"},
						{Field: "provider", Label: "Provider", Type: "select", Options: judgeProviderOptions(),
							Help: "(use the worker) leaves the checks where they are. A peer offering inference appears here too."},
						{Field: "model", Label: "Model", Type: "text", Placeholder: "e.g. Qwen/Qwen3.5-4B", SaveOnBlur: true,
							ShowWhen:          "provider",
							SuggestionsSource: "api/judge-llm/models", SuggestionsLabel: "List models",
							Help: "List models asks the server what it serves."},
					}),
				},
				// Which checks it answers, and how it does on their cases, at
				// the foot of its own section: one place for the Judge model.
				// Test connection links down to the check tests, so they are
				// found without scrolling past the list.
				ui.Subsection{
					Title:    "Offload map",
					Subtitle: "A check goes to the Offload model, or asks System One first, only once its test cases came out all right there twice in a row. Everything else stays on the worker. A row's switch keeps that check on the worker whatever the map says.",
					Detail: "A mapping is kept against what earned it: the Offload model as it runs, System One's choice, and the check's own prompts. When any of those changes, that check goes back to the worker, and the map tests it again on its own a few minutes later. " +
						"A model that missed a case is not tested on it again until Map the checks (below) is pressed, so a check it cannot do does not cost a run every few minutes. " +
						"A check with no test cases is never mapped.",
					Body: judgeMapTable(),
				},
				checkTestsPanel(),
			}},
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
			Title:    "Learned from model refusals",
			Subtitle: "Where a model refused part of a request, what gohort sends it instead.",
			Detail: "A model that refuses part of a request (a way of turning thinking off, a setting it does not take, pictures) is believed: gohort sends what its refusal points to, " +
				"keeps that for the model, and retries the call, so the call does not fail on it. Each row stands 30 days, then the model is sent the request as configured again, " +
				"since providers lift limits as well as add them. Forget does that now. A different model is a different row: changing a tier's model starts it fresh.",
			Body: ui.Table{
				Source:    "api/llm-adaptations",
				RowKey:    "_id",
				EmptyText: "No model has refused part of a request: each is sent every request as configured.",
				Columns: []ui.Col{
					{Field: "model", Label: "Model", Flex: 2},
					{Field: "what", Label: "Refused"},
					{Field: "sends", Label: "Sent instead", Flex: 2},
					{Field: "since", Label: "Since", Format: "reltime", Mute: true},
					{Field: "refusal", Label: "Its refusal", Flex: 3, Mute: true},
				},
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Forget", Method: "POST", PostTo: "api/llm-adaptations/forget?id={_id}", Compact: true,
						Confirm: "Send this model the request as configured again? If it refuses again, gohort learns it again from that refusal."},
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
			Title:    "Local Model Scheduler",
			Subtitle: "How requests share the model: concurrent-request caps for local backends (default 1, strictly serial; 8 for vLLM, which batches), and whether background work waits for people.",
			Detail:   "Raise it only when the backend supports parallel requests. Applies immediately on save, because the live LLM is rebuilt.",
			Body: ui.FormPanel{
				Assist: true,
				Source: "api/local-scheduler",
				Fields: []ui.FormField{
					{Field: "ollama_max_parallel", Label: "Ollama max parallel", Type: "number",
						Min: 1, Max: 16},
					{Field: "llamacpp_max_parallel", Label: "llama.cpp / vLLM max parallel (also caps peer-backed tiers)", Type: "number",
						Min: 1, Max: 64,
						Help:   "One queue for the local model server, whichever it is.",
						Detail: "vLLM batches concurrent requests, so it can take many more than llama.cpp; size it to the server's --max-num-seqs. A tier running on a peer goes through the same client, so this limit applies to it too."},
					{Field: "background_yield", Label: "Background work yields to people", Type: "select",
						Options: []ui.SelectOption{
							{Value: "", Label: "Auto: on a local or peer model, not on a hosted API"},
							{Value: "always", Label: "Always"},
							{Value: "never", Label: "Never"}},
						Help:   "Whether schedules, monitors and other unattended runs wait while someone is using the model.",
						Detail: "Parallel slots on one GPU share its compute and its cache, so a background run beside a person's turn slows the turn; Auto waits on local and peer models. A hosted API has no shared hardware, so Auto does not wait there. Choose Always for an OpenAI-compatible server on your own machine or a tight API rate limit, Never for a server with GPUs to spare. A peer borrowing this model is held to this setting here. Agents marked Lowest wait either way."},
					{Field: "image_render_yield", Label: "Scheduled image renders wait for people's", Type: "select",
						Options: []ui.SelectOption{
							{Value: "", Label: "Yes: a render somebody is waiting on goes first"},
							{Value: "never", Label: "No: first come, first served"}},
						Help:   "Whether a render a schedule or monitor asked for waits behind one a person asked for.",
						Detail: "Renders run one at a time and take turns between callers. With this on, a person's render goes ahead of a scheduled one queued before it, and a scheduled render that has waited 5 minutes is served anyway, so a busy day cannot stall a schedule. Turn it off when every render is equally urgent. It orders renders among themselves only: a render does not wait for anyone's chat."},
				},
			},
		},
	}
}

// thinkingBudgetHelp is shared by both tiers: what 0 means, and whether the
// value caps per-call budgets, depends on the provider reading it.
const thinkingBudgetHelp = "Default 4096. On llama.cpp and peers, 0 = unlimited and this is the ceiling for per-agent and per-route budgets. On Gemini, 0 = 16384 and per-call budgets are not capped."

// workerNoThink and leadNoThink show the no-think signal settings only for
// providers that send them (llama.cpp, vLLM and peers). A "!" list also
// matches an empty provider, so the Lead form first requires one to be picked,
// which excludes "(use primary)". The budget pair also leaves out vLLM, which
// ignores thinking_budget_tokens: a control there would do nothing.
func workerNoThink() string {
	return providerWhen(false, "ollama", "anthropic", "openai", "gemini", "bedrock")
}
func leadNoThink() string { return workerNoThink() }
func workerNoThinkBudget() string {
	return providerWhen(false, "ollama", "anthropic", "openai", "gemini", "bedrock", "vllm")
}
func leadNoThinkBudget() string { return workerNoThinkBudget() }

// providerWhen is a show_when for the tier's choices whose dialect is among
// dialects (include), or is not: each choice by the client gohort speaks to
// it (a peer's is llama.cpp).
func providerWhen(include bool, dialects ...string) string {
	set := map[string]bool{}
	for _, d := range dialects {
		set[d] = true
	}
	var values []string
	for _, o := range LLMProviderOptions(false) {
		if o.Value != "" && set[choiceDialect(o.Value)] == include {
			values = append(values, o.Value)
		}
	}
	if len(values) == 0 {
		values = []string{"-"}
	}
	return "provider:" + strings.Join(values, "|")
}

// choiceDialect is the dialect gohort speaks to a tier's choice.
func choiceDialect(value string) string {
	if strings.HasPrefix(value, PeerProviderValue("")) {
		return "llama.cpp"
	}
	return value
}

// leadOnly gates a shared tier field on the Lead form, where a blank provider
// means "(use primary)" and the worker's own settings apply instead.
// readsImagesField is the image capability, the same on both tiers. Stored as
// no_images so every model configured before it existed keeps reading them.
func readsImagesField(showWhen string) ui.FormField {
	return ui.FormField{Field: "no_images", Label: "Reads images", Type: "toggle", Invert: true, ShowWhen: showWhen,
		Help: "Off: this model is never sent an image. A model that reads them describes each one in its place.",
		Detail: "Turn it off for a model that cannot see, or for one you would rather not show your pictures to. " +
			"Each image or video still sent to this model is replaced by a description written by the other tier when that one reads images, worker first, or by a note that an image was there when neither does. " +
			"With the worker on and the lead off, pictures stay on this machine: the lead only ever gets the worker's words about them. " +
			"The image stays in the conversation, so a model that can see still gets it."}
}

// leadReadsImagesField is readsImagesField on the Lead form, which says where
// a new lead starts (newLeadSkipsImages).
func leadReadsImagesField() ui.FormField {
	f := readsImagesField("provider")
	f.Detail += " A new lead starts with this off when the worker reads images, so your pictures stay with the worker, unless Model Privacy says every model is private."
	return f
}

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

// tierProviderDetail is where a tier's model can come from.
const tierProviderDetail = "Ollama, llama.cpp and vLLM are servers on this machine or its network. OpenAI and Anthropic are their own APIs, or, given an address, any provider that speaks theirs: Groq, OpenRouter, Together, Mistral, DeepSeek, a gateway. A peer offering inference appears here too, and its GPU runs the turns. Token prices for cost estimates are the tier's rates under Costs."

// bedrockAPIDetail says which Bedrock API needs which permission.
const bedrockAPIDetail = "Converse is AWS's one format for every model Bedrock hosts (Claude, Llama, Mistral, Nova and the rest) and needs bedrock:InvokeModel. The other two speak Claude's own format and serve Claude only: the Messages API needs bedrock-mantle:CreateInference, and InvokeModel needs bedrock:InvokeModel. A 403 on CreateInference means switch to Converse or InvokeModel."

// bedrockRegionPresets lists regions that actually have a Messages-API
// endpoint, verified by resolving each host. AWS's published Bedrock region
// table is broader than this: it describes where the service is reachable via
// routing, and several of those regions have no endpoint to point a client at.
// Offered as presets rather than a closed select so a region AWS adds later
// can still be typed in.
func bedrockRegionPresets() []ui.FieldPreset {
	return []ui.FieldPreset{
		{Label: "us-east-1", Value: "us-east-1", Hint: "N. Virginia"},
		{Label: "us-east-2", Value: "us-east-2", Hint: "Ohio"},
		{Label: "us-west-2", Value: "us-west-2", Hint: "Oregon: use this one, not us-west-1"},
		{Label: "eu-west-1", Value: "eu-west-1", Hint: "Ireland"},
		{Label: "eu-central-1", Value: "eu-central-1", Hint: "Frankfurt"},
		{Label: "ap-northeast-1", Value: "ap-northeast-1", Hint: "Tokyo"},
		{Label: "ap-southeast-2", Value: "ap-southeast-2", Hint: "Sydney"},
	}
}

// tierReach are a tier's form fields with where it reaches its model put
// under the model (or the provider, in a form without one): a notice when it
// still names a connection, its address, its key and the key it has saved,
// and Bedrock's region, API and profile. Each shows only for the providers it
// is for, so none shows on a lead left on "(use primary)". Each saves when
// left, as the tier's other fields do: a save rebuilds the live model.
func tierReach(fields []ui.FormField) []ui.FormField {
	// What a blank region box calls here, read when the page is built, so a
	// blank box names the region in use rather than a fixed us-east-1.
	blankRegion, blankFrom := BedrockBlankRegion()
	reach := []ui.FormField{
		{Field: "_stale", Type: "readonly", Label: "Needs setting again", ShowWhen: "_stale"},
		{Field: "endpoint", Label: "Address", Type: "text", Placeholder: "blank: the provider's own", SaveOnBlur: true,
			ShowWhen: "provider:anthropic|openai|ollama|llama.cpp|vllm|bedrock",
			Help:     "OpenAI or Anthropic: blank uses their own API; an address uses any provider that speaks it. Local servers: where the server listens.",
			Detail:   "A key goes only to the address it was given for: change the address and give the key again.",
			Presets: []ui.FieldPreset{
				{Label: "Ollama", Value: "http://localhost:11434"},
				{Label: "llama.cpp", Value: "http://localhost:8080/v1"},
				{Label: "vLLM", Value: "http://localhost:8000/v1"},
				{Label: "Groq", Value: "https://api.groq.com/openai/v1", Hint: "with OpenAI"},
				{Label: "OpenRouter", Value: "https://openrouter.ai/api/v1", Hint: "with OpenAI"},
				{Label: "Together", Value: "https://api.together.xyz/v1", Hint: "with OpenAI"},
				{Label: "Mistral", Value: "https://api.mistral.ai/v1", Hint: "with OpenAI"},
				{Label: "DeepSeek", Value: "https://api.deepseek.com/v1", Hint: "with OpenAI"}}},
		{Field: "api_key_from", Label: "API key from", Type: "select", Default: keyOwn,
			ShowWhen:      "provider:anthropic|openai|gemini|bedrock|llama.cpp|vllm",
			OptionsSource: keystoreSource + "/choices?tier=1",
			Help:          "A key kept once under Services > Keystore, shared with every connector that talks to this provider, or one this tier keeps for itself.",
			Detail:        "A Keystore key is used only when it is given for this tier's address; saving with one that is not says so. Rotating it in the Keystore reloads this tier with the new key."},
		{Field: "api_key", Label: "API key", Type: "password", Placeholder: "(leave blank to keep current)", SaveOnBlur: true,
			ShowWhen: "provider:anthropic|openai|gemini|bedrock|llama.cpp|vllm;api_key_from:" + keyOwn,
			Help:     "Stored encrypted, for the address above. Ollama ignores it; llama.cpp, vLLM and a compatible provider at its own address send it only when given."},
		{Field: "_kept_key", Type: "readonly", Label: "Saved API key", ShowWhen: "provider;_kept_key"},
		{Field: "aws_region", Label: "AWS region", Type: "text", Placeholder: blankRegion, SaveOnBlur: true,
			ShowWhen: "provider:bedrock",
			Help:     "AWS Bedrock only. Blank calls " + blankRegion + " on this host, set by " + blankFrom + ".",
			Detail: "This tier has its OWN region. Setting it on one tier does not set it on the other, and a blank one falls through to $AWS_REGION on the gohort host and then to us-east-1. The debug log names the region and where it came from as each client is built.\n\n" +
				"Not every region AWS lists for Bedrock has a Messages-API endpoint. us-west-1 does not; use us-west-2.",
			Presets: bedrockRegionPresets()},
		{Field: "bedrock_api", Label: "Bedrock API", Type: "select", ShowWhen: "provider:bedrock",
			Options: []ui.SelectOption{
				{Value: "converse", Label: "Converse API (any Bedrock model)"},
				{Value: "messages", Label: "Messages API (bedrock-mantle, Claude only)"},
				{Value: "invoke", Label: "InvokeModel (bedrock-runtime, Claude only)"}},
			Help:   "Which Bedrock API your AWS role may call. All three stream.",
			Detail: bedrockAPIDetail},
		{Field: "aws_profile", Label: "AWS profile", Type: "text", Placeholder: "(default)", SaveOnBlur: true,
			ShowWhen: "provider:bedrock",
			Help:     "AWS Bedrock only. Blank uses $AWS_PROFILE.",
			Detail:   "Credentials are never stored here. For SSO, run `aws sso login` on the gohort host. The API key above is optional: a Bedrock API key (bearer token), or an AWS key pair as ACCESS_KEY_ID:SECRET_ACCESS_KEY."},
	}
	at := -1
	for i, f := range fields {
		if f.Field == "model" || (f.Field == "provider" && at < 0) {
			at = i
		}
	}
	out := append([]ui.FormField{}, fields[:at+1]...)
	out = append(out, reach...)
	return append(out, fields[at+1:]...)
}

// judgeProviderOptions are the providers a Judge model may use: the local
// servers, an OpenAI-compatible one at a local address, and peers. Hosted
// providers are left off (the save refuses them too: JudgePrivate).
func judgeProviderOptions() []ui.SelectOption {
	out := []ui.SelectOption{{Value: "", Label: "(use the worker)", Help: "The checks run on the worker model."}}
	for _, o := range LLMProviderOptions(false) {
		switch {
		case o.Value == "ollama", o.Value == "llama.cpp", o.Value == "vllm":
		case o.Value == "openai":
			o.Label = "OpenAI-compatible, at a local address"
			o.Help = "A server on this network that speaks the OpenAI API: give its address below."
		case strings.HasPrefix(o.Value, PeerProviderValue("")):
		default:
			continue
		}
		out = append(out, o)
	}
	return out
}
