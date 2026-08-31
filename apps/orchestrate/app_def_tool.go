// `app_def` — grouped tool for authoring gohort APPS (data-shaped OR fully
// interactive, e.g. a canvas game): real
// in-dashboard surfaces composed from ui primitives (FormPanel, Table,
// DisplayPanel, EmptyState), stored as an AppSpec and served by apps/customapps
// at /custom/<slug>/. This is the tool that lets Builder answer "build me an
// app" with an ACTUAL gohort app instead of a standalone HTML file.
//
//	create / update — author an app (name, sections[]).
//	list            — see the user's apps.
//	get             — read one app's definition.
//	delete          — remove an app.
//
// The LLM describes the app declaratively (sections of kind form/table/display/
// chart/empty/chat/workbench/actions, plus an html raw-HTML escape hatch); this
// tool translates that into a ui.Page, marshals it with
// ConfigJSON, and stores the bytes via core.SaveAppSpec. customapps serves the
// stored page + a generic per-app record store (the form writes records, the
// table lists them) with no per-app Go code. A chat section binds the app's
// agent (agent_id) to a live chat panel served under /custom/<slug>/chat/*.
//
// Specs are stored owner-keyed in the SHARED deployment root (core/appspec.go),
// NOT this app's DB bucket — otherwise a spec written here would be invisible to
// the customapps host, which holds a different bucket.
//
// Builder-only, same as the pipeline tool — authoring apps is Builder's job.

package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/tools/appscript"
)

func (t *chatTurn) appDefToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "app_def",
			Description: "Author and manage gohort APPS — real in-dashboard surfaces (NOT standalone HTML files) served at /custom/<slug>/. Two ways to build one, and BOTH are in scope. (1) Declarative sections (form/table/display/chart/chat/workbench): the framework renders them and gives you a per-app record store for free, no hand-written HTML/CSS/JS — best for anything data-shaped. (2) An `html` section: a full HTML/CSS/JS canvas where inline <script> RUNS, for anything the typed sections can't express — a GAME, canvas animation, a simulation, a custom visualization, a bespoke widget.\n\nYou CAN build an interactive or graphical app. If the user asks for a game or an animation, write it as an html section with a <canvas> and a requestAnimationFrame loop — do NOT tell them it is out of scope, needs a game engine, or is beyond this tool. It is not.\n\nReach for this whenever the user asks for \"an app\", \"a game\", \"a page where I can…\", \"a tool to track/manage X\", or any persistent surface inside gohort — never a standalone downloadable HTML file. Actions: create · update · list · get · delete. Call action=\"help\" for every section field and the good defaults.",
			Parameters: map[string]ToolParam{
				"action": {Type: "string", Description: "One of: create | update | patch_html | replace_function | revisions | revert | test | verify | list | get | delete | help. Every save keeps the version it replaced: if an edit turns out to have broken or deleted something, use revert (see revisions) — never try to reconstruct the app from memory, which is how the damage happens in the first place. To change PART of an html app, edit in place instead of re-sending the whole document through update — re-typing a long document is how working code gets silently rewritten around the fix. Rewriting a whole FUNCTION is replace_function (name it, hand over the new one; you never reproduce the old text). Anything smaller — a constant, a one-line bug, a couple of lines — is patch_html (exact find/replace). After authoring an app with script-backed data_sources or actions, run test to EXECUTE each script and see its real output/errors. Then run verify as the FINAL gate: it re-runs the scripts AND loads the app's page in a real headless browser (JavaScript executed, as the user), reporting console errors, failed fetches, and whether sections rendered — do not tell the user the app is ready until verify passes. Pass sample=[{...}] to either action to exercise the full form→data-source→output chain with example form data even before any records exist."},
				"find": {
					Type:        "string",
					Description: "(patch_html) The EXACT text to replace, copied verbatim from the app's current html (read it with action=\"get\"), whitespace included. It must match EXACTLY ONCE — include the surrounding lines until it is unique. Zero matches or several are both refused rather than guessed at, so a patch can never land somewhere you didn't mean.",
				},
				"replace": {
					Type:        "string",
					Description: "(patch_html/replace_function) What to put there instead. For patch_html: the text replacing `find` — may be empty to delete it. For replace_function: the WHOLE new function, definition line included (`function drawBird(){ … }`), which replaces the old one wherever it sits. Either way only that region changes — everything else in the document is left byte-for-byte alone, which is the whole point of editing in place instead of re-sending it.",
				},
				"function": {
					Type:        "string",
					Description: "(replace_function) The NAME of the function to replace — just the identifier, e.g. \"drawBird\". The server locates it (declaration through closing brace, in any of the usual forms) and swaps in `replace`, so you never reproduce a single line of the old one. This is the action for rewriting a function: it cannot fail on whitespace, and it does not need the current document in front of you. If the name is defined twice, or is not defined at all, it refuses and tells you what the section does define.",
				},
				"section": {
					Type:        "number",
					Description: "(patch_html/replace_function) Which html section to edit, 1-based among the app's html sections. Omit when the app has only one (the usual case, e.g. a game).",
				},
				"to": {
					Type:        "string",
					Description: "(revert) Which kept revision to restore — the # id from action=\"revisions\" (e.g. 3 or \"#3\"). Omit to restore the most recent one, which is what you want immediately after an edit went wrong. The id is stable; a timestamp is also accepted but two saves can share one second, so prefer the id. The version being replaced is itself kept, so a revert is undoable.",
				},
				"confirm_rewrite": {
					Type:        "boolean",
					Description: "(update) Confirm that you MEANT to replace an html app's document with a much shorter one. An update whose html is drastically smaller than what is stored, or which drops functions the remaining code still calls, is refused by default — that pattern is a half-finished rewrite, not an edit, and it silently deletes working code that everything else still parses and loads fine around. Set true only when you are deliberately re-authoring the app from scratch and the new document is complete. If you are actually trying to change one part, use replace_function or patch_html instead.",
				},
				"sample": {
					Type:        "array",
					Description: "(test) Example form submissions to run the data sources/actions against, standing in for the live record store. Each item is an object keyed by the FORM's field names — exactly what a record looks like after the user submits the form (e.g. [{\"city\":\"Santa Cruz, CA\"}]). Use this to test end-to-end before the app has any real records: the scripts receive these as the `records` env var, so you see whether 'add a location → forecast' actually produces output. If a data source returns [] against a sample that clearly should match, the script isn't reading the records env var (or has the wrong field name).",
					Items:       &ToolParam{Type: "object"},
				},
				"params": {
					Type:        "object",
					Description: "(test) Optional query-param inputs to simulate alongside sample, handed to each script as env vars (for filter-style data sources that read a param). Most form-driven apps don't need this — inputs come from sample/records, not params.",
				},
				"name":        {Type: "string", Description: "App name (shown in the dashboard). Required for create."},
				"slug":        {Type: "string", Description: "(create) URL slug, e.g. 'reading-list' → /custom/reading-list/. Optional — derived from the name when omitted. Lowercase letters, digits, hyphens."},
				"id":          {Type: "string", Description: "(update/get/delete) The app's slug, identifying which app to act on."},
				"description": {Type: "string", Description: "(create/update) One-line summary of what the app is for (shown on the Custom Apps index)."},
				"record_key":  {Type: "string", Description: "(create/update) The primary-key field of each record. Default 'id' — the host allocates one on save. Only override if the records have a natural key."},
				"full_width":  {Type: "boolean", Description: "(create/update) Render the page EDGE-TO-EDGE instead of the default centered ~900px column. Set true for DATA-HEAVY surfaces — a dashboard, a wide table with many columns, a live monitor — where the extra horizontal space helps. Leave false (default) for forms and simple lists, which read better in a narrow column. A workbench app is always full-width regardless of this flag."},
				"private_db":  {Type: "boolean", Description: "(create/update) Give this app its OWN dedicated database file instead of sharing the common custom-apps store. Set true for a data-heavy app (many records, or data you want isolated / independently disposable) — its records live in a separate hardware-encrypted file, and deleting the app removes that data cleanly. Leave false (default) for ordinary small apps; the shared store is fine. Opt-in only: flipping this on an EXISTING app starts a fresh empty store and does NOT migrate records already saved in the shared store, so choose it at create time."},
				"agent_id":    {Type: "string", Description: "(create/update) Optional name or id of an agent that powers this app (reserved for the chat surface). Stored on the app; not required."},
				"pipeline_id": {Type: "string", Description: "(create/update) Optional name or id of a stored pipeline this app RUNS — required by a \"pipeline\" section, which gives the app a submit form, a live stage-by-stage transcript, and a history of past runs. Author the pipeline first with the `pipeline` tool. This is how a multi-stage recipe (research, review, debate) becomes an app the user can open, rather than a tool only an agent can call."},
				"data_sources": {
					Type:        "array",
					Description: "(create/update) Optional script-backed data endpoints — the way to give an app real LOGIC instead of plain stored-record CRUD. Each is {name, script, language?, capabilities?}. The script (python by default; set language:\"bash\" for shell) COMPUTES the JSON a table/display renders: the app's stored records arrive as an ENVIRONMENT VARIABLE named records holding a JSON string, and each request query param arrives as its own env var; the script must PRINT a JSON value to stdout (a JSON array for a table, a JSON object for a display). Read the records like this (python): `import os, json` then `records = json.loads(os.environ.get('records', '[]'))` — do NOT write `json.loads(\"records\")` (that parses the literal word, not the data) or `os.environ['records']` without a default. Bash: `echo \"$records\"`. INPUTS COME FROM records, NOT query params: when the app has a FORM that saves entries (a city, a name, an item), the user's typed value was saved as a RECORD — read it from `records`, e.g. `recs = json.loads(os.environ.get('records','[]')); city = recs[-1].get('city')` (match the form field's name). Nothing passes form fields as query-param env vars, so `os.environ.get('city')` is ALWAYS empty and the panel shows nothing — this is the #1 reason a form-driven app looks 'disconnected' (you add a location but no forecast appears). A data source reads the SAVED RECORDS; query params are only for filters you wire yourself. CRITICAL: the section fetches this data source the moment the page LOADS, with NO query params set yet — so read EVERY param defensively (`os.environ.get('city', '')`, never `os.environ['city']`) and return valid JSON even when params are empty, or the app errors on open. To pull external data, call the gohort hook (fetch is granted by default — you need NOT declare capabilities for a public URL): `from gohort import fetch_url` then `r = fetch_url(url)` — the host performs the fetch (the sandbox has no raw network); the result is a dict `{status, headers, body}`. For a host behind one of the OWNER's API credentials, use `from gohort import fetch_via` then `fetch_via(\"<cred>\", url)` — auth is injected server-side, and the owner's credentials are AUTO-GRANTED to app scripts, so you do NOT declare capabilities for them (a plain fetch_url of a credential-covered host also auto-routes through it). Reuse an existing tool's credential + path here rather than hardcoding auth. fetch_url RAISES on a transport failure (bad/blocked host, timeout), so wrap it: `try: r = fetch_url(url)` / `except Exception as e:` and still PRINT valid JSON (e.g. `[]` or `{\"error\": str(e)}`) so the panel renders instead of 500ing. Then check `r['status']` and `json.loads(r['body'])`. URL-ENCODE every value you interpolate into a URL: `from urllib.parse import quote` then `url = f\"...?name={quote(city)}\"` — an unencoded space or comma (e.g. a city like 'Santa Cruz, CA') makes the fetch REFUSE the URL. Network HTTP libs (requests, urllib.request, curl, http.client) are BLOCKED — only fetch_url reaches the network — but urllib.parse (quote/urlencode) is fine and is the right tool for building URLs. A table/display section then sets source_script:\"<name>\" to read from it instead of the record store. Run app_def action=test after authoring to confirm the script prints valid JSON. Use this for apps that fetch/aggregate/transform (a dashboard over an API, a computed report) rather than just collecting form entries. Owner-only today.",
					Items:       &ToolParam{Type: "object"},
				},
				"actions": {
					Type:        "array",
					Description: "(create/update) Optional script-backed action buttons — the WRITE side of the logic seam (data_sources is the read side). Each is {name, label?, desc?, script, language?, capabilities?, confirm?, schedule?}. A button labeled `label` runs the script when clicked; the script receives the app's stored records as an env var named records (a JSON string — read it with `json.loads(os.environ.get('records', '[]'))`, NOT `json.loads(\"records\")`) plus any params, and PRINTS a JSON OBJECT {message?: string, records?: [...]}. The FRAMEWORK upserts any returned records into the app's store (so they appear in the tables — your script does NOT write the store itself) and shows the message. Use for app verbs like \"Sync now\", \"Generate\", \"Refresh from API\". Surface the buttons with an `actions` section. Set confirm for destructive ones. capabilities work the same as data_sources (e.g. [\"fetch\"] for `from gohort import fetch`).\n\nSELF-UPDATING (dashboard / tracker): add `schedule` to an action to ALSO run it unattended on a timer, with no one clicking and no page open — this is how an app keeps itself fresh. `schedule` is {interval_seconds?, cron?, max_idle_days?}: set EITHER interval_seconds (fixed cadence; floored to 300s / 5 min for background) OR cron (e.g. \"MON 09:00\", \"* 08:00\" style NextCronOccurrence spec). Each fire runs the SAME script the button runs, in the owner's sandbox, and upserts what it returns — so the shape you choose decides behavior: a TRACKER returns a record with a NEW/absent key each fire (a fresh row appended over time, e.g. hourly {ts, value} — pair it with a chart/table to see history); a DASHBOARD returns a record with a FIXED key each fire (the latest snapshot replaced in place). Set max_idle_days to auto-pause the schedule after N days with no page view (it re-arms on the next visit) so a tracker nobody watches stops burning the sandbox. Only the app's OWNER copy self-updates; other users of a shared app still get fresh data when they open it. An imported app runs no scheduled script until its owner enables it. Use a schedule for 'refresh the metrics every hour', 'log the price daily', 'keep the leaderboard current' — an action WITHOUT a schedule stays a manual button.",
					Items:       &ToolParam{Type: "object"},
				},
				"sections": {
					Type:        "array",
					Description: "(create/update) Ordered sections, each an object with a `kind` plus kind-specific fields; every section may set `title` and `subtitle`. Kinds: \"form\" (a create form — set `fields`, `submit_label`, and `modal`:true for the signature structured-create look) · \"table\" (the record list — set `columns`, ALWAYS set `empty_text`, and set `editable`/`deletable` on any table paired with a create form) · \"display\" (read-only pairs) · \"chart\" (bar|line|area|pie — set `chart_type` plus inline labels+series OR a `source_script`; this is how an app graphs/plots/trends) · \"chat\" (a conversation panel bound to an agent) · \"workbench\" (the SINGLE section that IS a three-panel list | document | chat app — do not also add form/table/chat) · \"html\" (set `html` — a full HTML/CSS/JS canvas rendered verbatim, inline <script> RUNS; this is how you build a GAME, a canvas animation, a simulation, or any custom visual the other kinds can't express, and the only place a declarative app can reach the runtime's extension registries — custom pipeline block renderers silently do nothing unless the section is listed BEFORE the pipeline section and the blob is a fragment rather than a whole document, so read action=\"help\" first) · \"actions\" (a row of script-backed buttons) · \"empty\" (a centered placeholder). Minimal good app = a modal form + an editable table over the same records; a game = ONE html section. **Call action=\"help\" for the full spec** — every field of every kind, data_sources, actions, schedules, and the worked examples.",
					Items:       &ToolParam{Type: "object"},
				},
			},
			Required: []string{"action"},
		},
		Handler: func(args map[string]any) (string, error) {
			action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
			switch action {
			case "create", "update":
				return t.appDefCreateOrUpdate(args, action == "update")
			case "list":
				return t.appDefList()
			case "get":
				return t.appDefGet(args)
			case "patch_html", "patch":
				return t.appDefPatchHTML(args)
			case "replace_function", "patch_function":
				return t.appDefReplaceFunction(args)
			case "revisions", "history":
				return t.appDefRevisions(args)
			case "revert", "undo", "restore":
				return t.appDefRevert(args)
			case "test":
				return t.appDefTest(args)
			case "verify":
				return t.appDefVerify(args)
			case "delete":
				return t.appDefDelete(args)
			case "help", "":
				return appDefHelpText, nil
			default:
				return "", fmt.Errorf("unknown action %q — use create | update | patch_html | replace_function | revisions | revert | test | verify | list | get | delete | help", action)
			}
		},
	}
}

