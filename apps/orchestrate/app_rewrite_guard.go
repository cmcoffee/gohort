package orchestrate

// The guards on action=update: an html app being wiped, and a functional
// section being dropped. Both are the same shape of loss — an update that
// replaces more than the author meant to replace, and passes every check
// downstream because what survives is internally consistent.
//
// update replaces the page with whatever it is handed, and for a one-section
// html app that means the entire program. When an author has spent several
// rounds fighting failed patches, what it eventually sends through update is
// not the document plus a fix — it is as much of the document as it could
// reconstruct. Every gate downstream passed that: the JavaScript parsed, the
// page loaded, the browser reported no errors, because a canvas game with no
// game loop left is a blank canvas, and a blank canvas throws nothing. The
// save was announced as a success and the game was gone.
//
// Two signals separate that from a real rewrite, and neither needs to guess at
// intent. The document collapsed in size, or the code that survived still
// calls functions the new document no longer defines. The first is a heuristic
// and only fires on a sharp drop; the second is close to proof — nothing
// deliberately ships a page that calls a function it deleted.
//
// A refusal here costs an author one argument (confirm_rewrite:true) when it
// really is re-authoring from scratch. Not refusing costs the user their app,
// silently, with a success message on top.

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// appRewriteShrinkRatio is how much of the previous document has to survive
// before an update stops looking like an edit. Real rewrites of a working app
// land near the same size or larger; the wipe that motivated this guard kept
// under a third.
const appRewriteShrinkRatio = 0.6

// appRewriteMinChars is the size below which none of this applies. A small
// html section can legitimately halve, and there is little to lose either way.
const appRewriteMinChars = 1500

// appRewriteRisk reports why an update looks like it is destroying the app
// rather than revising it, or "" when it looks fine. The message is the
// refusal the author reads, so it names what was lost and what to do instead.
func appRewriteRisk(prior, next string) string {
	if len(prior) < appRewriteMinChars {
		return ""
	}
	if strings.TrimSpace(next) == "" {
		return fmt.Sprintf("this update removes the app's html entirely (%d chars → nothing), which would leave a blank page. If you meant to replace the document, send it; if you meant to change part of it, use replace_function or patch_html. Pass confirm_rewrite:true to go ahead anyway.", len(prior))
	}

	broke := jsNewDanglingCalls(prior, next)
	shrank := float64(len(next)) < float64(len(prior))*appRewriteShrinkRatio
	if len(broke) == 0 && !shrank {
		return ""
	}

	var b strings.Builder
	b.WriteString("that update was NOT saved — it looks like a partial rewrite rather than a revision, and the app still serves the previous revision.\n\n")
	if shrank {
		fmt.Fprintf(&b, "- The html went from %d chars to %d (%d%% of the original).\n", len(prior), len(next), len(next)*100/len(prior))
	}
	if len(broke) > 0 {
		fmt.Fprintf(&b, "- Nothing in the new document defines %s, but the code you sent still calls %s.\n",
			appNameList(broke, 8), pluralIt(len(broke)))
	}
	if dropped := appDroppedFunctions(prior, next); len(dropped) > 0 {
		fmt.Fprintf(&b, "- Functions present before and missing now: %s.\n", appNameList(dropped, 12))
	}
	b.WriteString("\nThis is the failure that looks like a success: the page still PARSES and still LOADS clean, so nothing downstream would have caught it — a canvas app runs no code until the user interacts, and by then the tool has already told you it worked.\n\n")
	b.WriteString("If you are changing PART of the app, don't send the document at all:\n")
	b.WriteString("  · replace_function {id, function:\"<name>\", replace:\"<whole new function>\"} — rewrites one function; you never reproduce the old text.\n")
	b.WriteString("  · patch_html {id, find, replace} — for a constant or a one-line fix.\n")
	b.WriteString("If you really are re-authoring this app from scratch and the document you sent is COMPLETE, send it again with confirm_rewrite:true — the version it replaces is kept either way, so app_def(action=\"revert\") can put it back.")
	return b.String()
}

