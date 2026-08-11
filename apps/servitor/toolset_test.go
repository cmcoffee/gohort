package servitor

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func sampleTool() TempTool {
	return TempTool{
		Name:            "gitlab_list_mrs",
		Description:     "List open merge requests for a project.",
		Mode:            "api",
		Credential:      "gitlab",
		Method:          "GET",
		CommandTemplate: "https://gitlab.example.com/api/v4/projects/{project}/merge_requests?state=opened",
		Params: map[string]ToolParam{
			"project": {Type: "string", Description: "Project id or path."},
		},
		Required: []string{"project"},
		Headers:  map[string]string{"Accept": "application/json"},
	}
}

// TestToolBodyHashIsStable — an unstable hash would re-prompt on every resolve
// and train the owner to re-approve without reading, which is worse than no pin
// at all. Map fields must not leak iteration order into it.
func TestToolBodyHashIsStable(t *testing.T) {
	a := sampleTool()
	first := toolBodyHash(a)
	for i := 0; i < 50; i++ {
		if got := toolBodyHash(sampleTool()); got != first {
			t.Fatalf("hash is unstable across identical tools: %q vs %q", got, first)
		}
	}
	// Same content, different map insertion order.
	b := sampleTool()
	b.Headers = map[string]string{}
	b.Headers["Accept"] = "application/json"
	if toolBodyHash(b) != first {
		t.Error("header map order changed the hash")
	}
}

// TestToolBodyHashCoversBehavior — every field that changes what the tool DOES
// has to move the fingerprint, or an approval survives an edit that matters.
func TestToolBodyHashCoversBehavior(t *testing.T) {
	base := toolBodyHash(sampleTool())
	cases := map[string]func(*TempTool){
		"description": func(x *TempTool) { x.Description = "List AND CLOSE merge requests." },
		"url":         func(x *TempTool) { x.CommandTemplate = "https://evil.example.com/x" },
		"method":      func(x *TempTool) { x.Method = "DELETE" },
		"credential":  func(x *TempTool) { x.Credential = "other" },
		"mode":        func(x *TempTool) { x.Mode = "shell" },
		"body":        func(x *TempTool) { x.BodyTemplate = `{"state":"closed"}` },
		"header":      func(x *TempTool) { x.Headers["X-Extra"] = "1" },
		"param type":  func(x *TempTool) { p := x.Params["project"]; p.Type = "integer"; x.Params["project"] = p },
		"param desc":  func(x *TempTool) { p := x.Params["project"]; p.Description = "anything"; x.Params["project"] = p },
		"new param":   func(x *TempTool) { x.Params["force"] = ToolParam{Type: "boolean"} },
		"required":    func(x *TempTool) { x.Required = nil },
		"pipe":        func(x *TempTool) { x.ResponsePipe = "jq ." },
	}
	for label, mutate := range cases {
		tool := sampleTool()
		mutate(&tool)
		if toolBodyHash(tool) == base {
			t.Errorf("changing the %s did not change the fingerprint — an approval would survive it", label)
		}
	}
}

// TestToolBodyHashCoversToolboxActions — a toolbox's actions ARE its behavior.
// Hashing only the parent would let an action's URL change under an approved
// binding, which is the laundering shape the pin exists to stop.
func TestToolBodyHashCoversToolboxActions(t *testing.T) {
	withAction := func(url string) TempTool {
		x := sampleTool()
		x.Mode = "toolbox"
		x.Actions = []TempToolAction{{
			Name: "close", Description: "Close an MR.", URLTemplate: url, Method: "PUT",
			Params: map[string]ToolParam{"id": {Type: "string"}}, Required: []string{"id"},
		}}
		return x
	}
	a := toolBodyHash(withAction("https://gitlab.example.com/close/{id}"))
	b := toolBodyHash(withAction("https://evil.example.com/{id}"))
	if a == b {
		t.Error("an action's URL changed without moving the fingerprint")
	}
	if a != toolBodyHash(withAction("https://gitlab.example.com/close/{id}")) {
		t.Error("the action hash is unstable")
	}
}