const appDefHelpText = `app_def actions:
- create {name, slug?, description?, record_key?, sections:[…]} — author an app, served at /custom/<slug>/. Data-shaped or fully interactive (a game, an animation) — both are in scope; see the html section kind.
- update {id(slug), …, sections:[…]} — revise an app in place. REPLACES the page with what you send, so an html app means re-sending the whole document AND a sections array means the WHOLE set — a section you leave out is a section you delete. Dropping the one that runs the app (pipeline / chat / workbench) is REFUSED for that reason: the page still renders and verify still passes without it, so the loss is invisible everywhere else. Call action="get" first; it returns the sections in the shape update accepts. An update that shrinks an html app sharply, or that drops functions the rest of the code still calls, is REFUSED (pass confirm_rewrite:true if you really are re-authoring from scratch) — that shape is a half-finished rewrite, and it deletes working code while still parsing and loading clean.
- replace_function {id(slug), function, replace, section?} — swap ONE named function in an html section. Name it, hand over the whole new function, and the server finds the old one: you never reproduce a line of it, so this cannot fail on whitespace and does not need the current document in front of you. THE action for "rewrite drawBird" / "fix the collision function" / "make the car look different".
- patch_html {id(slug), find, replace, section?} — change PART of an html section by exact find/replace. For edits smaller than a function (a constant, a one-line bug): find must match EXACTLY ONCE (zero or several are refused, never guessed), and everything outside the match is left untouched. Both in-place edits are parsed, checked for calls to code they would delete, and loaded in a real browser BEFORE they are kept — an edit that breaks any of those is rolled back and the previous revision keeps serving.
- revisions {id(slug)} — the last few versions of the app, newest first, each shown as its SIZE and FUNCTION COUNT next to the one serving now. A version much larger than the current one is an edit that removed code.
- revert {id(slug), to?} — restore a kept revision (stamp, or its position in the listing; omit for the most recent). The version it replaces is kept too, so a revert is undoable. Reach for this the moment an edit turns out to have deleted something, instead of reconstructing the app from memory — reconstructing from memory is what deletes things.
- list — your apps: [{slug, name, desc}].
- get  {id(slug)} — one app's full section definition.
- test {id(slug), sample?:[{...}], params?:{...}} — RUN every data_source + action script and report each one's output/errors (catches broken scripts before the user opens the app). Run this after authoring any app with scripts. Pass sample=[{field:value,...}] (example form submissions, keyed by the form's field names) to exercise the full form→record→data-source→output chain even before any real records exist — e.g. test that adding {"city":"Santa Cruz, CA"} actually yields a forecast.
- verify {id(slug), sample?:[{...}]} — the FINAL gate before telling the user the app is ready: runs every script (like test) AND loads /custom/<slug>/ in a real headless browser as the user, reporting JS console errors, uncaught exceptions, failed requests, whether the sections actually rendered, and — per data source — whether the page really fetched its live endpoint (catches a working script no section is wired to). An app is NOT done until verify passes.
- delete {id(slug)}.

Section kinds: form (create form; set modal=true + submit_label for the structured-create look) | table (record list; always set empty_text; editable adds a per-row Edit dialog prefilled from the record, deletable + auto_refresh_ms keep it live) | display (read-only pairs) | chart (bar/line/area/pie from inline data or a source_script that prints {labels, series}) | empty (centered placeholder) | chat (live chat bound to the app's agent — requires agent_id) | pipeline (submit a run, watch its stages stream, browse past runs — requires pipeline_id) | workbench (three-column list|viewer|chat — the whole app; requires agent_id) | html (a full HTML/CSS/JS canvas — set the html field; inline <script> RUNS, so this covers anything the typed kinds can't express: games, canvas animation, simulations, custom visualizations, bespoke widgets).

Minimal good app = a form (modal=true) + a table (editable, deletable) over the same records. The form's saves and the table's source both point at the app's per-record store automatically — you don't wire endpoints. For an assistant app, set agent_id and add a chat section so the LLM lives inside the app. For a 'list | document viewer | chat' three-panel app, use ONE workbench section (it IS the whole app).

For LOGIC (fetch/aggregate/transform instead of plain CRUD): add data_sources:[{name, script, capabilities?}] — a python script that reads the app's records with 'records = json.loads(os.environ.get("records", "[]"))' (the records env var is a JSON STRING; never json.loads("records")) + query params, and PRINTS JSON; reach external data with 'from gohort import fetch_url; r = fetch_url(url)' (granted by default; r is {status,headers,body}; it RAISES on transport failure so wrap it in try/except and still print JSON). Then a table/display sets source_script:"<name>" to render the script's output. Served at /custom/<slug>/data/<name>. Run app_def action=test to execute the scripts and see their output/errors before telling the user it's ready. Owner-only.

For ACTION BUTTONS (the write side): add actions:[{name, label, script, capabilities?, confirm?, schedule?}] at the TOP LEVEL of the call, beside sections (NOT inside a section) — a script that gets the records + params and PRINTS {message?, records?}; the framework upserts the returned records (so they reach the tables) and shows the message. Add a section of kind "actions" to render the buttons; that section takes no fields of its own. Served at /custom/<slug>/action/<name>.

An action on an app with a pipeline_id ALSO receives the last FINISHED run, so a button can move a run into the record store: pipeline_output (the final stage's text) and pipeline_run (JSON: id, title, date, output, and blocks — one per stage, so the rounds survive, not just the verdict). Both are empty strings when nothing has finished, so read them with a default: run = json.loads(os.environ.get('pipeline_run') or '{}'). This is how "save this debate to history" is written — there is no other route from a run to the records, and inventing an env var name yields a script that prints valid JSON and does nothing forever.

For SELF-UPDATING apps (dashboard/tracker): add schedule:{interval_seconds?|cron?, max_idle_days?} to an action to run it unattended on a timer (no click, no open page). interval_seconds is floored to 300s; cron uses NextCronOccurrence ("MON 09:00"). Each fire runs the same script and upserts what it returns — return a new-key record to APPEND a row (tracker/history), a fixed-key record to REPLACE the snapshot (dashboard). max_idle_days auto-pauses after N unviewed days (re-arms on next visit). Owner copy only; imported apps don't fire until enabled.

=== SECTION KINDS (full spec) ===
(create/update) Ordered sections, each an object with a 'kind' plus kind-specific fields. Every section may set 'title' and 'subtitle'.

kind="form" — a create form. Fields: 'fields' (array of {field, label, type, placeholder, rows, help}; type is text|textarea|number|select|toggle|tags|password, default text; select needs 'options':[{value,label}]), 'submit_label' (button text, default "Add"), 'modal' (boolean — when true the form opens from a "New" button in a dialog; the signature structured-create pattern). The form saves a record to the app's store.

kind="table" — a list of the app's records. Fields: 'columns' (array of {field, label, flex, mute, link}; set 'link' to the name of another field holding a URL to render THIS cell as a clickable link — e.g. a story row {title, url} uses column {field:"title", link:"url"}. NEVER put raw <a>…</a> HTML in a cell value; cells are escaped and it shows as literal markup — use the link field instead), 'empty_text' (shown when there are no records — ALWAYS set this), 'editable' (boolean — adds an Edit button per row that opens the record in a PREFILLED dialog; the user fixes a typo or updates a value and saves in place. Fields default to the create form's fields (same types/labels/selects), or pass 'edit_fields' (same shape as form fields) to edit a different subset. Set this on any record-store table paired with a create form — records the user typed are records the user will want to fix. NOT for source_script tables: computed rows aren't stored records), 'deletable' (boolean — adds a Delete button per row), 'auto_refresh_ms' (poll interval; 2000 keeps the list live as records are added), 'source_script' (name of a data_sources entry — when set, the table's rows come from that SCRIPT instead of the record store; the script must print a JSON array).

kind="display" — a read-only labeled-value panel. Fields: 'pairs' (array of {label, field}), 'source_script' (name of a data_sources entry whose script prints a JSON object; defaults to the record store when omitted).

kind="chart" — a bar / line / area / pie chart. Set 'chart_type' (bar|line|area|pie; default bar). Data is EITHER inline — 'labels':[...] + 'series':[{name, points:[numbers]}] (one point per label; for pie use 'series':[{name, value}]) — OR computed: set 'source_script' to a data_sources entry whose script PRINTS a JSON object {"labels":[...], "series":[...]} (optionally chart_type/title/options), i.e. a chart OF the app's records. Options (flat on the section): 'stacked' (bars), 'height', 'auto_refresh_ms' (poll interval for a source_script chart — this is how a live monitor stays live; polling pauses while the tab is hidden and resumes on return, so an unattended dashboard does not run your script forever). The section title is the heading; the chart draws no duplicate title. Use this to VISUALIZE what a table lists — e.g. a form logging {day, amount} + a data source that buckets them, rendered as a bar chart.

kind="actions" — a row of script-backed action buttons (one per entry in the app's top-level 'actions'). Clicking a button runs its script and the framework persists what it returns + refreshes the tables. No fields needed; declare the scripts in 'actions' (see the actions parameter). Use for app verbs (Sync, Generate, Refresh).

kind="empty" — a centered empty-state placeholder (for a 'nothing selected' panel). Fields: 'icon' (an emoji), 'title', 'hint'.

GRAPHICS IN AN html SECTION: draw them in code. There is NO static asset route for apps — an app cannot reference /images/sprite.png — and generate_image is a CHAT tool that shows the user a picture, not an asset pipeline. So build visuals from: canvas 2D primitives (fillRect / arc / paths / gradients), inline <svg>, CSS shapes and animation, emoji or text glyphs as sprites, or a data: URI embedded in the markup. For a side-scroller that means drawing the runner and the obstacles with canvas calls rather than loading sprite files — which is also less to go wrong, since there is nothing to 404. Do not stall a build waiting on art that has nowhere to live.

kind="html" — a full HTML/CSS/JS canvas. Fields: 'html' (the markup, rendered VERBATIM and unescaped; inline <script> RUNS) and optional 'height' (any CSS length, e.g. "640px" / "80vh" — only used when the blob is a whole document; default min(80vh, 860px)). WRITE A COMPLETE DOCUMENT for anything with its own layout (a game, a canvas animation, a simulation): doctype, <head>, <style>, <body>. A whole document is given its OWN FRAME, so its CSS reset and body rules style only itself and its 100vh measures its own box; a bare fragment is spliced into the page and its styles apply page-wide. Same origin either way — a framed document still fetches 'data/<name>' and shares the page's cookies/storage. Anything that runs in a browser page runs here: <canvas> with a requestAnimationFrame loop, keyboard/pointer handlers, physics, collision, audio, SVG, WebGL. So YES — a game, an animation, a simulation, or a custom visualization is buildable, and this is how you build one. Do not tell the user an interactive or graphical app is out of scope; the typed sections are not the limit of what an app can be. The steer is about FIT, not permission: for a DATA app (records, forms, lists, dashboards) reach for a typed section first, because those give you the record store, editing, refresh, and styling for free, and hand-rolling that in html is wasted work. When the thing genuinely isn't a data app, html is the right and intended choice — use it without apology. TO LOAD A DATA SOURCE FROM AN html SECTION'S SCRIPT: use a PLAIN RELATIVE fetch — 'fetch('data/<name>').then(r => r.json())' — where <name> is the SLUGIFIED data_sources name (lowercase, hyphens; the endpoint is /custom/<slug>/data/<name>). There is NO client-side 'gohort' object on app pages (the 'from gohort import fetch_url' helper is PYTHON-side, inside the data-source script, not the browser) — calling 'gohort.fetch(...)' in html throws "gohort is not defined". If a plain table renders your data, prefer a typed table with source_script over hand-rolling fetch in html. The blob is trusted (owner-authored, owner-served), so it is not sanitized — do not interpolate untrusted data into it. CUSTOMIZING A pipeline SECTION'S CARDS: an html section's inline script is the only way a declarative app reaches the runtime's extension registries. Two rules, and BOTH fail silently rather than erroring. (1) FRAGMENT, always: a whole document is rendered in a frame, and a frame has its OWN window, so anything registered there is invisible to the page. Watch for a stray <body> tag (or a doctype, or <html>) anywhere near the top, which is enough on its own to make the blob count as a whole document and turn a working registration inert. (2) ORDER, for BLOCK RENDERERS specifically: list the html section BEFORE the pipeline section. A pipeline panel COPIES the renderer registry when it mounts, and sections mount in the order you list them, so an html section placed after it registers into a map nothing reads again and every card renders in the default style. Markdown extensions (window.uiRegisterMarkdownExtension) are read at RENDER time instead, so those work from anywhere on the page. Client actions (window.uiRegisterClientAction) are reached by a pipeline section's toolbar button with method "client", and the runtime looks THOSE up at CLICK time, so an html section registering one can sit anywhere on the page — the ordering rule above is block renderers only. WHAT TO REGISTER FOR: a pipeline block's type is the STAGE KIND that produced it (worker, panel, tool, agent, machine, loop, branch, fanout, synthesize), not a name you choose, and its title is the STAGE NAME. So register ONE renderer for the kind and branch on d.title inside it when different stages need different cards.

kind="chat" — a live chat panel bound to the app's agent (REQUIRES agent_id on the app). Sessions + streaming reply are wired automatically to the bound agent; the user talks to it right inside the app. Fields: 'list_title', 'empty_text', 'placeholder'. This is how you build a one-app assistant surface (e.g. sessions list + a viewer + a chat that drafts content) instead of sending the user off to a separate /chat URL.

kind="pipeline" — the RUN surface: a submit form on top, the run's stages streaming in below it as they finish, and every past run in a sidebar. REQUIRES pipeline_id on the app. Fields: 'fields' (the submit form — array of {name, label, type, placeholder, default, required, rows, options}; DEFAULTS to one required textarea named "topic", which is what the run surface reads as the pipeline's input), 'submit_label' (default "Start"), 'empty_text'. The stage transcript renders as markdown and past runs are batch-deletable. 'toolbar' (array of buttons shown above the transcript once a run is open, each {label, method, url?, title?, variant?, confirm?}) — method is one of: "copy" (copy the link to this run; url defaults to "?session={id}", which is the whole Copy Link button), "open" (a new tab), "post" (run one of this app's OWN action scripts — url is "action/<name>?id={id}", and the script gets id plus pipeline_output and pipeline_run for the last finished run), or "client" (url is the NAME of a handler an html section registered with window.uiRegisterClientAction — this is how Export/Print buttons work). The panel also has stream, modal, related and load; all four are REFUSED here and the error says why, because none of them can work against the endpoints a custom app has. 'suggest_script' (name of one of this app's data_sources) puts a Suggest button on the form: the script prints a JSON ARRAY for a popover of choices, or an object with a topic/text/suggestion key to fill the field directly. 'suggest_label' (default "Suggest") and 'suggest_target' (which field it fills; defaults to the first) go with it. 'meta' (array of {field, label?, style?, variants?, truncate?}) puts extra values under each sidebar row's title, so a run history can be SCANNED for its answer instead of opened one run at a time — style is "text" (a line), "badge" (a small neutral pill) or "pill" (colored per value via variants:{"for":"#3fb950", "against":"#f85149"}). The VALUES come from the pipeline, not from here: the pipeline must promote them with session_meta:["<stage>.<field>"], naming fields a stage declares in its "output" contract. A meta field the pipeline does not promote renders blank, and you are told so at authoring time. NOT available on a custom app: cancelling a run mid-flight and reconnecting to one after closing the tab — the run surface serves stream/sessions only, so do not promise either. Nothing to wire: the endpoints are relative to the app, the transcript persists per completed stage (so closing the tab loses the live view, never the result), and each user of a shared app gets their own run history over the owner's recipe.

EVERY field is a PARAMETER: it arrives in the pipeline's prompts as {field_name}. So a debate form asking for proposition / side_a / side_b lets the stages say "Argue {side_a} on: {proposition}". The RUN'S INPUT — what {input} resolves to and what titles the run in the sidebar — is the field named "input" or "topic", or else the first one. Non-strings come through as text ({rounds} is "3", a toggle is "true"). The interpreter's own tokens are reserved: a field named input, prev, item or iteration is not substituted, because it would redefine the template language. A loop's count is NOT templatable — how many passes is authored in the pipeline, not asked on the form.

=== BUILDING A MULTI-STAGE APP (research, debate, review) ===
This is the composition to reach for when the user asks for "deep research", "a debate", "a panel", "have several agents argue/critique/review X" — a job that is several LLM turns with structure between them, not one conversation. Build it in this order, and do not stop at the pipeline: a pipeline with no app is a tool only an agent can call, which is not what the user asked for.

1. AGENTS (create_agent) — one per distinct VOICE the run needs, each with a persona that states its stance and what it must not do. A debate wants two opposed advocates plus a judge; a research app often wants none at all (worker stages are cheaper and have no persona to leak).
2. PIPELINE (the pipeline tool) — the recipe, as data:
   • deep research: a worker stage that DECOMPOSES the question into sub-questions (declare an Output field holding the list), a fanout stage over that field that searches + reads each one, then a synthesize stage that writes the answer with citations. Add a loop around the fanout when the user wants it to chase gaps.
   • debate: a loop whose Body is one agent stage per debater (each prompt references the other's prior output via {stage:NAME}, which carries the previous ROUND's text on later passes — that is the rebuttal), Count = rounds, Collect "all" so the whole transcript survives, then a final judge stage.
   • ANY stage that must reach the web has to DECLARE its tools (e.g. tools:["web_search","fetch_url"]). A stage that declares none is a pure LLM transform with no access to anything — which is exactly right for synthesis and exactly wrong for research. This is the single most common way a research pipeline comes back fluent and sourceless.
3. APP (this tool) — create the app with pipeline_id set to that pipeline and ONE pipeline section. That is the whole app; do not add a form/table alongside it (the panel already has its own submit form and history).
4. VERIFY — run the pipeline ONCE (one narrow question; each run costs real searches and minutes) and read what came back. A run that returns confident prose with no sources means step 3's tools were not declared.

NEVER hand-roll the run surface in an html section. The pipeline section already streams the stages, persists each one as it completes, and lists past runs; a hand-written version has to reimplement SSE parsing, and the obvious shortcut for its history — localStorage — is per-browser and silently loses everything the moment the user opens the app anywhere else, which is precisely the "come back tomorrow and re-read it" promise the app was built for. If a pipeline section looks wrong, fix the section or say what it does; do not replace it with markup. The same rule holds generally: an app's data belongs in its record store or the run store, never in localStorage.

kind="workbench" — the THREE-COLUMN document workbench: an item list (left), a rendered document VIEWER of the selected item (center), and a chat bound to the app's agent (right). REQUIRES agent_id. This is the right shape for 'a list of docs/guides/notes, a formatted reader in the middle, and an AI assistant that helps write them' — clicking a list item shows it; the chat drafts content; each chat reply has an 'Add to document' button that appends it into the open item, and the viewer re-renders. ONE workbench section IS the whole app (don't add other sections). Fields: 'item_label' (record field for the list label, default title), 'body_field' (the markdown field shown + appended-to in the viewer, default content), 'item_noun' (e.g. 'guide' — used in the New button + 'Add to <noun>' label), 'new_fields' (form fields for creating an item; defaults to a single title field), 'list_title', 'empty_title', 'empty_hint', 'empty_icon'.

The document body is MARKDOWN, rendered as a formatted HTML-like document — '## Section' and '### Sub-section' headings, lists, code blocks, etc. The DATA LAYER IS THE APP. The workbench AUTOMATICALLY gives the bound agent an 'add_section(section_title, markdown)' tool that writes a section straight into the OPEN document's record (the store the viewer renders) — so 'add a section about hooks' appears in the guide with no button. You do NOT build that tool; it's provided. So a workbench agent should be told to call add_section to commit content, and must NOT be given its OWN storage tools (no file/python/JSON, no custom save) — those write to its workspace, never reaching the viewer. (A manual 'Add to document' button on each reply is also available as a fallback.)

Minimal good app = a form (modal=true, submit_label) + a table (empty_text, deletable, auto_refresh_ms) over the same records. For an assistant app, add agent_id + a chat section. For a 'sessions | viewer | chat' three-panel app, use ONE workbench section.
`

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// slugify derives a URL slug from a name: lowercase, non-alphanumerics → single
// hyphen, trimmed. "Reading List!" → "reading-list".
func slugify(name string) string {
	s := slugRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	return strings.Trim(s, "-")
}

