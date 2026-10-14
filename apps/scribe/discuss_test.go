package scribe

import (
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/textutil"
)

// A discussion's Author can look things up but not change the guide: every
// tool that writes is withheld, and a Private guide also loses what reaches
// the network or executes.
func TestADiscussionCannotChangeTheGuide(t *testing.T) {
	kit := []AgentToolDef{
		{Tool: Tool{Name: "edit_section", Caps: []Capability{CapWrite}}},
		{Tool: Tool{Name: "add_image", Caps: []Capability{CapWrite}}},
		{Tool: Tool{Name: "list_sections"}},
		{Tool: Tool{Name: "search_knowledge"}},
		{Tool: Tool{Name: "research", Caps: []Capability{CapNetwork}}},
		{Tool: Tool{Name: "investigate_box", Caps: []Capability{CapNetwork, CapExecute}}},
		{Tool: Tool{Name: "search_box_knowledge", Caps: []Capability{CapRead}}},
	}
	names := func(defs []AgentToolDef) string {
		var out []string
		for _, d := range defs {
			out = append(out, d.Tool.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names(discussTools(kit, false)); got != "list_sections,search_knowledge,research,investigate_box,search_box_knowledge" {
		t.Errorf("open guide: %s", got)
	}
	if got := names(discussTools(kit, true)); got != "list_sections,search_knowledge,search_box_knowledge" {
		t.Errorf("Private guide: %s", got)
	}
}

// The first turn frames the discussion (the guide, the section, what it must
// not repeat, the brief and Rules, the discuss-first reply contract); later
// turns carry only the current text and what the person said.
func TestADiscussionTurnIsFramedOnce(t *testing.T) {
	g := Guide{Title: "Dump Triage", Sections: []Section{
		{ID: "s1", Title: "Reading a dump", Order: 1},
		{ID: "s2", Title: "License timeouts", Markdown: "Old text.", Order: 2},
	}}
	first := discussTurn{guide: g, section: g.Sections[1], title: "License timeouts", message: "Ask me first.",
		draft: "Old text.", first: true, context: "THIS GUIDE'S WRITING BRIEF: lessons only."}.prompt()
	for _, want := range []string{`one section of the guide "Dump Triage"`, `The section is "License timeouts"`,
		"should not repeat: Reading a dump.", "lessons only", textutil.DiscussReplyContract, "Old text.", "## The author says\n\nAsk me first."} {
		if !strings.Contains(first, want) {
			t.Errorf("the first turn lacks %q", want)
		}
	}
	if strings.Contains(first, "should not repeat: Reading a dump; License timeouts") {
		t.Error("the section was listed among the ones it must not repeat")
	}
	later := discussTurn{guide: g, section: g.Sections[1], title: "License timeouts", message: "Go ahead.", draft: "Edited."}.prompt()
	if strings.Contains(later, textutil.DiscussReplyContract) || !strings.Contains(later, "Edited.") || !strings.HasSuffix(later, "Go ahead.") {
		t.Errorf("a later turn should carry the current text and the message only:\n%s", later)
	}
	fresh := discussTurn{guide: g, title: "Disk full", message: "Hi", first: true}.prompt()
	if !strings.Contains(fresh, `a NEW one, not written yet; its working title is "Disk full"`) || !strings.Contains(fresh, "(empty: nothing written yet)") {
		t.Errorf("a new section's first turn:\n%s", fresh)
	}
}

// The endpoint takes a POST with something to say, on a guide the caller may
// edit, about a section that is still there.
func TestTheDiscussEndpointChecksItsRequest(t *testing.T) {
	src, udb, g := guideSourceFixture(t)
	call := func(method, url, body string) int {
		w := httptest.NewRecorder()
		src.app.handleSectionDiscuss(w, httptest.NewRequest(method, url, strings.NewReader(body)), udb, "u")
		return w.Code
	}
	if c := call("GET", "/section/discuss?guide="+g.ID, ""); c != 405 {
		t.Errorf("GET: %d", c)
	}
	if c := call("POST", "/section/discuss?guide=nope", `{"message":"hi"}`); c != 404 {
		t.Errorf("unknown guide: %d", c)
	}
	if c := call("POST", "/section/discuss?guide="+g.ID, `{"message":"  "}`); c != 400 {
		t.Errorf("nothing said: %d", c)
	}
	if c := call("POST", "/section/discuss?guide="+g.ID+"&section=gone", `{"message":"hi"}`); c != 404 {
		t.Errorf("a section no longer there: %d", c)
	}
}

// The contract asks before it writes, and its draft block is the one
// SplitDraftReply reads, so a discussion's text comes back like any assist.
func TestTheDiscussContractWritesInTheSameBlock(t *testing.T) {
	if !strings.Contains(textutil.DiscussReplyContract, textutil.DraftFence) || !strings.Contains(textutil.DiscussReplyContract, "Do not write the text yet") {
		t.Error("the discuss contract does not ask first or does not use the draft fence")
	}
	reply, value := textutil.SplitDraftReply("Here it is.\n\n" + textutil.DraftFence + "\nThe lesson.\n```")
	if reply != "Here it is." || value != "The lesson." {
		t.Errorf("split = %q / %q", reply, value)
	}
}

// Discussing the brief: the Author is told what a brief is for and who
// follows it, sees the guide's sections, and writes the brief within its
// limit. It gets the user's Rules but not the current brief as an
// instruction, since that is the text being rewritten.
func TestTheBriefIsDiscussedAsTheGuidesStandingInstruction(t *testing.T) {
	g := Guide{Title: "Dump Triage", Brief: "Old brief.", Sections: []Section{{ID: "s1", Title: "Reading a dump", Order: 1}}}
	p := discussTurn{guide: g, brief: true, title: "Writing brief", message: "Ask me first.", draft: "Old brief.",
		first: true, context: "USER RULES: no names."}.prompt()
	for _, want := range []string{`write its WRITING BRIEF`, "the Guide Author when the author edits it, and the Guide Curator",
		"The guide's sections so far: Reading a dump.", "USER RULES: no names.", textutil.DiscussReplyContract,
		"## The brief's current text", "Old brief."} {
		if !strings.Contains(p, want) {
			t.Errorf("the brief's first turn lacks %q", want)
		}
	}
	if strings.Contains(p, "WRITING BRIEF, from its owner") {
		t.Error("the current brief was handed over as an instruction")
	}
}

// Only the owner discusses the brief, as only the owner saves it.
func TestTheBriefDiscussEndpointIsTheOwners(t *testing.T) {
	src, udb, g := guideSourceFixture(t)
	call := func(method, body, user string) int {
		w := httptest.NewRecorder()
		reqUDB := udb
		if user != "u" {
			reqUDB = UserDB(src.app.DB, user) // a request runs on its own user's store
		}
		src.app.handleBriefDiscuss(w, httptest.NewRequest(method, "/brief/discuss?id="+g.ID, strings.NewReader(body)), reqUDB, user)
		return w.Code
	}
	if c := call("GET", "", "u"); c != 405 {
		t.Errorf("GET: %d", c)
	}
	if c := call("POST", `{"message":" "}`, "u"); c != 400 {
		t.Errorf("nothing said: %d", c)
	}
	if c := call("POST", `{"message":"hi"}`, "someone-else"); c != 403 && c != 404 {
		t.Errorf("another user discussed the owner's brief: %d", c)
	}
}
