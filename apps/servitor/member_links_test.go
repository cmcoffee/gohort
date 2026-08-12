package servitor

import (
	"strings"
	"testing"
)

// TestPruneMemberLinksDropsWhatCannotBeActedOn — same discipline as
// pruneMemberRoles: unchecking a member must not leave a link behind that
// resurfaces if it is re-added.
func TestPruneMemberLinksDropsWhatCannotBeActedOn(t *testing.T) {
	members := []string{"a", "b"}
	in := []MemberLink{
		{From: "a", Rel: MemberRelRuns, To: "b"},    // keep
		{From: "a", Rel: MemberRelRuns, To: "gone"}, // endpoint not a member
		{From: "gone", Rel: MemberRelRuns, To: "b"}, // endpoint not a member
		{From: "a", Rel: "invented", To: "b"},       // relation we cannot act on
		{From: "a", Rel: MemberRelRuns, To: "a"},    // self-link
		{From: "", Rel: MemberRelRuns, To: "b"},     // empty endpoint
		{From: "a", Rel: MemberRelRuns, To: "b"},    // duplicate
		{From: " a ", Rel: " runs ", To: " b "},     // duplicate after trimming
		{From: "b", Rel: MemberRelTalksTo, To: "a"}, // keep — different relation
	}
	got := pruneMemberLinks(in, members)
	if len(got) != 2 {
		t.Fatalf("kept %d links, want 2: %+v", len(got), got)
	}
	for _, l := range got {
		if l.From == l.To {
			t.Error("a self-link survived")
		}
		if !validMemberRel(l.Rel) {
			t.Errorf("an unknown relation survived: %q", l.Rel)
		}
	}
	if pruneMemberLinks(nil, members) != nil {
		t.Error("no links should stay nil, not an empty slice")
	}
	if pruneMemberLinks(in, nil) != nil {
		t.Error("links with no members should prune to nothing")
	}
}

// TestMemberLinkReadsBothWays — a link is declared once and the coordinator
// arrives at a member from whichever direction the question came, so the roster
// has to state it on BOTH members.
func TestMemberLinkReadsBothWays(t *testing.T) {
	ws := Appliance{MemberLinks: []MemberLink{
		{From: "dump", Rel: MemberRelCapturedFrom, To: "box"},
	}}
	name := func(id string) string { return strings.ToUpper(id) }

	fromEnd := memberLinkLines(ws, "dump", name)
	if len(fromEnd) != 1 || !strings.Contains(fromEnd[0], "was captured from BOX") {
		t.Errorf("from-end reads %q", fromEnd)
	}
	toEnd := memberLinkLines(ws, "box", name)
	if len(toEnd) != 1 || !strings.Contains(toEnd[0], "DUMP") {
		t.Errorf("to-end reads %q", toEnd)
	}
	if fromEnd[0] == toEnd[0] {
		t.Error("both ends render identically — one of them is stating the relation backwards")
	}
	if len(memberLinkLines(ws, "unrelated", name)) != 0 {
		t.Error("an unrelated member picked up a link")
	}
}

// TestEveryRelationRendersBothDirections — a relation with no inverse phrasing
// silently reads as "related to", which tells the lead nothing.
func TestEveryRelationRendersBothDirections(t *testing.T) {
	for _, rel := range []string{MemberRelRuns, MemberRelCodeFor, MemberRelCapturedFrom, MemberRelTalksTo} {
		if !validMemberRel(rel) {
			t.Errorf("%q is not accepted by validMemberRel", rel)
		}
		fwd, inv := memberRelLabel(rel), memberRelInverse(rel)
		if strings.TrimSpace(fwd) == "" || strings.TrimSpace(inv) == "" {
			t.Errorf("%q renders blank in one direction", rel)
		}
		if inv == "related to" {
			t.Errorf("%q has no inverse phrasing — it degrades to a useless label", rel)
		}
		if fwd == inv {
			t.Errorf("%q reads the same both ways, so direction is lost", rel)
		}
	}
	if validMemberRel("anything-else") {
		t.Error("an arbitrary relation was accepted")
	}
}

