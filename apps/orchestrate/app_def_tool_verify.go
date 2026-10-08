package orchestrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/buildledger"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// gohortScriptHelpers is everything the sandbox's gohort module actually
// exports. Kept beside the hint because the point of the hint is this list.
var gohortScriptHelpers = []string{"fetch_url", "fetch", "fetch_via", "browse_page", "log", "secret", "call_tool", "HookError"}

// uncalledPythonFunctions lists the functions a python script defines at its
// top level and never calls. A build wrapped its whole data source in
// def current_weather_source_script(): and never called it, so the script
// ran in 80ms and printed nothing; five rounds blamed escaping, flushing and
// finally the platform, and gave up. Python runs only a file's top level.
func uncalledPythonFunctions(script string) []string {
	var defs []string
	for _, line := range strings.Split(script, "\n") {
		if rest, ok := strings.CutPrefix(line, "def "); ok {
			if i := strings.Index(rest, "("); i > 0 {
				defs = append(defs, strings.TrimSpace(rest[:i]))
			}
		}
	}
	var out []string
	for _, name := range defs {
		if strings.Count(script, name+"(") <= 1 {
			out = append(out, name)
		}
	}
	return out
}

func uncalledHint(names []string) string {
	return fmt.Sprintf("Hint: the script defines %s() but never calls it, and python runs only a file's top level, so nothing in it ran. Call it at the bottom of the script: %s()", strings.Join(names, "(), "), names[0])
}

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
	msg := "HINT: the gohort module exports only " + strings.Join(gohortScriptHelpers, ", ") + ", that is the network/secret channel, NOT the tool catalog."
	call := "the tool"
	if name != "" {
		msg += " " + strconv.Quote(name) + " is a gohort TOOL, and a tool cannot be imported or subprocessed from a script."
		call = strconv.Quote(name)
	}
	return msg + " To reuse one of the owner's tools, CALL it: add \"tool:<name>\" to the script's capabilities and run out = call_tool(" + call + ", param=value) (from gohort import call_tool), which returns the tool's output as text; only the owner's own tools or ones added from the catalog, and only ones that never ask before running. Otherwise a script does its own work in plain Python (with fetch_url for anything off-box), and a job for a tool that asks first belongs in a pipeline tool stage."
}