func (t *chatTurn) appDefCreateOrUpdate(args map[string]any, isUpdate bool) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	slug := slugify(stringArg(args, "slug"))

	var spec AppSpec
	// The html this app serves RIGHT NOW, captured before anything overwrites
	// it — an update is only recognizable as a wipe by comparison with what it
	// replaces (see appRewriteRisk).
	var priorHTML string
	if isUpdate {
		key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), name))
		existing, ok := LoadAppSpec(t.user, key)
		if !ok {
			return "", errors.New("no matching app to update — check the slug (app_def action=list)")
		}
		spec = existing
		priorHTML = appSpecHTMLText(existing)
		if name != "" {
			spec.Name = name
		}
	} else {
		if name == "" {
			// Point at the OTHER action too. This fires when an author is
			// re-sending a large payload to fix one thing, and three times in a
			// row the reflex was to re-send the same create rather than to
			// switch verbs — an app that already exists is revised, not
			// recreated, and the message never said so.
			return "", errors.New("name is required to create an app — pass name:\"My App\" (the slug is derived from it; pass slug explicitly to override). If the app already exists, use action=\"update\" with id=\"<slug>\" instead — app_def(action=\"list\") shows what you have")
		}
		if slug == "" {
			slug = slugify(name)
		}
		if slug == "" {
			return "", errors.New("could not derive a slug from the name — pass an explicit slug")
		}
		if _, exists := LoadAppSpec(t.user, slug); exists {
			return "", fmt.Errorf("an app with slug %q already exists — use action=update, or pick a different name/slug", slug)
		}
		spec = AppSpec{Slug: slug, Name: name, Owner: t.user}
	}

	if d := strings.TrimSpace(stringArg(args, "description")); d != "" {
		spec.Desc = d
	}
	if rk := strings.TrimSpace(stringArg(args, "record_key")); rk != "" {
		spec.RecordKey = rk
	}
	if spec.RecordKey == "" {
		spec.RecordKey = "id"
	}
	if a := strings.TrimSpace(stringArg(args, "agent_id")); a != "" {
		if ag, ok := findAgentByNameOrID(t.udb, t.user, a); ok {
			spec.AgentID = ag.ID
		} else {
			spec.AgentID = a // store as given; resolution is the chat surface's problem (step 2)
		}
	}
	// pipeline_id: the multi-stage RUN this app's pipeline section drives.
	// Resolved to a stored id where possible so a later rename of the pipeline
	// doesn't unbind the app; an unresolvable value is kept verbatim, since the
	// binding is looked up again by name at serve time.
	// Accepted at the app level OR on the pipeline SECTION itself. The section
	// is the more natural place to write it — that is where the binding is
	// used — and an author who guesses that way is not wrong about anything
	// except which object holds the field. Silently ignoring it produced an app
	// with a pipeline section and no pipeline, which fails at serve time with
	// nothing on the authoring side to explain it.
	pipeRef := strings.TrimSpace(stringArg(args, "pipeline_id"))
	if pipeRef == "" {
		pipeRef = sectionPipelineRef(args["sections"])
	}
	// promoted is what the bound pipeline puts on a run's sidebar row. Captured
	// here because this is the only place the DEFINITION is in hand; a meta
	// field naming something it does not promote renders an empty pill, which
	// reads as a broken panel rather than as a name that does not resolve.
	var promoted []string
	if pipeRef != "" {
		if def, ok := t.app.LookupAppPipeline(t.user, pipeRef); ok {
			spec.PipelineID = def.ID
			promoted = def.SessionMeta
		} else {
			spec.PipelineID = pipeRef
		}
	}
	// full_width: opt the app's page into edge-to-edge layout. Only honored when
	// the key is present so an update without it keeps the existing choice.
	if _, ok := args["full_width"]; ok {
		spec.FullWidth = boolArg(args, "full_width")
	}
	// private_db: opt the app into its own dedicated database file. Only honored
	// when the key is present so an update without it keeps the existing choice.
	// No migration — records already in the shared store stay there.
	if _, ok := args["private_db"]; ok {
		spec.PrivateDB = boolArg(args, "private_db")
	}
	// Script-backed data sources (the "logic" seam): a table/display section can
	// be backed by a python script instead of the record store. Passed wholesale
	// replaces the stored set on update (omit to keep existing).
	var parseNotes []string
	if raw, ok := args["data_sources"]; ok && raw != nil {
		var notes []string
		spec.DataSources, notes = appDataSources(raw)
		parseNotes = append(parseNotes, notes...)
	}
	// Script-backed actions (the write-side logic seam): buttons that run a
	// script which returns records the framework persists.
	if raw, ok := args["actions"]; ok && raw != nil {
		var notes []string
		spec.Actions, notes = appActionDefs(raw)
		parseNotes = append(parseNotes, notes...)
	}

	// Build the Page from the declarative sections. On update with no sections
	// passed, keep the existing page.
	if raw, ok := args["sections"]; ok && raw != nil {
		// Refuse an update that reads as a half-finished rewrite BEFORE it can
		// be stored. Everything downstream — the parser, the browser load —
		// passes a document that deleted its own game loop, so this is the
		// only place the loss is still visible.
		if isUpdate && !boolArg(args, "confirm_rewrite") {
			if risk := appRewriteRisk(priorHTML, appProposedHTMLText(raw)); risk != "" {
				return "", errors.New(risk)
			}
			// The same loss from the other direction: the sections array is
			// replaced wholesale, so a functional section left out of the new
			// one is deleted — silently, because what remains still renders.
			if risk := appDroppedFunctionSection(appSpecSections(spec), appProposedSections(raw)); risk != "" {
				return "", errors.New(risk)
			}
		}
		page, err := buildAppPage(spec, raw)
		if err != nil {
			return "", err
		}
		blob, err := page.ConfigJSON()
		if err != nil {
			return "", fmt.Errorf("render app page: %w", err)
		}
		spec.Page = blob
		// Keep the AUTHORING sections next to the page they compiled into, so
		// action=get can hand back something action=update actually accepts.
		// The rendered page is not valid input; without this, revising an app
		// meant re-authoring it blind from the rendered shape (which fails the
		// section parser outright) — the "the Builder can't update its own app"
		// bug this field exists to close.
		if src, err := json.Marshal(raw); err == nil {
			spec.Sections = src
		}
		// Record the workbench body field on the spec so the co-author tool +
		// viewer agree on which field is the document body.
		if arr, ok := raw.([]any); ok {
			for _, item := range arr {
				mm, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if mm = normalizeSection(mm); strings.EqualFold(strings.TrimSpace(mapStr(mm, "kind")), "workbench") {
					spec.BodyField = firstNonEmptyStr(mapStr(mm, "body_field"), "content")
				}
			}
		}
		parseNotes = append(parseNotes, unknownSectionKeyNotes(raw)...)
		parseNotes = append(parseNotes, appShapeNotes(raw, strings.TrimSpace(spec.PipelineID) != "")...)
		parseNotes = append(parseNotes, appSessionMetaNotes(raw, promoted)...)
	} else if !isUpdate {
		return "", errors.New("sections is required to create an app")
	}

	verb := "Created"
	reason := "create"
	if isUpdate {
		verb, reason = "Updated", "update"
		if boolArg(args, "confirm_rewrite") {
			reason = "update (confirmed rewrite)"
		}
	}
	saved := SaveAppSpecAs(spec, reason)
	msg := fmt.Sprintf("%s app %q at /custom/%s/ (revision %s) — open it in the dashboard under Custom Apps. Records save to the app's own store; the table lists them. Revise with app_def(action=\"update\", id=%q, …).",
		verb, saved.Name, saved.Slug, saved.Updated, saved.Slug)

	msg += "\n\n" + t.appInventoryLine(saved)

	// Report any name-normalization or dropped entries up front — a
	// slugified data-source name silently breaks a source_script/fetch
	// reference the author spelled the original way, and a dropped entry
	// reads as saved when it wasn't.
	if len(parseNotes) > 0 {
		msg += "\n\nHeads up — the framework adjusted your input:\n- " + strings.Join(parseNotes, "\n- ")
	}

	// Parse the inline JavaScript an html section carries. A script that
	// doesn't parse takes the WHOLE page down (a game is one section, so the
	// app is simply dead), and until this ran the write reported success and
	// the author found out one round-trip later from a browser check —
	// usually against the previous revision, which is how a single stray
	// token turns into six updates that never converge. Answer here, attached
	// to the write that caused it, naming the block and line.
	if raw, ok := args["sections"]; ok && raw != nil {
		var scriptProblems []string
		for i, html := range appHTMLSectionScripts(raw) {
			probs, checked := htmlScriptSyntaxProblems(t.sandboxCallerCtx(), html)
			if !checked {
				continue // no verdict reachable — say nothing rather than accuse
			}
			for _, p := range probs {
				scriptProblems = append(scriptProblems, fmt.Sprintf("html section %d, %s", i+1, p))
			}
		}
		if len(scriptProblems) > 0 {
			return fmt.Sprintf("%s app %q, BUT its inline JavaScript DOES NOT PARSE — the page will be blank/dead until this is fixed:\n- %s\n\nFix the markup with app_def(action=\"update\", id=%q, …) (it re-checks on save). Send the WHOLE corrected document, and do NOT tell the user the app is ready.",
				verb, saved.Name, strings.Join(scriptProblems, "\n- "), saved.Slug), nil
		}
		// Parsing says the document is well-formed, not that it is whole. A
		// page that calls a function nothing defines parses, loads, and (for a
		// canvas app, where nothing runs until the user clicks) reports clean
		// in the browser too — so this is the only check standing between the
		// author and a "success" on top of a dead app. The update path already
		// REFUSED this shape; reaching here means either a create, or a
		// rewrite the author explicitly confirmed. Say it plainly either way.
		if dangling := jsDanglingCalls(appProposedHTMLText(raw)); len(dangling) > 0 {
			return fmt.Sprintf("%s app %q, BUT the page CALLS CODE IT NEVER DEFINES — it parses and loads, and then dies the moment anyone uses it. Nothing defines: %s\n\nEither add those functions or remove the calls to them. Fix it with app_def(action=\"replace_function\", …) if you are adding one back, or action=\"update\" for the whole document. Do NOT tell the user the app is ready.",
				verb, saved.Name, appNameList(dangling, 12)), nil
		}

		// Parsing is only the cheap half. An html section IS the page, so load
		// the revision that was just written and report what the browser says
		// about it. Doing this ON SAVE is the point: action=verify runs against
		// whatever is stored when IT runs, so an author who batches an update
		// and a verify in one turn verifies the copy it just replaced, reads a
		// report about code it already rewrote, and "fixes" a line that no
		// longer exists. Checking the write's own output cannot go stale.
		if len(appHTMLSectionScripts(raw)) > 0 {
			if errs := appPageRuntimeErrors(t.user, saved.Slug); len(errs) > 0 {
				return fmt.Sprintf("%s app %q, BUT the page FAILS IN A REAL BROWSER — this is the revision you just saved, not an older one:\n- %s\n\nFix it with app_def(action=\"update\", id=%q, …) (it re-checks on save). Send the WHOLE corrected document, and do NOT tell the user the app is ready.",
					verb, saved.Name, strings.Join(errs, "\n- "), saved.Slug), nil
			}
		}
	}

	// Auto-verify the data sources: they fire when the page first opens (a table or
	// display fetches them), so a script that crashes is exactly the "errors on
	// load" footgun. Run them here — read-only by design, safe to execute — and on
	// failure return an error-shaped result so the author fixes the script before
	// telling the user it's ready, rather than the user hitting the 500. Actions
	// (the write side; a fetch cap can reach an external API) are NOT auto-run —
	// they wait for an explicit app_def action=test.
	if len(saved.DataSources) > 0 {
		report, _, _, fail := t.checkScripts(saved, false, nil, nil)
		if fail > 0 {
			return fmt.Sprintf("%s app %q, BUT a data source FAILED to run — the app will error on load until this is fixed:\n\n%s\nFix the script with app_def(action=\"update\", id=%q, …) (it re-checks on save). Do NOT tell the user the app is ready yet.",
				verb, saved.Name, strings.TrimSpace(report), saved.Slug), nil
		}
		msg += "\n\nData source check — all passed:\n" + strings.TrimSpace(report)
		msg += "\nTip: run app_def(action=\"test\", id=\"" + saved.Slug + "\", sample=[{…example form entry…}]) to confirm the full form→data-source→output chain produces real output."
	}
	// What to say about verification depends on what this save already did. An
	// html-section app was just loaded in a real browser above, so telling the
	// author to go verify it invites the exact loop this check exists to end:
	// a verify batched alongside the NEXT update reports on the revision being
	// replaced, and its findings read as fresh.
	if _, ok := args["sections"]; ok && len(appHTMLSectionScripts(args["sections"])) > 0 {
		msg += "\nThis save already parsed the inline JavaScript AND loaded /custom/" + saved.Slug + "/ in a real browser — it rendered with no JS errors. That check covered THIS revision, so you don't need a separate verify unless you change the app again."
	} else {
		msg += "\nBefore telling the user the app is ready, run app_def(action=\"verify\", id=\"" + saved.Slug + "\") — it loads the page in a real browser and catches render/JS/fetch failures the script checks can't see. Run it in a LATER turn than the update, never batched alongside one: verify reads whatever is stored when it runs, so an update and a verify in the same turn can report on the copy you just replaced."
	}
	return msg, nil
}

