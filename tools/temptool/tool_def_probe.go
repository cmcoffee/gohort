package temptool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// testGrouped verifies an api/toolbox tool end-to-end BEFORE it ships:
// it render- and JSON-validates every endpoint's body, checks that every
// required param is actually SENT somewhere (the #1 authoring bug —
// a POST param that lands in neither url_template nor body_template, so
// the API 400s with "field must be a string"), compile-checks every
// response_pipe, live-probes READ endpoints for a 2xx (running the pipe
// against the REAL body), and render-validates WRITE endpoints without
// firing them. Returns a per-endpoint PASS/FAIL report the author acts on.
func testGrouped(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required — the api/toolbox tool to verify")
	}
	tt, ok := loadExistingToolRecord(sess, name)
	if !ok {
		return "", fmt.Errorf("no tool named %q — use action=\"list\" to see what exists", name)
	}

	// Flatten to a uniform endpoint list. A single api tool becomes one
	// synthetic endpoint; a toolbox contributes each of its actions.
	//
	// Mode is resolved through effectiveTempToolMode, NOT read raw: shell
	// tools are stored with Mode=="" (the legacy spelling — see
	// createGrouped and add_tool), so a raw `case TempToolModeAPI, ""`
	// swept every shell tool into the api path and "verified" it by
	// HTTP-GETting its command_template. That produced the nonsense
	// `unsupported protocol scheme ""` verdict on a working script and
	// sent the authoring model editing command_template in a loop trying
	// to make a python3 invocation look like a URL.
	var endpoints []TempToolAction
	switch effectiveTempToolMode(tt) {
	case TempToolModeToolbox:
		endpoints = tt.Actions
	case TempToolModeShell:
		return testShellTool(tt, args, sess)
	case TempToolModeAPI:
		if strings.TrimSpace(tt.CommandTemplate) == "" {
			return "", fmt.Errorf("tool %q has no url_template — nothing to probe", name)
		}
		endpoints = []TempToolAction{{
			Name: name, Params: tt.Params, Required: tt.Required,
			URLTemplate: tt.CommandTemplate, Method: tt.Method,
			BodyTemplate: tt.BodyTemplate, ContentType: tt.ContentType,
			Headers:      tt.Headers,
			ResponsePipe: tt.ResponsePipe, ResponseExtract: tt.ResponseExtract,
		}}
	default:
		return "", fmt.Errorf("tool %q is mode=%q — test verifies shell, api and toolbox tools. For a %s tool, exercise it by calling it directly with real args", name, tt.Mode, tt.Mode)
	}
	if len(endpoints) == 0 {
		return "", fmt.Errorf("tool %q has no endpoints to test", name)
	}

	cases := parseTestCases(args["cases"])

	// Private mode / a blocked network connector means the offline checks
	// (param wiring, body render, pipe compile) still run, but live read
	// probes can't — degrade gracefully to offline-only rather than
	// reporting a spurious "live probe errored" on every read endpoint.
	netOK := sess.NetworkAllowed()

	var b strings.Builder
	fmt.Fprintf(&b, "Verification report for %q (%d endpoint(s)):\n\n", name, len(endpoints))
	if !netOK {
		b.WriteString("(network is blocked this turn — running OFFLINE checks only; read endpoints are not live-probed.)\n\n")
	}
	failCount, writeManual, emptyRead := 0, 0, 0

	for _, ep := range endpoints {
		method := strings.ToUpper(strings.TrimSpace(ep.Method))
		if method == "" {
			method = "GET"
		}
		// GET/HEAD and the read-only WebDAV query methods (REPORT, PROPFIND,
		// SEARCH — RFC 3253/4918) are safe to live-fire: they QUERY, never
		// mutate. A CalDAV list_events is a REPORT; without this it was
		// misclassified as a write, so verify refused to fire it and the model
		// punted a manual call to the user for a plain read.
		isRead := method == "GET" || method == "HEAD" || method == "REPORT" || method == "PROPFIND" || method == "SEARCH"
		sample := cases[strings.ToLower(ep.Name)]
		if sample == nil {
			sample = cases[""] // single-api-tool convenience: unlabeled case
		}

		var lines []string
		epFail := false
		fail := func(f string, a ...any) { lines = append(lines, "FAIL  "+fmt.Sprintf(f, a...)); epFail = true }
		pass := func(f string, a ...any) { lines = append(lines, "ok    "+fmt.Sprintf(f, a...)) }
		note := func(f string, a ...any) { lines = append(lines, "note  "+fmt.Sprintf(f, a...)) }

		// A. Every required param must be SENT somewhere. This is the
		//    deterministic, offline catch for the "content must be a
		//    string" class: a required param referenced in neither the
		//    url_template nor the body_template never reaches the API.
		var unref []string
		for _, r := range ep.Required {
			if !templateReferences(ep.URLTemplate, r) && !templateReferences(ep.BodyTemplate, r) {
				unref = append(unref, r)
			}
		}
		if len(unref) > 0 {
			if ep.BodyTemplate == "" && !isRead {
				fail("required param(s) %v are sent NOWHERE — this %s action has no body_template, so the API never receives them (the exact cause of a 400 like \"content must be a string\"). Add a body_template, e.g. {\"content\": {content}}.", unref, method)
			} else {
				fail("required param(s) %v appear in neither url_template nor body_template — the API will never receive them.", unref)
			}
		} else {
			pass("all required params are wired into the url/body templates")
		}

		// B. Body template renders with the sample args. A non-JSON
		// content_type (application/xml for CalDAV/SOAP) switches to RAW
		// substitution + no JSON validation — mirroring the dispatch path,
		// so an XML PROPFIND/REPORT body doesn't fail verify as "invalid
		// JSON". content_type is a tool-level field (toolbox actions are
		// JSON-only today), so it applies to the single-endpoint api case.
		if ep.BodyTemplate != "" {
			// Per-action content_type wins (toolbox actions each carry their own);
			// fall back to the tool-level one for a single api tool.
			epCT := ep.ContentType
			if epCT == "" {
				epCT = tt.ContentType
			}
			rawBody := epCT != "" && !isJSONContentType(epCT)
			if coversRequired(sample, ep.Required) {
				if rawBody {
					if _, err := substituteRaw(ep.BodyTemplate, ep.Params, ep.Required, sample); err != nil {
						fail("body_template render failed: %v", err)
					} else {
						pass("body_template renders (raw, %s — no JSON validation)", epCT)
					}
				} else if body, err := substituteJSON(ep.BodyTemplate, ep.Params, ep.Required, sample); err != nil {
					fail("body_template render failed: %v", err)
				} else if jerr := json.Unmarshal([]byte(body), new(any)); jerr != nil {
					fail("body_template produced INVALID JSON: %v — rendered body: %s. (For an XML/non-JSON API set content_type, e.g. \"application/xml\", so the body is sent RAW.)", jerr, oneLine(body, 200))
				} else {
					pass("body_template renders valid JSON")
				}
			} else {
				note("body_template not render-checked — no sample args covering required %v (pass a case)", ep.Required)
			}
		}

		// C. response_pipe compiles (catches a broken jq/awk filter).
		if ep.ResponsePipe != "" {
			if serr := pipeCompileError(ep.ResponsePipe, sess); serr != "" {
				fail("response_pipe has a syntax/compile error: %s", serr)
			} else {
				pass("response_pipe compiles")
			}
		}

		// D. READ endpoints: real call + assert 2xx + run pipe on the
		//    real body. WRITE endpoints are never auto-fired.
		if isRead {
			switch {
			case !netOK:
				note("read endpoint NOT live-probed — network is blocked this turn (private mode); offline checks only")
			case coversRequired(sample, ep.Required):
				status, body, derr := liveProbe(sess, tt.Credential, ep, sample)
				switch {
				case derr != nil:
					fail("live probe errored: %v", derr)
				case !isStatus2xx(status):
					fail("live call returned %q (want 2xx) — body: %s", status, oneLine(body, 200))
				default:
					pass("live %s returned %q", method, status)
					if ep.ResponsePipe != "" {
						if perr := runPipeAgainst(ep.ResponsePipe, body, sess); perr != "" {
							fail("response_pipe failed on the REAL response body (shape mismatch — e.g. the filter expects .posts[] but the body is a bare array): %s", perr)
						} else {
							pass("response_pipe runs clean on the real response")
						}
					}
					// A 2xx that carried NO records is the single most
					// misleading result this action can produce. It proves the
					// request was well-formed and says NOTHING about whether the
					// query is right — and it is exactly what a CalDAV REPORT
					// missing its Depth header returns (an empty 207
					// multistatus). Reported live: "all endpoints passed, tool
					// verified" on a list tool that could never return an event,
					// while the user was saying it didn't work.
					//
					// Not a FAIL — an empty collection is a legitimate state for
					// a fresh account — but it must never read as proof the tool
					// returns data.
					if emptyResultBody(body, ep) {
						note("live call returned 2xx but ZERO records — this proves the request is well-formed, NOT that the query is right. If you expected data: check the filter/date-range, and for WebDAV/CalDAV check headers (a REPORT/PROPFIND without \"Depth\": \"1\" matches nothing and returns exactly this). Confirm against data you know exists before calling it done.")
						emptyRead++
					}
				}
			default:
				note("read endpoint NOT live-probed — no sample args for required %v (pass a case with real values to hit the live API)", ep.Required)
			}
		} else {
			note("write endpoint NOT auto-fired — make ONE manual %s call and confirm a 2xx before calling this done", method)
			writeManual++
		}

		verdict := "PASS"
		if epFail {
			verdict = "FAIL"
			failCount++
		}
		fmt.Fprintf(&b, "[%s] %s (%s)\n", verdict, ep.Name, method)
		for _, l := range lines {
			fmt.Fprintf(&b, "   %s\n", l)
		}
		b.WriteByte('\n')
	}

	// Record the verdict where it's actually known, rather than leaving
	// downstream to parse this prose. Only a clean sweep counts as verified:
	// a FAIL obviously doesn't, and neither does "all automated checks passed
	// but N write endpoints still need a manual call" — an unfired write
	// endpoint is exactly the untested grenade this action exists to catch.
	switch {
	case failCount > 0:
		RecordToolVerification(sess, name, false, fmt.Sprintf("%d of %d endpoint(s) FAILED verification", failCount, len(endpoints)))
		fmt.Fprintf(&b, "RESULT: %d of %d endpoint(s) FAILED. Fix each with tool_def(action=\"update\", actions=[{name, ...}]) and re-run test until green. Do NOT call this tool done or hand it to a user while any endpoint is FAIL.", failCount, len(endpoints))
	case writeManual > 0:
		RecordToolVerification(sess, name, false, fmt.Sprintf("%d write endpoint(s) never fired — needs one manual live call each to confirm a 2xx", writeManual))
		fmt.Fprintf(&b, "RESULT: all automated checks passed. %d write endpoint(s) still need ONE manual live call each — fire one, confirm a 2xx, then it's done.", writeManual)
	case emptyRead > 0:
		// Checks passed, but every read came back empty — the tool is
		// UNPROVEN, not verified. Signing it off here is what let a list tool
		// that could never return a row ship as "verified".
		RecordToolVerification(sess, name, false, fmt.Sprintf("%d read endpoint(s) returned 2xx with zero records — not proven to return data", emptyRead))
		fmt.Fprintf(&b, "RESULT: the request shape is valid, but %d read endpoint(s) came back EMPTY — nothing here proves the tool returns data. Point a case at a record you KNOW exists and re-run; if it is still empty, the query (filter, date range, headers) is wrong, not the plumbing.", emptyRead)
	default:
		RecordToolVerification(sess, name, true, "")
		b.WriteString("RESULT: all endpoints passed. Tool verified.")
	}
	return b.String(), nil
}

