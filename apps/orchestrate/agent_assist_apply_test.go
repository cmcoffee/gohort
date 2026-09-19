package orchestrate

// The agent assistant's Apply button.
//
// It shipped 2026-09-09 sending PATCH to /api/agents/<id>. That route had
// answered GET, POST and DELETE since three days earlier and has never answered
// PATCH, so every press returned "method not allowed" for nine days. Nothing
// caught it: the panel builds, the route is registered, the proposal arrives,
// and the failure is one verb deep on a path nothing exercises until a person
// clicks.
//
// So the pieces are pinned end to end: the verb the client sends, the route
// that serves it, the id it takes from the path, and the allowlist every
// spelling has to go through.

import (
	"strings"
	"testing"
)

// applyBlock returns the Apply handler's client source.
func applyBlock(t *testing.T) string {
	t.Helper()
	src := readFile(t, "page_agent.go")
	i := strings.Index(src, "apply.onclick=function(){")
	if i < 0 {
		t.Fatal("the Apply button is gone")
	}
	end := strings.Index(src[i:], "body.appendChild(log);")
	if end < 0 {
		t.Fatal("could not bound the Apply handler")
	}
	return src[i : i+end]
}

func TestApplyTargetsARouteThatServesItsVerb(t *testing.T) {
	block := applyBlock(t)
	if !strings.Contains(block, "method:'PATCH'") {
		t.Fatal("Apply no longer PATCHes; this test is reading the wrong place")
	}
	// Whichever spelling it uses, the route behind it has to accept PATCH.
	http := readFile(t, "agents_http.go")
	one := funcBody(t, http, "func (T *OrchestrateApp) handleAgentOne")
	list := funcBody(t, http, "func (T *OrchestrateApp) handleAgentList")
	switch {
	case strings.Contains(block, "'../api/agents/'+id"):
		if !strings.Contains(one, "case http.MethodPatch:") {
			t.Error("Apply PATCHes /api/agents/<id>, which does not serve PATCH: every press is a 405")
		}
	case strings.Contains(block, "'../api/agents?id='"):
		if !strings.Contains(list, "case http.MethodPatch:") {
			t.Error("Apply PATCHes /api/agents, which does not serve PATCH")
		}
	default:
		t.Errorf("Apply posts somewhere this test does not recognise; check the route serves PATCH:\n%s", block)
	}
}

// Both spellings must reach the SAME merge, or a second way to do something is
// a second set of rules for it. patchAgentFields leaves guardrails and the
// injection-scan settings out BY NAME so no partial save can weaken the rule it
// is about to be judged against; a path that skipped it would undo that.
func TestEverySpellingOfPatchGoesThroughTheAllowlist(t *testing.T) {
	http := readFile(t, "agents_http.go")
	one := funcBody(t, http, "func (T *OrchestrateApp) handleAgentOne")
	list := funcBody(t, http, "func (T *OrchestrateApp) handleAgentList")
	for name, body := range map[string]string{"handleAgentOne": one, "handleAgentList": list} {
		if !strings.Contains(body, "case http.MethodPatch:") {
			continue
		}
		if !strings.Contains(body, "T.patchAgent(") {
			t.Errorf("%s serves PATCH without going through patchAgent, so patchAgentFields does not apply to it", name)
		}
	}
	if !strings.Contains(funcBody(t, http, "func (T *OrchestrateApp) patchAgent"), "patchAgentFields[k]") {
		t.Error("patchAgent no longer consults the allowlist")
	}
	// The fields the assistant may propose all have to be settable, or Apply
	// succeeds on some changes and 400s on others with no warning beforehand.
	assist := readFile(t, "agent_assist.go")
	for field := range assistableFields {
		if !patchAgentFields[field] {
			t.Errorf("assist may propose %q but PATCH refuses it: Apply fails on any proposal touching it", field)
		}
		if !strings.Contains(assist, `"`+field+`"`) {
			t.Errorf("%q is not named in agent_assist.go", field)
		}
	}
}

// The path id has to actually reach the merge; taking it only from the query
// would make the RESTful spelling a 400 instead of a 405, which is a different
// error with the same outcome.
func TestThePathIdReachesTheMerge(t *testing.T) {
	http := readFile(t, "agents_http.go")
	body := funcBody(t, http, "func (T *OrchestrateApp) patchAgent")
	if !strings.Contains(body, "pathID") {
		t.Fatal("patchAgent does not take a path id")
	}
	flat := strings.Join(strings.Fields(body), " ")
	if !strings.Contains(flat, "id := strings.TrimSpace(pathID)") {
		t.Error("the path id is not the first source of the target")
	}
	// And the collection route still works without one.
	if !strings.Contains(flat, `r.URL.Query().Get("id")`) || !strings.Contains(flat, `patch["id"]`) {
		t.Error("the query and body fallbacks are gone, so a FormPanel PATCH has no target")
	}
	if !strings.Contains(funcBody(t, http, "func (T *OrchestrateApp) handleAgentList"), `T.patchAgent(w, r, udb, user, "")`) {
		t.Error("the collection route no longer passes an empty path id")
	}
}

// funcBody returns one function's source, from its signature to the closing
// brace at column 0.
func funcBody(t *testing.T, src, sig string) string {
	t.Helper()
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("no %q", sig)
	}
	j := strings.Index(src[i:], "\n}\n")
	if j < 0 {
		t.Fatalf("%q never closes", sig)
	}
	return src[i : i+j]
}