func (t *chatTurn) appDefDelete(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", appNotFound(args, "to delete")
	}
	DeleteAppSpec(t.user, spec.Slug)
	t.forgetAppStanding(spec.Slug)
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
		return "", appNotFound(args, "to test")
	}
	if len(spec.DataSources) == 0 && len(spec.Actions) == 0 {
		return fmt.Sprintf("App %q has no script-backed components (data_sources or actions) to test: a plain form/table app uses the built-in record store and needs no script test.", spec.Name), nil
	}
	// Optional example form data: run the chain against THESE records instead of
	// the (often empty) live store, so the full form→record→data-source→output
	// path is exercised with realistic input. `sample` is an array of objects
	// keyed by the form's field names; `params` simulates query-param inputs.
	sample, err := appSampleRecords(args["sample"])
	if err != nil {
		return "", err
	}
	params := mapArg(args["params"])
	src := "stored"
	if sample != nil {
		// Keep it: the next test, the next verify and every save's auto-check
		// run against the same fixture instead of a fresh invention.
		spec.RecordSample(sample)
		spec.Sample = sample
		src = "sample"
	} else if len(spec.Sample) > 0 && len(appStoredRecords(t.user, spec)) == 0 {
		src = "retained sample"
	}
	report, records, pass, fail := t.runScriptChecks(spec, appScriptRun{includeActions: true, sample: sample, params: params, preview: testOutputPreview})
	var b strings.Builder
	fmt.Fprintf(&b, "Tested app %q with %d %s record(s).\n\n%s\n%d passed, %d failed.", spec.Name, records, src, report, pass, fail)
	for _, w := range appSampleFieldWarnings(spec.RecordFields, sample) {
		b.WriteString("\n" + w)
	}
	if sample == nil && records == 0 {
		b.WriteString("\nNote: the store is empty, so data sources only saw []. Pass sample=[{...}] (example form submissions) to test the full form→data-source→output chain with real input; it is kept on the app for later runs.")
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
		return "", appNotFound(args, "to verify")
	}
	var b strings.Builder
	failures := 0
	// What kind of failure each one was, for the build ledger.
	var classes []string
	// The revision stamp ties this report to ONE saved spec: a verify
	// issued alongside an update in the same round checks the OLD
	// revision, and without the stamp its findings read as if the fix
	// never landed.
	fmt.Fprintf(&b, "Verified app %q end-to-end (spec revision saved %s: if you updated the app AFTER that, this report describes the OLD revision; verify again).\n\n", spec.Name, spec.Updated)

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
			classes = append(classes, "pipeline-unbound")
			fmt.Fprintf(&b, "FAIL binding: pipeline_id %q resolves to nothing. The page will render and Start will fail; author the pipeline first (pipeline action=list shows yours), then update the app.\n\n", ref)
		} else {
			fmt.Fprintf(&b, "OK   binding: pipeline_id resolves to %q (%d stage(s)).\n\n", def.Name, len(def.Stages))
		}
	}

	if len(spec.DataSources) > 0 || len(spec.Actions) > 0 {
		sample, err := appSampleRecords(args["sample"])
		if err != nil {
			return "", err
		}
		if sample != nil {
			spec.RecordSample(sample)
			spec.Sample = sample
		}
		report, _, _, fail := t.runScriptChecks(spec, appScriptRun{includeActions: true, sample: sample, params: mapArg(args["params"]), preview: verifyOutputPreview})
		failures += fail
		if fail > 0 {
			classes = append(classes, "script-fail")
		}
		fmt.Fprintf(&b, "Script checks:\n%s\n", strings.TrimSpace(report))
		for _, w := range appSampleFieldWarnings(spec.RecordFields, sample) {
			b.WriteString(w + "\n")
		}
	}

	// A browser load is a weak witness for an html app: a canvas game runs
	// almost nothing until the player interacts, so a page missing half its
	// functions loads silently clean and verify would sign off on it. Read the
	// code statically first — calls to names the document never defines are the
	// signature of a rewrite that dropped something.
	if html := appSpecHTMLText(spec); html != "" {
		if dangling := jsDanglingCalls(html); len(dangling) > 0 {
			failures++
			classes = append(classes, "dangling-call")
			fmt.Fprintf(&b, "Code check:\nFAIL the page calls code it never defines: %s\nThese parse fine and the page below may well load clean, the failure happens when someone actually USES the app. Restore the missing functions (app_def action=\"replace_function\") or drop the calls.\n\n", appNameList(dangling, 12))
		} else {
			b.WriteString("Code check: OK, every function the page calls is defined somewhere in it.\n\n")
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
	pageCheckBroke := false
	if err != nil {
		failures++
		pageCheckBroke = true
		fmt.Fprintf(&b, "Page check: COULD NOT RUN, %v\n", err)
	} else {
		b.WriteString("Page check (headless browser, JS executed):\n")
		for _, e := range rep.PageErrors {
			failures++
			classes = append(classes, "js-exception")
			fmt.Fprintf(&b, "FAIL uncaught JS exception: %s\n", e)
		}
		for _, e := range rep.ConsoleErrors {
			failures++
			classes = append(classes, "console-error")
			fmt.Fprintf(&b, "FAIL console error: %s\n", e)
		}
		for _, e := range rep.FailedRequests {
			// A missing favicon is browser noise, not an app defect.
			if strings.Contains(e, "/favicon.ico") {
				continue
			}
			failures++
			classes = append(classes, "failed-request")
			fmt.Fprintf(&b, "FAIL request: %s\n", e)
		}
		n, cls := appVerifyDataSources(&b, spec, rep)
		failures += n
		classes = append(classes, cls...)
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
				classes = append(classes, "render-blank")
				b.WriteString("FAIL render: no sections mounted; the page is blank.\n")
			case expected > 0 && pr.Sections < expected:
				failures++
				classes = append(classes, "render-partial")
				fmt.Fprintf(&b, "FAIL render: only %d of %d sections mounted; a section config is likely invalid.\n", pr.Sections, expected)
			default:
				fmt.Fprintf(&b, "OK   render: %d section(s) mounted (%d table(s)).\n", pr.Sections, pr.Tables)
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
				classes = append(classes, "render-empty")
				fmt.Fprintf(&b, "FAIL render: page body is nearly empty (%d chars of text, nothing drawn).\n", pr.BodyChars)
			}
			if pr.Panels > 0 {
				fmt.Fprintf(&b, "OK   %d live panel(s) mounted (chat / pipeline / workbench): these look empty until a run or a message starts, which is correct for a fresh one.\n", pr.Panels)
			}
			if pr.Frames > 0 {
				fmt.Fprintf(&b, "OK   %d framed document(s) rendered (an html section holding a complete page gets its own frame).\n", pr.Frames)
			}
			for _, txt := range pr.EmptyTexts {
				fmt.Fprintf(&b, "NOTE a table is showing its empty state: %q, fine for a fresh store; a problem if records/data should exist.\n", txt)
			}
		} else {
			failures++
			classes = append(classes, "runtime-not-booted")
			b.WriteString("FAIL render: the DOM probe returned nothing; the page runtime likely never booted.\n")
		}
	}

	b.WriteString("\n" + t.appInventoryLine(spec) + "\n")

	// Pin the verdict to the revision it checked. Before this the report's
	// own caveat ("if you updated after this…") was the only record, and it
	// lived in a tool result the next session never sees.
	summary := "PASS"
	if failures > 0 {
		summary = fmt.Sprintf("FAIL: %d problem(s)", failures)
	}
	spec.RecordVerify(failures == 0, summary)
	t.recordAppVerify(spec.Slug, failures, classes, pageCheckBroke)
	if failures > 0 {
		t.noteAppStanding(spec.Slug, false, "its last verify failed ("+summary+")")
	} else {
		t.noteAppStanding(spec.Slug, true, "")
	}
	if failures > 0 {
		fmt.Fprintf(&b, "\nVERDICT: FAIL, %d problem(s) above. Fix with app_def action=update and run verify again. Do NOT tell the user the app is ready.", failures)
	} else {
		b.WriteString("\nVERDICT: PASS, scripts run clean and the page renders in a real browser with no JS errors or failed fetches. Safe to tell the user it's ready.")
	}
	b.WriteString(" (Recorded against revision " + spec.Updated + "; any later edit makes it stale.)")
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