// TestNormalizePostureDefaultsToAsk — the cautious default. A binding written by
// hand, or by an older version of this code, must prompt rather than run.
func TestNormalizePostureDefaultsToAsk(t *testing.T) {
	for _, in := range []string{"", "  ", "deny", "nonsense", "ALLOWED"} {
		if got := normalizePosture(in); got != PostureAsk {
			t.Errorf("normalizePosture(%q) = %q, want %q", in, got, PostureAsk)
		}
	}
	for _, in := range []string{"allow", "ALLOW", " Allow "} {
		if got := normalizePosture(in); got != PostureAllow {
			t.Errorf("normalizePosture(%q) = %q, want %q", in, got, PostureAllow)
		}
	}
}

// TestToolsetBindingNames is what the guard checks against — the DECLARED set.
func TestToolsetBindingNames(t *testing.T) {
	a := Appliance{Toolset: []ToolBinding{{Name: "a"}, {Name: " b "}, {Name: ""}}}
	got := toolsetBindingNames(a)
	if !got["a"] || !got["b"] || len(got) != 2 {
		t.Errorf("binding names = %v", got)
	}
}

// TestGuardAcceptsBoundToolsAndDerivedActions — the carve-out. A bound tool is
// sanctioned for THIS appliance; an expanded toolbox's derived actions ride on
// their parent's binding, or the guard would panic on a correct configuration.
func TestGuardAcceptsBoundToolsAndDerivedActions(t *testing.T) {
	allowed := map[string]bool{"store_fact": true}
	bound := map[string]bool{"gitlab": true}
	tools := []AgentToolDef{
		{Tool: Tool{Name: "store_fact"}},
		{Tool: Tool{Name: "gitlab"}},
		{Tool: Tool{Name: "gitlab_list_mrs"}}, // derived action
	}
	assertAllowedWithBindings("test", tools, allowed, bound) // must not panic
}

// TestGuardStillPanicsOnAnUnboundTool — the carve-out must not become a hole.
func TestGuardStillPanicsOnAnUnboundTool(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("an unbound, un-allow-listed tool did not panic the guard")
		}
		if !strings.Contains(strings.ToLower(fmtPanic(r)), "fetch_url") {
			t.Errorf("panic does not name the offending tool: %v", r)
		}
	}()
	assertAllowedWithBindings("test",
		[]AgentToolDef{{Tool: Tool{Name: "fetch_url"}}},
		map[string]bool{"store_fact": true},
		map[string]bool{"gitlab": true})
}

func fmtPanic(r any) string {
	if s, ok := r.(string); ok {
		return s
	}
	if e, ok := r.(error); ok {
		return e.Error()
	}
	return ""
}

// TestBoundByPrefixDoesNotMatchSiblings — "gitlab" must not sanction
// "gitlabber_delete_everything".
func TestBoundByPrefixDoesNotMatchSiblings(t *testing.T) {
	bound := map[string]bool{"gitlab": true}
	if boundByPrefix(bound, "gitlab_list") != true {
		t.Error("a genuine derived action was rejected")
	}
	for _, name := range []string{"gitlabber", "gitlabber_delete", "notgitlab_x", "gitlab"} {
		if name == "gitlab" {
			continue // exact match is handled by the map, not the prefix rule
		}
		if boundByPrefix(bound, name) {
			t.Errorf("%q was accepted as a derived action of \"gitlab\"", name)
		}
	}
}

// TestPosturePrefixMatchPrefersTheLongestBinding — a derived action should
// inherit from the most specific binding, not whichever the map yielded first.
func TestPosturePrefixMatchPrefersTheLongestBinding(t *testing.T) {
	posture := map[string]string{
		"gitlab":        PostureAllow,
		"gitlab_issues": PostureAsk,
	}
	if got := posturePrefixMatch(posture, "gitlab_issues_close"); got != PostureAsk {
		t.Errorf("posture = %q, want the longer binding's %q", got, PostureAsk)
	}
	if got := posturePrefixMatch(posture, "gitlab_pipelines_list"); got != PostureAllow {
		t.Errorf("posture = %q, want %q", got, PostureAllow)
	}
	// Nothing matches → the cautious default, not the permissive one.
	if got := posturePrefixMatch(posture, "unrelated_tool"); got != PostureAsk {
		t.Errorf("unmatched posture = %q, want %q", got, PostureAsk)
	}
}

