package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
)

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

func (t *chatTurn) appDefDelete(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app to delete")
	}
	DeleteAppSpec(t.user, spec.Slug)
	return fmt.Sprintf("Deleted app %q (/apps/%s/).", spec.Name, spec.Slug), nil
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
	rep, err := CheckPageAsUser(RootDB, t.user, "/apps/"+spec.Slug+"/", probe)
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
			endpoint := "/apps/" + spec.Slug + "/data/" + ds.Name
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