// appVerifyDataSources is the positive per-data-source confirmation: the page
// must have actually FETCHED each source's live endpoint and gotten a good
// status. A source that was never requested means no section references it
// (source_script), the "script works but the page never calls it" disconnect
// the script checks can't see. Returns the failures it found and their classes.
func appVerifyDataSources(b *strings.Builder, spec AppSpec, rep *PageCheckReport) (failures int, classes []string) {
	var html string
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
		if status == 0 && !requested && html == "" {
			html = appSpecHTMLText(spec)
		}
		switch {
		case status == 0 && requested:
			// The wiring is proven (the page called the endpoint);
			// the script just didn't answer inside the check window.
			// A latency problem, not a structure problem — warn, but
			// don't send the author chasing section config.
			fmt.Fprintf(b, "WARN data source %q: the page DID request %s but the response had not arrived when the check ended. The wiring is correct; the SCRIPT IS SLOW (a script that makes many sequential fetch_url calls takes that long on every page load). Reduce the calls or accept slow loads: do NOT change the section wiring.\n", ds.Name, endpoint)
		case status == 0 && appHTMLReferencesData(html, ds.Name):
			// A source the page's own code names but only calls when someone
			// plays: a click-driven game fetches its next step on a click, and
			// a page load never clicks. Failing it as unwired sent a build into
			// adding a load-time warm-up fetch, then a probe branch in the
			// script, then a whole extra section, all to satisfy this check,
			// and the app came out worse for it. The reference is the wiring
			// evidence a load cannot give; test is what runs the script.
			fmt.Fprintf(b, "WARN data source %q: the page's code references %s but did not fetch it on load, presumably it does on interaction (a click, a move), which a page load cannot exercise. That is fine, do NOT add a load-time fetch to satisfy this check; action=test is what runs the script.\n", ds.Name, "data/"+ds.Name)
		case status == 0:
			failures++
			classes = append(classes, "source-unwired")
			fmt.Fprintf(b, "FAIL data source %q: the page NEVER fetched %s; no section is wired to it. Set source_script:%q on the table/display that should render it, or (from an html section's script), call fetch(%q) (plain relative fetch; there is no client-side gohort object in app pages).\n", ds.Name, endpoint, ds.Name, "data/"+ds.Name)
		case status >= 400:
			// Already counted via FailedRequests above; this line
			// just names the source for the fix.
			fmt.Fprintf(b, "     ^ that failing request is data source %q.\n", ds.Name)
		default:
			fmt.Fprintf(b, "OK   data source %q: page fetched %s live (HTTP %d).\n", ds.Name, endpoint, status)
		}
	}
	return failures, classes
}