// appDroppedFunctions lists functions the previous document defined and the
// new one does not, called or not — the plainest description of what an
// update deleted.
func appDroppedFunctions(prior, next string) []string {
	have := map[string]bool{}
	for _, n := range jsDefinedFunctions(next) {
		have[n] = true
	}
	var out []string
	for _, n := range jsDefinedFunctions(prior) {
		if !have[n] {
			out = append(out, n)
		}
	}
	return out
}

// appSpecHTMLText concatenates the html an app currently serves, across every
// html section, for comparison against what an update proposes.
func appSpecHTMLText(spec AppSpec) string {
	sections, err := appAuthoringSections(spec)
	if err != nil {
		return ""
	}
	raw := make([]any, len(sections))
	for i := range sections {
		raw[i] = sections[i]
	}
	return strings.Join(appHTMLSectionScripts(raw), "\n")
}

// appProposedHTMLText concatenates the html an update is proposing, in the
// same order, so the two are comparable.
func appProposedHTMLText(raw any) string {
	return strings.Join(appHTMLSectionScripts(raw), "\n")
}

// appNameList renders up to max names, saying how many more there are.
func appNameList(names []string, max int) string {
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(names[:max], ", "), len(names)-max)
}

func pluralIt(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// functionalSectionKinds are the section kinds that ARE what an app does. A
// form, a table, or a display presents data; these three RUN something — a
// multi-stage job, a conversation, a document workbench. Losing one does not
// degrade the page, it removes the reason the page exists.
var functionalSectionKinds = map[string]string{
	"pipeline":  "runs the app's pipeline (submit, live stages, run history)",
	"run":       "runs the app's pipeline (submit, live stages, run history)",
	"chat":      "the app's conversation with its agent",
	"workbench": "the app's list | document | chat surface",
}

// appDroppedFunctionSection reports an update that REMOVES a functional section
// the stored revision had, or "" when nothing load-bearing was lost.
//
// Same failure as the html wipe above, arrived at from the other direction. An
// author several rounds deep in fixing one part of a page re-sends the sections
// array from what it can reconstruct, and a section it stopped thinking about
// is simply absent. Every gate downstream passes: the page renders, verify
// reports the sections that remain, and it PASSES — a form and a table are a
// perfectly good page. The app just cannot do the thing it was built for, and
// the save says success.
//
// This is not hypothetical. An app built to run a five-pass writing pipeline
// had a working pipeline section, verified; the next update dropped it while
// the author narrated adding live progress. What shipped was a form, a table,
// and a button that set a status field.
//
// Only removals count. Adding, reordering, and re-titling are ordinary edits.
func appDroppedFunctionSection(prior, next []map[string]any) string {
	have := func(secs []map[string]any) map[string]bool {
		out := map[string]bool{}
		for _, m := range secs {
			k := strings.ToLower(strings.TrimSpace(mapStr(m, "kind")))
			if _, ok := functionalSectionKinds[k]; ok {
				out[k] = true
			}
		}
		return out
	}
	before, after := have(prior), have(next)
	var lost []string
	for kind := range before {
		if !after[kind] {
			lost = append(lost, kind)
		}
	}
	if len(lost) == 0 {
		return ""
	}
	sort.Strings(lost)
	var b strings.Builder
	b.WriteString("this update DROPS the section(s) that make the app work: ")
	for i, kind := range lost {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%q — %s", kind, functionalSectionKinds[kind])
	}
	b.WriteString(".\n\nupdate REPLACES the sections array, so a section you left out is a section you deleted. The page will still render and verify will still pass — a form and a table are a valid page — but the thing the app is FOR will be gone, and the save would have reported success.\n\n")
	b.WriteString("Send the sections you want plus the ones already there (app_def(action=\"get\") returns them in the shape update accepts). If you really do mean to remove it, send this again with confirm_rewrite:true — the version it replaces is kept either way, so app_def(action=\"revert\") can put it back.")
	return b.String()
}