// buildAppPage translates the declarative sections array into a ui.Page scoped
// to the app's mount. Endpoints are fixed and relative ("records" / "record")
// so a spec cannot point a binding outside its own app.
func buildAppPage(spec AppSpec, raw any) (ui.Page, error) {
	arr, ok := raw.([]any)
	if !ok {
		return ui.Page{}, errors.New("sections must be an array of section objects")
	}
	if len(arr) == 0 {
		return ui.Page{}, errors.New("an app needs at least one section")
	}
	// Normalize once, up front: every scan below keys off `kind`, so a section
	// that only implies its kind has to be resolved before the first look, not
	// at build time.
	secs := make([]map[string]any, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return ui.Page{}, fmt.Errorf("section %d must be an object", i+1)
		}
		secs = append(secs, normalizeSection(m))
	}
	// A workbench is a whole-page shape (three full-height columns), so when one
	// is present it owns the page: full width, single no-chrome section.
	for _, m := range secs {
		if strings.EqualFold(strings.TrimSpace(mapStr(m, "kind")), "workbench") {
			wb, err := buildWorkbench(spec, m)
			if err != nil {
				return ui.Page{}, err
			}
			return ui.Page{
				Title:     spec.Name,
				ShowTitle: true,
				BackURL:   "/custom/",
				MaxWidth:  "100%",
				Sections:  []ui.Section{{NoChrome: true, Body: wb}},
			}, nil
		}
	}
	// A pipeline panel is a two-column shape too — run history beside a stage
	// transcript — so it takes the full width the same way a workbench does,
	// without the author having to know to ask. It does NOT own the page: a
	// pipeline section can sit under a display panel or beside a table.
	for _, m := range secs {
		if k := strings.ToLower(strings.TrimSpace(mapStr(m, "kind"))); k == "pipeline" || k == "run" {
			spec.FullWidth = true
			break
		}
	}
	// Default to a centered ~900px column; the author opts into full width for
	// data-heavy surfaces (wide tables / dashboards).
	maxWidth := "900px"
	if spec.FullWidth {
		maxWidth = "100%"
	}
	page := ui.Page{
		Title:     spec.Name,
		ShowTitle: true,
		BackURL:   "/custom/",
		MaxWidth:  maxWidth,
	}
	// The first form section's fields are the natural default for an editable
	// table's edit dialog — same labels/types/selects the record was created
	// with. Scanned up front so section order doesn't matter.
	var createFields []ui.FormField
	for _, m := range secs {
		if strings.EqualFold(strings.TrimSpace(mapStr(m, "kind")), "form") {
			if fields := appFormFields(m["fields"]); len(fields) > 0 {
				createFields = fields
				break
			}
		}
	}
	for i, m := range secs {
		sec, err := buildAppSection(spec, m, createFields)
		if err != nil {
			return ui.Page{}, fmt.Errorf("section %d: %w", i+1, err)
		}
		page.Sections = append(page.Sections, sec)
	}
	return page, nil
}

// normalizeSection makes a section object parseable when the author sent a
// near-miss instead of the documented {kind, …} shape. Two arrive constantly:
// a section read back from a RENDERED page (fields nested under `body`, kind
// carried as the body's component `type`), and a section whose kind is simply
// implied by the field that was set (an `html` blob, a `columns` list). Both
// state the intent unambiguously, so infer rather than reject — a hard error
// here reads as "the app can't be edited" and the author re-writes it blind.
// sectionKeys is what each section kind actually READS, so a key that is not
// listed here is a key the framework threw away. Kept beside the builder it
// mirrors: if a kind learns a field, it belongs in both places, and the cost of
// forgetting is one spurious note, never a refused save.
var sectionKeys = map[string][]string{
	"":          {"kind", "title", "subtitle", "group", "collapsed"}, // every kind
	"form":      {"fields", "submit_label", "modal"},
	"table":     {"columns", "empty_text", "editable", "edit_fields", "deletable", "auto_refresh_ms", "source_script"},
	"display":   {"pairs", "source_script"},
	"chart":     {"chart_type", "labels", "series", "source_script", "stacked", "legend", "height", "auto_refresh_ms"},
	"actions":   {"empty_text"},
	"empty":     {"icon", "hint"},
	"chat":      {"list_title", "empty_text", "placeholder"},
	"pipeline":  {"fields", "submit_label", "empty_text", "input_label", "placeholder", "pipeline_id", "toolbar", "suggest_script", "suggest_label", "suggest_target", "meta"},
	"run":       {"fields", "submit_label", "empty_text", "input_label", "placeholder", "pipeline_id", "toolbar", "suggest_script", "suggest_label", "suggest_target", "meta"},
	"workbench": {"item_label", "body_field", "item_noun", "new_fields", "new_label", "new_title", "list_title", "list_empty", "empty_title", "empty_hint", "empty_icon", "chat_empty", "placeholder"},
	"html":      {"html", "height"},
	"card":      {"html", "height"},
}

// topLevelAppKeys are app_def parameters that sit BESIDE sections, not inside
// one. A section carrying one of these isn't using a key that doesn't exist —
// it put a real key one level too deep, which is a different mistake with a
// different fix. pipeline_id is absent deliberately: it is valid in both places.
var topLevelAppKeys = map[string]bool{
	"actions": true, "data_sources": true, "agent_id": true,
	"full_width": true, "private_db": true, "record_key": true,
	"name": true, "description": true, "slug": true,
}

// unknownSectionKeyNotes reports keys a section carried that its kind does not
// read.
//
// A dropped key used to be perfectly silent: the save succeeded, the app came
// back missing the behavior the key was supposed to add, and the only way to
// find out was to guess. Guessing is what actually happened — an invented
// pipeline_label and a borrowed source_script were both accepted without
// comment, so the author concluded the SECTION KIND was broken and rewrote a
// working app by hand.
//
// A note, not an error: the section is otherwise valid and saving it is right.
// The note just makes the difference between "what I sent" and "what was
// stored" visible in the same breath as the save.
func unknownSectionKeyNotes(raw any) []string {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var notes []string
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		m = normalizeSection(m)
		kind := strings.ToLower(strings.TrimSpace(mapStr(m, "kind")))
		known, ok := sectionKeys[kind]
		if !ok {
			continue // an unknown kind is buildAppSection's error to raise, not ours
		}
		allowed := map[string]bool{}
		for _, k := range append(append([]string{}, sectionKeys[""]...), known...) {
			allowed[k] = true
		}
		var stray []string
		for k := range m {
			if !allowed[strings.ToLower(strings.TrimSpace(k))] {
				stray = append(stray, k)
			}
		}
		if len(stray) == 0 {
			continue
		}
		sort.Strings(stray) // map order is not an order
		note := fmt.Sprintf("section %d (kind %q): ignored %s — this kind reads: %s. Nothing you sent under those keys was stored.",
			i+1, kind, strings.Join(stray, ", "), strings.Join(append(append([]string{}, known...), sectionKeys[""]...), ", "))
		// Where it DOES belong, when the key is a real parameter one level up.
		// Listing what the kind reads answers "why was this dropped" and not
		// "where does it go", and the difference is rounds: an actions array
		// nested in an actions SECTION was re-sent unchanged, then moved on a
		// guess, because the note never said the word "top-level".
		var misplaced []string
		for _, k := range stray {
			if topLevelAppKeys[strings.ToLower(strings.TrimSpace(k))] {
				misplaced = append(misplaced, k)
			}
		}
		if len(misplaced) > 0 {
			note += fmt.Sprintf(" %s is a TOP-LEVEL app_def parameter — move it out of the section, beside \"sections\".",
				strings.Join(misplaced, " and "))
		}
		notes = append(notes, note)
	}
	return notes
}

// appShapeNotes reports section COMBINATIONS that parse cleanly and then don't
// do what the author is about to promise. Every one of these produced a working
// page and a false claim to the user.
func appShapeNotes(raw any, boundPipeline bool) []string {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var notes []string
	pipelineAt := -1
	var recordViews []string
	var clientButtonAt []int
	htmlSection := false
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		m = normalizeSection(m)
		switch strings.ToLower(strings.TrimSpace(mapStr(m, "kind"))) {
		case "pipeline", "run":
			if pipelineAt < 0 {
				pipelineAt = i
			}
			if appToolbarUsesClient(m["toolbar"]) {
				clientButtonAt = append(clientButtonAt, i+1)
			}
			// Extra submit fields are the pipeline's PARAMETERS: each arrives as
			// {name} in every stage's prompt. Nothing to warn about — the note
			// that used to live here said they went nowhere, which was true of
			// the run surface before it carried them.
			_ = appPipelineFields(m["fields"])
		case "table", "display":
			// A record-backed view; a source_script one computes its own rows.
			if strings.TrimSpace(mapStr(m, "source_script")) == "" {
				recordViews = append(recordViews, strconv.Itoa(i+1))
			}
		case "html", "card":
			htmlSection = true
		}
	}
	// A client-method button dispatches by NAME to a handler the app has to
	// register itself, and an html section's inline script is the only place a
	// declarative app can do that. With no html section anywhere the button
	// renders perfectly and toasts "No handler for client action" on click.
	//
	// Anywhere, not before: the runtime looks a client handler up at CLICK
	// time, unlike a block renderer, which the panel snapshots at mount. Two
	// registries, two different rules, and asserting the stricter one here
	// would send an author reordering a page that was already correct.
	if len(clientButtonAt) > 0 && !htmlSection {
		notes = append(notes, fmt.Sprintf("section(s) %s have a toolbar button with method \"client\", but the app has no html section — nothing registers the handler, so the button renders and does nothing but toast an error when clicked. Add an html section whose script calls window.uiRegisterClientAction(\"<the button's url>\", fn).",
			strings.Join(intsToStrings(clientButtonAt), ", ")))
	}
	// A pipeline bound with nothing to run it. The app carries a pipeline_id,
	// so the author means to run it — and without the section there is no
	// button, no transcript, and no history anywhere on the page. It is the
	// exact end state of an app that grew a form, a table and a script-backed
	// "run" button instead: everything parses, verify passes, and the thing the
	// app is for cannot be started.
	//
	// Only when a binding exists: an app with no pipeline_id is simply not a
	// pipeline app, and has nothing to be missing.
	if pipelineAt < 0 && boundPipeline {
		notes = append(notes, "this app binds a pipeline (pipeline_id) but has NO section of kind \"pipeline\" — nothing on the page can start a run, show its stages, or list past ones. Add {kind:\"pipeline\"}. An action script cannot run a pipeline; the section is the only surface that does.")
	}
	if pipelineAt >= 0 && len(recordViews) > 0 {
		notes = append(notes, fmt.Sprintf("section(s) %s read the app's RECORD store, which a pipeline never writes to — a run's history lives in the pipeline panel's own sidebar (section %d). Those sections will stay on their empty state forever unless an action script writes records, so do not tell the user past runs will appear there.",
			strings.Join(recordViews, ", "), pipelineAt+1))
	}
	return notes
}

// pipelineFieldsNameTheInput reports whether the form explicitly names its
// input field, in which case the first field carries no special meaning.
func pipelineFieldsNameTheInput(fields []ui.PipelineField) bool {
	for _, f := range fields {
		if strings.EqualFold(f.Name, "input") || strings.EqualFold(f.Name, "topic") {
			return true
		}
	}
	return false
}

// sectionPipelineRef finds a pipeline_id written on a pipeline SECTION rather
// than at the app level — the same binding, one object down. First one wins:
// an app has a single pipeline binding, so two different values is a conflict
// no guess resolves, and taking the first keeps the behavior predictable
// (the stray one is reported by unknownSectionKeyNotes... it isn't, since
// pipeline_id IS a key this kind reads — which is the point: it is honored).
func sectionPipelineRef(raw any) string {
	arr, ok := raw.([]any)
	if !ok {
		return ""
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		m = normalizeSection(m)
		switch strings.ToLower(strings.TrimSpace(mapStr(m, "kind"))) {
		case "pipeline", "run":
			if p := strings.TrimSpace(mapStr(m, "pipeline_id")); p != "" {
				return p
			}
		}
	}
	return ""
}

func normalizeSection(m map[string]any) map[string]any {
	if strings.TrimSpace(mapStr(m, "kind")) != "" {
		return m
	}
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	// Rendered shape: {title, body:{type:"card", html:…}} — lift the body's
	// fields up and translate its component type to the authoring kind.
	if body, ok := m["body"].(map[string]any); ok {
		delete(out, "body")
		for k, v := range body {
			if k == "type" {
				continue
			}
			if _, taken := out[k]; !taken {
				out[k] = v
			}
		}
		if kind, ok := bodyTypeToKind[strings.TrimSpace(mapStr(body, "type"))]; ok {
			out["kind"] = kind
			return out
		}
	}
	// Kind implied by the defining field of exactly one kind.
	switch {
	case strings.TrimSpace(mapStr(out, "html")) != "":
		out["kind"] = "html"
	case out["fields"] != nil:
		out["kind"] = "form"
	case out["columns"] != nil:
		out["kind"] = "table"
	case out["pairs"] != nil:
		out["kind"] = "display"
	case out["series"] != nil || out["chart_type"] != nil:
		out["kind"] = "chart"
	}
	return out
}

