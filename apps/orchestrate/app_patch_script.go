package orchestrate

// patch / replace_function for SCRIPTS — the data sources and actions.
//
// The html section got in-place editing because re-sending a 17KB document
// to fix one line is how working code gets rewritten around the fix. A
// script has the same shape and had none of it: get omitted the bodies for
// size, and the only way to change a line of a data source was to pass the
// whole data_sources array back through update, which REPLACES the stored
// set — so a typo in one script meant reproducing every script. Same
// failure, smaller documents.
//
// So the two html edit actions take a `script` argument naming a data source
// or action, and edit that body instead. Same rules: an exact find that
// matches once, or a named function the server locates. Python functions are
// found by indentation (a `def` and the block under it); a bash function by
// its braces, through the JavaScript locator, whose brace-matching does not
// care which language wrote them.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// appScriptRef points at one script body on a spec: which list it lives in
// and where. kind is "data" or "action", matching checkScripts' labels.
type appScriptRef struct {
	kind string
	idx  int
}

func (r appScriptRef) label(spec AppSpec) string {
	if r.kind == "data" {
		return "data source " + quoteName(spec.DataSources[r.idx].Name)
	}
	return "action " + quoteName(spec.Actions[r.idx].Name)
}

func quoteName(s string) string { return fmt.Sprintf("%q", s) }

func (r appScriptRef) body(spec AppSpec) (lang, script string) {
	if r.kind == "data" {
		return spec.DataSources[r.idx].Language, spec.DataSources[r.idx].Script
	}
	return spec.Actions[r.idx].Language, spec.Actions[r.idx].Script
}

func (r appScriptRef) set(spec *AppSpec, script string) {
	if r.kind == "data" {
		spec.DataSources[r.idx].Script = script
		return
	}
	spec.Actions[r.idx].Script = script
}

