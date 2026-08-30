package servitor

import (
	"context"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/bundle"
)

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
		res, err := bundle.Open("no-such-user", "no-such-bundle").Search(bundle.Query{Pattern: regexpQuoteMeta(term)})
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
