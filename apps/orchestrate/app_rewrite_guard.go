package orchestrate

// The guard on action=update for an html app.
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