// pickAppScript resolves a `script` argument to a stored data source or
// action. Names are matched as stored (slugified on parse) and as given, so
// an author who remembers the display form still lands. A name present in
// both lists is refused rather than guessed.
func pickAppScript(spec AppSpec, name string) (appScriptRef, error) {
	want := strings.TrimSpace(name)
	if want == "" {
		return appScriptRef{}, errors.New("script is required — the name of the data source or action to edit (app_def action=get lists them)")
	}
	slug := slugify(want)
	var hits []appScriptRef
	for i, d := range spec.DataSources {
		if d.Name == slug || strings.EqualFold(d.Name, want) {
			hits = append(hits, appScriptRef{kind: "data", idx: i})
		}
	}
	for i, a := range spec.Actions {
		if a.Name == slug || strings.EqualFold(a.Name, want) {
			hits = append(hits, appScriptRef{kind: "action", idx: i})
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		var have []string
		for _, d := range spec.DataSources {
			have = append(have, "data source "+quoteName(d.Name))
		}
		for _, a := range spec.Actions {
			have = append(have, "action "+quoteName(a.Name))
		}
		if len(have) == 0 {
			return appScriptRef{}, fmt.Errorf("this app has no scripts — no data source or action named %q to edit", want)
		}
		return appScriptRef{}, fmt.Errorf("no data source or action named %q — this app has: %s", want, strings.Join(have, ", "))
	default:
		return appScriptRef{}, fmt.Errorf("%q names both a data source and an action — pass script=\"data:%s\" or script=\"action:%s\"", want, slug, slug)
	}
}

// pickAppScriptQualified accepts the "data:<name>" / "action:<name>" forms the
// ambiguity refusal offers, else falls through to the plain lookup.
func pickAppScriptQualified(spec AppSpec, name string) (appScriptRef, error) {
	n := strings.TrimSpace(name)
	kind := ""
	switch {
	case strings.HasPrefix(strings.ToLower(n), "data:"):
		kind, n = "data", n[5:]
	case strings.HasPrefix(strings.ToLower(n), "action:"):
		kind, n = "action", n[7:]
	}
	if kind == "" {
		return pickAppScript(spec, n)
	}
	slug := slugify(n)
	if kind == "data" {
		for i, d := range spec.DataSources {
			if d.Name == slug {
				return appScriptRef{kind: "data", idx: i}, nil
			}
		}
	} else {
		for i, a := range spec.Actions {
			if a.Name == slug {
				return appScriptRef{kind: "action", idx: i}, nil
			}
		}
	}
	return appScriptRef{}, fmt.Errorf("no %s named %q on this app", map[string]string{"data": "data source", "action": "action"}[kind], n)
}

// applyTextPatch is applyHTMLPatch's rule over any text: the find must occur
// exactly once. what names the document in the refusal.
func applyTextPatch(text, find, replace, what, slug string) (string, error) {
	switch n := strings.Count(text, find); {
	case n == 0:
		return "", fmt.Errorf("that text does not appear in %s — you may be patching a version the app no longer has. Call app_def(action=\"get\", id=%q, script=<name>) to read the CURRENT script, copy the exact text from it (whitespace included), and patch again", what, slug)
	case n > 1:
		return "", fmt.Errorf("that text appears %d times in %s — a patch has to identify ONE place. Extend the find text with the surrounding lines until it is unique", n, what)
	}
	return strings.Replace(text, find, replace, 1), nil
}

// pyDefRE matches a Python def line and captures its indentation and name.
var pyDefRE = regexp.MustCompile(`(?m)^([ \t]*)(?:async[ \t]+)?def[ \t]+([A-Za-z_][A-Za-z0-9_]*)[ \t]*\(`)

// pyFunctionSpan locates a Python function by name: the def line through the
// last line of its indented block, decorators included. Ambiguity is refused
// like the JavaScript locator's. The end excludes trailing blank lines, so a
// replacement that ends without a newline leaves the spacing as it was.
func pyFunctionSpan(src, name string) (start, end int, err error) {
	var hits [][]int
	for _, m := range pyDefRE.FindAllStringSubmatchIndex(src, -1) {
		if src[m[4]:m[5]] == name {
			hits = append(hits, m)
		}
	}
	switch {
	case len(hits) == 0:
		defined := pyDefinedFunctions(src)
		if len(defined) == 0 {
			return 0, 0, fmt.Errorf("no function named %q in this script, and no def was found at all — read it with app_def(action=\"get\", script=<name>) before editing", name)
		}
		return 0, 0, fmt.Errorf("no function named %q in this script. It defines: %s", name, strings.Join(defined, ", "))
	case len(hits) > 1:
		return 0, 0, fmt.Errorf("%q is defined %d times in this script — a replacement has to identify ONE of them, so use patch with enough surrounding text to be unique", name, len(hits))
	}
	m := hits[0]
	start = m[0]
	indent := m[3] - m[2]
	// Decorators directly above the def, at the same indentation, belong to it.
	for {
		prevEnd := start
		if prevEnd == 0 {
			break
		}
		lineStart := strings.LastIndex(src[:prevEnd-1], "\n") + 1
		line := src[lineStart : prevEnd-1]
		trimmed := strings.TrimLeft(line, " \t")
		if len(line)-len(trimmed) != indent || !strings.HasPrefix(trimmed, "@") {
			break
		}
		start = lineStart
	}
	// The block runs while lines are blank or indented deeper than the def.
	end = m[1]
	if nl := strings.IndexByte(src[end:], '\n'); nl >= 0 {
		end += nl + 1
	} else {
		return start, len(src), nil
	}
	lastContent := end
	for end < len(src) {
		nl := strings.IndexByte(src[end:], '\n')
		lineEnd := len(src)
		if nl >= 0 {
			lineEnd = end + nl + 1
		}
		line := src[end:lineEnd]
		trimmed := strings.TrimLeft(line, " \t")
		if strings.TrimSpace(line) == "" {
			end = lineEnd
			continue
		}
		if len(line)-len(trimmed) <= indent {
			break
		}
		end = lineEnd
		lastContent = lineEnd
	}
	return start, lastContent, nil
}

// pyDefinedFunctions lists the functions a Python script defines, top level or
// nested, for the refusal that names what IS there.
func pyDefinedFunctions(src string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range pyDefRE.FindAllStringSubmatch(src, -1) {
		if !seen[m[2]] {
			seen[m[2]] = true
			out = append(out, m[2])
		}
	}
	return out
}

// scriptFunctionSpan dispatches on the script's language.
func scriptFunctionSpan(lang, src, name string) (int, int, error) {
	if strings.EqualFold(strings.TrimSpace(lang), "bash") {
		return jsFunctionSpan(src, name) // `name() { … }` matches the shorthand form; braces are braces
	}
	return pyFunctionSpan(src, name)
}

// scriptDefines reports whether a fragment defines the named function in the
// script's language.
func scriptDefines(lang, fragment, name string) bool {
	if strings.EqualFold(strings.TrimSpace(lang), "bash") {
		return definesFunction(fragment, name)
	}
	for _, n := range pyDefinedFunctions(fragment) {
		if n == name {
			return true
		}
	}
	return false
}

// appDefPatchScript applies one exact find/replace to a data source or action.
func (t *chatTurn) appDefPatchScript(args map[string]any, spec AppSpec) (string, error) {
	ref, err := pickAppScriptQualified(spec, stringArg(args, "script"))
	if err != nil {
		return "", err
	}
	find := stringArg(args, "find")
	if strings.TrimSpace(find) == "" {
		if fn := strings.TrimSpace(stringArg(args, "function")); fn != "" {
			return t.appDefReplaceScriptFunction(args, spec)
		}
		return "", fmt.Errorf("find is required — the EXACT text to replace, copied from the current script (app_def action=\"get\", id=%q, script=%q). If what you have is a rewritten FUNCTION, use action=\"replace_function\" with function=\"<name>\" instead", spec.Slug, stringArg(args, "script"))
	}
	replace := stringArg(args, "replace")
	if find == replace {
		return "", errors.New("find and replace are identical — nothing to do")
	}
	_, prior := ref.body(spec)
	next, err := applyTextPatch(prior, find, replace, ref.label(spec), spec.Slug)
	if err != nil {
		return "", err
	}
	summary := fmt.Sprintf("Patched %s of %%q (revision %%s) — replaced %d chars with %d.", ref.label(spec), len(find), len(replace))
	return t.saveScriptEdit(spec, ref, next, summary, "patch", "patch "+ref.label(spec), args)
}

// appDefReplaceScriptFunction swaps one named function in a data source or
// action for the text the author supplies.
func (t *chatTurn) appDefReplaceScriptFunction(args map[string]any, spec AppSpec) (string, error) {
	ref, err := pickAppScriptQualified(spec, stringArg(args, "script"))
	if err != nil {
		return "", err
	}
	fn := strings.TrimSpace(stringArg(args, "function"))
	if fn == "" {
		return "", errors.New("function is required — the NAME of the function to replace, e.g. function=\"build_rows\"")
	}
	if !isJSFunctionName(fn) {
		return "", fmt.Errorf("%q is not a plain function name — pass just the identifier, not a call or a signature", fn)
	}
	replace := stringArg(args, "replace")
	if strings.TrimSpace(replace) == "" {
		return "", errors.New("replace is required — the WHOLE new function, def line included. To delete a function instead, use patch with an empty replace")
	}
	lang, prior := ref.body(spec)
	start, end, err := scriptFunctionSpan(lang, prior, fn)
	if err != nil {
		return "", err
	}
	if !scriptDefines(lang, replace, fn) {
		return "", fmt.Errorf("the replacement text does not define %q — pass the WHOLE new function including its definition line, not just the body", fn)
	}
	next := prior[:start] + strings.TrimRight(replace, "\n") + prior[end:]
	if end < len(prior) && !strings.HasSuffix(next[:start+len(strings.TrimRight(replace, "\n"))], "\n") && !strings.HasPrefix(prior[end:], "\n") {
		next = prior[:start] + strings.TrimRight(replace, "\n") + "\n" + prior[end:]
	}
	summary := fmt.Sprintf("Replaced function %s in %s of %%q (revision %%s) — %d chars became %d.", fn, ref.label(spec), end-start, len(replace))
	return t.saveScriptEdit(spec, ref, next, summary, "replacement", "replace_function "+fn+" in "+ref.label(spec), args)
}

// saveScriptEdit is the write path both script edits share. A syntax check
// stands in front of the save when an interpreter is reachable; a data source
// is then RUN (they fire on page load and are read-only by contract, the same
// auto-check create and update perform) and rolled back on failure. An action
// is not run — it may reach an external API — so it gets the syntax verdict
// and a pointer at action=test.
func (t *chatTurn) saveScriptEdit(spec AppSpec, ref appScriptRef, next, summary, verb, reason string, args map[string]any) (string, error) {
	lang, _ := ref.body(spec)
	if problem, checked := scriptSyntaxProblem(t.sandboxCallerCtx(), lang, next); checked && problem != "" {
		return "", fmt.Errorf("that %s would break the script, so it was NOT applied — the app still serves the previous revision:\n- %s\n\nFix the replacement text and try again", verb, problem)
	}
	before := spec
	ref.set(&spec, next)
	spec.ChangeNote = strings.TrimSpace(stringArg(args, "note"))
	saved := SaveAppSpecAs(spec, reason)
	msg := fmt.Sprintf(summary, saved.Name, saved.Updated)
	if ref.kind == "data" {
		report, _, _, fail := t.checkScripts(saved, false, nil, nil)
		if fail > 0 {
			SaveAppSpecAs(before, AppSaveNoHistory)
			return "", fmt.Errorf("that %s made a data source fail when run, so it was ROLLED BACK — the app is serving the previous revision again:\n%s\nFix the replacement text and try again", verb, strings.TrimSpace(report))
		}
		msg += " Every data source was run after the change and printed valid JSON."
	} else {
		msg += " Actions are not run on save (one may reach an external API) — run app_def(action=\"test\", id=" + quoteName(saved.Slug) + ") to execute it."
	}
	return msg + " " + saved.VerifyStatus() + ".", nil
}

// scriptSyntaxProblem checks a script body with the language's own parser —
// python3 -m py_compile, bash -n — when one is on PATH. Same posture as the
// html check: only ever accuse when the interpreter itself says so; a missing
// interpreter or a timeout yields no verdict.
func scriptSyntaxProblem(ctx context.Context, lang, body string) (string, bool) {
	bin, argv, ext := "python3", []string{"-m", "py_compile"}, ".py"
	if strings.EqualFold(strings.TrimSpace(lang), "bash") {
		bin, argv, ext = "bash", []string{"-n"}, ".sh"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", false
	}
	dir, err := os.MkdirTemp("", "appscript-")
	if err != nil {
		return "", false
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file := filepath.Join(dir, "script"+ext)
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		return "", false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, append(argv, file)...).CombinedOutput()
	if ctx.Err() != nil {
		return "", false
	}
	if err == nil {
		return "", true
	}
	if _, isExit := err.(*exec.ExitError); !isExit {
		return "", false
	}
	msg := strings.ReplaceAll(strings.TrimSpace(string(out)), file, "script"+ext)
	return appOneLine(msg, 300), true
}

// appScriptSummary describes one script for action=get without its body: its
// size and the functions it defines, so an author can target replace_function
// or ask for the body by name.
func appScriptSummary(lang, script string) map[string]any {
	m := map[string]any{"bytes": len(script)}
	var fns []string
	if strings.EqualFold(strings.TrimSpace(lang), "bash") {
		fns = jsDefinedFunctions("<script>" + script + "</script>")
	} else {
		fns = pyDefinedFunctions(script)
	}
	if len(fns) > 0 {
		m["functions"] = fns
	}
	return m
}