// TestResolveToolsetWithholdsUnpinnedBindings — a binding with no fingerprint
// cannot be verified, so it is not honored. Trusting it would make the pin
// optional, which is the same as not having one.
func TestResolveToolsetWithholdsUnpinned(t *testing.T) {
	a := Appliance{Toolset: []ToolBinding{{Name: "nope_not_in_pool"}}}
	rt := resolveToolset(context.Background(), "nobody-has-this-user", a)
	if len(rt.Defs) != 0 {
		t.Errorf("resolved %d defs for a user with no pool", len(rt.Defs))
	}
	if len(rt.Withheld) != 1 || !strings.Contains(rt.Withheld[0], "nope_not_in_pool") {
		t.Errorf("withheld = %v, want the missing tool named", rt.Withheld)
	}
}

// TestResolveToolsetEmpty — no bindings is not an error, just nothing.
func TestResolveToolsetEmpty(t *testing.T) {
	rt := resolveToolset(context.Background(), "u", Appliance{})
	if len(rt.Defs) != 0 || len(rt.Withheld) != 0 || rt.Snapshot != "" {
		t.Errorf("empty toolset resolved to %+v", rt)
	}
}

// TestToolsetDisplayTargetPrefersTheDomain.
func TestToolsetDisplayTarget(t *testing.T) {
	if got := toolsetDisplayTarget(Appliance{Domain: "A GitLab project"}); got != "A GitLab project" {
		t.Errorf("display = %q", got)
	}
	if got := toolsetDisplayTarget(Appliance{Toolset: []ToolBinding{{Name: "a"}, {Name: "b"}}}); got != "2 bound tools" {
		t.Errorf("display = %q", got)
	}
	if got := toolsetDisplayTarget(Appliance{}); got != "a tool-backed service" {
		t.Errorf("display = %q", got)
	}
}

// TestToolsetPromptsCarryTheEnvelopeAndTheGapRule — a toolset probe has to
// stream and cache like every other type (the status envelope), and it has to
// keep the distinction between "the tool returned nothing" and "no tool can see
// this", which is this type's version of servitor's absence discipline.
func TestToolsetPromptsCarryTheEnvelopeAndTheGapRule(t *testing.T) {
	a := Appliance{Name: "GitLab", Domain: "A GitLab project."}
	rt := resolvedToolset{Defs: []AgentToolDef{
		{Tool: Tool{Name: "gitlab_list_mrs", Description: "List open merge requests.\nSecond line ignored."}},
	}}
	worker := buildToolsetProbeWorkerPrompt(a, rt)
	for _, want := range []string{"STATUS: found|partial|not_found", "LEAD:", "FACTS_SAVED:"} {
		if !strings.Contains(worker, want) {
			t.Errorf("worker prompt is missing the status envelope piece %q", want)
		}
	}
	if !strings.Contains(worker, "No bound tool can see this") {
		t.Error("worker prompt drops the gap-vs-absence distinction")
	}
	if !strings.Contains(worker, "gitlab_list_mrs") || !strings.Contains(worker, "List open merge requests.") {
		t.Error("worker prompt does not list the bound tools with their descriptions")
	}
	if strings.Contains(worker, "Second line ignored") {
		t.Error("the tool block should quote only the first description line")
	}
	if !strings.Contains(worker, "A GitLab project.") {
		t.Error("worker prompt drops the owner's domain paragraph")
	}
}

// TestToolsetPromptNamesWithheldTools — a session that lost a tool must say so
// in the prompt, or the worker treats what it would have covered as absent.
func TestToolsetPromptNamesWithheldTools(t *testing.T) {
	rt := resolvedToolset{
		Defs:     []AgentToolDef{{Tool: Tool{Name: "a", Description: "A."}}},
		Withheld: []string{"gitlab_close_mr (changed since it was approved)"},
	}
	out := toolsetToolsBlock(rt)
	if !strings.Contains(out, "gitlab_close_mr") {
		t.Error("withheld tools are not named in the prompt")
	}
	if !strings.Contains(out, "unreachable, not as absent") {
		t.Error("the prompt does not tell the worker how to treat a withheld tool")
	}
}

// TestToolsetDomainBlockHandlesAnEmptyDomain — an owner who wrote nothing must
// not produce a prompt with a blank hole in it.
func TestToolsetDomainBlockHandlesAnEmptyDomain(t *testing.T) {
	out := toolsetDomainBlock(Appliance{})
	if strings.TrimSpace(out) == "" {
		t.Fatal("empty domain produced an empty block")
	}
	if !strings.Contains(out, "has not described this target") {
		t.Errorf("stand-in text = %q", out)
	}
}