func buildAppSection(spec AppSpec, m map[string]any, createFields []ui.FormField) (ui.Section, error) {
	m = normalizeSection(m)
	kind := strings.ToLower(strings.TrimSpace(mapStr(m, "kind")))
	sec := ui.Section{Title: mapStr(m, "title"), Subtitle: mapStr(m, "subtitle")}
	switch kind {
	case "form":
		form := ui.FormPanel{
			PostURL:     "records",
			SubmitLabel: firstNonEmptyStr(mapStr(m, "submit_label"), "Add"),
			Fields:      appFormFields(m["fields"]),
			// New records should show up without a reload — refresh the plain record
			// lists ("records") AND every script-backed panel, since a data source's
			// output is computed FROM the records (the "set a city → see its weather"
			// wiring). Without the data/<name> sources here a form and a source_script
			// display stay disconnected: the record saves but the computed panel never
			// re-fetches.
			Invalidate: appRecordWriteInvalidations(spec),
		}
		if len(form.Fields) == 0 {
			return ui.Section{}, entryListError("form", "field", "fields", m["fields"])
		}
		if boolArg(m, "modal") {
			// Structured-create: the form opens from a "New" button in a dialog —
			// the signature pattern instead of an always-visible form.
			sec.Body = ui.ModalButton{
				Label:    firstNonEmptyStr(mapStr(m, "submit_label"), "New"),
				Title:    firstNonEmptyStr(sec.Title, "New"),
				Subtitle: sec.Subtitle,
				Body:     form,
			}
			// The modal carries its own title; clear the section chrome so it reads
			// as a single action button.
			sec.Title, sec.Subtitle = "", ""
		} else {
			sec.Body = form
		}
	case "table":
		tbl := ui.Table{
			Source:        appSectionSource(m),
			RowKey:        spec.RecordKey,
			Columns:       appTableCols(m["columns"]),
			EmptyText:     firstNonEmptyStr(mapStr(m, "empty_text"), "Nothing here yet."),
			AutoRefreshMS: intFromArgs(m, "auto_refresh_ms"),
		}
		if len(tbl.Columns) == 0 {
			return ui.Section{}, entryListError("table", "column", "columns", m["columns"])
		}
		// editable: an Edit button per row opens the record in a prefilled
		// dialog (FormPanel: Source GETs the row, submit upserts it back to
		// the records store, invalidations refresh the table + any computed
		// panels). Field precedence: explicit edit_fields → the create form's
		// fields → a plain text input per column. Record-store tables only —
		// a source_script table's rows are computed, not stored records.
		if boolArg(m, "editable") {
			fields := appFormFields(m["edit_fields"])
			if len(fields) == 0 {
				fields = createFields
			}
			if len(fields) == 0 {
				for _, c := range tbl.Columns {
					if c.Field == spec.RecordKey || c.Field == "created" {
						continue
					}
					fields = append(fields, ui.FormField{
						Field: c.Field,
						Label: firstNonEmptyStr(c.Label, c.Field),
						Type:  "text",
					})
				}
			}
			if len(fields) > 0 {
				tbl.RowActions = append(tbl.RowActions, ui.ModalAction("Edit", ui.FormPanel{
					Source:      "record?id={" + spec.RecordKey + "}",
					PostURL:     "records",
					SubmitLabel: "Save",
					Fields:      fields,
					Invalidate:  appRecordWriteInvalidations(spec),
				}))
			}
		}
		if boolArg(m, "deletable") {
			tbl.RowActions = append(tbl.RowActions, ui.RowAction{
				Type: "button", Label: "Delete", Method: "DELETE",
				PostTo: "record?id={" + spec.RecordKey + "}", Confirm: "Delete this item?",
			})
		}
		sec.Body = tbl
	case "display":
		sec.Body = ui.DisplayPanel{Source: appSectionSource(m), Pairs: appDisplayPairs(m["pairs"])}
	case "actions":
		// A row of buttons, one per declared action (the app's `actions`). Each
		// button POSTs to action/<name>; the framework runs the script, persists
		// any returned records, and refreshes the records table. Button labels +
		// per-action confirm ride on the items (see handleActionsList).
		sec.Body = ui.ActionList{
			Source:    "actions",
			DescField: "desc",
			PostTo:    "action/{name}",
			// An action upserts records, so refresh the record lists AND every
			// script-backed panel computed from them (same as a form save).
			Invalidate: appRecordWriteInvalidations(spec),
			EmptyText:  firstNonEmptyStr(mapStr(m, "empty_text"), "No actions."),
		}
	case "empty":
		sec.Body = ui.EmptyState{
			Icon:  mapStr(m, "icon"),
			Title: firstNonEmptyStr(mapStr(m, "title"), "Nothing selected"),
			Hint:  mapStr(m, "hint"),
		}
		// EmptyState carries its own title; avoid a duplicate section heading.
		sec.Title, sec.Subtitle = "", ""
	case "chat":
		// The chat panel binds to the app's agent (agent_id). customapps serves
		// the SSE + session endpoints under chat/* (handleChat → orchestrate's
		// PublicHandle*), so the URLs are relative to the app mount, same as the
		// records store. Requires an agent_id on the app.
		if strings.TrimSpace(spec.AgentID) == "" {
			return ui.Section{}, errors.New("a chat section needs the app to have an agent_id (the agent that powers the chat)")
		}
		sec.NoChrome = true // the panel manages its own layout
		sec.Body = ui.AgentLoopPanel{
			ListURL:      "chat/sessions",
			LoadURL:      "chat/sessions/{id}",
			DeleteURL:    "chat/sessions/{id}",
			SendURL:      "chat/send",
			CancelURL:    "chat/cancel",
			ListTitle:    firstNonEmptyStr(mapStr(m, "list_title"), "Sessions"),
			NewLabel:     "New",
			ListPosition: "top",
			Markdown:     true,
			EmptyText:    firstNonEmptyStr(mapStr(m, "empty_text"), "Ask the assistant to get started."),
			Placeholder:  firstNonEmptyStr(mapStr(m, "placeholder"), "Ask anything…"),
		}
	case "pipeline", "run":
		// The RUN surface: submit form on top, live stage-by-stage transcript
		// below, past runs in the sidebar. Bound to the app's pipeline_id;
		// customapps serves the SSE + session endpoints under pipeline/*
		// (handlePipeline → orchestrate's PublicHandlePipeline), so the URLs are
		// relative to the app mount exactly like chat/* and the record store.
		//
		// This is what makes a multi-stage recipe an APP rather than a tool an
		// agent happens to own: the user gets a page to launch it, watch it
		// work, and read what it produced last week.
		if strings.TrimSpace(spec.PipelineID) == "" {
			return ui.Section{}, errors.New("a pipeline section needs the app to have a pipeline_id (the stored pipeline this app runs) — author the pipeline first with the `pipeline` tool, then pass its name or id as pipeline_id")
		}
		sec.NoChrome = true // the panel manages its own layout
		fields := appPipelineFields(m["fields"])
		if len(fields) == 0 {
			// The default is the one field every pipeline takes: its input. Named
			// "topic" because the stream endpoint accepts input|topic and the
			// panel titles a run from it.
			fields = []ui.PipelineField{{
				Name: "topic", Type: "textarea", Required: true, Rows: 3,
				Label:       firstNonEmptyStr(mapStr(m, "input_label"), "What should this run?"),
				Placeholder: mapStr(m, "placeholder"),
			}}
		}
		panel := ui.PipelinePanel{
			SessionsListURL:  "pipeline/sessions",
			SessionLoadURL:   "pipeline/sessions/{id}",
			SessionDeleteURL: "pipeline/sessions/{id}",
			SubmitURL:        "pipeline/stream",
			SubmitLabel:      firstNonEmptyStr(mapStr(m, "submit_label"), "Start"),
			Fields:           fields,
			// Name the deep-link param, because an app page's own ?id= is
			// whatever THAT app means by it — a record in a table section,
			// usually — and the panel's generic fallback would read it as a
			// run to open.
			DeepLinkParam: "session",
			// A stage transcript is prose — headings, lists, citations — so it
			// renders as markdown, and past runs get checkboxes because a run
			// history is something you prune in batches.
			Markdown:   true,
			BulkSelect: true,
			EmptyText:  firstNonEmptyStr(mapStr(m, "empty_text"), "Start a run to see it here."),
		}
		// The furniture: a per-run toolbar and a Suggest button. Both are
		// OPTIONAL and both refuse rather than render when they are pointed at
		// something this app cannot serve.
		toolbar, err := appPipelineToolbar(spec, m["toolbar"])
		if err != nil {
			return ui.Section{}, err
		}
		panel.Actions = toolbar
		if err := appPipelinePrefill(spec, m, fields, &panel); err != nil {
			return ui.Section{}, err
		}
		metaFields, err := appPipelineMetaFields(m["meta"])
		if err != nil {
			return ui.Section{}, err
		}
		panel.SessionMetaFields = metaFields
		sec.Body = panel
	case "chart":
		// A chart is either STATIC (inline labels + series) or COMPUTED by
		// a data source that prints {labels, series[, chart_type, title,
		// options]} — the source-script path is the useful one for a data
		// app (a chart of the records). The section title is the heading;
		// the SVG carries no duplicate title.
		cp := ui.ChartPanel{
			ChartType:     firstNonEmptyStr(strings.ToLower(strings.TrimSpace(mapStr(m, "chart_type"))), "bar"),
			Labels:        appChartLabels(m["labels"]),
			Series:        appChartSeries(m["series"]),
			Options:       appChartOptions(m),
			AutoRefreshMS: intFromArgs(m, "auto_refresh_ms"),
		}
		if name := slugify(mapStr(m, "source_script")); name != "" {
			cp.Source = "data/" + name
		}
		if cp.Source == "" && len(cp.Series) == 0 {
			return ui.Section{}, errors.New("a chart section needs a source_script (computed data) or inline series")
		}
		sec.Body = cp
	case "html", "card":
		// Raw-HTML escape hatch (ui.Card): render an author-supplied HTML blob
		// verbatim, for the rare surface the typed primitives don't model — a
		// bespoke layout, an embedded widget. The HTML is rendered UNescaped and
		// any inline <script> runs, so this is trusted input: same owner-only
		// trust level as the python data_sources (which run arbitrary code
		// server-side). Reach for a typed section first; this is a last resort.
		html := mapStr(m, "html")
		if strings.TrimSpace(html) == "" {
			return ui.Section{}, errors.New("an html section needs an `html` field (the raw HTML to render) — pass the markup itself, not a nested object")
		}
		// A COMPLETE document gets its own frame; a fragment is inlined. An
		// author writing a game or an animation writes a whole document
		// (doctype, <head>, a `* { margin: 0 }` reset, `body { … 100vh }`),
		// and inlining that leaks its reset and body rules into the host page
		// while its 100vh layout measures the browser window instead of its
		// own box. Framing it keeps both cascades to themselves — same origin
		// either way, so relative data-source fetches still work.
		if isFullHTMLDocument(html) {
			sec.Body = ui.Frame{HTML: html, Height: mapStr(m, "height")}
		} else {
			sec.Body = ui.Card{HTML: html}
		}
	default:
		if kind == "" {
			return ui.Section{}, errors.New("this section has no `kind` and none could be inferred from its fields — every section needs kind: form | table | display | chart | empty | chat | workbench | actions | html. Call action=get to read the app's current sections in editable form (or action=help for each kind's fields)")
		}
		return ui.Section{}, fmt.Errorf("unknown section kind %q — use form | table | display | chart | empty | chat | workbench | actions | html", kind)
	}
	return sec, nil
}

// buildWorkbench assembles the three-column WorkbenchPanel from a workbench
// section spec: a list + viewer over the app's records, a New modal to create an
// item, and a chat bound to the app's agent. Requires agent_id.
func buildWorkbench(spec AppSpec, m map[string]any) (ui.WorkbenchPanel, error) {
	if strings.TrimSpace(spec.AgentID) == "" {
		return ui.WorkbenchPanel{}, errors.New("a workbench needs the app to have an agent_id (the agent that powers the chat)")
	}
	itemLabel := firstNonEmptyStr(mapStr(m, "item_label"), "title")
	bodyField := firstNonEmptyStr(mapStr(m, "body_field"), "content")

	// The New form: the fields the LLM gave, or a sensible default (a title + the
	// body field) so creating an item always works. Posts to the records store
	// and invalidates it so the list refreshes.
	newFields := appFormFields(m["new_fields"])
	if len(newFields) == 0 {
		newFields = []ui.FormField{
			{Field: itemLabel, Label: "Title", Type: "text", Placeholder: "Name this " + firstNonEmptyStr(mapStr(m, "item_noun"), "item")},
		}
	}
	newButton := ui.ModalButton{
		Label: firstNonEmptyStr(mapStr(m, "new_label"), "New"),
		Title: firstNonEmptyStr(mapStr(m, "new_title"), "Create"),
		Body: ui.FormPanel{
			PostURL:     "records",
			SubmitLabel: firstNonEmptyStr(mapStr(m, "new_label"), "Create"),
			Fields:      newFields,
			Invalidate:  []string{"records"},
		},
	}

	// AgentLoopPanel in no-list mode: one chat window, NO sessions rail (we omit
	// list/load/delete URLs) and NO activity pane (LockActivity). The workbench's
	// own document list is the app nav, so a second session list is redundant.
	// MUST be AgentLoopPanel (not ChatPanel): chat/send emits the AgentLoopPanel
	// SSE format (sse.Send) — ChatPanel's parser ignores those frames, so its
	// replies never render. See sseWriter.SendChatEvent vs Send.
	chat := ui.AgentLoopPanel{
		SendURL:      "chat/send",
		CancelURL:    "chat/cancel",
		Markdown:     true,
		LockActivity: true,
		EmptyText:    firstNonEmptyStr(mapStr(m, "chat_empty"), "Ask the assistant to draft or add a section."),
		Placeholder:  firstNonEmptyStr(mapStr(m, "placeholder"), "Ask the assistant…"),
	}

	noun := firstNonEmptyStr(mapStr(m, "item_noun"), "document")
	return ui.WorkbenchPanel{
		ListURL:          "records",
		ItemKey:          spec.RecordKey,
		ItemLabel:        itemLabel,
		ListTitle:        firstNonEmptyStr(mapStr(m, "list_title"), "Items"),
		ListEmpty:        firstNonEmptyStr(mapStr(m, "list_empty"), "Nothing yet — create one."),
		NewButton:        newButton,
		DeleteURL:        "record?id={id}",
		RecordURL:        "record?id={id}",
		BodyField:        bodyField,
		ViewerTitleField: itemLabel,
		EmptyIcon:        firstNonEmptyStr(mapStr(m, "empty_icon"), "📄"),
		EmptyTitle:       firstNonEmptyStr(mapStr(m, "empty_title"), "Nothing selected"),
		EmptyHint:        firstNonEmptyStr(mapStr(m, "empty_hint"), "Pick an item on the left, or create one."),
		RefreshOn:        []string{"records"},
		// Tell the server which document is open so the agent's add_section tool
		// writes into it; the viewer re-fetches when the chat round finishes.
		ActiveURL: "chat/active",
		// Co-author: each assistant reply gets an "Add to <noun>" button that
		// appends it to the open record (upsert to the records store).
		CoAuthor:     true,
		CoAuthorVerb: "Add to " + noun,
		SaveURL:      "records",
		Chat:         chat,
	}, nil
}

// entryListError explains why a fields/columns list came out EMPTY.
//
// "a form section needs at least one field" was true of the parsed result and
// false of what the author sent: three fields arrived and all three were
// dropped for lacking the key the parser reads. The author, reading a message
// that contradicted the payload in front of them, re-sent the same shape six
// times and then simplified to a single field to isolate it — which produced
// the same sentence, because the count was never the problem.
//
// So: distinguish "you sent none" from "every one was discarded, here is what
// yours carry instead."
func entryListError(section, item, plural string, raw any) error {
	arr, isArr := raw.([]any)
	switch {
	case raw == nil, !isArr && raw == nil:
		return fmt.Errorf("a %s section needs at least one %s — pass %s:[{%q:…, \"label\":…}]", section, item, plural, "field")
	case !isArr:
		return fmt.Errorf("a %s section's %s must be an ARRAY of objects, got %T", section, plural, raw)
	case len(arr) == 0:
		return fmt.Errorf("a %s section needs at least one %s — %s was empty", section, item, plural)
	}
	// Non-empty in, nothing out: every entry lacked the key. Report the keys
	// they DO carry, which is the fastest possible route to the fix.
	keys := map[string]bool{}
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			for k := range m {
				keys[k] = true
			}
		}
	}
	have := make([]string, 0, len(keys))
	for k := range keys {
		have = append(have, k)
	}
	sort.Strings(have)
	msg := fmt.Sprintf("a %s section got %d %s but every one was DROPPED: each needs a \"field\" key naming the record field it reads or writes.",
		section, len(arr), plural)
	if len(have) > 0 {
		msg += " Yours carry: " + strings.Join(have, ", ") + "."
	}
	for _, alias := range []string{"key", "id", "column", "value"} {
		if keys[alias] {
			msg += fmt.Sprintf(" Rename %q to \"field\".", alias)
			break
		}
	}
	return errors.New(msg)
}

// gohortScriptHelpers is everything the sandbox's gohort module actually
// exports. Kept beside the hint because the point of the hint is this list.
var gohortScriptHelpers = []string{"fetch_url", "fetch", "fetch_via", "browse_page", "log", "secret", "HookError"}

// scriptFailureHint turns a raw Python traceback into the one sentence that
// resolves it, when the traceback is one we recognize.
//
// A tool is not importable from a script. An author reached for
// "from gohort import create_docx", got Python's bare ImportError, tried
// "from gohort import workspace", got the same, then tried default_api.create_docx
// — three rounds against a message that names the missing symbol and nothing
// about what IS available.
func scriptFailureHint(output string) string {
	if !strings.Contains(output, "ImportError") || !strings.Contains(output, "gohort") {
		return ""
	}
	name := ""
	if i := strings.Index(output, "cannot import name "); i >= 0 {
		rest := output[i+len("cannot import name "):]
		if j := strings.IndexAny(strings.TrimPrefix(rest, "'"), "'\""); j >= 0 {
			name = strings.TrimPrefix(rest, "'")[:j]
		}
	}
	msg := "HINT: the gohort module exports only " + strings.Join(gohortScriptHelpers, ", ") + " — that is the network/secret channel, NOT the tool catalog."
	if name != "" {
		msg += " " + strconv.Quote(name) + " is a gohort TOOL, and a tool cannot be imported or subprocessed from a script."
	}
	return msg + " A script does its own work in plain Python (with fetch_url for anything off-box); if the job genuinely needs a tool, it belongs in a pipeline tool stage, not in here."
}

// appSpecSections reads the stored AUTHORING sections off a spec — the shape
// update accepts, which is what a comparison against an incoming update needs.
// Empty for a spec written before that field existed; the guard then has
// nothing to compare and stays quiet, which is the right way to be wrong.
func appSpecSections(spec AppSpec) []map[string]any {
	if len(spec.Sections) == 0 {
		return nil
	}
	var raw []any
	if json.Unmarshal(spec.Sections, &raw) != nil {
		return nil
	}
	return appProposedSections(raw)
}

// appProposedSections normalizes an incoming sections array for comparison.
func appProposedSections(raw any) []map[string]any {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, normalizeSection(m))
		}
	}
	return out
}

// appFormFields converts the declarative fields array into ui.FormField values.
func appFormFields(raw any) []ui.FormField {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []ui.FormField
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// `name` is accepted as an alias for `field`. The two spellings mean the
		// same thing to an author, the pipeline section already took `name`, and
		// refusing it here made ONE key behave differently in three sections —
		// which cost six round-trips of "a form section needs at least one
		// field" against a payload that plainly carried three.
		field := strings.TrimSpace(firstNonEmptyStr(mapStr(m, "field"), mapStr(m, "name")))
		if field == "" {
			continue
		}
		ff := ui.FormField{
			Field:       field,
			Label:       firstNonEmptyStr(mapStr(m, "label"), field),
			Type:        firstNonEmptyStr(strings.ToLower(mapStr(m, "type")), "text"),
			Placeholder: mapStr(m, "placeholder"),
			Help:        mapStr(m, "help"),
			Rows:        intFromArgs(m, "rows"),
		}
		if opts := appSelectOptions(m["options"]); len(opts) > 0 {
			ff.Options = opts
		}
		out = append(out, ff)
	}
	return out
}

