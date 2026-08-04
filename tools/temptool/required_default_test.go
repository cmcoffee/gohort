// What a toolbox action is required to be given when its author didn't say.
//
// The old answer was "all of it", and it was the largest source of tool errors
// in production: 371 in two days on one toolbox, ~360 of them a model bounced
// for omitting "cursor", "limit" or "sort" on a feed read. A cursor is the
// handle for the NEXT page — on a first call it cannot exist — so the action
// was uncallable by construction and the model had no way to find that out
// except by failing. It failed 260 times on one action.
package temptool

import (
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func params(names ...string) map[string]ToolParam {
	m := make(map[string]ToolParam, len(names))
	for _, n := range names {
		m[n] = ToolParam{Type: "string"}
	}
	return m
}

func TestOnlyWhatTheURLCannotBeBuiltWithoutIsRequired(t *testing.T) {
	// The failing shape. Nothing in the path, so nothing is mandatory: a feed
	// read with no arguments at all is the FIRST page, which is exactly what a
	// caller means when it says nothing.
	if got := defaultRequiredParams("https://api.example.com/feed", params("cursor", "limit", "sort")); len(got) != 0 {
		t.Errorf("pagination params must not be required, got %v", got)
	}

	// A path placeholder is required whatever the endpoint thinks — without it
	// there is no URL to request.
	got := defaultRequiredParams("https://api.example.com/posts/{post_id}/comments", params("post_id", "cursor", "limit"))
	if !slices.Equal(got, []string{"post_id"}) {
		t.Errorf("required = %v, want just the path placeholder", got)
	}

	// A placeholder that is not a declared param was never the caller's to
	// supply — it is spliced in from somewhere else.
	if got := defaultRequiredParams("https://api.example.com/{workspace}/feed", params("cursor")); len(got) != 0 {
		t.Errorf("an undeclared placeholder is not the caller's problem, got %v", got)
	}

	// Query-string placeholders are not path placeholders: the request still
	// resolves without them.
	if got := defaultRequiredParams("https://api.example.com/feed?sort={sort}", params("sort")); len(got) != 0 {
		t.Errorf("a query placeholder must not be required, got %v", got)
	}
}

func TestAStoredToolboxRepairsItselfOnRegistration(t *testing.T) {
	// Authoring-time defaults only help tools authored from now on. The ones
	// doing the damage are already saved.
	act := TempToolAction{
		Name:        "get_feed",
		URLTemplate: "https://api.example.com/feed",
		Params:      params("cursor", "limit", "sort"),
		Required:    []string{"cursor", "limit", "sort"}, // what the old default wrote
	}
	if got := liveRequired(act); len(got) != 0 {
		t.Errorf("a saved all-required feed read must be narrowed, got %v", got)
	}
}

func TestARepairNeverOverridesAnAuthorWhoSaidWhatTheyMeant(t *testing.T) {
	base := params("cursor", "limit", "sort")

	// Partial list — deliberate by construction, since the old default could
	// never have produced it.
	act := TempToolAction{Name: "get_feed", URLTemplate: "https://x/feed", Params: base, Required: []string{"sort"}}
	if got := liveRequired(act); !slices.Equal(got, []string{"sort"}) {
		t.Errorf("a partial list is the author speaking, got %v", got)
	}

	// Nothing required stays nothing required.
	act.Required = nil
	if got := liveRequired(act); len(got) != 0 {
		t.Errorf("no requirements must stay none, got %v", got)
	}

	// Every param IS a path placeholder: all-required is correct here, and
	// narrowing it would be a no-op that still churns the definition.
	act = TempToolAction{
		Name:        "get_comment",
		URLTemplate: "https://x/posts/{post_id}/comments/{comment_id}",
		Params:      params("post_id", "comment_id"),
		Required:    []string{"post_id", "comment_id"},
	}
	if got := liveRequired(act); len(got) != 2 {
		t.Errorf("genuinely-required params must survive, got %v", got)
	}

	// Required naming something that isn't a declared param is not the old
	// default's handiwork — leave it alone rather than guess.
	act = TempToolAction{Name: "odd", URLTemplate: "https://x/feed", Params: params("a"), Required: []string{"ghost"}}
	if got := liveRequired(act); !slices.Equal(got, []string{"ghost"}) {
		t.Errorf("an unrecognized shape must be left alone, got %v", got)
	}
}

func TestAWriteStillCarriesItsPayload(t *testing.T) {
	// The trap in narrowing `required`: the body scaffold used to be built FROM
	// required, so shrinking it to path ids would post a body holding the ids
	// and leave the content behind — a write that silently sends nothing.
	got := writeBodyParams("https://x/posts/{post_id}/reply", params("post_id", "content", "notify"))
	if !slices.Equal(got, []string{"content", "notify"}) {
		t.Errorf("body params = %v, want everything the URL doesn't already carry", got)
	}
	// Including the optional one: it drops out cleanly when omitted
	// (TestSubstituteJSONOptionalDrop), so carrying it costs nothing and NOT
	// carrying it makes it unsendable.
	tpl := scaffoldBodyTemplate(got)
	for _, want := range []string{"content", "notify"} {
		if !strings.Contains(tpl, "{"+want+"}") {
			t.Errorf("scaffolded body drops %q: %s", want, tpl)
		}
	}
	if strings.Contains(tpl, "{post_id}") {
		t.Errorf("the path id must not be duplicated into the body: %s", tpl)
	}
}