// appDataRefRE finds a data endpoint named in page code: "data/<name>" right
// after a quote, a backtick or a slash, so 'data/x', "./data/x" and
// "/apps/<slug>/data/x" all count and an identifier like metadata/x does not.
var appDataRefRE = regexp.MustCompile("(?:^|['\"`/])data/([A-Za-z0-9_-]+)")

// appHTMLReferencesData reports whether the page's html, framed documents
// included, names the data source called name. A reference written with
// underscores counts: the served app answers data/a_b for a source saved as
// a-b, since names are slugged when they are saved.
func appHTMLReferencesData(html, name string) bool {
	if html == "" || name == "" {
		return false
	}
	want := slugify(name)
	for _, m := range appDataRefRE.FindAllStringSubmatch(html, -1) {
		if slugify(m[1]) == want {
			return true
		}
	}
	return false
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
func appSampleRecords(raw any) ([]map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	// The shapes a model sends a list in (an array, the array as a JSON
	// string, one object). Anything else is refused: a sample that did not
	// parse was run as no sample, against the empty store, and the report
	// said OK, so four builds tested nothing and never knew.
	arr, _, ok := appArrayArg(raw, "sample")
	if !ok {
		why := "it must be an array of records, e.g. [{\"item\": \"bolts\", \"quantity\": 5}]"
		if str, isStr := raw.(string); isStr {
			var probe any
			if err := json.Unmarshal([]byte(strings.TrimSpace(str)), &probe); err != nil {
				why += "; the JSON did not parse: " + err.Error()
			}
		}
		return nil, fmt.Errorf("sample was not used, nothing ran: %s", why)
	}
	if len(arr) == 0 {
		return nil, nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
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
//
// The saves' automatic check reads this; test and verify go through
// runScriptChecks to choose how much of each script's output to show.
func (t *chatTurn) checkScripts(spec AppSpec, includeActions bool, sample []map[string]any, params map[string]any) (report string, records, pass, fail int) {
	return t.runScriptChecks(spec, appScriptRun{includeActions: includeActions, sample: sample, params: params})
}

// appScriptRun is how one script check runs: which components, against what
// input, and how many characters of each passing script's output the report
// shows (0 shows none, the save check's terse report).
type appScriptRun struct {
	includeActions bool
	sample         []map[string]any
	params         map[string]any
	preview        int
}

// testOutputPreview is how much of each script's output action=test shows,
// and verifyOutputPreview the same for verify, which is the final gate and
// reads best short: enough to tell a real answer from a caught error.
const (
	testOutputPreview   = 600
	verifyOutputPreview = 160
)

// runScriptChecks is checkScripts with the run's options spelled out.
func (t *chatTurn) runScriptChecks(spec AppSpec, opt appScriptRun) (report string, records, pass, fail int) {
	// The store the host hands a live script: the owner's part of the app's
	// data. It used to be UserDB(RootDB, ...), which nothing on the script's
	// path reads today (tool lookups resolve RootDB themselves), but a check
	// should not depend on that staying true.
	db := appscript.RecordBase(spec, t.user)
	recs := opt.sample
	var b strings.Builder
	if recs == nil {
		recs = appStoredRecords(t.user, spec)
		// An empty store with a retained sample: run against the sample, and
		// say so, rather than testing every data source against [].
		if len(recs) == 0 && len(spec.Sample) > 0 {
			recs = spec.Sample
			fmt.Fprintf(&b, "NOTE the store is empty; running against the app's retained sample (%d record(s)).\n", len(recs))
		}
		if recs == nil {
			recs = []map[string]any{}
		}
	}
	recJSON, _ := json.Marshal(recs)
	gapIn := appGapInput{outputs: map[string]string{}, empty: map[string]string{}}
	fixed := map[string]string{"caller": appscript.CallerAlias(spec, t.user)}
	sharedRecs := 0
	if len(spec.SharedCollections) > 0 {
		fixed["shared"] = appscript.SharedInput(db, spec)
		var counts []string
		for _, name := range spec.SharedCollections {
			n := len(appscript.ReadShared(db, spec, name))
			sharedRecs += n
			counts = append(counts, fmt.Sprintf("%s (%d)", name, n))
		}
		fmt.Fprintf(&b, "Shared collections given as shared, as stored now: %s. A shared write an action prints is NOT saved by a check.\n", strings.Join(counts, ", "))
	}
	baseArgs, applied, shadowed := appScriptArgs(spec, string(recJSON), opt.params, fixed)
	if len(applied) > 0 {
		fmt.Fprintf(&b, "Params applied as env vars: %s.\n", strings.Join(applied, ", "))
	}
	for _, name := range shadowed {
		if _, ok := fixed[name]; ok {
			fmt.Fprintf(&b, "NOTE param %q was NOT applied: the framework sets %s itself, and live no page can change it.\n", name, name)
			continue
		}
		fmt.Fprintf(&b, "NOTE param %q was NOT applied: it names a declared setting, and live a setting always beats a param, so the scripts ran with the setting's value %q, as they will when served.\n", name, fmt.Sprint(baseArgs[name]))
	}
	run := func(kind, name, lang, script string, caps []string) {
		label := fmt.Sprintf("%s %q", kind, name)
		scriptArgs := make(map[string]any, len(baseArgs))
		for k, v := range baseArgs {
			scriptArgs[k] = v
		}
		out, err := appscript.Run(t.user, db, spec.Slug, kind, name, lang, script, caps, scriptArgs)
		if err != nil {
			fail++
			fmt.Fprintf(&b, "FAIL %s, could not run: %v\n", label, err)
			return
		}
		trimmed := strings.TrimSpace(out)
		if trimmed == "" {
			uncalled := uncalledPythonFunctions(script)
			if kind == "action" { // an action may legitimately print nothing
				pass++
				fmt.Fprintf(&b, "OK   %s: ran, printed nothing (no message/records).\n", label)
				if len(uncalled) > 0 {
					fmt.Fprintf(&b, "     %s\n", uncalledHint(uncalled))
				}
				return
			}
			fail++
			fmt.Fprintf(&b, "FAIL %s: printed nothing; a data source must print JSON to stdout.\n", label)
			if len(uncalled) > 0 {
				fmt.Fprintf(&b, "     %s\n", uncalledHint(uncalled))
			}
			return
		}
		if !json.Valid([]byte(trimmed)) {
			fail++
			fmt.Fprintf(&b, "FAIL %s: did not print valid JSON. Output:\n%s\n", label, truncate(trimmed, 800))
			if hint := scriptFailureHint(trimmed); hint != "" {
				fmt.Fprintf(&b, "     %s\n", hint)
			}
			if strings.Contains(trimmed, `json.loads("records")`) || strings.Contains(trimmed, "json.loads('records')") {
				b.WriteString("     Hint: read records with json.loads(os.environ.get('records', '[]')), json.loads(\"records\") parses the literal word, not the data.\n")
			} else if strings.Contains(trimmed, "KeyError") || strings.Contains(trimmed, "os.environ[") {
				b.WriteString("     Hint: a data source runs on page load with NO query params set, read every env var with a default, e.g. os.environ.get('city', ''), never os.environ['city'].\n")
			}
			return
		}
		var v any
		_ = json.Unmarshal([]byte(trimmed), &v)
		if kind == "data" {
			gapIn.outputs[name] = trimmed
		}
		// What the script actually printed, on the OK lines too. A script that
		// catches its own failure prints a perfectly valid object, and an OK
		// that names only the shape cannot tell a scene from {"error": ...,
		// "detail": ...}: a build ran test three times with different params
		// and saw "printed a JSON object" every time, never the object.
		shown := ""
		if opt.preview > 0 {
			shown = "\n     output: " + appOutputPreview(trimmed, opt.preview)
		}
		if kind == "data" {
			// The section is what the user sees: a source that prints valid
			// JSON its sections cannot read renders an empty page that every
			// shape-only check called OK.
			probs := appSectionShapeProblems(spec, name, v)
			// Nothing saved and no sample: a source that reads the records
			// just printed its first-visit output, placeholders and an empty
			// series. Judging that as the forecast failed a correct script,
			// and a build rewrote it six times against the same report.
			if len(probs) > 0 && len(recs) == 0 && strings.Contains(script, "records") {
				fmt.Fprintf(&b, "NOTE %s: nothing is saved and no sample was given, so this ran as a FIRST VISIT and how its sections read it was not judged. Check the real path with app_def(action=\"test\", sample=[{...one entry shaped like the form's...}]).\n", label)
				probs = nil
			}
			for _, p := range probs {
				fail++
				fmt.Fprintf(&b, "FAIL %s: %s\n", label, p)
			}
		}
		switch kind {
		case "data":
			if arr, isArr := v.([]any); isArr {
				pass++
				readsShared := len(spec.SharedCollections) > 0 && strings.Contains(script, "shared")
				if len(arr) == 0 && readsShared && sharedRecs == 0 {
					// A source over a shared collection with nothing in it yet is
					// empty for the right reason. Pointing it at records instead,
					// as the warning below does, sent a leaderboard reading the
					// shared board to read the player's own store.
					fmt.Fprintf(&b, "OK   %s: printed an empty array; it reads the shared collections, which hold no records yet, so that is expected. A check does not save a shared write, so its logic shows once an action has written one live.\n", label)
				} else if len(arr) == 0 && len(recs) > 0 {
					// Valid JSON, but empty output while the app HAS records is the
					// signature of a script that reads a query param nothing supplies
					// (os.environ.get('city')) instead of pulling the saved entries
					// from the records env var — the "added a location, no forecast"
					// disconnect. Pass (it's valid) but flag it loudly.
					fmt.Fprintf(&b, "WARN %s: printed an EMPTY array though the app has %d saved record(s). The script is probably reading a query param (e.g. os.environ.get('city')) that is never set; read the saved entries from the `records` env var instead, e.g. recs = json.loads(os.environ.get('records','[]')).\n", label, len(recs))
				} else {
					fmt.Fprintf(&b, "OK   %s: printed a JSON array (%d item(s)); good for a table.%s%s\n", label, len(arr), emptyStoreNote(recs), shown)
				}
			} else if obj, isObj := v.(map[string]any); isObj && len(obj) == 1 && obj["error"] != nil {
				// The script caught its own failure and printed it: valid JSON,
				// and still a script that failed. OK here was the builder's cue
				// to tell the user the app worked.
				fail++
				fmt.Fprintf(&b, "FAIL %s: printed an error: %s\n", label, truncate(fmt.Sprint(obj["error"]), 400))
			} else {
				pass++
				fmt.Fprintf(&b, "OK   %s: printed a JSON object; good for a display (a table section needs a JSON array).%s%s\n", label, emptyStoreNote(recs), shown)
			}
		case "action":
			if _, isObj := v.(map[string]any); isObj {
				pass++
				fmt.Fprintf(&b, "OK   %s: printed a JSON object {message?, records?}.%s\n", label, shown)
			} else {
				fail++
				fmt.Fprintf(&b, "FAIL %s: an action must print a JSON OBJECT {message?, records?}, got %T.\n", label, v)
			}
		}
	}

	for _, ds := range spec.DataSources {
		run("data", ds.Name, ds.Language, ds.Script, ds.Capabilities)
	}
	if opt.includeActions {
		for _, act := range spec.Actions {
			run("action", act.Name, act.Language, act.Script, act.Capabilities)
		}
	}
	// The first visit: what each source that reads the records prints with
	// none saved. When the run above already had none, that run is the answer.
	for _, ds := range spec.DataSources {
		if !strings.Contains(ds.Script, "records") {
			continue
		}
		if len(recs) == 0 {
			if o, ok := gapIn.outputs[ds.Name]; ok {
				gapIn.empty[ds.Name] = o
			}
			continue
		}
		emptyArgs := make(map[string]any, len(baseArgs))
		for k, v := range baseArgs {
			emptyArgs[k] = v
		}
		emptyArgs["records"] = "[]"
		if out, err := appscript.Run(t.user, db, spec.Slug, "data", ds.Name, ds.Language, ds.Script, ds.Capabilities, emptyArgs); err == nil {
			gapIn.empty[ds.Name] = strings.TrimSpace(out)
		}
	}
	b.WriteString(appBuildGaps(t.user, spec, gapIn))
	return b.String(), len(recs), pass, fail
}

// appScriptArgs is the env a checked script runs with, built in the order the
// served app builds it: the records, then each param, then every declared
// setting LAST, so a param naming a setting loses to it here as it does live
// (customapps applies settings after the query params, because anyone holding
// a link can set a param). The test used to let the param win, which passed a
// value through that the served app would have thrown away. Settings arrive
// at their declared defaults: the values someone set live in the app's own
// store, which a check does not read.
//
// fixed are the inputs the framework sets itself (caller, shared), applied
// last as live, and a param naming one is shadowed like one naming a setting.
//
// applied lists the params that reached the scripts (name=value, sorted) and
// shadowed the ones a setting overrode, so the report can say which is which.
func appScriptArgs(spec AppSpec, records string, params map[string]any, fixed map[string]string) (args map[string]any, applied, shadowed []string) {
	args = map[string]any{"records": records}
	for k, v := range params {
		args[k] = fmt.Sprint(v)
	}
	isSetting := map[string]bool{}
	for _, st := range spec.Settings {
		if st.Name == "" {
			continue
		}
		isSetting[st.Name] = true
		args[st.Name] = st.Default
	}
	for k, v := range fixed {
		args[k] = v
		isSetting[k] = true
	}
	for k, v := range params {
		if isSetting[k] {
			shadowed = append(shadowed, k)
			continue
		}
		applied = append(applied, k+"="+strconv.Quote(fmt.Sprint(v)))
	}
	sort.Strings(applied)
	sort.Strings(shadowed)
	return args, applied, shadowed
}

// appOutputPreview is one line of what a script printed, capped at max
// characters: JSON compacted so a pretty-printed object does not spend the
// budget on indentation, anything else with its whitespace folded.
func appOutputPreview(out string, max int) string {
	var buf bytes.Buffer
	s := out
	if json.Compact(&buf, []byte(out)) == nil {
		s = buf.String()
	} else {
		s = strings.Join(strings.Fields(out), " ")
	}
	if len(s) <= max {
		return s
	}
	return fmt.Sprintf("%s... (%d more chars)", s[:max], len(s)-max)
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

// recordAppVerify puts a verify run in the build ledger. A run whose only
// failure was the page check itself not running (no browser, a budget guard)
// found nothing wrong with the app, so it is unproven rather than failed: the
// ledger is evidence about how apps get built, and an outage is not.
func (t *chatTurn) recordAppVerify(slug string, failures int, classes []string, pageCheckBroke bool) {
	o := buildledger.Outcome{Owner: t.user, Session: t.chatSessionID(), Agent: t.agent.ID,
		Kind: buildledger.KindApp, Target: slug, Via: "app_def verify", Verdict: buildledger.Pass}
	switch {
	case failures == 0:
	case pageCheckBroke && len(classes) == 0:
		o.Verdict, o.Classes, o.Detail = buildledger.Unproven, []string{"page-check-unavailable"}, "the page check could not run"
	default:
		o.Verdict, o.Classes, o.Detail = buildledger.Fail, classes, fmt.Sprintf("%d problem(s)", failures)
	}
	buildledger.Record(o)
}

// emptyStoreNote says what an OK against no records proves: that the script
// runs, not that its logic is right. A gradebook whose averages crashed on
// the first real score passed here on an empty store, and the builder told
// the user it was verified.
func emptyStoreNote(recs []map[string]any) string {
	if len(recs) > 0 {
		return ""
	}
	return " That was against an EMPTY store, so it only shows the script runs: pass sample=[{...}] with a few records shaped like the form's to check its logic."
}
