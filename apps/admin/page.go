// New admin page — framework-rendered with the simple sections migrated
// to core/ui's declarative components. Sections that need new framework
// primitives (DB Browser tree, Cost Rates split chart, Routing
// per-row select+number table, Watchers/Tools/Tasks edit dialogs) still
// live at /admin/legacy until each gets ported.

package admin

import (
	"net/http"

	"github.com/cmcoffee/gohort/core/ui"
)

func (a *AdminApp) serveNewAdminPage(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	page := ui.Page{
		Title:     "Administrator",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "900px", // wider than mobile-default; admin lives on a desktop browser
		Sections: []ui.Section{
			{
				Title:    "System Status",
				Subtitle: "Live readiness summary. Refreshes every 10 seconds.",
				Body: ui.DisplayPanel{
					Source:        "api/status",
					AutoRefreshMS: 10000,
					Pairs: []ui.DisplayPair{
						{Label: "TLS enabled", Field: "tls_enabled"},
						{Label: "TLS self-signed", Field: "tls_self_signed"},
						{Label: "Auth enabled", Field: "auth_enabled"},
						{Label: "User count", Field: "user_count"},
						{Label: "Active sessions", Field: "active_sessions"},
						{Label: "Public signup", Field: "allow_signup"},
					},
				},
			},
			{
				Title:    "Site Settings",
				Subtitle: "Authentication, naming, and operational quotas. Saved automatically as you edit.",
				Body: ui.FormPanel{
					Source: "api/settings",
					Fields: []ui.FormField{
						{Field: "allow_signup", Label: "Allow public signup", Type: "toggle",
							Help: "When off, only existing accounts can sign in. Approvals can still happen via the user list."},
						{Field: "service_name", Label: "Service name", Type: "text",
							Placeholder: "gohort", Help: "Shown in the page title and email From: line."},
						{Field: "external_url", Label: "External URL", Type: "text",
							Placeholder: "https://gohort.example.com",
							Help: "Used to build links in notification emails. Include scheme."},
						{Field: "notify_from", Label: "Notification From", Type: "text",
							Placeholder: "noreply@example.com"},
						{Field: "session_days", Label: "Session lifetime (days)", Type: "number",
							Min: 1, Max: 90, Help: "Default 7."},
						{Field: "max_login_attempts", Label: "Max login attempts", Type: "number",
							Min: 1, Max: 100, Help: "Default 5. Failed attempts above this trigger a temporary lockout."},
						{Field: "lockout_minutes", Label: "Lockout duration (minutes)", Type: "number",
							Min: 1, Max: 1440, Help: "Default 15."},
						{Field: "fetch_cache_quota_mb", Label: "Fetch cache quota (MB)", Type: "number",
							Min: 0, Max: 10240, Help: "Disk budget for the URL fetch cache. 0 disables caching."},
					},
				},
			},
			{
				Title:    "Users",
				Subtitle: "Approve pending signups, grant or revoke admin, manage app access, or delete accounts. Pending users see a placeholder page until approved.",
				Body: ui.Table{
					Source: "api/users",
					RowKey: "username",
					Columns: []ui.Col{
						{Field: "username", Flex: 1},
					},
					RowActions: []ui.RowAction{
						// Admin toggle — partial PUT with {admin: bool}.
						// Label clarifies what the switch controls
						// (without it, the lone switch in the row was
						// ambiguous — users couldn't tell if it was
						// "user enabled" or "admin role").
						{
							Type:   "toggle",
							Field:  "admin",
							Label:  "Admin",
							PostTo: "api/users/{username}",
							Method: "PUT",
						},
						// Approve / reject — visible only while the user is pending.
						{Type: "button", Label: "Approve", PostTo: "api/users/{username}/approve",
							Method: "POST", OnlyIf: "pending"},
						{Type: "button", Label: "Reject", PostTo: "api/users/{username}/reject",
							Method: "POST", OnlyIf: "pending", Variant: "danger"},
						// Apps access via chip picker. RecordSource hits the
						// per-user GET we just added so the picker shows the
						// user's actual current apps (not the global list).
						func() ui.RowAction {
							a := ui.Expand("Apps", ui.ChipPicker{
								OptionsSource: "api/apps",
								RecordSource:  "api/users/{username}",
								Field:         "apps",
								PostTo:        "api/users/{username}/apps",
								Method:        "PUT",
								NameField:     "path", // value stored in user.apps[]
								LabelField:    "name", // friendly label rendered on the chip
							})
							a.Compact = true
							return a
						}(),
						// Delete with confirm — danger variant.
						{Type: "button", Label: "Delete", PostTo: "api/users/{username}",
							Method:  "DELETE",
							Confirm: "Delete this user permanently? Their sessions and apps assignment go with them.",
							Variant: "danger"},
					},
					EmptyText: "No users yet.",
				},
			},
			{
				Title:    "Default Apps",
				Subtitle: "Apps every newly-approved user gets access to by default. Per-user overrides above take precedence.",
				Body: ui.ChipPicker{
					OptionsSource: "api/apps",
					RecordSource:  "api/settings",
					Field:         "default_apps",
					PostTo:        "api/settings",
					Method:        "PUT",
					NameField:     "path",
					LabelField:    "name",
				},
			},
			{
				Title:     "LLM Routing",
				Subtitle:  "Pick which tier handles each pipeline stage. \"lead\" uses the precision (remote) LLM. \"worker\" uses the local model. \"worker (thinking)\" enables extended reasoning on the local model. Budget caps thinking tokens for that stage (0 = stage default). Private stages cannot route to lead.",
				Collapsed: true,
				Body: ui.Table{
					Source: "api/routing",
					RowKey: "key",
					Columns: []ui.Col{
						{Field: "label", Flex: 1},
						{Field: "group", Mute: true},
					},
					RowActions: []ui.RowAction{
						{
							Type:   "select",
							Field:  "value",
							PostTo: "api/routing",
							Method: "POST",
							Width:  "10rem",
							Options: []ui.SelectOption{
								{Value: "lead", Label: "Lead"},
								{Value: "worker", Label: "Worker"},
								{Value: "worker (thinking)", Label: "Worker (Thinking)"},
							},
							// Hide the "lead" option when the stage is
							// private — private stages can't escalate.
							FilterOptionsIf: "private",
							FilterOptions:   "lead",
							// Mark the option matching the stage's
							// out-of-the-box Default with an asterisk
							// so operators can tell at a glance what
							// the registered default is even when
							// they've overridden it.
							DefaultField: "default",
						},
						{
							Type:   "number",
							Field:  "think_budget",
							Label:  "budget",
							PostTo: "api/routing",
							Method: "POST",
							Min:    0,
							Max:    65536,
							Width:  "7rem",
						},
					},
					EmptyText: "No routing stages registered.",
				},
			},
			{
				Title:    "Worker LLM Thinking",
				Subtitle: "Default thinking settings for the worker (local) LLM. Per-route overrides in the routing table take precedence. Budget 0 = unlimited.",
				Body: ui.FormPanel{
					Source: "api/worker-thinking",
					Fields: []ui.FormField{
						{Field: "enabled", Label: "Thinking enabled by default", Type: "toggle"},
						{Field: "budget", Label: "Default thinking budget (tokens)", Type: "number",
							Min: 0, Max: 65536, Help: "0 = unlimited (model decides).",
							ShowWhen: "enabled"},
					},
				},
			},
			{
				Title:    "Cost History (Last 30 Days)",
				Subtitle: "Daily LLM + search spend across all pipelines. Hover any bar for the per-day breakdown. Tap **Adjust prices** to edit the per-token / per-call rates that feed the dollar estimate.",
				Body: ui.Stack{
					Children: []ui.Component{
						ui.BarChart{
							Source:    "api/cost-history?days=30",
							XField:    "date",
							YField:    "cost",
							XFormat:   "date",
							YPrefix:   "$",
							YDecimals: 4,
							HeightPx:  220,
							EmptyText: "No usage recorded in the last 30 days.",
							Breakdown: []ui.DisplayPair{
								{Label: "Runs", Field: "run_count", Format: "thousands"},
								{Label: "Worker in", Field: "worker_input", Format: "thousands", Mono: true},
								{Label: "Worker out", Field: "worker_output", Format: "thousands", Mono: true},
								{Label: "Lead in", Field: "lead_input", Format: "thousands", Mono: true},
								{Label: "Lead out", Field: "lead_output", Format: "thousands", Mono: true},
								{Label: "Searches", Field: "search_calls", Format: "thousands", Mono: true},
								{Label: "Images", Field: "image_calls", Format: "thousands", Mono: true},
							},
						},
						ui.ModalButton{
							Label:    "Adjust prices",
							Title:    "Adjust prices",
							Subtitle: "Per-token and per-call dollar rates used to estimate run costs. Worker = local LLM, Lead = remote LLM. Set to 0 for free tiers.",
							Body: ui.FormPanel{
								Source: "api/cost-rates",
								Method: "PUT",
								Fields: []ui.FormField{
									{Field: "worker_input_per_1k", Label: "Worker input ($/1K tokens)",
										Type: "number", Decimals: 6, Min: 0,
										Help: "Cost of one thousand input tokens to the worker LLM."},
									{Field: "worker_output_per_1k", Label: "Worker output ($/1K tokens)",
										Type: "number", Decimals: 6, Min: 0},
									{Field: "lead_input_per_1k", Label: "Lead input ($/1K tokens)",
										Type: "number", Decimals: 6, Min: 0,
										Help: "Cost of one thousand input tokens to the lead (remote) LLM."},
									{Field: "lead_output_per_1k", Label: "Lead output ($/1K tokens)",
										Type: "number", Decimals: 6, Min: 0},
									{Field: "search_per_call", Label: "Search ($/call)",
										Type: "number", Decimals: 6, Min: 0,
										Help: "Cost per web-search API call (Brave, Tavily, etc.)."},
									{Field: "image_per_call", Label: "Image generation ($/call)",
										Type: "number", Decimals: 6, Min: 0},
								},
							},
						},
					},
				},
			},
			{
				Title:    "Ollama Proxy",
				Subtitle: "Expose gohort as a fair-queued Ollama endpoint. Point Ollama clients at gohort's port instead of Ollama's; they share the local model scheduler. Requires restart when port changes.",
				Body: ui.FormPanel{
					Source: "api/settings",
					Fields: []ui.FormField{
						{Field: "ollama_proxy_enabled", Label: "Enable Ollama Proxy", Type: "toggle"},
						{Field: "ollama_proxy_port", Label: "Proxy port", Type: "number",
							Min: 1024, Max: 65535,
							Help:        "TCP port the proxy listens on. Default suggestion: 11435.",
							ShowWhen:    "ollama_proxy_enabled",
							Placeholder: "11435"},
					},
				},
			},
			{
				Title:    "Embeddings",
				Subtitle: "Vector store ingestion + semantic search. Endpoint is an Ollama-compatible /api/embed server — typically the same host as the worker LLM. Disabling makes ingestion and search no-ops.",
				Body: ui.FormPanel{
					Source:    "api/embeddings",
					TestURL:   "api/embeddings/test",
					TestLabel: "Test embed call",
					Fields: []ui.FormField{
						{Field: "enabled", Label: "Enable embeddings", Type: "toggle"},
						{Field: "endpoint", Label: "Endpoint", Type: "text",
							Placeholder: "http://localhost:11434/api",
							Help:        "Base URL including the API version prefix — gohort appends /embeddings. Pick a preset below for the canonical path on common platforms.",
							ShowWhen:    "enabled",
							Presets: []ui.FieldPreset{
								{Label: "Ollama", Value: "http://localhost:11434/api", Hint: "Ollama native API (→ /api/embeddings)"},
								{Label: "llama.cpp", Value: "http://localhost:8080/v1", Hint: "llama.cpp OpenAI-compatible (→ /v1/embeddings)"},
								{Label: "vLLM", Value: "http://localhost:8000/v1", Hint: "vLLM OpenAI-compatible (→ /v1/embeddings)"},
								{Label: "OpenAI", Value: "https://api.openai.com/v1", Hint: "OpenAI hosted (→ /v1/embeddings, requires API key support)"},
							}},
						{Field: "model", Label: "Model", Type: "text",
							Placeholder: "nomic-embed-text",
							Help:        "Leave blank for single-model backends (llama.cpp, vLLM, hf-tei — they ignore this field). Required for Ollama. Click a chip below to fill from the endpoint's model list.",
							ShowWhen:    "enabled",
							ChipsSource: "api/embeddings/models"},
						{Field: "api_key", Label: "API Key", Type: "password",
							Help:     "Optional bearer token. Set for OpenAI hosted / authenticated proxies; leave blank for local Ollama, llama.cpp, or vLLM.",
							ShowWhen: "enabled"},
					},
				},
			},
			{
				Title:    "Audio Transcription (STT)",
				Subtitle: "OpenAI-compatible /audio/transcriptions endpoint used for video / audio attachment transcription. Endpoint includes the API version prefix; gohort appends /audio/transcriptions.",
				Body: ui.FormPanel{
					Source:    "api/transcribe",
					TestURL:   "api/transcribe/test",
					TestLabel: "Test endpoint",
					Fields: []ui.FormField{
						{Field: "enabled", Label: "Enable transcription", Type: "toggle"},
						{Field: "endpoint", Label: "Endpoint", Type: "text",
							Placeholder: "http://localhost:8089/v1",
							Help:        "Base URL with the version prefix — gohort appends /audio/transcriptions.",
							ShowWhen:    "enabled",
							Presets: []ui.FieldPreset{
								{Label: "whisper.cpp", Value: "http://localhost:8089/v1", Hint: "Default whisper.cpp HTTP server port"},
								{Label: "OpenAI", Value: "https://api.openai.com/v1", Hint: "OpenAI hosted Whisper"},
							}},
						{Field: "model", Label: "Model", Type: "text",
							Placeholder: "whisper-1",
							Help:        "Optional. whisper.cpp ignores this; OpenAI expects 'whisper-1'.",
							ShowWhen:    "enabled"},
						{Field: "api_key", Label: "API Key", Type: "password",
							Help:     "Optional bearer token. Set for real OpenAI / authenticated proxies; leave blank for local whisper.cpp.",
							ShowWhen: "enabled"},
					},
				},
			},
			{
				Title:    "Image Generation",
				Subtitle: "LLM-driven image generation provider used by tools that produce illustrations or thumbnails. Leave API key blank to reuse the matching LLM provider's key (e.g. Gemini for the gemini image model).",
				Body: ui.FormPanel{
					Source:    "api/image-gen",
					TestURL:   "api/image-gen/test",
					TestLabel: "Test API key",
					Fields: []ui.FormField{
						{Field: "provider", Label: "Provider", Type: "select",
							Options: []ui.SelectOption{
								{Value: "gemini", Label: "Gemini (Imagen)"},
								{Value: "openai", Label: "OpenAI (DALL-E)"},
								{Value: "none", Label: "Disabled"},
							}},
						{Field: "api_key", Label: "API Key", Type: "password",
							Help:     "Provider API key. Leave blank to reuse the matching LLM provider's key.",
							ShowWhen: "provider"},
					},
				},
			},
			{
				Title:    "Web Search",
				Subtitle: "Provider for the web_search tool. DuckDuckGo and a SearXNG instance require no key; Brave / Google / Serper need one.",
				Body: ui.FormPanel{
					Source:    "api/web-search",
					TestURL:   "api/web-search/test",
					TestLabel: "Test search call",
					Fields: []ui.FormField{
						{Field: "provider", Label: "Provider", Type: "select",
							Options: []ui.SelectOption{
								{Value: "duckduckgo", Label: "DuckDuckGo (no key)"},
								{Value: "brave", Label: "Brave"},
								{Value: "google", Label: "Google"},
								{Value: "serper", Label: "Serper"},
								{Value: "searxng", Label: "SearXNG (self-hosted)"},
							}},
						{Field: "api_key", Label: "API Key", Type: "password",
							Help: "Required for Brave / Google / Serper."},
						{Field: "endpoint", Label: "Endpoint", Type: "text",
							Placeholder: "https://searx.example.com",
							Help:        "Required for SearXNG. The base URL of your instance."},
					},
				},
			},
			{
				Title:    "Mail (SMTP)",
				Subtitle: "Outbound SMTP for notification emails — signup approvals, scheduled deliveries, watcher alerts. Leave Server blank for localhost:25.",
				Body: ui.FormPanel{
					Source:    "api/mail",
					TestURL:   "api/mail/test",
					TestLabel: "Send test email",
					Fields: []ui.FormField{
						{Field: "server", Label: "SMTP Server", Type: "text",
							Placeholder: "smtp.gmail.com:587",
							Presets: []ui.FieldPreset{
								{Label: "Gmail",   Value: "smtp.gmail.com:587"},
								{Label: "Outlook", Value: "smtp-mail.outlook.com:587"},
								{Label: "iCloud",  Value: "smtp.mail.me.com:587"},
								{Label: "Local",   Value: "localhost:25"},
							}},
						{Field: "from", Label: "From Address", Type: "text",
							Placeholder: "noreply@example.com"},
						{Field: "recipient", Label: "Default Recipient", Type: "text",
							Help: "Test emails and pipeline reports go here when no per-call recipient is given."},
						{Field: "username", Label: "SMTP Username", Type: "text"},
						{Field: "password", Label: "SMTP Password", Type: "password"},
					},
				},
			},
			{
				Title:    "Network Timeouts",
				Subtitle: "Outbound HTTP timeouts for source hooks and search APIs. Raise when working against slow upstreams; lower to fail fast in a tight loop.",
				Body: ui.FormPanel{
					Source: "api/network",
					Fields: []ui.FormField{
						{Field: "connect_timeout_seconds", Label: "Connect timeout (seconds)",
							Type: "number", Min: 1, Max: 120, Placeholder: "10",
							Help: "TCP + TLS connection timeout. Default 10."},
						{Field: "request_timeout_seconds", Label: "Request timeout (seconds)",
							Type: "number", Min: 1, Max: 300, Placeholder: "15",
							Help: "Per-read I/O timeout for HTTP response bodies. Default 15."},
					},
				},
			},
			{
				Title:    "Scheduled Tasks",
				Subtitle: "Pending background work — proactive messages, scheduled updates. Expand a row for the full record + payload.",
				Body: ui.Table{
					Source: "api/scheduled-tasks",
					RowKey: "id",
					Columns: []ui.Col{
						{Field: "kind", Flex: 1},
						// Future-aware relative time: "in 5m" while pending,
						// flips to "5m ago" if the worker missed firing it.
						{Field: "run_at", Format: "fromnow", Mute: true},
					},
					RowActions: []ui.RowAction{
						// One combined "Details" expand showing the full
						// record AND the payload JSON, instead of two
						// adjacent buttons. Stack composes them in one panel.
						ui.Expand("Details", ui.Stack{
							Children: []ui.Component{
								ui.RecordView{
									Pairs: []ui.DisplayPair{
										{Label: "ID", Field: "id", Mono: true},
										{Label: "Kind", Field: "kind"},
										{Label: "Fires", Field: "run_at", Format: "fromnow"},
										{Label: "Run at (UTC)", Field: "run_at", Mono: true},
										{Label: "Created", Field: "created", Format: "reltime"},
									},
								},
								ui.JSONView{Field: "payload", Title: "Task payload"},
							},
						}),
						{
							Type: "button", Label: "Cancel",
							PostTo:  "api/scheduled-tasks?id={id}",
							Method:  "DELETE",
							Variant: "warning",
							Confirm: "Cancel this scheduled task?",
						},
					},
					AutoRefreshMS: 30000,
					EmptyText:     "No tasks scheduled.",
				},
			},
			{
				Title:    "Watchers",
				Subtitle: "Recurring background checks. Each watcher runs a tool on its interval, evaluates the result with the chosen evaluator, and delivers a notification when the condition fires. Status reflects the most recent fire; expand a row to see the full fire history and the last-result body.",
				Body: ui.Table{
					Source:   "api/watchers",
					RowKey:   "id",
					SortBy:   "last_fired_at",
					SortDesc: true,
					Columns: []ui.Col{
						{Field: "name", Flex: 1},
						{Field: "description", Flex: 2, Mute: true},
						{Field: "tool_name", Mute: true, Flex: 1},
						{Field: "interval_sec", Format: "duration", Label: "Interval"},
						{Field: "fire_count", Format: "thousands", Label: "Fires"},
						{
							Field: "status", Label: "Status", Type: "badge",
							Badges: []ui.BadgeMapping{
								{Value: "ok", Label: "OK", Color: "success"},
								{Value: "error", Label: "Error", Color: "danger"},
								{Value: "idle", Label: "Idle", Color: "mute"},
								{Value: "disabled", Label: "Disabled", Color: "mute"},
							},
						},
						{Field: "last_fired_at", Format: "reltime", Label: "Last fire", Mute: true},
						{Field: "next_fire_at", Format: "fromnow", Label: "Next fire", Mute: true},
					},
					RowActions: []ui.RowAction{
						ui.Expand("Details", ui.Stack{
							Children: []ui.Component{
								ui.RecordView{
									Pairs: []ui.DisplayPair{
										{Label: "ID", Field: "id", Mono: true},
										{Label: "Name", Field: "name"},
										{Label: "Description", Field: "description"},
										{Label: "Owner", Field: "owner", Mono: true},
										{Label: "Tool", Field: "tool_name", Mono: true},
										{Label: "Tool args (JSON)", Field: "tool_args", Mono: true},
										{Label: "Interval", Field: "interval_sec", Format: "duration"},
										{Label: "Evaluator", Field: "evaluator"},
										{Label: "Action prompt", Field: "action_prompt"},
										{Label: "Delivery prefix", Field: "delivery_prefix", Mono: true},
										{Label: "Target", Field: "target", Mono: true},
										{Label: "Created", Field: "created_at", Format: "reltime"},
										{Label: "Last fired", Field: "last_fired_at", Format: "reltime"},
										{Label: "Next fire", Field: "next_fire_at", Format: "fromnow"},
										{Label: "Total fires", Field: "fire_count"},
										{Label: "Stored fires", Field: "results_count"},
										{Label: "Last error", Field: "last_error", Mono: true},
									},
								},
								ui.Table{
									Source: "api/watchers/results?id={id}",
									RowKey: "idx",
									Columns: []ui.Col{
										{Field: "timestamp", Format: "reltime", Label: "When"},
										{
											Field: "status", Label: "Result", Type: "badge",
											Badges: []ui.BadgeMapping{
												{Value: "ok", Label: "OK", Color: "success"},
												{Value: "error", Label: "Error", Color: "danger"},
											},
										},
										{Field: "reply_short", Label: "Reply", Flex: 3},
									},
									RowActions: []ui.RowAction{
										ui.Expand("Full", ui.RecordView{
											Pairs: []ui.DisplayPair{
												{Label: "When", Field: "timestamp", Format: "reltime"},
												{Label: "Trigger", Field: "trigger", Mono: true},
												{Label: "Reply", Field: "reply"},
												{Label: "Error", Field: "error", Mono: true},
											},
										}),
									},
									EmptyText: "No fires yet.",
								},
								ui.JSONView{Field: "last_result_body", Title: "Last tool response (raw)"},
							},
						}),
						{Type: "button", Label: "Enable",
							PostTo: "api/watchers?action=enable&id={id}",
							Method: "POST", HideIf: "enabled", Variant: "success"},
						{Type: "button", Label: "Disable",
							PostTo:  "api/watchers?action=disable&id={id}",
							Method:  "POST", OnlyIf: "enabled",
							Variant: "warning"},
						{Type: "button", Label: "Delete",
							PostTo:  "api/watchers?id={id}",
							Method:  "DELETE",
							Confirm: "Delete this watcher permanently?",
							Variant: "danger"},
					},
					EmptyText: "No watchers registered.",
				},
			},
			{
				Title:    "API Credentials",
				Subtitle: "Secure-API credentials the LLM can call via tools. Status badges show the current state at a glance. \"Secure\" hides the direct call_<name> tool but leaves wrapped temp tools working; \"Disable\" suspends the credential entirely.",
				Body: ui.Table{
					Source: "api/secure-api",
					RowKey: "name",
					Columns: []ui.Col{
						{Field: "name", Flex: 1},
						{Field: "type", Mute: true},
						// Status badges — at-a-glance current state.
						{
							Field: "disabled", Type: "badge",
							Badges: []ui.BadgeMapping{
								{Value: true, Label: "Disabled", Color: "danger"},
								{Value: false, Label: "Enabled", Color: "success"},
							},
						},
						{
							Field: "restricted", Type: "badge",
							Badges: []ui.BadgeMapping{
								{Value: true, Label: "Secured", Color: "warning"},
								{Value: false, Label: "Open", Color: "mute"},
							},
						},
					},
					RowActions: []ui.RowAction{
						ui.Expand("Details", ui.RecordView{
							Pairs: []ui.DisplayPair{
								{Label: "Name", Field: "name", Mono: true},
								{Label: "Type", Field: "type"},
								{Label: "Description", Field: "description"},
								{Label: "Allowed URL pattern", Field: "allowed_url_pattern", Mono: true},
								{Label: "Param name", Field: "param_name", Mono: true},
								{Label: "Requires confirm", Field: "requires_confirm"},
								{Label: "Disabled", Field: "disabled"},
								{Label: "Restricted", Field: "restricted"},
								{Label: "Max calls / day", Field: "max_calls_per_day"},
							},
						}),
						// Enable/Disable pair — only one renders depending on
						// current state. Disable uses warning (amber) so it
						// reads as suspend-not-destroy, distinct from Delete.
						{Type: "button", Label: "Enable",
							PostTo: "api/secure-api?action=enable&name={name}",
							Method: "POST", OnlyIf: "disabled", Variant: "success"},
						{Type: "button", Label: "Disable",
							PostTo:  "api/secure-api?action=disable&name={name}",
							Method:  "POST",
							HideIf:  "disabled",
							Variant: "warning"},
						// Open/Secure pair — Secure (formerly Restrict) reads
						// more naturally for "lock down to wrapped tools only".
						{Type: "button", Label: "Open",
							PostTo: "api/secure-api?action=open&name={name}",
							Method: "POST", OnlyIf: "restricted", Variant: "success"},
						{Type: "button", Label: "Secure",
							PostTo:  "api/secure-api?action=restrict&name={name}",
							Method:  "POST",
							HideIf:  "restricted",
							Variant: "warning"},
						// Delete stays red — irreversible destruction.
						{Type: "button", Label: "Delete",
							PostTo:  "api/secure-api?name={name}",
							Method:  "DELETE",
							Confirm: "Delete this credential? The encrypted secret goes with it.",
							Variant: "danger"},
					},
					EmptyText: "No credentials registered yet — add one via the legacy admin's API Credentials section.",
				},
			},
			{
				Title:    "Persistent Tools (Pending)",
				Subtitle: "LLM-discovered API patterns awaiting your approval. Approve to make permanent; reject to discard. The description is the LLM's own summary of what the tool does.",
				Body: ui.Table{
					Source:       "api/persistent-tools",
					RecordsField: "pending",
					// The records' actual TempTool fields live under .tool.* —
					// the wrapper carries `requested_at` etc on the outer
					// object. Use dotted paths to surface tool fields.
					RowKey: "tool.name",
					Columns: []ui.Col{
						{Field: "tool.name", Flex: 1},
						{Field: "owner", Flex: 0, Mute: true},
						{Field: "tool.description", Flex: 2, Mute: true},
					},
					RowActions: []ui.RowAction{
						ui.Expand("View", ui.RecordView{
							Pairs: []ui.DisplayPair{
								{Label: "Name", Field: "tool.name", Mono: true},
								{Label: "Owner", Field: "owner"},
								{Label: "Description", Field: "tool.description"},
								{Label: "Mode", Field: "tool.mode"},
								{Label: "Method", Field: "tool.method", Mono: true},
								{Label: "Command / URL template", Field: "tool.command_template", Mono: true},
								{Label: "Body template", Field: "tool.body_template", Mono: true},
								{Label: "Credential", Field: "tool.credential", Mono: true},
								{Label: "Requested at", Field: "requested_at", Format: "reltime"},
								{Label: "From session", Field: "requested_session", Mono: true},
							},
						}),
						{Type: "button", Label: "Approve",
							PostTo: "api/persistent-tools?action=approve&name={tool.name}&owner={owner}",
							Method: "POST", Variant: "success"},
						{Type: "button", Label: "Reject",
							PostTo:     "api/persistent-tools?action=reject&name={tool.name}&owner={owner}",
							Method:     "POST", Variant: "warning",
							Confirm:    "Reject this pending tool? It'll be discarded.",
							Optimistic: true},
					},
					EmptyText: "No pending tools.",
				},
			},
			{
				Title:    "Persistent Tools (Active)",
				Subtitle: "Approved tools the LLM gets in every session. Description shows what each one does. Delete to revoke immediately.",
				Body: ui.Table{
					Source:       "api/persistent-tools",
					RecordsField: "active",
					RowKey:       "tool.name",
					Columns: []ui.Col{
						{Field: "tool.name", Flex: 1},
						{Field: "owner", Flex: 0, Mute: true},
						{Field: "tool.description", Flex: 2, Mute: true},
						{Field: "last_used_at", Format: "reltime", Mute: true},
					},
					RowActions: []ui.RowAction{
						ui.Expand("View", ui.RecordView{
							Pairs: []ui.DisplayPair{
								{Label: "Name", Field: "tool.name", Mono: true},
								{Label: "Owner", Field: "owner"},
								{Label: "Description", Field: "tool.description"},
								{Label: "Mode", Field: "tool.mode"},
								{Label: "Method", Field: "tool.method", Mono: true},
								{Label: "Command / URL template", Field: "tool.command_template", Mono: true},
								{Label: "Body template", Field: "tool.body_template", Mono: true},
								{Label: "Credential", Field: "tool.credential", Mono: true},
								{Label: "Approved at", Field: "approved_at", Format: "reltime"},
								{Label: "Last used", Field: "last_used_at", Format: "reltime"},
							},
						}),
						{Type: "button", Label: "Delete",
							PostTo:     "api/persistent-tools?name={tool.name}&owner={owner}",
							Method:     "DELETE",
							Variant:    "danger",
							Confirm:    "Delete this active tool? The LLM will lose access immediately.",
							Optimistic: true},
					},
					EmptyText: "No active persistent tools.",
				},
			},
			{
				Title:    "Tool Groups",
				Subtitle: "Bundle related chat tools (a Calendar API, a communications suite, an Acme integration) under one logical heading. The runtime catalog collapses members into a single expandable entry — context savings when a deployment has many tools that share a purpose. Pick which tools to group; the LLM proposes the name and description based on what you selected.",
				Body: ui.Stack{
					Children: []ui.Component{
						// Auto-create: pick tools → LLM names + describes.
						// Skips the awkward "what should I call this?"
						// step since the LLM is the one that'll have to
						// call the group later anyway.
						ui.Card{
							HTML: `<div id="tg-create-wrap">
  <div id="tg-create-tools" style="display:flex;flex-wrap:wrap;gap:0.3rem;margin-bottom:0.6rem">Loading tools…</div>
  <div style="display:flex;gap:0.6rem;align-items:center">
    <button id="tg-create-btn" type="button" class="ui-row-btn primary" disabled>Create group (0 selected)</button>
    <span id="tg-create-status" style="color:var(--text-mute);font-size:0.85rem"></span>
  </div>
</div>
<style>
#tg-create-tools .tg-chip {
  background: transparent; border: 1px solid var(--border);
  color: var(--text-mute); padding: 0.2rem 0.55rem; border-radius: 999px;
  cursor: pointer; font-size: 0.78rem; font-family: inherit;
}
#tg-create-tools .tg-chip:hover { color: var(--text); border-color: var(--text-mute); }
#tg-create-tools .tg-chip.on { color: var(--accent); border-color: var(--accent); }
</style>
<script>
(function(){
  var selected = {};
  var wrap = document.getElementById('tg-create-tools');
  var btn = document.getElementById('tg-create-btn');
  var status = document.getElementById('tg-create-status');
  function updateBtn() {
    var n = Object.keys(selected).length;
    btn.textContent = 'Create group (' + n + ' selected)';
    btn.disabled = n === 0;
  }
  fetch('api/tool-groups/registry?exclude_grouped=true').then(function(r){return r.json()}).then(function(tools){
    wrap.innerHTML = '';
    if (!tools || !tools.length) { wrap.textContent = '(no tools available)'; return; }
    tools.forEach(function(t){
      var chip = document.createElement('button');
      chip.type = 'button';
      chip.className = 'tg-chip';
      chip.textContent = t.name;
      if (t.description) chip.title = t.description;
      chip.addEventListener('click', function(){
        if (selected[t.name]) { delete selected[t.name]; chip.classList.remove('on'); }
        else { selected[t.name] = true; chip.classList.add('on'); }
        updateBtn();
      });
      wrap.appendChild(chip);
    });
  }).catch(function(err){ wrap.textContent = 'Failed to load tools: ' + err.message; });
  btn.addEventListener('click', function(){
    var members = Object.keys(selected);
    if (!members.length) return;
    btn.disabled = true;
    status.textContent = 'Asking the LLM to name + describe…';
    fetch('api/tool-groups/auto-create', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({members: members})
    }).then(function(r){
      if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP '+r.status)); });
      return r.json();
    }).then(function(g){
      status.textContent = 'Created "' + g.name + '". Reloading…';
      setTimeout(function(){ window.location.reload(); }, 600);
    }).catch(function(err){
      status.textContent = 'Failed: ' + (err && err.message || err);
      btn.disabled = false;
    });
  });
})();
</script>`,
						},
						// Table of existing groups with per-row editor + delete.
						ui.Table{
							Source: "api/tool-groups",
							RowKey: "id",
							Columns: []ui.Col{
								{Field: "name", Flex: 1},
								{Field: "description", Flex: 2, Mute: true},
							},
							RowActions: []ui.RowAction{
								ui.Expand("Edit", ui.Stack{
									Children: []ui.Component{
										// Name + description edit. Loads the
										// group, POSTs the full record back.
										ui.FormPanel{
											Source:  "api/tool-groups/{id}",
											PostURL: "api/tool-groups",
											Method:  "POST",
											Fields: []ui.FormField{
												{Field: "name", Type: "text", Label: "Name",
													SuggestURL: "api/tool-groups/suggest"},
												{Field: "description", Type: "textarea", Label: "Description", Rows: 2,
													SuggestURL: "api/tool-groups/suggest"},
											},
										},
										// Members chip picker — toggleable
										// across the global tool registry.
										ui.ChipPicker{
											OptionsSource: "api/tool-groups/registry?exclude_grouped=true&except_group={id}",
											RecordSource:  "api/tool-groups/{id}",
											Field:         "members",
											PostTo:        "api/tool-groups",
											Method:        "POST",
											NameField:     "name",
											LabelField:    "name",
											DescField:     "description",
										},
									},
								}),
								// Admin-curated groups: Delete drops the row.
								{Type: "button", Label: "Delete",
									PostTo:  "api/tool-groups?id={id}",
									Method:  "DELETE",
									Confirm: "Delete this tool group? The member tools themselves are unaffected; only the grouping definition disappears.",
									Variant: "danger",
									HideIf:  "is_builtin"},
								// Framework-default groups: Revert drops the
								// admin shadow (if any) so the in-code default
								// surfaces again. No-op if no shadow has been
								// saved — the backend returns an error which
								// surfaces as a toast.
								{Type: "button", Label: "Revert",
									PostTo:  "api/tool-groups?id={id}",
									Method:  "DELETE",
									Confirm: "Revert this framework-default tool group to its in-code defaults? Any admin edits you made are discarded.",
									OnlyIf:  "is_builtin"},
							},
							EmptyText: "No tool groups defined. Create one above to start collapsing related tools into a single catalog entry.",
						},
					},
				},
			},
			{
				Title:    "Skills",
				Subtitle: "Conditional prompt addendums that auto-activate based on the user's message — dynamic personas. Builder is the canonical authoring path (talk to it in Agency); this surface manages what's been authored. Disabled skills stay defined but the classifier skips them.",
				Body: ui.Table{
					Source: "api/skills",
					RowKey: "id",
					Columns: []ui.Col{
						{Field: "name", Flex: 1},
						{Field: "description", Flex: 2, Mute: true},
						{
							Field: "disabled", Label: "Status", Type: "dot",
							// Green = active, red = disabled. Hover
							// tooltip carries the word so screen
							// readers / colorblind users still see it.
							Badges: []ui.BadgeMapping{
								{Value: true, Label: "Disabled", Color: "danger"},
								{Value: false, Label: "Active", Color: "success"},
							},
						},
					},
					RowActions: []ui.RowAction{
						ui.Expand("Edit", ui.Stack{
							Children: []ui.Component{
								ui.FormPanel{
									Source:  "api/skills/{id}",
									PostURL: "api/skills",
									Method:  "POST",
									Fields: []ui.FormField{
										{Field: "name", Type: "text", Label: "Name"},
										{Field: "description", Type: "textarea", Label: "Description (classifier's match target)", Rows: 2,
											Help: "One-sentence \"when to use this skill\" hint. The classifier embeds this and matches it against user messages."},
										{Field: "triggers", Type: "tags", Label: "Triggers",
											Help: "Substring patterns matched against the user's message (case-insensitive). Empty = embedding-only activation."},
										{Field: "instructions", Type: "textarea", Label: "Instructions (markdown)", Rows: 10,
											Help: "Body appended to the active agent's system prompt when the skill activates. Framework prepends an `## Skill: <name>` H2 header."},
									},
								},
								// Allowed tools — picker from the registered
								// tool pool. Same pattern Tool Groups uses;
								// avoids typos and exposes the user to what's
								// actually available. Posts independently of
								// the FormPanel above (the chip click immediately
								// updates the record).
								ui.ChipPicker{
									OptionsSource: "api/tool-groups/registry",
									RecordSource:  "api/skills/{id}",
									Field:         "allowed_tools",
									PostTo:        "api/skills",
									Method:        "POST",
									NameField:     "name",
									LabelField:    "name",
									DescField:     "description",
								},
							},
						}),
						// Active skill → "Disable" button; disabled skill →
						// "Enable" button. Partial-update via the
						// ?action=enable|disable query param so the rest
						// of the record stays intact.
						{Type: "button", Label: "Disable",
							PostTo: "api/skills?action=disable&id={id}",
							Method: "POST",
							HideIf: "disabled"},
						{Type: "button", Label: "Enable",
							PostTo: "api/skills?action=enable&id={id}",
							Method: "POST",
							OnlyIf: "disabled"},
						{Type: "button", Label: "Delete",
							PostTo:  "api/skills?id={id}",
							Method:  "DELETE",
							Confirm: "Delete this skill? The definition is gone for good; Builder will need to re-author if you want it back.",
							Variant: "danger"},
					},
					EmptyText: "No skills defined. Talk to Builder in Agency to author one — \"create a skill called X that fires when…\".",
				},
			},
			{
				Title:    "Pipelines",
				Subtitle: "Declarative multi-stage workflows authored in Agency (the pipeline tool, or via Builder). This surface lists every user's pipelines and lets you inspect the stages or delete a definition. Deleting one also drops it from any agent it was attached to.",
				Body: ui.Table{
					Source:       "api/pipelines",
					RecordsField: "pipelines",
					RowKey:       "id",
					Columns: []ui.Col{
						{Field: "owner", Label: "Owner", Flex: 1, Mute: true},
						{Field: "name", Label: "Name", Flex: 1},
						{Field: "description", Label: "Description", Flex: 2, Mute: true},
						{Field: "stages", Label: "Stages", Flex: 0},
					},
					RowActions: []ui.RowAction{
						ui.Expand("View", ui.JSONView{Field: "detail", Title: "Definition"}),
						{Type: "button", Label: "Delete",
							PostTo:     "api/pipelines?id={id}",
							Method:     "DELETE",
							Confirm:    "Delete this pipeline definition? It's removed for the owning user and detached from any agent that used it. Authoring it again means re-creating the stages.",
							Variant:    "danger",
							Optimistic: true},
					},
					EmptyText: "No pipelines defined. They're authored in Agency via the pipeline tool or Builder.",
				},
			},
			{
				Title:    "Local Model Scheduler",
				Subtitle: "Concurrent-request caps for local LLM backends. Default 1 (strict serial). Raise only when the backend supports parallel requests. Requires restart to apply.",
				Body: ui.FormPanel{
					Source: "api/local-scheduler",
					Fields: []ui.FormField{
						{Field: "ollama_max_parallel", Label: "Ollama max parallel", Type: "number",
							Min: 1, Max: 16},
						{Field: "llamacpp_max_parallel", Label: "llama.cpp max parallel", Type: "number",
							Min: 1, Max: 16},
					},
				},
			},
			{
				Title:    "Maintenance",
				Subtitle: "One-shot operations that fix stale state or rebuild derived data. Each runs in the background and reports the number of records touched.",
				Body: ui.ActionList{
					Source:     "api/maintenance",
					LabelField: "Label",
					DescField:  "Desc",
					PostTo:     "api/maintenance?key={Label}",
					Method:     "POST",
					ButtonText: "Run",
					EmptyText:  "No maintenance functions registered.",
				},
			},
			{
				Title:     "Migrations",
				Subtitle:  "Schema / data migrations the apps have run on this deployment. Auto-fire on app init when triggered (no manual button) and never run twice for the same (app, name, owner). An error column indicates a panic during the run — clear the marker in the DB to retry after a fix.",
				Collapsed: true,
				Body: ui.Table{
					Source: "api/migrations",
					RowKey: "key",
					Columns: []ui.Col{
						{Field: "app", Label: "App", Flex: 1},
						{Field: "name", Label: "Migration", Flex: 2},
						{Field: "owner", Label: "Owner", Mute: true},
						{Field: "ran_at", Label: "Ran", Format: "reltime", Mute: true},
						{Field: "changed", Label: "Changed", Mute: true},
						{Field: "error", Label: "Error", Mute: true},
					},
					EmptyText: "No migrations have run on this deployment yet.",
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}