// appPipelineFields converts the declarative fields array into the submit-form
// fields of a pipeline section. Accepts the same `field`/`name` spelling the
// form sections use, so an author who has written one section already doesn't
// have to learn a second key for the same idea.
//
// The names matter more here than in a form: the panel POSTs them as the run's
// JSON body, and the run surface reads `input` (or `topic`) as the pipeline's
// input. A field named anything else is carried but not consumed.
func appPipelineFields(raw any) []ui.PipelineField {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []ui.PipelineField
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(firstNonEmptyStr(mapStr(m, "name"), mapStr(m, "field")))
		if name == "" {
			continue
		}
		pf := ui.PipelineField{
			Name:        name,
			Label:       firstNonEmptyStr(mapStr(m, "label"), name),
			Type:        firstNonEmptyStr(strings.ToLower(strings.TrimSpace(mapStr(m, "type"))), "text"),
			Placeholder: mapStr(m, "placeholder"),
			Default:     mapStr(m, "default"),
			Required:    boolArg(m, "required"),
			Rows:        intFromArgs(m, "rows"),
		}
		for _, o := range appSelectOptions(m["options"]) {
			pf.Options = append(pf.Options, o.Value)
		}
		out = append(out, pf)
	}
	return out
}

// --- pipeline section: toolbar + suggest -------------------------------------
//
// The pipeline SECTION is a preset over ui.PipelinePanel, and for a long time
// it set four URLs and nothing else — so an app authored declaratively got the
// run surface but none of the furniture a compiled app hangs on it (a Copy
// Link button, a Suggest button, an export). These two knobs close most of
// that gap. What they deliberately do NOT expose is anything the declarative
// surface cannot actually serve; see appPipelineToolbarRefusals.

// appPipelineToolbarAllowed is what a toolbar button may DO here.
//
//	open   — navigate to the url in a new tab
//	copy   — copy the substituted url to the clipboard
//	post   — POST to one of the app's own action scripts, then refresh
//	client — call a browser-side handler the app registered itself
var appPipelineToolbarAllowed = map[string]bool{
	"open": true, "copy": true, "post": true, "client": true,
}

// appPipelineToolbarRefusals is the rest of what ui.PipelinePanel supports,
// with the reason each one cannot work on an app built out of sections.
//
// Refused at AUTHORING time rather than rendered, because every one of them
// fails at CLICK time instead: the button draws correctly, sits there looking
// finished, and breaks one user at a time long after the author declared the
// app done. An error here costs one retry; the alternative costs a bug report
// that starts in the wrong place.
var appPipelineToolbarRefusals = map[string]string{
	"stream": "it POSTs and expects an SSE transcript back, and the only endpoint " +
		"a custom app has that speaks SSE is pipeline/stream — the recipe itself, which " +
		"the Start button already runs. Pointed at anything else the transcript just empties",
	"modal": "it streams SSE into a dialog, and a custom app has no second streaming " +
		"endpoint to stream from (a data source answers once, in JSON, and the modal would " +
		"sit empty). Generate-a-report belongs in a second pipeline the app binds, not here",
	"related": "it fetches a list of RELATED runs keyed off fields on the session " +
		"summary, and a custom app's summary carries ID, Title and Date only",
	"load": "it jumps to another run named by a {FieldName} placeholder read off the " +
		"session summary, which here carries ID, Title and Date only — every other " +
		"placeholder renders empty and the button goes nowhere",
}

// appPipelineToolbar parses the pipeline section's toolbar: the row of buttons
// that appears above the transcript once a run is open.
func appPipelineToolbar(spec AppSpec, raw any) ([]ui.PipelineAction, error) {
	if raw == nil {
		return nil, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("a pipeline section's toolbar must be an ARRAY of buttons, got %T — pass toolbar:[{\"label\":\"Copy Link\", \"method\":\"copy\"}]", raw)
	}
	var out []ui.PipelineAction
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("toolbar entry %d is not an object — each button is {label, method, url?}", i+1)
		}
		label := strings.TrimSpace(mapStr(m, "label"))
		if label == "" {
			return nil, fmt.Errorf("toolbar entry %d needs a label (it is the button's text)", i+1)
		}
		method := strings.ToLower(strings.TrimSpace(firstNonEmptyStr(mapStr(m, "method"), "open")))
		if why, refused := appPipelineToolbarRefusals[method]; refused {
			return nil, fmt.Errorf("toolbar button %q asks for method %q, which a custom app cannot honor: %s", label, method, why)
		}
		if !appPipelineToolbarAllowed[method] {
			return nil, fmt.Errorf("toolbar button %q has method %q — use open (new tab), copy (to clipboard), post (run one of this app's action scripts) or client (a handler the app registered itself)", label, method)
		}
		url := strings.TrimSpace(mapStr(m, "url"))
		if url == "" {
			// The overwhelmingly common copy button is a link to the run being
			// looked at, and making the author spell that out is a chance to
			// get it subtly wrong for no gain.
			if method != "copy" {
				return nil, fmt.Errorf("toolbar button %q needs a url — for method %q that is %s", label, method, appPipelineToolbarURLHint(method))
			}
			url = "?session={id}"
		}
		if err := appPipelineToolbarTarget(spec, label, method, url); err != nil {
			return nil, err
		}
		out = append(out, ui.PipelineAction{
			Label:   label,
			URL:     url,
			Method:  method,
			Title:   strings.TrimSpace(mapStr(m, "title")),
			Variant: strings.ToLower(strings.TrimSpace(mapStr(m, "variant"))),
			Confirm: strings.TrimSpace(mapStr(m, "confirm")),
		})
	}
	return out, nil
}

func appPipelineToolbarURLHint(method string) string {
	switch method {
	case "post":
		return "\"action/<one of this app's actions>\" (add {id} as a query param to tell the script which run was open)"
	case "client":
		return "the NAME of a handler registered with window.uiRegisterClientAction, not a path"
	default:
		return "the address to open — an absolute URL, or one relative to this app like \"data/<source>\""
	}
}

// appPipelineToolbarTarget refuses a button pointed at something this app does
// not have.
//
// action/ and data/ names are slugified on the way in, so an action declared as
// "Save Run" is reachable at action/save-run and nowhere else. Getting that
// wrong produces a 404 the author meets by clicking their own finished app,
// which is late; the spec already knows every script it declares, so the check
// is free here.
func appPipelineToolbarTarget(spec AppSpec, label, method, url string) error {
	if method == "client" {
		// Not a path: the url field carries the registered handler's name.
		if strings.ContainsAny(url, "/?#") {
			return fmt.Errorf("toolbar button %q is method \"client\", so its url must be the NAME of a handler registered with window.uiRegisterClientAction (e.g. \"print_transcript\"), not the path %q", label, url)
		}
		return nil
	}
	path := url
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	switch {
	case strings.HasPrefix(path, "action/"):
		name := strings.TrimPrefix(path, "action/")
		if !appHasNamed(appActionNames(spec), name) {
			return fmt.Errorf("toolbar button %q points at action/%s, which this app does not declare%s", label, name, appDeclaredList("actions", appActionNames(spec)))
		}
		if method != "post" {
			return fmt.Errorf("toolbar button %q opens action/%s with method %q, but an action endpoint answers POST only — use method:\"post\"", label, name, method)
		}
	case strings.HasPrefix(path, "data/"):
		name := strings.TrimPrefix(path, "data/")
		if !appHasNamed(appDataSourceNames(spec), name) {
			return fmt.Errorf("toolbar button %q points at data/%s, which this app does not declare%s", label, name, appDeclaredList("data_sources", appDataSourceNames(spec)))
		}
	}
	return nil
}

// appPipelinePrefill wires the Suggest button to one of the app's own data
// sources: the script prints a JSON array (a popover of choices) or an object
// with a topic/text/suggestion key (dropped straight into the field), and the
// panel does the rest. It is the declarative form of the hand-written suggest
// endpoints the compiled apps carry.
func appPipelinePrefill(spec AppSpec, m map[string]any, fields []ui.PipelineField, panel *ui.PipelinePanel) error {
	given := strings.TrimSpace(mapStr(m, "suggest_script"))
	if given == "" {
		// A label or a target with nothing behind it renders no button at all,
		// so say which key is missing rather than quietly dropping both.
		if strings.TrimSpace(mapStr(m, "suggest_label")) != "" || strings.TrimSpace(mapStr(m, "suggest_target")) != "" {
			return errors.New("this pipeline section sets suggest_label/suggest_target but no suggest_script — the Suggest button is the data source, so without one there is nothing to render")
		}
		return nil
	}
	name := slugify(given)
	if !appHasNamed(appDataSourceNames(spec), name) {
		return fmt.Errorf("suggest_script names %q, which this app does not declare%s", name, appDeclaredList("data_sources", appDataSourceNames(spec)))
	}
	target := strings.TrimSpace(mapStr(m, "suggest_target"))
	if target == "" && len(fields) > 0 {
		target = fields[0].Name
	}
	// A target that names no field is the silent failure this check exists for:
	// the button fetches, the script runs, and the value lands nowhere.
	if !appHasPipelineField(fields, target) {
		return fmt.Errorf("suggest_target names the field %q, which this section's form does not have%s", target, appDeclaredList("fields", appPipelineFieldNames(fields)))
	}
	panel.PrefillURL = "data/" + name
	panel.PrefillLabel = firstNonEmptyStr(strings.TrimSpace(mapStr(m, "suggest_label")), "Suggest")
	panel.PrefillTarget = target
	return nil
}

func appActionNames(spec AppSpec) []string {
	out := make([]string, 0, len(spec.Actions))
	for _, a := range spec.Actions {
		out = append(out, a.Name)
	}
	return out
}

func appDataSourceNames(spec AppSpec) []string {
	out := make([]string, 0, len(spec.DataSources))
	for _, d := range spec.DataSources {
		out = append(out, slugify(d.Name))
	}
	return out
}

func appPipelineFieldNames(fields []ui.PipelineField) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Name)
	}
	return out
}

func appHasNamed(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func appHasPipelineField(fields []ui.PipelineField, want string) bool {
	for _, f := range fields {
		if strings.EqualFold(f.Name, want) {
			return true
		}
	}
	return false
}

// appDeclaredList names what the app DOES have, so a typo is one read away
// from its fix instead of sending the author back to action=get.
func appDeclaredList(kind string, names []string) string {
	if len(names) == 0 {
		return " (it declares no " + kind + " at all)"
	}
	return " — its " + kind + ": " + strings.Join(names, ", ")
}

// appToolbarUsesClient reports whether any button in a toolbar dispatches to a
// browser-side handler, which is what makes the html-section ordering rule
// apply to this app (see appSectionNotes).
func appToolbarUsesClient(raw any) bool {
	arr, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			if strings.EqualFold(strings.TrimSpace(mapStr(m, "method")), "client") {
				return true
			}
		}
	}
	return false
}

// appPipelineMetaStyles is what a sidebar row can render a promoted field as.
var appPipelineMetaStyles = map[string]bool{"text": true, "badge": true, "pill": true}

// appPipelineMetaFields parses the pipeline section's `meta`: the extra values
// shown under each sidebar row's title, so a run history can be SCANNED for
// its answer instead of opened one run at a time.
//
// The values themselves come from the pipeline, not from here — a def promotes
// declared stage output fields with session_meta, and this only says how to
// draw them.
func appPipelineMetaFields(raw any) ([]ui.SessionMetaField, error) {
	if raw == nil {
		return nil, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("a pipeline section's meta must be an ARRAY, got %T — pass meta:[{\"field\":\"winner\", \"style\":\"pill\"}]", raw)
	}
	var out []ui.SessionMetaField
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("meta entry %d is not an object — each is {field, label?, style?, variants?, truncate?}", i+1)
		}
		field := strings.TrimSpace(firstNonEmptyStr(mapStr(m, "field"), mapStr(m, "name")))
		if field == "" {
			return nil, fmt.Errorf("meta entry %d needs a field (the name the pipeline promotes it under)", i+1)
		}
		style := strings.ToLower(strings.TrimSpace(mapStr(m, "style")))
		if style != "" && !appPipelineMetaStyles[style] {
			return nil, fmt.Errorf("meta entry %d (%s) has style %q — use \"text\" (a line under the title), \"badge\" (a small neutral pill) or \"pill\" (colored by value, see variants)", i+1, field, style)
		}
		smf := ui.SessionMetaField{
			Field:    field,
			Label:    strings.TrimSpace(mapStr(m, "label")),
			Style:    style,
			Truncate: intFromArgs(m, "truncate"),
		}
		// variants colors a pill per VALUE ("for" green, "against" red). Only
		// a pill reads them, so a variants map on a text row is a instruction
		// that quietly does nothing.
		if v, ok := m["variants"].(map[string]any); ok && len(v) > 0 {
			if style != "pill" {
				return nil, fmt.Errorf("meta entry %d (%s) sets variants but its style is %q — only a pill is colored by value", i+1, field, firstNonEmptyStr(style, "text"))
			}
			smf.Variants = map[string]string{}
			for key, val := range v {
				smf.Variants[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(fmt.Sprint(val))
			}
		}
		out = append(out, smf)
	}
	return out, nil
}

// appSessionMetaNotes reports meta fields the bound pipeline does not promote.
//
// A NOTE and not a refusal, deliberately: the app and the pipeline are edited
// separately, so a field that resolves today can stop resolving tomorrow when
// somebody trims the def, and a check here can never be the guarantee. What it
// can do is catch the common case — the name was guessed — at the moment the
// author is looking, and say which names would have worked.
func appSessionMetaNotes(raw any, promoted []string) []string {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	have := map[string]bool{}
	for _, ref := range promoted {
		if _, field, ok := strings.Cut(strings.TrimSpace(ref), "."); ok {
			have[field] = true
		}
	}
	var notes []string
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		m = normalizeSection(m)
		switch strings.ToLower(strings.TrimSpace(mapStr(m, "kind"))) {
		case "pipeline", "run":
		default:
			continue
		}
		fields, err := appPipelineMetaFields(m["meta"])
		if err != nil || len(fields) == 0 {
			continue
		}
		var missing []string
		for _, f := range fields {
			if !have[f.Field] {
				missing = append(missing, f.Field)
			}
		}
		if len(missing) == 0 {
			continue
		}
		if len(have) == 0 {
			notes = append(notes, fmt.Sprintf("section %d shows meta field(s) %s, but the bound pipeline promotes NOTHING onto its run rows — those rows will render blank. Add session_meta:[\"<stage>.<field>\"] to the pipeline (the field must be one the stage declares in its output).",
				i+1, strings.Join(missing, ", ")))
			continue
		}
		notes = append(notes, fmt.Sprintf("section %d shows meta field(s) %s, which the bound pipeline does not promote — it promotes: %s. Those rows will render blank.",
			i+1, strings.Join(missing, ", "), strings.Join(promotedFieldNames(promoted), ", ")))
	}
	return notes
}

func promotedFieldNames(promoted []string) []string {
	out := make([]string, 0, len(promoted))
	for _, ref := range promoted {
		if _, field, ok := strings.Cut(strings.TrimSpace(ref), "."); ok {
			out = append(out, field)
		}
	}
	return out
}

func appSelectOptions(raw any) []ui.SelectOption {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []ui.SelectOption
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		v := strings.TrimSpace(mapStr(m, "value"))
		if v == "" {
			continue
		}
		out = append(out, ui.SelectOption{Value: v, Label: firstNonEmptyStr(mapStr(m, "label"), v)})
	}
	return out
}

func appTableCols(raw any) []ui.Col {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []ui.Col
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// Same alias as appFormFields — one spelling, one meaning, everywhere.
		field := strings.TrimSpace(firstNonEmptyStr(mapStr(m, "field"), mapStr(m, "name")))
		if field == "" {
			continue
		}
		out = append(out, ui.Col{
			Field: field,
			Label: mapStr(m, "label"),
			Flex:  intFromArgs(m, "flex"),
			Mute:  boolArg(m, "mute"),
			Link:  strings.TrimSpace(mapStr(m, "link")),
		})
	}
	return out
}

// appRecordWriteInvalidations lists the data-source URLs a record write (a form
// save or an action) must refresh: the plain record lists ("records") PLUS every
// script-backed panel, because a data source computes its output FROM the stored
// records. Returning the data/<name> sources here is what connects a form/action
// (which changes records) to a source_script table/display (which renders a
// function of those records) — omit them and the computed panel silently goes
// stale after every save (the "set a city but the weather never updates" bug).
func appRecordWriteInvalidations(spec AppSpec) []string {
	out := []string{"records"}
	for _, ds := range spec.DataSources {
		out = append(out, "data/"+ds.Name)
	}
	return out
}

// appSectionSource resolves where a table/display reads its data: the generic
// record store ("records") by default, or a script-backed data source
// ("data/<name>") when the section names one via source_script.
func appSectionSource(m map[string]any) string {
	if name := slugify(mapStr(m, "source_script")); name != "" {
		return "data/" + name
	}
	return "records"
}

