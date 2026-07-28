package orchestrate

import (
	"strings"
	"testing"
)

// Builder refused to build a game — "my current toolset, particularly app_def,
// is designed for building data-driven applications" — while app_def has an
// html section kind whose inline <script> runs, which is exactly how you build
// one. It had built games before.
//
// The cause was the description: the html kind sat last in the list, framed as
// a "LAST RESORT" with "prefer typed sections", so the model read the typed
// kinds as the boundary of what an app can BE and generalized "discouraged"
// into "impossible".
//
// These assertions pin the reframing, because it is the description alone that
// decides whether the capability is reachable.
func TestAppDefHTMLKindReadsAsCapableNotForbidden(t *testing.T) {
	low := strings.ToLower(appDefHelpText)

	// Discouraging phrasings the model generalized from.
	for _, banned := range []string{"last resort", "escape hatch"} {
		if strings.Contains(low, banned) {
			t.Errorf("app_def still describes the html kind as %q — the model reads that as a prohibition and refuses interactive apps outright", banned)
		}
	}
	// It must say plainly that script runs, or there is no signal the kind is
	// capable of anything beyond static markup.
	if !strings.Contains(low, "<script> runs") && !strings.Contains(low, "script> runs") {
		t.Error("description no longer states that inline <script> runs")
	}
}

// The refusal named specific things it believed impossible. The description
// should name them as buildable, so the model has a direct counter to the
// belief it formed.
func TestAppDefNamesInteractiveCapabilities(t *testing.T) {
	low := strings.ToLower(appDefHelpText)
	for _, want := range []string{"game", "canvas", "animation"} {
		if !strings.Contains(low, want) {
			t.Errorf("description never mentions %q — nothing contradicts \"I can't build that\"", want)
		}
	}
}

// The fit guidance must SURVIVE the reframing. Losing it would send Builder
// hand-rolling tables in raw HTML and throwing away the record store, editing,
// and refresh the typed sections provide.
func TestAppDefStillSteersDataAppsToTypedSections(t *testing.T) {
	low := strings.ToLower(appDefHelpText)
	if !strings.Contains(low, "typed section") {
		t.Error("lost the steer toward typed sections for data apps")
	}
	if !strings.Contains(low, "record store") {
		t.Error("lost the reason typed sections are better for data apps")
	}
}

// The HEADLINE description is what sits in the tool schema every single turn;
// the help text is only read when the model calls action="help". So the
// headline is where a scope belief actually forms.
//
// It used to open with "data-driven gohort APPS ... with no hand-written
// HTML/CSS/JS", and Builder refused a game twice on exactly that basis — even
// after the help text was fixed, because the headline framed everything it
// read afterward.
func TestAppDefHeadlineAdmitsInteractiveApps(t *testing.T) {
	desc := (&chatTurn{}).appDefToolDef().Tool.Description
	low := strings.ToLower(desc)

	if strings.Contains(low, "data-driven") {
		t.Error("headline still opens with \"data-driven\" — the model reads that as the boundary of what an app can be")
	}
	// The old phrasing flatly denied custom code. It may only be said about
	// the TYPED sections now.
	if i := strings.Index(low, "no hand-written html"); i >= 0 {
		window := low[max0(i-160):i]
		if !strings.Contains(window, "record store") && !strings.Contains(window, "declarative") {
			t.Error("\"no hand-written HTML\" is stated unqualified — it must be scoped to the typed sections, or it reads as a blanket prohibition")
		}
	}
	for _, want := range []string{"game", "canvas", "html"} {
		if !strings.Contains(low, want) {
			t.Errorf("headline never mentions %q — nothing tells the model an interactive app is buildable", want)
		}
	}
	// The refusal it produced named "out of scope" and "game engine". Counter
	// both directly, in the text that is always present.
	if !strings.Contains(low, "out of scope") || !strings.Contains(low, "game engine") {
		t.Error("headline should explicitly forbid the \"out of scope / needs a game engine\" refusal it kept producing")
	}
}

func max0(i int) int {
	if i < 0 {
		return 0
	}
	return i
}

// sectionsParamDescription returns the always-on schema text for the
// `sections` parameter — the authoritative statement of what a caller may
// actually pass.
func sectionsParamDescription(t *testing.T) string {
	t.Helper()
	p, ok := (&chatTurn{}).appDefToolDef().Tool.Parameters["sections"]
	if !ok {
		t.Fatal("app_def has no `sections` parameter")
	}
	return p.Description
}

// THE REGRESSION, second half. v0.5.564 compressed the sections spec into a
// one-line kind list and `html` was dropped from it, so Builder — which had
// built a flappy-bird clone before that commit — refused a side-scroller.
//
// The parameter schema outranks prose: a headline saying "write it as an html
// section" while the schema enumerates kinds WITHOUT html is a contradiction,
// and the schema is what the model treats as the set of legal values.
func TestSectionsSchemaListsEveryRenderableKind(t *testing.T) {
	desc := sectionsParamDescription(t)
	low := strings.ToLower(desc)
	// Every kind buildSection actually accepts must be listed, or it is
	// unreachable in practice.
	for _, kind := range []string{"form", "table", "display", "chart", "chat", "workbench", "html", "actions", "empty"} {
		if !strings.Contains(low, `"`+kind+`"`) {
			t.Errorf("kind %q is renderable but missing from the always-on sections schema — the model will not know it can pass it", kind)
		}
	}
}

// html must arrive with enough to USE it: the field name, and that script
// runs. A bare mention would leave Builder knowing the kind exists but not how
// to author one without calling help.
func TestSectionsSchemaMakesHTMLUsable(t *testing.T) {
	low := strings.ToLower(sectionsParamDescription(t))
	if !strings.Contains(low, "`html`") {
		t.Error("the html kind is listed but its `html` field is not named")
	}
	if !strings.Contains(low, "script") {
		t.Error("schema never says inline script runs — the one fact that makes a game possible")
	}
	if !strings.Contains(low, "game") {
		t.Error("schema should name a game as the case html exists for")
	}
}
