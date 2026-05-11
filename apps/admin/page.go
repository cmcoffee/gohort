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
							Type:    "toggle",
							Field:   "admin",
							Label:   "Admin",
							PostTo:  "api/users/{username}",
							Method:  "PUT",
							Leading: true,
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
					NameField:     "path",
					LabelField:    "name",
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
				Title:    "Voice (STT/TTS)",
				Subtitle: "Whisper for transcription and Piper for synthesis. HTTP server URL takes precedence over the binary fallback for each backend.",
				Body: ui.FormPanel{
					Source: "api/voice",
					Fields: []ui.FormField{
						{Field: "enabled", Label: "Enable voice integration", Type: "toggle"},
						{Field: "whisper_server_url", Label: "Whisper server URL", Type: "text",
							Placeholder: "http://llama.snuglab.local:8090",
							Help:        "When set, transcription POSTs to this URL. Takes precedence over the binary fields.",
							ShowWhen:    "enabled"},
						{Field: "whisper_bin", Label: "Whisper binary (shell fallback)", Type: "text",
							Placeholder: "whisper-cli", ShowWhen: "enabled"},
						{Field: "whisper_model", Label: "Whisper model path (shell fallback)", Type: "text",
							Placeholder: "/opt/whisper.cpp/models/ggml-base.en.bin", ShowWhen: "enabled"},
						{Field: "piper_server_url", Label: "Piper server URL", Type: "text",
							Placeholder: "http://llama.snuglab.local:5000",
							Help:        "When set, synthesis POSTs to this URL. Takes precedence over the binary fields.",
							ShowWhen:    "enabled"},
						{Field: "piper_bin", Label: "Piper binary (shell fallback)", Type: "text",
							Placeholder: "piper", ShowWhen: "enabled"},
						{Field: "piper_voice", Label: "Piper voice path (shell fallback)", Type: "text",
							Placeholder: "/opt/piper/voices/en_US-amy-medium.onnx", ShowWhen: "enabled"},
					},
				},
			},
			{
				Title:    "LLM Routing",
				Subtitle: "Pick which tier handles each pipeline stage. \"lead\" uses the precision (remote) LLM. \"worker\" uses the local model. \"worker (thinking)\" enables extended reasoning on the local model. Budget caps thinking tokens for that stage (0 = stage default). Private stages cannot route to lead.",
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
				Title:    "Cost History (Last 30 Days)",
				Subtitle: "Daily LLM + search spend across all pipelines. Hover any bar for the per-day breakdown — token counts by tier and per-tool call counts that fed the dollar estimate.",
				Body: ui.BarChart{
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
				Title:    "Cost Rates",
				Subtitle: "Per-token and per-call dollar rates used to estimate run costs. Worker = local LLM, Lead = remote LLM. Set to 0 for free tiers (e.g. local-Ollama worker).",
				Body: ui.FormPanel{
					Source: "api/cost-rates",
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
			{
				Title:    "Cost Sources",
				Subtitle: "Pipelines whose telemetry feeds the cost chart above. Auto-registered at startup.",
				Body: ui.Card{
					HTML: `<div id="cost-sources-list" style="font-size:0.85rem;color:var(--text-mute)">Loading…</div>
<script>
fetch('api/cost-sources').then(function(r){return r.json()}).then(function(s){
  var el = document.getElementById('cost-sources-list');
  if (!s || !s.length) { el.textContent = 'None registered.'; return; }
  el.textContent = s.join(' · ');
}).catch(function(err){ document.getElementById('cost-sources-list').textContent = 'Failed: ' + err.message; });
</script>`,
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
				Title:    "Vector Index",
				Subtitle: "Embeddings cache stats. Empty count > 0 means some chunks were ingested while the embedding service was unreachable — re-run the embed maintenance to fix.",
				Body: ui.DisplayPanel{
					Source: "api/vector-stats",
					Pairs: []ui.DisplayPair{
						{Label: "Total chunks", Field: "total"},
						{Label: "Embedded", Field: "embedded"},
						{Label: "Empty (re-embed needed)", Field: "empty"},
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
				Title:    "Scheduled Tasks",
				Subtitle: "Pending background work — proactive messages, scheduled updates, voice synthesis pre-warming. Expand a row for the full record + payload.",
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
						{Field: "tool.description", Flex: 2, Mute: true},
					},
					RowActions: []ui.RowAction{
						ui.Expand("View", ui.RecordView{
							Pairs: []ui.DisplayPair{
								{Label: "Name", Field: "tool.name", Mono: true},
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
							PostTo: "api/persistent-tools?action=approve&name={tool.name}",
							Method: "POST", Variant: "success"},
						{Type: "button", Label: "Reject",
							PostTo:  "api/persistent-tools?action=reject&name={tool.name}",
							Method:  "POST", Variant: "warning",
							Confirm: "Reject this pending tool? It'll be discarded."},
					},
					EmptyText: "No pending tools.",
				},
			},
			{
				Title:    "Watchers",
				Subtitle: "Recurring background checks. Each watcher runs a tool on its interval, evaluates the result with the chosen evaluator, and delivers a notification when the condition fires. Enable/Disable suspends; Delete removes permanently.",
				Body: ui.Table{
					Source: "api/watchers",
					RowKey: "id",
					Columns: []ui.Col{
						{Field: "name", Flex: 1},
						{Field: "owner", Mute: true},
						{Field: "tool_name", Mute: true},
						{
							Field: "enabled", Type: "badge",
							Badges: []ui.BadgeMapping{
								{Value: true, Label: "Enabled", Color: "success"},
								{Value: false, Label: "Disabled", Color: "mute"},
							},
						},
						{Field: "last_fired_at", Format: "reltime", Mute: true},
					},
					RowActions: []ui.RowAction{
						ui.Expand("Details", ui.Stack{
							Children: []ui.Component{
								ui.RecordView{
									Pairs: []ui.DisplayPair{
										{Label: "ID", Field: "id", Mono: true},
										{Label: "Name", Field: "name"},
										{Label: "Owner", Field: "owner", Mono: true},
										{Label: "Tool", Field: "tool_name", Mono: true},
										{Label: "Tool args (JSON)", Field: "tool_args", Mono: true},
										{Label: "Interval", Field: "interval_sec", Format: "duration"},
										{Label: "Evaluator", Field: "evaluator"},
										{Label: "Action prompt", Field: "action_prompt"},
										{Label: "Delivery prefix", Field: "delivery_prefix", Mono: true},
										{Label: "Target", Field: "target", Mono: true},
										{Label: "Fire count", Field: "fire_count"},
										{Label: "Last fired", Field: "last_fired_at", Format: "reltime"},
									},
								},
								ui.JSONView{Field: "last_result_body", Title: "Last result body"},
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
				Title:    "Persistent Tools (Active)",
				Subtitle: "Approved tools the LLM gets in every session. Description shows what each one does. Delete to revoke immediately.",
				Body: ui.Table{
					Source:       "api/persistent-tools",
					RecordsField: "active",
					RowKey:       "tool.name",
					Columns: []ui.Col{
						{Field: "tool.name", Flex: 1},
						{Field: "tool.description", Flex: 2, Mute: true},
						{Field: "last_used_at", Format: "reltime", Mute: true},
					},
					RowActions: []ui.RowAction{
						ui.Expand("View", ui.RecordView{
							Pairs: []ui.DisplayPair{
								{Label: "Name", Field: "tool.name", Mono: true},
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
							PostTo:  "api/persistent-tools?name={tool.name}",
							Method:  "DELETE",
							Variant: "danger",
							Confirm: "Delete this active tool? The LLM will lose access immediately."},
					},
					EmptyText: "No active persistent tools.",
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
		},
	}
	page.ServeHTTP(w, r)
}

