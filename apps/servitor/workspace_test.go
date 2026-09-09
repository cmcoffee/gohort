package servitor

import (
	"context"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bundle"
	"github.com/cmcoffee/snugforge/kvlite"
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

func member(rec Appliance) wsMember {
	return wsMember{ID: "m1", Rec: rec, Owner: "u"}
}

// TestMemberKindDistinguishesEveryType — Kind is not cosmetic. The lead routes
// on it, the scout picks its cheap pass from it, and the search tools refuse a
// member of the wrong kind. Folding every non-repo type into "system" told the
// lead a log dump was a live host, which is wrong in the one direction that
// matters: a host can be re-queried and a dump cannot.
func TestMemberKindDistinguishesEveryType(t *testing.T) {
	cases := map[string]string{
		"repo":    "repo",
		"bundle":  "evidence",
		"toolset": "service",
		"ssh":     "system",
		"command": "system",
		"":        "system",
	}
	seen := map[string]bool{}
	for typ, want := range cases {
		got := member(Appliance{Type: typ}).Kind()
		if got != want {
			t.Errorf("type %q → kind %q, want %q", typ, got, want)
		}
		seen[got] = true
	}
	if len(seen) != 4 {
		t.Errorf("only %d distinct kinds; the lead cannot tell the types apart", len(seen))
	}
}

// TestKindNoteCarriesTheConsequence — a bare label does not help the lead route.
// "evidence" is only useful if it also knows the evidence is fixed.
func TestKindNoteCarriesTheConsequence(t *testing.T) {
	note := member(Appliance{Type: "bundle"}).KindNote()
	if !strings.Contains(strings.ToLower(note), "cannot be re-queried") {
		t.Errorf("evidence note does not say the snapshot is fixed: %q", note)
	}
	note = member(Appliance{Type: "toolset"}).KindNote()
	if !strings.Contains(note, "no shell") {
		t.Errorf("service note does not say what it cannot reach: %q", note)
	}
	for _, typ := range []string{"repo", "bundle", "toolset", "ssh"} {
		if strings.TrimSpace(member(Appliance{Type: typ}).KindNote()) == "" {
			t.Errorf("type %q has no kind note", typ)
		}
	}
}

// TestMemberTargetIsNeverBlankForTheNewTypes — two similarly-named members are
// told apart by Target. Returning Rec.Host for a bundle or toolset produced an
// empty column and made them indistinguishable in the roster.
func TestMemberTargetIsNeverBlankForTheNewTypes(t *testing.T) {
	if got := member(Appliance{Type: "bundle", BundleSources: []string{"dump.tar.gz"}}).Target(); got != "dump.tar.gz" {
		t.Errorf("bundle target = %q", got)
	}
	if got := member(Appliance{Type: "bundle"}).Target(); got == "" {
		t.Error("an empty bundle still needs a target label")
	}
	if got := member(Appliance{Type: "toolset", Domain: "A GitLab project"}).Target(); got != "A GitLab project" {
		t.Errorf("toolset target = %q", got)
	}
	if got := member(Appliance{Type: "toolset"}).Target(); got == "" {
		t.Error("a toolset with no domain still needs a target label")
	}
	// Unchanged for the existing types.
	if got := member(Appliance{Type: "command", Command: "kubectl"}).Target(); got != "kubectl" {
		t.Errorf("command target = %q", got)
	}
	if got := member(Appliance{Type: "ssh", Host: "web01"}).Target(); got != "web01" {
		t.Errorf("ssh target = %q", got)
	}
}

// TestScoutBlockRendersKindAndNote — the roster is what the lead routes on.
func TestScoutBlockRendersKindAndNote(t *testing.T) {
	scouts := []memberScout{
		{Member: wsMember{ID: "b1", Rec: Appliance{Type: "bundle", Name: "Cust dump"}, Owner: "u"}},
		{Member: wsMember{ID: "t1", Rec: Appliance{Type: "toolset", Name: "GitLab", Domain: "A GitLab project"}, Owner: "u"}},
	}
	out := scoutBlock(scouts, nil)
	for _, want := range []string{"evidence", "service", "cannot be re-queried", "A GitLab project"} {
		if !strings.Contains(out, want) {
			t.Errorf("roster is missing %q:\n%s", want, out)
		}
	}
}

// TestScoutBlockLabelsEvidenceHitsAsLogs — calling a dump's matches "Code
// matches" tells the lead it is reading source.
func TestScoutBlockLabelsEvidenceHits(t *testing.T) {
	scouts := []memberScout{{
		Member: wsMember{ID: "b1", Rec: Appliance{Type: "bundle", Name: "Dump"}, Owner: "u"},
		Hits:   []repoSearchHit{{Path: "var/log/app.log", Line: 12, Text: "connection refused"}},
	}}
	out := scoutBlock(scouts, nil)
	if !strings.Contains(out, "Log matches") {
		t.Errorf("evidence hits are labelled as code:\n%s", out)
	}
	if strings.Contains(out, "Code matches") {
		t.Errorf("evidence hits still carry the code label:\n%s", out)
	}
}

// TestRegexpQuoteMetaProtectsTheCheapPass — scout terms come from the user's
// question. An unescaped "(" would error, and at scout time an error reads as
// "this member has nothing", which is the one conclusion the cheap pass must
// never reach by accident.
func TestRegexpQuoteMetaProtectsTheCheapPass(t *testing.T) {
	for _, term := range []string{"foo(bar", "a*b", "x[", "why?"} {
		res, err := bundle.Open("no-such-user", "no-such-bundle").Search(context.Background(), bundle.Query{Pattern: regexpQuoteMeta(term)})
		// No store, so no hits — but it must not be a REGEX error.
		if err != nil && strings.Contains(err.Error(), "not a valid regular expression") {
			t.Errorf("term %q was not escaped: %v", term, err)
		}
		_ = res
	}
}

// TestReadOnlyDrillWithholdsAskTools — a workspace drill answers every
// confirmation with a denial, so an "ask" tool would be offered, planned around,
// called, and refused. Withholding it up front tells the worker the truth before
// it builds a plan on a tool it cannot use.
func TestReadOnlyDrillWithholdsAskTools(t *testing.T) {
	a := Appliance{Toolset: []ToolBinding{
		{Name: "gitlab_close_mr", Posture: PostureAsk, BodyHash: "deadbeef"},
	}}
	rt := resolveToolset(WithReadOnlyDrill(context.Background()), "u", "u", a)
	if len(rt.Withheld) != 1 {
		t.Fatalf("withheld = %v, want the ask-posture tool held back", rt.Withheld)
	}
	if !strings.Contains(rt.Withheld[0], "per-call approval") {
		t.Errorf("withheld reason does not explain itself: %q", rt.Withheld[0])
	}
	if !strings.Contains(rt.Withheld[0], "open this system directly") {
		t.Errorf("withheld reason does not say what the operator can do instead: %q", rt.Withheld[0])
	}
}

// TestDrillReadOnlyMarkerRoundTrips.
func TestDrillReadOnlyMarker(t *testing.T) {
	if drillIsReadOnly(context.Background()) {
		t.Error("a plain context reported itself as a read-only drill")
	}
	if !drillIsReadOnly(WithReadOnlyDrill(context.Background())) {
		t.Error("the marker did not survive the context")
	}
	if drillIsReadOnly(nil) {
		t.Error("a nil context should not report as a drill")
	}
}

// TestSearchEvidenceIsOnTheWorkspaceAllowList — the coordinator's guard panics
// on an unlisted tool, so adding one to the tool list without the allow-list
// entry takes down every workspace session.
func TestSearchEvidenceIsOnTheWorkspaceAllowList(t *testing.T) {
	for _, name := range []string{"investigate_member", "investigate_cluster", "search_code", "search_evidence"} {
		if !servitorWorkspaceToolAllowList[name] {
			t.Errorf("%q is not on the workspace allow-list — the coordinator would panic", name)
		}
	}
}

// A workspace exists to be a handle for the machines inside it. Connecting an
// agent to one and then refusing every machine in it by name is the grant
// appearing not to work: the general question succeeds and the obvious
// follow-up — "and what about lab-box specifically?" — fails.

// estate builds a workspace with two members plus an unrelated machine.
func estate(t *testing.T) Database {
	t.Helper()
	udb := &DBase{Store: kvlite.MemStore()}
	for _, a := range []Appliance{
		{ID: "ws-1", Name: "Lab Estate", Type: "workspace", Members: []string{"box-1", "box-2"}},
		{ID: "box-1", Name: "lab-box", Type: "ssh"},
		{ID: "box-2", Name: "db-box", Type: "ssh"},
		{ID: "other", Name: "unrelated", Type: "ssh"},
	} {
		udb.Set(applianceTable, a.ID, a)
	}
	return udb
}

// TestAWorkspaceGrantReachesItsMembers — the ask the user made.
func TestAWorkspaceGrantReachesItsMembers(t *testing.T) {
	udb := estate(t)
	SaveCommandGrant(udb, "agent-1", "ws-1", nil)

	for _, id := range []string{"ws-1", "box-1", "box-2"} {
		if !applianceAskableForAgent(udb, "agent-1", id) {
			t.Errorf("connected to the workspace but cannot ask about %q", id)
		}
	}
	// Membership is not a wildcard: a machine outside the workspace stays out.
	if applianceAskableForAgent(udb, "agent-1", "other") {
		t.Error("a workspace grant reached a machine that is not in it")
	}
	// And an agent with no grant at all reaches nothing.
	if applianceAskableForAgent(udb, "agent-2", "box-1") {
		t.Error("an unconnected agent may ask about a machine")
	}
}

// TestMembershipGrantsQuestionsNotShells — connecting is not permitting, and
// putting two boxes in a workspace together is not a decision to hand an agent
// a shell on both.
func TestMembershipGrantsQuestionsNotShells(t *testing.T) {
	udb := estate(t)
	SaveCommandGrant(udb, "agent-1", "ws-1", nil)

	if !applianceAskableForAgent(udb, "agent-1", "box-1") {
		t.Fatal("the member is not askable")
	}
	if applianceEnabledForAgent(udb, "agent-1", "box-1") {
		t.Error("a workspace grant made a member CONNECTED — approved command tools " +
			"on that machine would be inherited by an agent nobody connected to it")
	}
	// A direct grant on the member does both, as it always did.
	SaveCommandGrant(udb, "agent-1", "box-1", nil)
	if !applianceEnabledForAgent(udb, "agent-1", "box-1") {
		t.Error("a direct grant on the member did not connect it")
	}
}

// TestTheQuestionToolListsWhatItAccepts — the failure this pairing prevents.
// The handler re-checks the grant, so a list built from a different rule would
// name machines the same tool then refuses. Same defect as a picker offering an
// option the server rejects.
func TestTheQuestionToolListsWhatItAccepts(t *testing.T) {
	udb := estate(t)
	SaveCommandGrant(udb, "agent-1", "ws-1", nil)

	var listed []Appliance
	for _, id := range []string{"ws-1", "box-1", "box-2", "other"} {
		var a Appliance
		if udb.Get(applianceTable, id, &a) && applianceAskableForAgent(udb, "agent-1", a.ID) {
			listed = append(listed, a)
		}
	}
	if len(listed) != 3 {
		t.Fatalf("listed %d systems, want the workspace and its two members", len(listed))
	}
	for _, a := range listed {
		if !applianceAskableForAgent(udb, "agent-1", a.ID) {
			t.Errorf("%q is listed but would be refused by the handler", a.ID)
		}
	}
}

// TestAMissingMemberIsSkipped — a workspace can name a machine that was since
// deleted, and a dangling id must not become a reachable phantom.
func TestAMissingMemberIsSkipped(t *testing.T) {
	udb := estate(t)
	var ws Appliance
	udb.Get(applianceTable, "ws-1", &ws)
	ws.Members = append(ws.Members, "deleted-box")
	udb.Set(applianceTable, ws.ID, ws)
	SaveCommandGrant(udb, "agent-1", "ws-1", nil)

	if applianceAskableForAgent(udb, "agent-1", "") {
		t.Error("an empty appliance id resolved as askable")
	}
	// The dangling id is "askable" by membership but resolves to no appliance,
	// so findAppliance refuses it first — the important thing is that nothing
	// panics and the real members still work.
	if !applianceAskableForAgent(udb, "agent-1", "box-1") {
		t.Error("a dangling member id broke resolution for the real ones")
	}
}

// TestScoutTerms guards the search-term reduction: searchRepo is a substring
// match, so leaving stop words in makes every repo match every question.
func TestScoutTerms(t *testing.T) {
	got := scout_terms("Why does the checkout service log 'upstream timed out' on node-2?")
	joined := strings.Join(got, " ")
	for _, want := range []string{"checkout", "service", "upstream", "timed", "node-2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected term %q in %v", want, got)
		}
	}
	for _, unwanted := range []string{"why", "does", "the", "on"} {
		for _, g := range got {
			if g == unwanted {
				t.Errorf("stop word %q survived: %v", unwanted, got)
			}
		}
	}
	// Duplicates collapse, and the list stays bounded.
	if terms := scout_terms(strings.Repeat("alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu ", 3)); len(terms) > 12 {
		t.Errorf("term list should cap at 12, got %d", len(terms))
	}
	if terms := scout_terms("nginx nginx nginx"); len(terms) != 1 {
		t.Errorf("duplicate terms should collapse, got %v", terms)
	}
}

