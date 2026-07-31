package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// patch_html — change part of an html section without re-sending the document.
//
// An html app's page is one blob, and action=update replaces it wholesale, so
// fixing one line meant regenerating the entire document. A model asked to
// re-emit ~17KB does not reproduce it faithfully: across a handful of
// revisions the same game got progressively minified, its variables renamed,
// and new syntax errors introduced by the rewrite rather than by the fix. The
// author was spending its effort re-typing working code and its mistakes were
// landing in the parts it had not been asked to touch.
//
// So: find/replace against the stored section. The match must be EXACT and
// must occur EXACTLY ONCE — zero matches means the author is patching a
// version it no longer has, and several means it cannot know which one it
// changed. Both refuse rather than guess. Everything the save path checks
// (parse, then a real page load) still runs, and a patch that fails either
// check is ROLLED BACK: unlike an update, where the author is holding the
// document it just sent, a bad patch would leave a dead app and nothing to
// restore it from.

// appDefPatchHTML applies one exact find/replace to an app's html section.
func (t *chatTurn) appDefPatchHTML(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app — check the slug (app_def action=list)")
	}
	find := stringArg(args, "find")
	if strings.TrimSpace(find) == "" {
		// An author reaching for patch_html with only a replacement in hand is
		// almost always holding a rewritten FUNCTION and no anchor for it —
		// observed twice in one session, both times followed by a full-document
		// update that wiped working code. Name the action that wants exactly
		// what it is holding.
		if fn := strings.TrimSpace(stringArg(args, "function")); fn != "" {
			return t.appDefReplaceFunction(args)
		}
		return "", errors.New("find is required — the EXACT text to replace, copied from the app's current html (app_def action=get). Include enough surrounding lines to be unique. If what you have is a rewritten FUNCTION, use action=\"replace_function\" with function=\"<name>\" and replace=\"<the whole new function>\" instead — it finds the old one for you, so you don't have to reproduce any of it")
	}
	replace := stringArg(args, "replace")
	if find == replace {
		return "", errors.New("find and replace are identical — nothing to do")
	}

	// The authoring sections are the edit surface. A spec written before they
	// were stored reverses out of the rendered page, which is lossless for an
	// html section (its authoring field IS its html).
	sections, err := appAuthoringSections(spec)
	if err != nil {
		return "", err
	}
	idx, err := pickHTMLSection(sections, args["section"])
	if err != nil {
		return "", err
	}
	prior := mapStr(sections[idx], "html")
	patched, err := applyHTMLPatch(sections, idx, find, replace, spec.Slug)
	if err != nil {
		return "", err
	}
	summary := fmt.Sprintf("Patched html section %%d of %%q (revision %%s) — replaced %d chars with %d.", len(find), len(replace))
	return t.saveHTMLSectionEdit(spec, sections, idx, prior, patched, summary, "patch", "patch_html")
}

// saveHTMLSectionEdit is the write path every partial html edit shares —
// patch_html and replace_function differ only in how they arrive at the new
// document, not in what has to be true before it is kept.
//
// Three gates, cheapest first, and the LAST two are the ones this exists for.
// Parsing catches a broken edit. The dangling-call diff catches the edit that
// parses and is still fatal: a replacement that dropped a helper the rest of
// the code calls. And the browser load catches what neither can see — except
// that for a canvas app it sees very little, because nothing runs until the
// player clicks, which is exactly why the static check has to stand in front
// of it rather than behind it.
func (t *chatTurn) saveHTMLSectionEdit(spec AppSpec, sections []map[string]any, idx int, prior, next, summary, verb, reason string) (string, error) {
	sections[idx]["html"] = next

	if problems, checked := htmlScriptSyntaxProblems(next); checked && len(problems) > 0 {
		return "", fmt.Errorf("that %s would break the page's JavaScript, so it was NOT applied — the app still serves the previous revision:\n- %s\n\nFix the replacement text and try again", verb, strings.Join(problems, "\n- "))
	}
	if broke := jsNewDanglingCalls(prior, next); len(broke) > 0 {
		return "", fmt.Errorf("that %s was NOT applied — it removes code the rest of the page still calls, which parses fine and then dies the moment the app runs. Nothing now defines: %s\n\nEither keep those definitions in your replacement text, or remove the calls to them as well. The app still serves the previous revision.",
			verb, strings.Join(broke, ", "))
	}

	raw := make([]any, len(sections))
	for i := range sections {
		raw[i] = sections[i]
	}
	page, err := buildAppPage(spec, raw)
	if err != nil {
		return "", fmt.Errorf("edited sections no longer build a page: %w", err)
	}
	blob, err := page.ConfigJSON()
	if err != nil {
		return "", fmt.Errorf("render app page: %w", err)
	}
	before := spec
	spec.Page = blob
	if src, err := json.Marshal(raw); err == nil {
		spec.Sections = src
	}
	saved := SaveAppSpecAs(spec, reason)

	// Now the accurate check, against the revision that was just written. On
	// failure put the previous revision back rather than leaving a dead app.
	// The restore files no history of its own — the broken revision existed for
	// milliseconds and is not a version anyone would want back.
	if errs := appPageRuntimeErrors(t.user, saved.Slug); len(errs) > 0 {
		SaveAppSpecAs(before, AppSaveNoHistory)
		return "", fmt.Errorf("that %s broke the page in a real browser, so it was ROLLED BACK — the app is serving the previous revision again:\n- %s\n\nFix the replacement text and try again",
			verb, strings.Join(errs, "\n- "))
	}
	return fmt.Sprintf(summary, htmlSectionOrdinal(sections, idx), saved.Name, saved.Updated) +
		" The page was parsed and loaded in a real browser after the change and came up clean, so there is nothing further to verify. Tell the user what changed.", nil
}