// appDataSources parses the declarative data_sources array into AppDataSource
// records. Each needs a name + script; language defaults to python at dispatch.
// notes reports back anything the author must know that the parse changed or
// dropped — a slugified name means every reference (source_script, an html
// section's fetch path) must use the NEW spelling, and a silently skipped entry
// reads as "saved" when it wasn't.
func appDataSources(raw any) (out []AppDataSource, notes []string) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, nil
	}
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			notes = append(notes, fmt.Sprintf("data_sources entry %d IGNORED — not an object", i+1))
			continue
		}
		given := strings.TrimSpace(mapStr(m, "name"))
		name := slugify(given)
		script := mapStr(m, "script")
		if name == "" || strings.TrimSpace(script) == "" {
			notes = append(notes, fmt.Sprintf("data_sources entry %d IGNORED — needs both a name and a script", i+1))
			continue
		}
		if name != given {
			notes = append(notes, fmt.Sprintf("data source %q is registered as %q (names are slugified: lowercase, non-alphanumerics → \"-\") — reference it by the slugified name in source_script and in any fetch of data/%s", given, name, name))
		}
		out = append(out, AppDataSource{
			Name:         name,
			Language:     strings.ToLower(strings.TrimSpace(mapStr(m, "language"))),
			Script:       script,
			Capabilities: appStringList(m["capabilities"]),
		})
	}
	return out, notes
}

// appActionDefs parses the declarative actions array into AppAction records.
// notes mirrors appDataSources: renames and dropped entries are reported, not
// swallowed.
func appActionDefs(raw any) (out []AppAction, notes []string) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, nil
	}
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			notes = append(notes, fmt.Sprintf("actions entry %d IGNORED — not an object", i+1))
			continue
		}
		given := strings.TrimSpace(mapStr(m, "name"))
		name := slugify(given)
		script := mapStr(m, "script")
		if name == "" || strings.TrimSpace(script) == "" {
			notes = append(notes, fmt.Sprintf("actions entry %d IGNORED — needs both a name and a script", i+1))
			continue
		}
		if name != given {
			notes = append(notes, fmt.Sprintf("action %q is registered as %q (names are slugified: lowercase, non-alphanumerics → \"-\") — its endpoint is action/%s", given, name, name))
		}
		act := AppAction{
			Name:         name,
			Label:        strings.TrimSpace(mapStr(m, "label")),
			Desc:         strings.TrimSpace(mapStr(m, "desc")),
			Language:     strings.ToLower(strings.TrimSpace(mapStr(m, "language"))),
			Script:       script,
			Capabilities: appStringList(m["capabilities"]),
			Confirm:      strings.TrimSpace(mapStr(m, "confirm")),
		}
		sch, snotes := appSchedule(m["schedule"], name)
		act.Schedule = sch
		notes = append(notes, snotes...)
		out = append(out, act)
	}
	return out, notes
}

// appSchedule parses an action's optional `schedule` object into an *AppSchedule
// (the self-update cadence). Returns nil when there's no schedule or it names no
// cadence. Notes report a floored interval, a cron/interval clash, or a schedule
// object that would do nothing — the same "report, don't swallow" contract the
// rest of app_def parsing follows.
func appSchedule(raw any, action string) (*AppSchedule, []string) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, nil
	}
	var notes []string
	sch := &AppSchedule{Cron: strings.TrimSpace(mapStr(m, "cron"))}
	if f, ok := floatVal(m["interval_seconds"]); ok && f > 0 {
		sch.IntervalSeconds = int(f)
	}
	if f, ok := floatVal(m["max_idle_days"]); ok && f > 0 {
		sch.MaxIdleDays = int(f)
	}
	// Cron and interval are mutually exclusive at the engine (cron wins); make the
	// stored spec unambiguous and say so.
	if sch.Cron != "" && sch.IntervalSeconds > 0 {
		notes = append(notes, fmt.Sprintf("action %q schedule sets both cron and interval_seconds — using cron, ignoring the interval", action))
		sch.IntervalSeconds = 0
	}
	if sch.IntervalSeconds > 0 && sch.IntervalSeconds < MinAppScheduleSeconds {
		notes = append(notes, fmt.Sprintf("action %q schedule interval_seconds %d is below the %d-second minimum for unattended updates — it will run every %d seconds", action, sch.IntervalSeconds, MinAppScheduleSeconds, MinAppScheduleSeconds))
		sch.IntervalSeconds = MinAppScheduleSeconds
	}
	if !sch.Scheduled() {
		notes = append(notes, fmt.Sprintf("action %q has a schedule object with no cron or interval_seconds — it will NOT self-update (add interval_seconds or cron)", action))
		return nil, notes
	}
	return sch, notes
}

// appStringList coerces a declarative value to []string: a JSON array of
// strings, or a single string. Empty entries are dropped.
func appStringList(raw any) []string {
	var out []string
	switch v := raw.(type) {
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

func appDisplayPairs(raw any) []ui.DisplayPair {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []ui.DisplayPair
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		field := strings.TrimSpace(mapStr(m, "field"))
		if field == "" {
			continue
		}
		out = append(out, ui.DisplayPair{Label: firstNonEmptyStr(mapStr(m, "label"), field), Field: field})
	}
	return out
}

// appChartSeries parses the declarative series array into ui.ChartSeries.
// Each item is {name?, points?:[numbers]} for bar/line/area, or
// {name?, value?:number} for a pie slice.
func appChartSeries(raw any) []ui.ChartSeries {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []ui.ChartSeries
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		s := ui.ChartSeries{
			Name:   strings.TrimSpace(mapStr(m, "name")),
			Points: appFloatList(m["points"]),
		}
		if v, ok := floatVal(m["value"]); ok {
			s.Value = &v
		}
		if len(s.Points) == 0 && s.Value == nil && s.Name == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// appChartOptions reads the flat chart tweaks off a section map (height /
// width / stacked / legend). Returns nil when none are set so the
// renderer's defaults apply.
func appChartOptions(m map[string]any) *ui.ChartOptions {
	opt := ui.ChartOptions{
		Height:  intFromArgs(m, "height"),
		Width:   intFromArgs(m, "width"),
		Stacked: boolArg(m, "stacked"),
	}
	if lv, ok := m["legend"].(bool); ok {
		opt.Legend = &lv
	}
	if opt.Height == 0 && opt.Width == 0 && !opt.Stacked && opt.Legend == nil {
		return nil
	}
	return &opt
}

// appChartLabels coerces a chart's labels array to []string, keeping
// index alignment with the series points. Unlike appStringList it does
// NOT drop non-strings: a numeric label (2020, from a JSON number) is
// stringified rather than silently dropped, which would otherwise leave
// the axis blank / renumbered 0,1,2. A bare comma-string list falls back
// to the shared string parser.
func appChartLabels(raw any) []string {
	arr, ok := raw.([]any)
	if !ok {
		return appStringList(raw)
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		out = append(out, labelString(e))
	}
	return out
}

// labelString renders one chart label value as display text: strings
// pass through, integer-valued numbers render without a trailing ".0"
// (2020, not 2020.0), other numbers use their shortest form, nil is "".
func labelString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	}
	if f, ok := floatVal(v); ok {
		if f == float64(int64(f)) {
			return strconv.FormatInt(int64(f), 10)
		}
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}

// appFloatList coerces a JSON array to []float64, keeping index
// alignment (a non-numeric entry becomes 0 so a series stays aligned
// with its labels).
func appFloatList(raw any) []float64 {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(arr))
	for _, e := range arr {
		f, _ := floatVal(e)
		out = append(out, f)
	}
	return out
}

// floatVal coerces the common JSON-decoded numeric shapes (and a
// stringified number) to float64.
func floatVal(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	}
	return 0, false
}