// TestExtractValues pins what counts as a comparable value between nodes.
func TestExtractValues(t *testing.T) {
	vals := extract_values("nginx 1.24.0 listens on 10.0.0.7:8080 and reads /etc/nginx/nginx.conf")
	for _, want := range []string{"1.24.0", "10.0.0.7:8080", "/etc/nginx/nginx.conf"} {
		if !vals[want] {
			t.Errorf("expected %q among extracted values %v", want, vals)
		}
	}
	// Bare prose contributes nothing to a comparison.
	if got := extract_values("the service is running and healthy"); len(got) != 0 {
		t.Errorf("prose should yield no comparable values, got %v", got)
	}
}

// TestDivergenceReport is the cluster payoff: three nodes answering the same
// question should surface what one of them does not share.
func TestDivergenceReport(t *testing.T) {
	if got := divergence_report(map[string]string{"only-node": "/etc/app.conf"}); got != "" {
		t.Errorf("a single node has nothing to compare against, got %q", got)
	}

	agree := divergence_report(map[string]string{
		"node-1": "nginx 1.24.0 at /etc/nginx/nginx.conf",
		"node-2": "nginx 1.24.0 at /etc/nginx/nginx.conf",
	})
	if !strings.Contains(agree, "no divergence detected") {
		t.Errorf("identical reports should read as agreement, got:\n%s", agree)
	}

	drift := divergence_report(map[string]string{
		"node-1": "nginx 1.24.0, config /etc/nginx/conf.d/app.conf",
		"node-2": "nginx 1.24.0, config /etc/nginx/conf.d/app.conf",
		"node-3": "nginx 1.18.0, config /etc/nginx/conf.d/app.conf",
	})
	if !strings.Contains(drift, "1.18.0") || !strings.Contains(drift, "node-3") {
		t.Errorf("the odd node's version should be called out, got:\n%s", drift)
	}
	if !strings.Contains(drift, "NOT reported by node-1, node-2") {
		t.Errorf("1.18.0 should be marked absent from the other two, got:\n%s", drift)
	}
	// The shared config path agreed everywhere, so it must not be listed as a
	// difference — a comparison that flags everything flags nothing.
	for _, line := range strings.Split(drift, "\n") {
		if strings.HasPrefix(line, "- `/etc/nginx/conf.d/app.conf`") {
			t.Errorf("a value present on every node must not appear as a difference: %s", line)
		}
	}
	// Findings are framed as leads, per the grounding rule.
	if !strings.Contains(drift, "LEADS, not conclusions") {
		t.Errorf("divergence output must not read as verified fact, got:\n%s", drift)
	}
}