// effectiveTempToolMode resolves a stored record's mode. Mode=="" is the
// legacy spelling of shell (createGrouped, add_tool and the shell dispatch
// path all treat it that way), so an empty mode resolves to shell — EXCEPT
// for a record whose command_template is plainly an http(s) URL, which is an
// api tool written before Mode was populated. Never returns "".
func effectiveTempToolMode(tt TempTool) string {
	if m := strings.TrimSpace(tt.Mode); m != "" {
		return m
	}
	cmd := strings.TrimSpace(tt.CommandTemplate)
	if strings.HasPrefix(cmd, "http://") || strings.HasPrefix(cmd, "https://") {
		return TempToolModeAPI
	}
	return TempToolModeShell
}

// testShellTool verifies a shell-mode tool. There are no endpoints to probe,
// so the checks are the ones that actually catch shell-tool bugs:
//
//	(1) the script PARSES — an unterminated string or a bad indent means every
//	    dispatch dies before doing any work, and nothing else in the report
//	    matters until it's fixed;
//	(2) each required param has a delivery route the script can read;
//	(3) the tool actually RUNS, for real, with the author's sample args.
//
// (3) is the only thing that verifies a shell tool — there is no offline
// substitute — so a tool tested without `cases` stays UNVERIFIED and the
// report says why. The run is a genuine dispatch: the tool's side effects
// happen. Pass sample args you're willing to have executed.
func testShellTool(tt TempTool, args map[string]any, sess *ToolSession) (string, error) {
	cases := parseTestCases(args["cases"])
	sample := cases[strings.ToLower(tt.Name)]
	if sample == nil {
		sample = cases[""] // single-tool convenience: unlabeled case
	}

	var lines []string
	failed := false
	fail := func(f string, a ...any) { lines = append(lines, "FAIL  "+fmt.Sprintf(f, a...)); failed = true }
	pass := func(f string, a ...any) { lines = append(lines, "ok    "+fmt.Sprintf(f, a...)) }
	note := func(f string, a ...any) { lines = append(lines, "note  "+fmt.Sprintf(f, a...)) }

	// A. Does the script parse? Runs the interpreter's own syntax checker —
	//    no args, no network, no side effects. This is the deterministic
	//    catch for the class where a tool was authored with a broken
	//    f-string / quote and every single call returns a SyntaxError.
	if strings.TrimSpace(tt.ScriptBody) != "" {
		lang, problem, checked := scriptSyntaxCheck(tt, sess)
		switch {
		case !checked:
			note("script_body not syntax-checked (no checker available for this language) — the live run is the only proof")
		case problem != "":
			fail("script_body has a SYNTAX ERROR — every dispatch dies before the tool does any work: %s", problem)
		default:
			pass("script_body parses clean (%s)", lang)
		}
	} else if strings.Contains(tt.CommandTemplate, "{workspace_dir}") {
		note("no script_body on the record, but command_template references a workspace file — the tool breaks the moment that workspace is wiped. Re-author with script_body so the script travels with the tool record.")
	}

	// B. Param delivery. A shell tool receives every arg BOTH as a {param}
	//    substitution in command_template AND as a lowercase env var, so a
	//    param missing from the template is not a bug the way it is for an
	//    api tool — it just means the script must read it from the
	//    environment. Report the route rather than failing on it.
	var envOnly []string
	for _, r := range tt.Required {
		if !templateReferences(tt.CommandTemplate, r) {
			envOnly = append(envOnly, r)
		}
	}
	switch {
	case len(envOnly) > 0:
		note("required param(s) %v are not in command_template — they reach the script ONLY as lowercase env vars (os.environ[%q] / $%s). Confirm the script reads them there, not from argv.", envOnly, envOnly[0], envOnly[0])
	case len(tt.Required) > 0:
		pass("every required param is substituted into command_template")
	}

	// B2. Does the script call a hook the tool never declared?
	//
	// The hook methods are capability-gated at dispatch: an undeclared one
	// comes back "method %q not granted", which surfaces inside the script as
	// whatever that failure does to the code after it. An agent repairing a
	// tool swapped fetch_url for browse_page and left hook_capabilities alone;
	// the run died downstream and it concluded the SITE was blocking it. This
	// is a static, deterministic check — the same class as the syntax check —
	// so it fails rather than notes.
	if body := tt.ScriptBody; strings.TrimSpace(body) != "" {
		for _, hc := range []struct{ call, capability string }{
			{"fetch_url", "fetch"},
			{"browse_page", "browse_page"},
			{"fetch_via", "fetch_via"},
			{"secret", "secret"},
		} {
			if !scriptCallsHook(body, hc.call) || hookCapabilityDeclared(tt.HookCapabilities, hc.capability) {
				continue
			}
			fail("script_body calls %s() but hook_capabilities does not include %q — that call is refused at dispatch (\"method not granted\"), and the script fails on whatever it does with the result. Add %q to hook_capabilities.",
				hc.call, hc.capability, hc.capability)
		}
	}

	// C. The real run.
	ran := false
	switch {
	case sample == nil:
		note("tool NOT run — pass cases=[{args:{...}}] with real values. Running it is the ONLY thing that verifies a shell tool; the checks above can't.")
	case !coversRequired(sample, tt.Required):
		note("tool NOT run — the supplied case doesn't cover required %v. Pass a value for each.", tt.Required)
	default:
		ran = true
		out, derr := DispatchTempToolDirect(sess, &tt, sample)
		switch {
		case derr != nil:
			fail("live run FAILED: %v", derr)
		case shellRunFailed(out):
			fail("live run returned a non-zero exit / timeout: %s", oneLine(out, 300))
		default:
			pass("live run succeeded — output: %s", oneLine(out, 200))
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Verification report for %q (shell tool):\n\n", tt.Name)
	verdict := "PASS"
	if failed {
		verdict = "FAIL"
	} else if !ran {
		verdict = "UNVERIFIED"
	}
	fmt.Fprintf(&b, "[%s] %s\n", verdict, tt.Name)
	for _, l := range lines {
		fmt.Fprintf(&b, "   %s\n", l)
	}
	b.WriteByte('\n')

	switch {
	case failed:
		RecordToolVerification(sess, tt.Name, false, "shell tool failed verification")
		b.WriteString("RESULT: FAILED. Fix with tool_def(action=\"update\", script_body=\"...\") and re-run test until it's green. Do NOT call this tool done or hand it to a user while it FAILs.")
	case !ran:
		RecordToolVerification(sess, tt.Name, false, "never run — test was called without cases")
		b.WriteString("RESULT: NOT VERIFIED. The static checks passed, but the tool was never executed. Re-run: tool_def(action=\"test\", name=\"" + tt.Name + "\", cases=[{args:{...}}]) with real values.")
	default:
		RecordToolVerification(sess, tt.Name, true, "")
		b.WriteString("RESULT: ran clean with the supplied args. Tool verified.")
	}
	return b.String(), nil
}

// shellRunFailed reports whether a shell dispatch result is the framework's
// rendering of a non-zero exit or a killed command. dispatchTempTool returns
// those as OUTPUT with a trailing marker rather than as an error, so a report
// that only checks err would call a script that died on line 1 a success.
func shellRunFailed(out string) bool {
	return strings.Contains(out, "[exit: ") || strings.Contains(out, "[TIMED OUT")
}

// scriptSyntaxCheck parses tt.ScriptBody with the interpreter's own syntax
// checker inside the sandbox. Returns the language, a one-line problem
// description (empty when it parses), and whether a verdict could be reached
// at all — an unknown extension or a missing interpreter yields checked=false
// rather than a false accusation of a syntax error.
func scriptSyntaxCheck(tt TempTool, sess *ToolSession) (lang, problem string, checked bool) {
	name := tt.CanonicalScriptName
	if name == "" {
		name = tt.ScriptName
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		// No filename on the record — infer from the interpreter the
		// command_template invokes.
		switch {
		case strings.Contains(tt.CommandTemplate, "python"):
			ext = ".py"
		case strings.Contains(tt.CommandTemplate, "bash"), strings.Contains(tt.CommandTemplate, "sh "):
			ext = ".sh"
		case strings.Contains(tt.CommandTemplate, "node"):
			ext = ".js"
		}
	}
	var checker string
	switch ext {
	case ".py":
		lang, checker = "python3", "python3 -m py_compile %s"
	case ".sh", ".bash":
		lang, checker = "bash", "bash -n %s"
	case ".js":
		lang, checker = "node", "node --check %s"
	case ".rb":
		lang, checker = "ruby", "ruby -c %s"
	default:
		return "", "", false
	}

	dir, err := MintToolDispatchDir("toolsyntax-")
	if err != nil {
		return lang, "", false
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "script"+ext)
	if werr := os.WriteFile(path, []byte(tt.ScriptBody), 0700); werr != nil {
		return lang, "", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	// Authoring-time checks are still sandboxed runs, so they carry the same
	// caller stamp as a dispatch — otherwise an admin on a host that cannot
	// confine gets their tool refused by the checker that was supposed to
	// help them write it, with "sandbox refused" as the only verdict.
	ctx = sess.ContextWithSandboxCaller(ctx)
	res := RunSandboxedShell(ctx, fmt.Sprintf(checker, shellQuote(path)), dir)
	if res.Err == nil && !res.TimedOut {
		return lang, "", true
	}
	// A non-zero exit is only evidence of a SYNTAX problem when the output
	// says so. Anything else (interpreter not installed, sandbox refused,
	// timeout) is a checker failure, not the author's bug — say nothing
	// rather than send them rewriting a script that's fine.
	out := strings.TrimSpace(res.Output)
	low := strings.ToLower(out)
	for _, marker := range []string{"syntaxerror", "syntax error", "unexpected", "indentationerror", "parse error", "unterminated"} {
		if strings.Contains(low, marker) {
			return lang, oneLine(out, 300), true
		}
	}
	return lang, "", false
}

// stringMapArg reads a {name: value} object arg into a string map, tolerating
// the shapes an LLM actually emits: a JSON object (the normal path) or a JSON
// STRING containing one (models quote objects surprisingly often). Non-string
// values are stringified rather than dropped, so headers={"Depth": 1} still
// sends "1" instead of silently sending nothing. Returns nil when empty, so an
// absent field stays absent through the create/update round-trip.
func stringMapArg(args map[string]any, key string) map[string]string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	// Already a string map — the shape tempToolToCreateArgs emits when an
	// update round-trips a stored tool. Missing this case is how a field
	// survives create and vanishes on the next unrelated edit.
	if m, ok := raw.(map[string]string); ok {
		if len(m) == 0 {
			return nil
		}
		out := make(map[string]string, len(m))
		for k, v := range m {
			if k = strings.TrimSpace(k); k != "" {
				out[k] = v
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	if str, isStr := raw.(string); isStr {
		str = strings.TrimSpace(str)
		if str == "" {
			return nil
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(str), &m); err != nil {
			return nil
		}
		raw = m
	}
	m, isMap := raw.(map[string]any)
	if !isMap || len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		k = strings.TrimSpace(k)
		if k == "" || v == nil {
			continue
		}
		if str, isStr := v.(string); isStr {
			out[k] = str
			continue
		}
		out[k] = strings.TrimSpace(fmt.Sprint(v))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseTestCases normalizes the `cases` arg into action-name → args.
// Each case is {action?: "<sub>", args: {...}}; a case with no action
// is stored under "" for the single-api-tool convenience path.
func parseTestCases(v any) map[string]map[string]any {
	out := map[string]map[string]any{}
	list, ok := v.([]any)
	if !ok {
		return out
	}
	for _, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(StringArg(m, "action")))
		a, _ := m["args"].(map[string]any)
		if a == nil {
			a = map[string]any{}
		}
		out[key] = a
	}
	return out
}

// coversRequired reports whether sample supplies a non-empty value for
// every required param — the precondition for a body render or a live
// probe that would otherwise error on a missing arg (which is not an
// authoring bug, just an absent sample).
func coversRequired(sample map[string]any, required []string) bool {
	for _, r := range required {
		v, ok := lookupArgCI(sample, r)
		if !ok || v == nil {
			return false
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			return false
		}
	}
	return true
}

// liveProbe dispatches an endpoint for real with its response_pipe
// CLEARED, so the raw "HTTP <code>\n<body>" comes back for status
// classification and for running the pipe separately against the true
// body. Reuses the production api dispatch path end-to-end.
func liveProbe(sess *ToolSession, cred string, ep TempToolAction, sample map[string]any) (status, body string, err error) {
	syn := TempTool{
		Name: "test." + ep.Name, Params: ep.Params, Required: ep.Required,
		Mode: TempToolModeAPI, CommandTemplate: ep.URLTemplate, Credential: cred,
		Method: ep.Method, BodyTemplate: ep.BodyTemplate,
		// Carry content_type so a raw XML/CalDAV/iCalendar body is sent as-is
		// (not JSON-validated). ResponsePipe + ResponseExtract are suppressed:
		// the probe only confirms a live 2xx; projection/extraction correctness
		// is checked separately, and running them here could turn a healthy 2xx
		// into a spurious FAIL.
		// Headers MUST ride along: probing without the Depth header a CalDAV
		// REPORT requires would test a different request than dispatch sends,
		// and report a 2xx for a call the real tool can't make work.
		ContentType: ep.ContentType, Headers: ep.Headers, ResponsePipe: "",
	}
	inner := canonicalizeArgKeys(cloneArgs(sample), ep.Required, ep.Params)
	raw, derr := dispatchAPIModeTempTool(sess, &syn, inner)
	if derr != nil {
		return "", raw, derr
	}
	status, body = splitStatusLine(raw)
	return status, body, nil
}

// emptyResultBody reports whether a 2xx response carried no records. It is
// deliberately conservative — only shapes that unambiguously mean "nothing
// came back" count, because a false positive would nag about a healthy tool.
//
// Recognized: an empty body; a bare empty JSON array/object; and the WebDAV
// signature that motivated this check — a multistatus element with no
// <response> children, which is what a CalDAV REPORT returns when it matched
// nothing (classically, a missing Depth header). When the endpoint declares a
// response_extract, the extraction is run and its RESULT is what's judged: an
// extractor yielding [] over a body full of XML is still zero records.
func emptyResultBody(body string, ep TempToolAction) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return true
	}
	if ep.ResponseExtract != nil {
		out, err := ExtractXML([]byte(trimmed), *ep.ResponseExtract)
		if err != nil {
			return false // extraction problems are reported elsewhere
		}
		trimmed = strings.TrimSpace(string(out))
	}
	switch trimmed {
	case "[]", "{}", "null":
		return true
	}
	// WebDAV: <multistatus/> or <multistatus ...></multistatus> with no
	// <response> child. Namespace prefixes vary (D:, d:, none), so match on
	// the local name rather than a fixed spelling.
	low := strings.ToLower(trimmed)
	if strings.Contains(low, "multistatus") && !strings.Contains(low, "<response") &&
		!strings.Contains(low, ":response") {
		return true
	}
	return false
}

// pipeCompileError runs a response_pipe against a trivial JSON doc and
// returns a non-empty message ONLY for a syntax/compile error — those
// fire regardless of input shape and are true authoring bugs. A runtime
// error against the dummy input (null iteration, missing field) is not a
// compile bug and yields "" (the real shape is checked live for reads).
func pipeCompileError(pipe string, sess *ToolSession) string {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	// Authoring-time checks are still sandboxed runs, so they carry the same
	// caller stamp as a dispatch — otherwise an admin on a host that cannot
	// confine gets their tool refused by the checker that was supposed to
	// help them write it, with "sandbox refused" as the only verdict.
	ctx = sess.ContextWithSandboxCaller(ctx)
	res := RunSandboxedShellPipe(ctx, pipe, "{}")
	if res.Err == nil {
		return ""
	}
	msg := strings.ToLower(fmt.Sprint(res.Err) + " " + res.Output)
	if strings.Contains(msg, "syntax error") || strings.Contains(msg, "compile error") || strings.Contains(msg, "unexpected") {
		return oneLine(res.Output, 200)
	}
	return ""
}

// runPipeAgainst runs a response_pipe against a real response body and
// returns a non-empty message if it failed (bad filter, shape mismatch).
func runPipeAgainst(pipe, body string, sess *ToolSession) string {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	// Authoring-time checks are still sandboxed runs, so they carry the same
	// caller stamp as a dispatch — otherwise an admin on a host that cannot
	// confine gets their tool refused by the checker that was supposed to
	// help them write it, with "sandbox refused" as the only verdict.
	ctx = sess.ContextWithSandboxCaller(ctx)
	res := RunSandboxedShellPipe(ctx, pipe, body)
	if res.TimedOut {
		return "timed out"
	}
	if res.Err != nil {
		return oneLine(res.Output, 200)
	}
	return ""
}
