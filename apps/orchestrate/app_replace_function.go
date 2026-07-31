package orchestrate

// replace_function — swap one named function without reproducing any of the
// document.
//
// patch_html already exists and is the right shape for a constant or a
// one-line fix. It is the wrong shape for "rewrite drawChristmasTree", because
// the anchor an author must supply IS the function it is replacing: to change
// 40 lines it first has to reproduce those 40 lines byte-for-byte, whitespace
// and all, out of a tool result that the context window may since have elided.
// The observed failure is not subtle — a patch is refused for not matching,
// the author re-reads the app, patches again, is refused again, and eventually
// gives up and sends the WHOLE document through update, which is how a working
// game loses its game loop.
//
// The signal that this action was missing is in the transcript twice: patch
// calls carrying a complete rewritten function and NO find at all. The author
// knew what it wanted to say and the tool had no way to hear it. So: name the
// function, hand over the new one, and let the server find the old one.

import (
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// appDefReplaceFunction replaces a named function in an app's html section
// with the text the author supplies.
func (t *chatTurn) appDefReplaceFunction(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app — check the slug (app_def action=list)")
	}
	fn := strings.TrimSpace(stringArg(args, "function"))
	if fn == "" {
		return "", errors.New("function is required — the NAME of the function to replace, e.g. function=\"drawBird\". Read the app's html with app_def(action=\"get\") if you're not sure what it defines")
	}
	if !isJSFunctionName(fn) {
		return "", fmt.Errorf("%q is not a plain function name — pass just the identifier (e.g. \"drawBird\"), not a call, a signature, or a path", fn)
	}
	replace := stringArg(args, "replace")
	if strings.TrimSpace(replace) == "" {
		return "", errors.New("replace is required — the WHOLE new function, definition line included (e.g. \"function drawBird() { … }\"). To delete a function instead, use patch_html")
	}

	sections, err := appAuthoringSections(spec)
	if err != nil {
		return "", err
	}
	idx, err := pickHTMLSection(sections, args["section"])
	if err != nil {
		return "", err
	}
	prior := mapStr(sections[idx], "html")

	start, end, err := jsFunctionSpan(prior, fn)
	if err != nil {
		return "", err
	}
	// The replacement has to still define the function it is replacing.
	// Otherwise every call site in the rest of the page is now dangling, which
	// the diff below would catch anyway — but saying it HERE names the actual
	// mistake instead of listing its consequences.
	if !definesFunction(replace, fn) {
		return "", fmt.Errorf("the replacement text does not define %q — pass the WHOLE new function including its definition line (e.g. \"function %s(…) { … }\"), not just the body", fn, fn)
	}

	next := prior[:start] + strings.TrimRight(replace, "\n") + prior[end:]
	summary := fmt.Sprintf("Replaced function %s in html section %%d of %%q (revision %%s) — %d chars became %d.", fn, end-start, len(replace))
	return t.saveHTMLSectionEdit(spec, sections, idx, prior, next, summary, "replacement", "replace_function "+fn)
}

// definesFunction reports whether a fragment of JavaScript defines the named
// function in any of the forms jsFunctionSpan recognizes.
func definesFunction(fragment, name string) bool {
	wrapped := "<script>" + fragment + "</script>"
	for _, n := range jsDefinedFunctions(wrapped) {
		if n == name {
			return true
		}
	}
	return false
}

// isJSFunctionName reports whether s is a bare JavaScript identifier.
func isJSFunctionName(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isJSIdentByte(s[i]) {
			return false
		}
	}
	return true
}