// TestFindMember covers how the lead addresses members: by ID, or by the name
// it read off the roster (whose casing it will not always reproduce).
func TestFindMember(t *testing.T) {
	members := []wsMember{
		{ID: "a1", Rec: Appliance{Name: "Node One", Type: "ssh"}},
		{ID: "b2", Rec: Appliance{Name: "orchestrator", Type: "repo"}},
		{ID: "c3", Rec: Appliance{Type: "ssh"}}, // unnamed — falls back to ID
	}
	for _, ref := range []string{"a1", "Node One", "node one", "NODE ONE"} {
		m, ok := findMember(members, ref)
		if !ok || m.ID != "a1" {
			t.Errorf("findMember(%q) = %+v, %v; want a1", ref, m, ok)
		}
	}
	if m, ok := findMember(members, "c3"); !ok || m.Name() != "c3" {
		t.Errorf("an unnamed member should fall back to its ID, got %+v %v", m, ok)
	}
	if _, ok := findMember(members, "does-not-exist"); ok {
		t.Error("unknown reference should not resolve")
	}
	if _, ok := findMember(members, ""); ok {
		t.Error("empty reference should not resolve")
	}
	// An ID match wins over a name match so an appliance named after another's
	// ID can't hijack the dispatch.
	shadow := []wsMember{
		{ID: "x", Rec: Appliance{Name: "y"}},
		{ID: "y", Rec: Appliance{Name: "z"}},
	}
	if m, _ := findMember(shadow, "y"); m.ID != "y" {
		t.Errorf("ID match must take precedence over name match, got %q", m.ID)
	}
}