func (t *chatTurn) appDefList() (string, error) {
	specs := ListAppSpecs(t.user)
	if len(specs) == 0 {
		return "No apps yet. Author one with app_def(action=\"create\", name=…, sections=[…]).", nil
	}
	type row struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
		Desc string `json:"desc,omitempty"`
		URL  string `json:"url"`
	}
	out := make([]row, len(specs))
	for i, s := range specs {
		out[i] = row{Slug: s.Slug, Name: s.Name, Desc: s.Desc, URL: "/custom/" + s.Slug + "/"}
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func (t *chatTurn) appDefGet(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app — check the slug (app_def action=list)")
	}
	out := map[string]any{
		"slug":       spec.Slug,
		"name":       spec.Name,
		"desc":       spec.Desc,
		"record_key": spec.RecordKey,
		"agent_id":   spec.AgentID,
		// Echoed like agent_id so an author reading the app back can see what a
		// pipeline section is bound to — a get that omits a binding invites an
		// update that silently drops it.
		"pipeline_id": spec.PipelineID,
		"full_width":  spec.FullWidth,
		"url":         "/custom/" + spec.Slug + "/",
	}
	// Hand back the AUTHORING sections — the shape action=update accepts — not
	// the rendered page. Returning the page invited the obvious next move (feed
	// it back to update), which fails the section parser on every section, so
	// the author either gave up or re-wrote the app blind.
	switch {
	case len(spec.Sections) > 0:
		out["sections"] = json.RawMessage(spec.Sections)
	default:
		// Authored before sections were stored. Reconstruct from the rendered
		// page: exact where the authoring fields ARE the body's fields (an html
		// canvas, an empty state), best-effort otherwise. Say which, because a
		// best-effort section pasted into update would silently drop whatever
		// the reversal couldn't recover.
		secs, exact := authoringSectionsFromPage(spec.Page)
		if len(secs) == 0 {
			out["page"] = json.RawMessage(spec.Page)
			out["sections_note"] = "This app predates section storage and its page could not be reversed. `page` above is the RENDERED page — NOT valid input to action=update. Re-author the sections array from scratch (each section needs a `kind`); the next successful update stores it for real."
			break
		}
		out["sections"] = secs
		if exact {
			out["sections_note"] = "Reconstructed from the stored page (this app predates section storage) — lossless for these section kinds. Edit and pass back to action=update."
		} else {
			out["sections_note"] = "Reconstructed BEST-EFFORT from the stored page (this app predates section storage). Section kinds are right, but per-kind fields may be incomplete — check them against action=help before passing back to action=update, since update REPLACES the page with what you send."
		}
	}
	// Surface the logic seam so an update can inspect + revise it (scripts omitted
	// for size; names/caps/schedule are what you edit). schedule is the self-update
	// cadence — present here means the action fires unattended.
	if len(spec.DataSources) > 0 {
		ds := make([]map[string]any, len(spec.DataSources))
		for i, d := range spec.DataSources {
			ds[i] = map[string]any{"name": d.Name, "language": d.Language, "capabilities": d.Capabilities}
		}
		out["data_sources"] = ds
	}
	if len(spec.Actions) > 0 {
		acts := make([]map[string]any, len(spec.Actions))
		for i, a := range spec.Actions {
			m := map[string]any{"name": a.Name, "label": a.Label, "capabilities": a.Capabilities}
			if a.Confirm != "" {
				m["confirm"] = a.Confirm
			}
			if a.Schedule.Scheduled() {
				m["schedule"] = a.Schedule
			}
			acts[i] = m
		}
		out["actions"] = acts
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// isFullHTMLDocument reports whether a blob is a whole page rather than a
// fragment — it opens with a doctype or an <html> tag, or carries its own
// <body>. Whole documents render in their own frame (see the html section);
// fragments splice into the page.
func isFullHTMLDocument(html string) bool {
	head := strings.ToLower(strings.TrimSpace(html))
	if len(head) > 2048 {
		head = head[:2048]
	}
	return strings.HasPrefix(head, "<!doctype") || strings.HasPrefix(head, "<html") || strings.Contains(head, "<body")
}

// bodyTypeToKind maps a RENDERED section body's component type back to the
// authoring section kind that produces it. Reverse of buildAppSection's switch.
var bodyTypeToKind = map[string]string{
	"card":             "html",
	"frame":            "html",
	"empty_state":      "empty",
	"form_panel":       "form",
	"modal_button":     "form",
	"table":            "table",
	"display_panel":    "display",
	"chart_panel":      "chart",
	"chat_panel":       "chat",
	"agent_loop_panel": "chat",
	"workbench_panel":  "workbench",
	"action_list":      "actions",
}

// exactReverseKinds are the kinds whose authoring fields ARE the rendered
// body's fields, so reversing loses nothing: an html canvas is its html, an
// empty state its icon/title/hint. Everything else reverses best-effort —
// the kind is certain, individual fields may not survive.
var exactReverseKinds = map[string]bool{"html": true, "empty": true}

// authoringSectionsFromPage turns a stored pageConfig back into the authoring
// sections array, for specs written before AppSpec.Sections existed. Returns
// the sections and whether every one of them reversed exactly. Callers must
// surface the exactness — update REPLACES the page with what it is handed, so
// a lossy reversal fed back would quietly drop fields.
func authoringSectionsFromPage(page json.RawMessage) ([]map[string]any, bool) {
	if len(page) == 0 {
		return nil, false
	}
	var pc struct {
		Sections []struct {
			Title    string         `json:"title"`
			Subtitle string         `json:"subtitle"`
			Body     map[string]any `json:"body"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(page, &pc); err != nil || len(pc.Sections) == 0 {
		return nil, false
	}
	// Plumbing the framework wires itself (endpoints, cache invalidation). It is
	// not authoring input and re-emitting it only invites confusion.
	plumbing := map[string]bool{"type": true, "source": true, "post_url": true, "invalidate": true}
	out := make([]map[string]any, 0, len(pc.Sections))
	exact := true
	for _, s := range pc.Sections {
		body := s.Body
		kind, ok := bodyTypeToKind[strings.TrimSpace(mapStr(body, "type"))]
		if !ok {
			return nil, false
		}
		sec := map[string]any{"kind": kind}
		if s.Title != "" {
			sec["title"] = s.Title
		}
		if s.Subtitle != "" {
			sec["subtitle"] = s.Subtitle
		}
		// A modal form renders as a ModalButton wrapping the form; the section's
		// own chrome moved onto the modal, so read the title back from there.
		if mapStr(body, "type") == "modal_button" {
			sec["modal"] = true
			if lbl := mapStr(body, "label"); lbl != "" {
				sec["submit_label"] = lbl
			}
			if ttl := mapStr(body, "title"); ttl != "" {
				sec["title"] = ttl
			}
			inner, _ := body["body"].(map[string]any)
			body = inner
		}
		for k, v := range body {
			if plumbing[k] || k == "" {
				continue
			}
			if _, taken := sec[k]; taken {
				continue
			}
			sec[k] = v
		}
		// A script-backed panel points at "data/<name>"; authoring names the
		// data source directly.
		if src := mapStr(body, "source"); strings.HasPrefix(src, "data/") {
			sec["source_script"] = strings.TrimPrefix(src, "data/")
		}
		if !exactReverseKinds[kind] {
			exact = false
		}
		out = append(out, sec)
	}
	return out, exact
}

func (t *chatTurn) appDefDelete(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app to delete")
	}
	DeleteAppSpec(t.user, spec.Slug)
	return fmt.Sprintf("Deleted app %q (/custom/%s/).", spec.Name, spec.Slug), nil
}

// appDefTest executes every script-backed component of an app — each data source
// and each action — through the SAME runner the host uses at request time
// (appscript.Run), and reports per component: did it run, did it print the JSON
// shape its section expects, and the captured output/traceback when it didn't.
// This is the authoring-time feedback loop that catches script bugs (e.g.
// json.loads("records") instead of json.loads(os.environ['records'])) before the
// user ever opens the app.
func (t *chatTurn) appDefTest(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app to test — check the slug (app_def action=list)")
	}
	if len(spec.DataSources) == 0 && len(spec.Actions) == 0 {
		return fmt.Sprintf("App %q has no script-backed components (data_sources or actions) to test — a plain form/table app uses the built-in record store and needs no script test.", spec.Name), nil
	}
	// Optional example form data: run the chain against THESE records instead of
	// the (often empty) live store, so the full form→record→data-source→output
	// path is exercised with realistic input. `sample` is an array of objects
	// keyed by the form's field names; `params` simulates query-param inputs.
	sample := appSampleRecords(args["sample"])
	params := mapArg(args["params"])
	report, records, pass, fail := t.checkScripts(spec, true, sample, params)
	src := "stored"
	if sample != nil {
		src = "sample"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Tested app %q with %d %s record(s).\n\n%s\n%d passed, %d failed.", spec.Name, records, src, report, pass, fail)
	if sample == nil && records == 0 {
		b.WriteString("\nNote: the store is empty, so data sources only saw []. Pass sample=[{...}] (example form submissions) to test the full form→data-source→output chain with real input.")
	}
	if fail > 0 {
		b.WriteString(" Fix the failing scripts with app_def action=update, then test again before telling the user the app is ready.")
	}
	return b.String(), nil
}

// appDefVerify is the start-to-finish gate an app must pass before
// Builder may call it done. Two halves: the script checks action=test
// runs (same engine), PLUS a real headless-browser load of the app's
// page as this user — JavaScript executed, sections mounted, data
// sources fetched live over HTTP. The browser half catches what a
// script run can't see: a section wired to a missing source, runtime JS
// errors, a data endpoint that 500s when served, a page that renders
// blank.
func (t *chatTurn) appDefVerify(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app to verify — check the slug (app_def action=list)")
	}
	var b strings.Builder
	failures := 0
	// The revision stamp ties this report to ONE saved spec: a verify
	// issued alongside an update in the same round checks the OLD
	// revision, and without the stamp its findings read as if the fix
	// never landed.
	fmt.Fprintf(&b, "Verified app %q end-to-end (spec revision saved %s — if you updated the app AFTER that, this report describes the OLD revision; verify again).\n\n", spec.Name, spec.Updated)

	// A bound pipeline that does not resolve. The page renders fine without it
	// — the panel does not touch the pipeline until someone presses Start — so
	// every other check passes and the app is declared ready. The first person
	// to use it gets the failure instead, which is the wrong order.
	//
	// This is not hypothetical: an app was created with pipeline_id naming a
	// pipeline that did not exist yet, verified PASS, and only worked because
	// the pipeline happened to be authored a minute later under that same name.
	if ref := strings.TrimSpace(spec.PipelineID); ref != "" {
		if def, ok := t.app.LookupAppPipeline(t.user, ref); !ok {
			failures++
			fmt.Fprintf(&b, "FAIL binding — pipeline_id %q resolves to nothing. The page will render and Start will fail; author the pipeline first (pipeline action=list shows yours), then update the app.\n\n", ref)
		} else {
			fmt.Fprintf(&b, "OK   binding — pipeline_id resolves to %q (%d stage(s)).\n\n", def.Name, len(def.Stages))
		}
	}

	if len(spec.DataSources) > 0 || len(spec.Actions) > 0 {
		report, _, _, fail := t.checkScripts(spec, true, appSampleRecords(args["sample"]), mapArg(args["params"]))
		failures += fail
		fmt.Fprintf(&b, "Script checks:\n%s\n", strings.TrimSpace(report))
	}

	// A browser load is a weak witness for an html app: a canvas game runs
	// almost nothing until the player interacts, so a page missing half its
	// functions loads silently clean and verify would sign off on it. Read the
	// code statically first — calls to names the document never defines are the
	// signature of a rewrite that dropped something.
	if html := appSpecHTMLText(spec); html != "" {
		if dangling := jsDanglingCalls(html); len(dangling) > 0 {
			failures++
			fmt.Fprintf(&b, "Code check:\nFAIL the page calls code it never defines: %s\nThese parse fine and the page below may well load clean — the failure happens when someone actually USES the app. Restore the missing functions (app_def action=\"replace_function\") or drop the calls.\n\n", appNameList(dangling, 12))
		} else {
			b.WriteString("Code check: OK — every function the page calls is defined somewhere in it.\n\n")
		}
	}

	// The DOM probe counts what the runtime actually mounted. Empty-state
	// texts ride along as information — an empty table can be a fresh
	// store (fine) or a data source printing [] (test's WARN covers that).
	// Content lives in two places the naive innerText count misses: inside a
	// framed document (an html section holding a whole page), and on a canvas
	// (a game or animation renders pixels, not text). Counting only top-level
	// text fails a working app for having nothing to say.
	probe := `() => {
		var txt = (document.body && document.body.innerText || '');
		var visuals = document.querySelectorAll('canvas, svg, img, video').length;
		var frames = document.querySelectorAll('iframe');
		Array.prototype.forEach.call(frames, function(f) {
			try {
				var d = f.contentDocument;
				if (!d) return;
				txt += ' ' + (d.body && d.body.innerText || '');
				visuals += d.querySelectorAll('canvas, svg, img, video').length;
			} catch (e) {}
		});
		return JSON.stringify({
			sections: document.querySelectorAll('.ui-section,[data-ui-section]').length,
			panels: document.querySelectorAll('.ui-pl,.ui-chat,.ui-wb,.ui-cw').length,
			tables: document.querySelectorAll('.ui-table-list').length,
			empty_texts: Array.prototype.slice.call(document.querySelectorAll('.ui-table-empty'), 0, 8).map(function(e){ return e.textContent.trim(); }),
			body_chars: txt.length,
			visuals: visuals,
			frames: frames.length
		});
	}`
	rep, err := CheckPageAsUser(RootDB, t.user, "/custom/"+spec.Slug+"/", probe)
	if err != nil {
		failures++
		fmt.Fprintf(&b, "Page check: COULD NOT RUN — %v\n", err)
	} else {
		b.WriteString("Page check (headless browser, JS executed):\n")
		for _, e := range rep.PageErrors {
			failures++
			fmt.Fprintf(&b, "FAIL uncaught JS exception — %s\n", e)
		}
		for _, e := range rep.ConsoleErrors {
			failures++
			fmt.Fprintf(&b, "FAIL console error — %s\n", e)
		}
		for _, e := range rep.FailedRequests {
			// A missing favicon is browser noise, not an app defect.
			if strings.Contains(e, "/favicon.ico") {
				continue
			}
			failures++
			fmt.Fprintf(&b, "FAIL request — %s\n", e)
		}
		// Positive per-data-source confirmation: the page must have
		// actually FETCHED each source's live endpoint and gotten a
		// good status. A source that was never requested means no
		// section references it (source_script) — the "script works
		// but the page never calls it" disconnect the script checks
		// can't see.
		for _, ds := range spec.DataSources {
			endpoint := "/custom/" + spec.Slug + "/data/" + ds.Name
			status := 0
			for _, req := range rep.Requests {
				if pathOfURL(req.URL) == endpoint {
					status = req.Status
					break
				}
			}
			requested := status != 0
			if !requested {
				for _, u := range rep.PendingRequests {
					if pathOfURL(u) == endpoint {
						requested = true
						break
					}
				}
			}
			switch {
			case status == 0 && requested:
				// The wiring is proven (the page called the endpoint);
				// the script just didn't answer inside the check window.
				// A latency problem, not a structure problem — warn, but
				// don't send the author chasing section config.
				fmt.Fprintf(&b, "WARN data source %q — the page DID request %s but the response had not arrived when the check ended. The wiring is correct; the SCRIPT IS SLOW (a script that makes many sequential fetch_url calls takes that long on every page load). Reduce the calls or accept slow loads — do NOT change the section wiring.\n", ds.Name, endpoint)
			case status == 0:
				failures++
				fmt.Fprintf(&b, "FAIL data source %q — the page NEVER fetched %s; no section is wired to it. Set source_script:%q on the table/display that should render it, or — from an html section's script — call fetch(%q) (plain relative fetch; there is no client-side gohort object in app pages).\n", ds.Name, endpoint, ds.Name, "data/"+ds.Name)
			case status >= 400:
				// Already counted via FailedRequests above; this line
				// just names the source for the fix.
				fmt.Fprintf(&b, "     ^ that failing request is data source %q.\n", ds.Name)
			default:
				fmt.Fprintf(&b, "OK   data source %q — page fetched %s live (HTTP %d).\n", ds.Name, endpoint, status)
			}
		}
		var pr struct {
			Sections   int      `json:"sections"`
			Panels     int      `json:"panels"`
			Tables     int      `json:"tables"`
			EmptyTexts []string `json:"empty_texts"`
			BodyChars  int      `json:"body_chars"`
			Visuals    int      `json:"visuals"`
			Frames     int      `json:"frames"`
		}
		if rep.ProbeJSON != "" && json.Unmarshal([]byte(rep.ProbeJSON), &pr) == nil {
			expected := countSpecSections(spec)
			switch {
			case pr.Sections == 0:
				failures++
				b.WriteString("FAIL render — no sections mounted; the page is blank.\n")
			case expected > 0 && pr.Sections < expected:
				failures++
				fmt.Fprintf(&b, "FAIL render — only %d of %d sections mounted; a section config is likely invalid.\n", pr.Sections, expected)
			default:
				fmt.Fprintf(&b, "OK   render — %d section(s) mounted (%d table(s)).\n", pr.Sections, pr.Tables)
			}
			// A canvas app draws instead of writing, so visuals count as
			// content — and a LIVE panel (chat, pipeline, workbench) is mostly
			// empty until someone types in it, which is the correct look for a
			// fresh one, not a broken page. Only a page with none of the three
			// is actually blank.
			//
			// This used to fail an app whose one table was legitimately showing
			// its empty state, two lines above a NOTE saying exactly that. A
			// verdict that contradicts its own evidence gets believed anyway:
			// the reported fix is to rebuild the page, and rebuilding a page
			// that was never broken is how a working app becomes a hand-rolled
			// one.
			if pr.BodyChars < 40 && pr.Visuals == 0 && pr.Panels == 0 && len(pr.EmptyTexts) == 0 {
				failures++
				fmt.Fprintf(&b, "FAIL render — page body is nearly empty (%d chars of text, nothing drawn).\n", pr.BodyChars)
			}
			if pr.Panels > 0 {
				fmt.Fprintf(&b, "OK   %d live panel(s) mounted (chat / pipeline / workbench) — these look empty until a run or a message starts, which is correct for a fresh one.\n", pr.Panels)
			}
			if pr.Frames > 0 {
				fmt.Fprintf(&b, "OK   %d framed document(s) rendered (an html section holding a complete page gets its own frame).\n", pr.Frames)
			}
			for _, txt := range pr.EmptyTexts {
				fmt.Fprintf(&b, "NOTE a table is showing its empty state: %q — fine for a fresh store; a problem if records/data should exist.\n", txt)
			}
		} else {
			failures++
			b.WriteString("FAIL render — the DOM probe returned nothing; the page runtime likely never booted.\n")
		}
	}

	b.WriteString("\n" + t.appInventoryLine(spec) + "\n")

	if failures > 0 {
		fmt.Fprintf(&b, "\nVERDICT: FAIL — %d problem(s) above. Fix with app_def action=update and run verify again. Do NOT tell the user the app is ready.", failures)
	} else {
		b.WriteString("\nVERDICT: PASS — scripts run clean and the page renders in a real browser with no JS errors or failed fetches. Safe to tell the user it's ready.")
	}
	return b.String(), nil
}

// pathOfURL reduces a full URL to its path — scheme/host stripped,
// query and fragment dropped — for endpoint matching against the
// browser's request log.
func pathOfURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Path
}

// countSpecSections reads the section count out of the stored pageConfig
// JSON; -1 when the page bytes don't parse (never a verify failure by
// itself — the browser probe judges the rendered result).
func countSpecSections(spec AppSpec) int {
	var pg struct {
		Sections []json.RawMessage `json:"sections"`
	}
	if json.Unmarshal(spec.Page, &pg) != nil {
		return -1
	}
	return len(pg.Sections)
}

// mapArg coerces an arg to a map[string]any (the test action's `params`),
// returning nil when it isn't an object.
func mapArg(raw any) map[string]any {
	if m, ok := raw.(map[string]any); ok && len(m) > 0 {
		return m
	}
	return nil
}

// appSampleRecords parses the test action's `sample` argument — an array of
// example form submissions (objects keyed by form field name) — into records to
// stand in for the live store. Returns nil when absent so checkScripts falls back
// to the stored records.
func appSampleRecords(raw any) []map[string]any {
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// checkScripts executes an app's script-backed components through the SAME runner
// the host uses at request time (appscript.Run) and returns a per-component
// report plus the stored-record count and pass/fail tallies. Each script gets the
// app's real stored records as the `records` env var but NO query params — which
// is exactly the state a table/display data source loads in when the page first
// opens, so a script that crashes on a missing param surfaces here.
//
// includeActions gates the WRITE side: data sources are read-only and are what
// the page fires on load, so they are always safe to run; actions can carry a
// fetch capability that hits an external API, so they run only on an explicit
// action=test, never as part of the automatic create/update check.
//
// sample, when non-nil, stands in for the app's stored records — letting the
// author exercise the full form→record→data-source→output chain with EXAMPLE
// form submissions before any real data exists (a fresh app's store is empty, so
// without this every data source just sees []). params are extra env vars handed
// to each script, simulating query-param inputs for filter-style sources.
func (t *chatTurn) checkScripts(spec AppSpec, includeActions bool, sample []map[string]any, params map[string]any) (report string, records, pass, fail int) {
	db := UserDB(RootDB, t.user)
	recs := sample
	if recs == nil {
		recs = []map[string]any{}
		if db != nil {
			tbl := "custom_records:" + spec.Slug
			for _, k := range db.Keys(tbl) {
				var rec map[string]any
				if db.Get(tbl, k, &rec) {
					recs = append(recs, rec)
				}
			}
		}
	}
	recJSON, _ := json.Marshal(recs)

	var b strings.Builder
	run := func(kind, name, lang, script string, caps []string) {
		label := fmt.Sprintf("%s %q", kind, name)
		scriptArgs := map[string]any{"records": string(recJSON)}
		for k, v := range params {
			scriptArgs[k] = fmt.Sprint(v)
		}
		out, err := appscript.Run(t.user, db, spec.Slug, kind, name, lang, script, caps, scriptArgs)
		if err != nil {
			fail++
			fmt.Fprintf(&b, "FAIL %s — could not run: %v\n", label, err)
			return
		}
		trimmed := strings.TrimSpace(out)
		if trimmed == "" {
			if kind == "action" { // an action may legitimately print nothing
				pass++
				fmt.Fprintf(&b, "OK   %s — ran, printed nothing (no message/records).\n", label)
				return
			}
			fail++
			fmt.Fprintf(&b, "FAIL %s — printed nothing; a data source must print JSON to stdout.\n", label)
			return
		}
		if !json.Valid([]byte(trimmed)) {
			fail++
			fmt.Fprintf(&b, "FAIL %s — did not print valid JSON. Output:\n%s\n", label, truncate(trimmed, 800))
			if hint := scriptFailureHint(trimmed); hint != "" {
				fmt.Fprintf(&b, "     %s\n", hint)
			}
			if strings.Contains(trimmed, `json.loads("records")`) || strings.Contains(trimmed, "json.loads('records')") {
				b.WriteString("     Hint: read records with json.loads(os.environ.get('records', '[]')) — json.loads(\"records\") parses the literal word, not the data.\n")
			} else if strings.Contains(trimmed, "KeyError") || strings.Contains(trimmed, "os.environ[") {
				b.WriteString("     Hint: a data source runs on page load with NO query params set — read every env var with a default, e.g. os.environ.get('city', ''), never os.environ['city'].\n")
			}
			return
		}
		var v any
		_ = json.Unmarshal([]byte(trimmed), &v)
		switch kind {
		case "data":
			if arr, isArr := v.([]any); isArr {
				pass++
				if len(arr) == 0 && len(recs) > 0 {
					// Valid JSON, but empty output while the app HAS records is the
					// signature of a script that reads a query param nothing supplies
					// (os.environ.get('city')) instead of pulling the saved entries
					// from the records env var — the "added a location, no forecast"
					// disconnect. Pass (it's valid) but flag it loudly.
					fmt.Fprintf(&b, "WARN %s — printed an EMPTY array though the app has %d saved record(s). The script is probably reading a query param (e.g. os.environ.get('city')) that is never set; read the saved entries from the `records` env var instead, e.g. recs = json.loads(os.environ.get('records','[]')).\n", label, len(recs))
				} else {
					fmt.Fprintf(&b, "OK   %s — printed a JSON array (%d item(s)); good for a table.\n", label, len(arr))
				}
			} else {
				pass++
				fmt.Fprintf(&b, "OK   %s — printed a JSON object; good for a display (a table section needs a JSON array).\n", label)
			}
		case "action":
			if _, isObj := v.(map[string]any); isObj {
				pass++
				fmt.Fprintf(&b, "OK   %s — printed a JSON object {message?, records?}.\n", label)
			} else {
				fail++
				fmt.Fprintf(&b, "FAIL %s — an action must print a JSON OBJECT {message?, records?}, got %T.\n", label, v)
			}
		}
	}

	for _, ds := range spec.DataSources {
		run("data", ds.Name, ds.Language, ds.Script, ds.Capabilities)
	}
	if includeActions {
		for _, act := range spec.Actions {
			run("action", act.Name, act.Language, act.Script, act.Capabilities)
		}
	}
	return b.String(), len(recs), pass, fail
}

// boolArg coerces a section-map field to bool: native bool, or the strings
// "true"/"1"/"yes" (LLMs sometimes stringify booleans).
func boolArg(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

// firstNonEmptyStr returns the first trimmed-non-empty argument, or "".
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// intsToStrings renders section positions for a note. Sections are addressed
// by their 1-based position everywhere an author reads about them, so the
// notes have to agree with the array they were written from.
func intsToStrings(in []int) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, strconv.Itoa(v))
	}
	return out
}