// applyHTMLPatch resolves the find/replace against one section's html and
// returns the patched document. The uniqueness rule lives here because it IS
// the safety property: a patch that matched nothing is aimed at a version the
// author no longer has, and one that matched several places cannot report
// which it changed. Refusing both is what makes patching safer than re-sending
// the document, not just cheaper.
func applyHTMLPatch(sections []map[string]any, idx int, find, replace, slug string) (string, error) {
	html := mapStr(sections[idx], "html")
	switch n := strings.Count(html, find); {
	case n == 0:
		return "", fmt.Errorf("that text does not appear in html section %d — you may be patching a version the app no longer has. Call app_def(action=\"get\", id=%q) to read the CURRENT html, copy the exact text from it (whitespace included), and patch again", htmlSectionOrdinal(sections, idx), slug)
	case n > 1:
		return "", fmt.Errorf("that text appears %d times in html section %d — a patch has to identify ONE place. Extend the find text with the surrounding lines until it is unique", n, htmlSectionOrdinal(sections, idx))
	}
	return strings.Replace(html, find, replace, 1), nil
}

// appAuthoringSections returns a spec's sections in AUTHORING form, ready to
// edit and hand back to buildAppPage.
func appAuthoringSections(spec AppSpec) ([]map[string]any, error) {
	if len(spec.Sections) > 0 {
		var arr []map[string]any
		if err := json.Unmarshal(spec.Sections, &arr); err == nil && len(arr) > 0 {
			return arr, nil
		}
	}
	secs, _ := authoringSectionsFromPage(spec.Page)
	if len(secs) == 0 {
		return nil, errors.New("this app's sections could not be read back for editing — revise it with action=\"update\" instead")
	}
	return secs, nil
}

// pickHTMLSection resolves which html section to patch. With one html section
// the argument is unnecessary; with several it is required, because guessing
// would edit a page the author didn't name.
func pickHTMLSection(sections []map[string]any, sectionArg any) (int, error) {
	var htmlIdx []int
	for i, m := range sections {
		if strings.EqualFold(strings.TrimSpace(mapStr(m, "kind")), "html") {
			htmlIdx = append(htmlIdx, i)
		}
	}
	switch {
	case len(htmlIdx) == 0:
		return 0, errors.New("this app has no html section to patch — patch_html edits a raw html canvas; use action=\"update\" for form/table/chart sections")
	case sectionArg == nil && len(htmlIdx) == 1:
		return htmlIdx[0], nil
	case sectionArg == nil:
		return 0, fmt.Errorf("this app has %d html sections — pass `section` (1-%d) to say which one to patch", len(htmlIdx), len(htmlIdx))
	}
	n := 0
	switch v := sectionArg.(type) {
	case float64:
		n = int(v)
	case int:
		n = v
	case string:
		fmt.Sscanf(strings.TrimSpace(v), "%d", &n)
	}
	if n < 1 || n > len(htmlIdx) {
		return 0, fmt.Errorf("section %d is out of range — this app has %d html section(s)", n, len(htmlIdx))
	}
	return htmlIdx[n-1], nil
}

// htmlSectionOrdinal renders a section's position among the HTML sections
// (1-based), which is the numbering the `section` argument uses.
func htmlSectionOrdinal(sections []map[string]any, idx int) int {
	n := 0
	for i, m := range sections {
		if strings.EqualFold(strings.TrimSpace(mapStr(m, "kind")), "html") {
			n++
			if i == idx {
				return n
			}
		}
	}
	return 1
}