// TestStringList covers the array argument shapes an LLM actually sends.
func TestStringList(t *testing.T) {
	if got := stringList([]any{"a", "b"}); len(got) != 2 || got[0] != "a" {
		t.Errorf("[]any of strings should coerce, got %v", got)
	}
	if got := stringList("solo"); len(got) != 1 || got[0] != "solo" {
		t.Errorf("a bare string should coerce to a one-element list, got %v", got)
	}
	if got := stringList([]any{"a", 7, "", "  b  "}); len(got) != 2 || got[1] != "b" {
		t.Errorf("non-strings and blanks should drop, values should trim, got %v", got)
	}
	if got := stringList(nil); got != nil {
		t.Errorf("nil should coerce to nil, got %v", got)
	}
}

// TestRosterShowsRolesUnconditionally is the heterogeneous-cluster fix: a
// function that lives on exactly one node has to be visible to the lead even
// when the question never mentions it. Filtering the roster by question match
// is what made single-node functions invisible.
func TestRosterShowsRolesUnconditionally(t *testing.T) {
	scouts := []memberScout{
		{
			Member: wsMember{ID: "n1", Rec: Appliance{Name: "node-1", Type: "ssh", Host: "10.0.0.1"}},
			Role:   "scheduler + primary DB",
			// Score 0: nothing about this question matched. It must still appear.
		},
		{
			Member:     wsMember{ID: "n2", Rec: Appliance{Name: "node-2", Type: "ssh", Host: "10.0.0.2"}},
			Capability: "nginx 1.24.0; app-server on :8080",
			Score:      2,
			Docs:       []scoutDoc{{Name: "services", Age: "94 days ago — STALE, re-verify"}},
		},
		{
			Member: wsMember{ID: "n3", Rec: Appliance{Name: "node-3", Type: "ssh", Host: "10.0.0.3"}},
		},
	}
	out := scoutBlock(scouts, nil)

	if !strings.Contains(out, "scheduler + primary DB") {
		t.Error("a role must render even when the member scored no matches — that is the whole point of declaring it")
	}
	if !strings.Contains(out, "nginx 1.24.0; app-server on :8080") {
		t.Error("derived capability missing from the roster")
	}
	for _, id := range []string{"n1", "n2", "n3"} {
		if !strings.Contains(out, id) {
			t.Errorf("member %s missing from the roster", id)
		}
	}
	// Staleness has to reach the lead, or it answers confidently from a map of
	// how the system used to work.
	if !strings.Contains(out, "STALE, re-verify") {
		t.Error("doc age/staleness not surfaced in the roster")
	}
	// A member with neither a role nor a map should say so rather than looking
	// like a member that was checked and found irrelevant.
	if !strings.Contains(out, "Role not declared and nothing mapped yet") {
		t.Error("an unmapped, role-less member should be called out as unknown, not silently blank")
	}
	// The lead needs to be told how to read these fields.
	if !strings.Contains(out, "Members are NOT interchangeable") {
		t.Error("roster header must tell the lead to route on role")
	}
}