// TestRosterRendersMemberLinks — the lead routes on the roster, so a declared
// link that never reaches it might as well not exist.
func TestRosterRendersMemberLinks(t *testing.T) {
	ws := Appliance{MemberLinks: []MemberLink{
		{From: "b1", Rel: MemberRelCapturedFrom, To: "s1"},
	}}
	scouts := []memberScout{
		{Member: wsMember{ID: "b1", Rec: Appliance{Type: "bundle", Name: "Cust dump"}}},
		{Member: wsMember{ID: "s1", Rec: Appliance{Type: "ssh", Name: "web01", Host: "web01"}}},
	}
	out := scoutBlockFor(ws, scouts, nil)
	if !strings.Contains(out, "Linked:") {
		t.Fatalf("roster has no link line:\n%s", out)
	}
	if !strings.Contains(out, "was captured from web01") {
		t.Errorf("roster does not state the link from the dump's side:\n%s", out)
	}
	if !strings.Contains(out, "Cust dump") {
		t.Errorf("roster does not state the link from the system's side:\n%s", out)
	}
	// The plain roster (no workspace record) must still render.
	if plain := scoutBlock(scouts, nil); strings.Contains(plain, "Linked:") {
		t.Error("a roster with no workspace record invented links")
	}
}

// TestGraphRelFilterMatchesLoosely — a caller types "calls" and the recorder
// wrote "calls_into". An exact match would return nothing and read as "this has
// no connections", which is the wrong conclusion.
func TestGraphRelFilterMatchesLoosely(t *testing.T) {
	all := graphRelFilter("")
	if !all("anything") {
		t.Error("an empty filter should follow every relation")
	}
	calls := graphRelFilter("calls")
	for _, rel := range []string{"calls", "calls_into", "CALLS"} {
		if !calls(rel) {
			t.Errorf("filter %q rejected %q", "calls", rel)
		}
	}
	if calls("reads") {
		t.Error("the filter matched an unrelated relation")
	}
	// Spaces normalize to the underscore form the store uses.
	if !graphRelFilter("logged to")("logged_to") {
		t.Error("a spaced relation did not match its stored form")
	}
}

// TestDedupeLinesKeepsOrder — a bidirectional walk reaches the same edge from
// both ends, and printing it twice reads as two facts.
func TestDedupeLinesKeepsOrder(t *testing.T) {
	got := dedupeLines([]string{"a", "b", "a", "c", "b"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestMapToolsAreOnBothAllowLists — the worker AND the orchestrator get them,
// and servitor panics on an unlisted tool rather than erroring.
func TestMapToolsAreOnBothAllowLists(t *testing.T) {
	for _, name := range []string{"map_find", "map_neighbors", "map_path"} {
		if !servitorWorkerToolAllowList[name] {
			t.Errorf("%q missing from the worker allow-list", name)
		}
		if !servitorOrchestratorToolAllowList[name] {
			t.Errorf("%q missing from the orchestrator allow-list", name)
		}
	}
}

// TestMapToolsDegradeWithoutMemory — with orchestrate unwired every tool must
// say the map is unavailable and point at searching, not return an empty result
// that reads as "this thing has no connections".
func TestMapToolsDegradeWithoutMemory(t *testing.T) {
	tools := mapTools("some-appliance")
	if len(tools) != 3 {
		t.Fatalf("mapTools returned %d tools, want 3", len(tools))
	}
	for _, td := range tools {
		args := map[string]any{"name": "thing", "from": "a", "to": "b"}
		out, err := td.Handler(args)
		if err != nil {
			t.Errorf("%s errored with no memory wired: %v", td.Tool.Name, err)
			continue
		}
		if !strings.Contains(out, "unavailable") {
			t.Errorf("%s returned %q — it should say the map is unavailable rather than imply absence", td.Tool.Name, out)
		}
	}
}

// TestMapToolsRequireTheirArguments.
func TestMapToolsRequireTheirArguments(t *testing.T) {
	if _, err := mapFind("app", "  "); err == nil {
		t.Error("map_find accepted an empty name")
	}
	if _, err := mapNeighbors("app", "", 1, ""); err == nil {
		t.Error("map_neighbors accepted an empty name")
	}
	if _, err := mapPath("app", "a", ""); err == nil {
		t.Error("map_path accepted an empty destination")
	}
}
