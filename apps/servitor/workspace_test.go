package servitor

import (
	"strings"
	"testing"
)

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