// TestMemberCapability covers the derived summary: structure lines from the
// member's own map, prose ignored, bounded.
func TestMemberCapability(t *testing.T) {
	// Exercised through the line-picking logic via a fake doc body. Storage is
	// covered by the appliance tests; what matters here is the extraction.
	body := `[Last updated: 3 days ago]

Some prose about this host that should not be quoted.

- **nginx** 1.24.0 listening on :443
- postgres 15 primary
* redis 7
# Scheduler
- cron: nightly-rollup
- extra-one
- extra-two
- extra-three-should-be-dropped`
	got := extract_capability_lines(body)
	if len(got) != capability_lines {
		t.Fatalf("expected %d lines, got %d: %v", capability_lines, len(got), got)
	}
	if strings.Contains(strings.Join(got, " "), "prose about this host") {
		t.Error("prose paragraphs should not be quoted as capabilities")
	}
	if strings.Contains(strings.Join(got, " "), "Last updated") {
		t.Error("the age header is not a capability")
	}
	if got[0] != "nginx 1.24.0 listening on :443" {
		t.Errorf("markdown emphasis should be stripped, got %q", got[0])
	}
	if strings.Contains(strings.Join(got, " "), "extra-three-should-be-dropped") {
		t.Error("capability list must stay bounded")
	}
}

