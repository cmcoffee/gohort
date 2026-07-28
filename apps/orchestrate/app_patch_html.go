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
		return "", errors.New("find is required — the EXACT text to replace, copied from the app's current html (app_def action=get). Include enough surrounding lines to be unique")
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
	patched, err := applyHTMLPatch(sections, idx, find, replace, spec.Slug)
	if err != nil {
		return "", err
	}
	sections[idx]["html"] = patched

	// Parse BEFORE saving. A patch that doesn't compile never reaches the
	// stored app — the author still has the old revision serving.
	if problems, checked := htmlScriptSyntaxProblems(patched); checked && len(problems) > 0 {
		return "", fmt.Errorf("that patch would break the page's JavaScript, so it was NOT applied — the app still serves the previous revision:\n- %s\n\nFix the replacement text and patch again", strings.Join(problems, "\n- "))
	}

	raw := make([]any, len(sections))
	for i := range sections {
		raw[i] = sections[i]
	}
	page, err := buildAppPage(spec, raw)
	if err != nil {
		return "", fmt.Errorf("patched sections no longer build a page: %w", err)
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
	saved := SaveAppSpec(spec)

	// Now the accurate check, against the revision that was just written. On
	// failure put the previous revision back rather than leaving a dead app.
	if errs := appPageRuntimeErrors(t.user, saved.Slug); len(errs) > 0 {
		SaveAppSpec(before)
		return "", fmt.Errorf("that patch broke the page in a real browser, so it was ROLLED BACK — the app is serving the previous revision again:\n- %s\n\nFix the replacement text and patch again",
			strings.Join(errs, "\n- "))
	}
	return fmt.Sprintf("Patched html section %d of %q (revision %s) — replaced %d chars with %d. The page was parsed and loaded in a real browser after the change and came up clean, so there is nothing further to verify. Tell the user what changed.",
		htmlSectionOrdinal(sections, idx), saved.Name, saved.Updated, len(find), len(replace)), nil
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