// TestPruneMemberRoles: unchecking a member must not leave a role behind that
// silently reappears if it is re-added.
func TestPruneMemberRoles(t *testing.T) {
	got := pruneMemberRoles(
		map[string]string{"a": "scheduler", "b": "  ", "c": "worker", "gone": "old role"},
		[]string{"a", "b", "c"})
	if got["a"] != "scheduler" || got["c"] != "worker" {
		t.Errorf("selected members should keep their roles, got %v", got)
	}
	if _, ok := got["gone"]; ok {
		t.Error("role for an unselected member should be dropped")
	}
	if _, ok := got["b"]; ok {
		t.Error("a blank role should be dropped, not stored empty")
	}
	if pruneMemberRoles(nil, []string{"a"}) != nil {
		t.Error("no roles should stay nil so the field omits from the record")
	}
	if pruneMemberRoles(map[string]string{"a": ""}, []string{"a"}) != nil {
		t.Error("all-blank roles should collapse to nil")
	}
	long := pruneMemberRoles(map[string]string{"a": strings.Repeat("x", 400)}, []string{"a"})
	if len(long["a"]) != 120 {
		t.Errorf("role should be capped at 120 chars, got %d", len(long["a"]))
	}
}

// TestNodeLabels: a comparison naming "node-1" is useless when two members are
// both called node-1.
func TestNodeLabels(t *testing.T) {
	got := nodeLabels([]wsMember{
		{ID: "a", Rec: Appliance{Name: "node-1"}},
		{ID: "b", Rec: Appliance{Name: "node-1"}},
		{ID: "c", Rec: Appliance{Name: "node-2"}},
	})
	if got[0] != "node-1 (a)" || got[1] != "node-1 (b)" {
		t.Errorf("colliding names should be disambiguated by ID, got %v", got)
	}
	if got[2] != "node-2" {
		t.Errorf("a unique name should stay bare, got %q", got[2])
	}
}

// TestWorkspacePlanToolsAllowed keeps the tool guard honest: the coordinator
// builds the plan group, so every plan tool name must be on the workspace
// allow-list or assertOnlyAllowedTools panics mid-question.
func TestWorkspacePlanToolsAllowed(t *testing.T) {
	for _, td := range buildPlanTools("test-session", false).All() {
		if !servitorWorkspaceToolAllowList[td.Tool.Name] {
			t.Errorf("plan tool %q missing from servitorWorkspaceToolAllowList", td.Tool.Name)
		}
	}
}

// TestScratchDir checks the path construction that the risk gate trusts: a
// caller-supplied session ID must not be able to escape the prefix.
func TestScratchDir(t *testing.T) {
	if got := scratch_dir("abc-123"); got != "/tmp/servitor-abc-123" {
		t.Errorf("scratch_dir = %q", got)
	}
	for _, hostile := range []string{"../../etc", "a/b", "a b; rm -rf /", "$(whoami)", ""} {
		got := scratch_dir(hostile)
		if !strings.HasPrefix(got, scratch_prefix) {
			t.Errorf("scratch_dir(%q) = %q — escaped the prefix", hostile, got)
		}
		if strings.ContainsAny(strings.TrimPrefix(got, scratch_prefix), "/ ;$()&|`") {
			t.Errorf("scratch_dir(%q) = %q — leaked a path or shell metacharacter", hostile, got)
		}
	}
}
