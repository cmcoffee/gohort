package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The thumbs on an agent's replies go first to whoever looks after it. Bob's
// thumbs-down on Alice's shared agent reaches Alice (a notice and her list,
// with a link to fix it with Builder), not the admin, and Bob cannot move it;
// Alice sends it on and only then is it the admin's. Her own thumbs stay on
// her list and tell her nothing. Thumbs on gohort's own agents go to the
// admin as before, and a thumbs-up there counts for nothing until kept.
// Builder reads an agent's open feedback, fenced, when it reads the agent.
func TestAnAgentsThumbsGoToWhoeverLooksAfterIt(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevRoot, prevBase := RootDB, orchestrateBaseDB
	RootDB, orchestrateBaseDB = root, root
	t.Cleanup(func() { RootDB, orchestrateBaseDB = prevRoot, prevBase })
	auth := &DBase{Store: kvlite.MemStore()}
	prevAuth := AuthDB
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { AuthDB = prevAuth })
	tokens := map[string]string{}
	for _, u := range []string{"alice", "bob", "boss"} {
		AuthSetUser(auth, u, "pw", u == "boss")
		tokens[u] = AuthCreateSession(auth, u)
	}
	T := &OrchestrateApp{AppCore: AppCore{DB: root}}
	mux := routed(T)
	call := func(user, method, path, body string) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://example.com")
		r.Host = "example.com"
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: tokens[user]})
		w := httptest.NewRecorder()
		mux(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		if out == nil {
			out = map[string]any{"_code": float64(w.Code), "_body": w.Body.String()}
		}
		out["_code"] = float64(w.Code)
		return out
	}
	if _, err := saveAgent(UserDB(root, "alice"), AgentRecord{ID: "a1", Name: "Helper", Owner: "alice", OrchestratorPrompt: "p", AllowedUsers: []string{"bob"}}); err != nil {
		t.Fatal(err)
	}
	flag := func(user, agent, verdict, why string) map[string]any {
		b, _ := json.Marshal(map[string]string{"agent_id": agent, "session_id": "s1", "reply": "The answer is 4.", "verdict": verdict, "evaluation": why})
		return call(user, "POST", "/api/reply-flag", string(b))
	}
	adminRows := func(field string) []any {
		v, _ := call("boss", "GET", "/api/console/reply-flags", "")[field].([]any)
		return v
	}

	got := flag("bob", "a1", "down", "It got the sum wrong.")
	if got["_code"] != float64(200) || !strings.Contains(got["message"].(string), "Sent to alice") {
		t.Fatalf("bob's thumbs-down: %v", got)
	}
	id, _ := got["id"].(string)
	if n := notices.List(RootDB, "alice"); len(n) != 1 || !strings.Contains(n[0].Title, "bob flagged a reply of Helper") || !strings.Contains(n[0].Body, "It got the sum wrong.") {
		t.Errorf("alice's notice: %+v", n)
	}
	if rows := adminRows("review"); len(rows) != 0 {
		t.Errorf("an owner's feedback reached the admin: %v", rows)
	}
	fb, _ := call("alice", "GET", "/api/agent-feedback?agent=a1", "")["feedback"].([]any)
	if len(fb) != 1 {
		t.Fatalf("alice's list: %v", fb)
	}
	row := fb[0].(map[string]any)
	if row["who"] != "bob" || row["_open"] != true || !strings.Contains(row["fix_url"].(string), "intake.kind=Agent&intake.target=a1") {
		t.Errorf("the row alice reads: %v", row)
	}
	if out, _ := call("bob", "GET", "/api/agent-feedback?agent=a1", "")["feedback"].([]any); len(out) != 0 {
		t.Errorf("bob read alice's feedback: %v", out)
	}
	if code := call("bob", "POST", "/api/agent-feedback/status?id="+id+"&to=admin", "{}")["_code"]; code != float64(404) {
		t.Errorf("bob moved the feedback on alice's agent: %v", code)
	}

	// Builder reads what is still open, fenced.
	flag("alice", "a1", "up", "")
	if n := notices.List(RootDB, "alice"); len(n) != 1 {
		t.Errorf("alice was told of her own thumbs: %+v", n)
	}
	view := string(T.withFeedback([]byte(`{"id":"a1"}`), "alice", "a1"))
	if !strings.Contains(view, "UNTRUSTED") || !strings.Contains(view, "DOWN, from a person using it: It got the sum wrong.") || !strings.Contains(view, "UP, from the owner") {
		t.Errorf("what Builder reads about the agent: %s", view)
	}
	if other := string(T.withFeedback([]byte(`{"id":"a1"}`), "bob", "a1")); strings.Contains(other, "feedback") {
		t.Errorf("someone else's view carried alice's feedback: %s", other)
	}

	// Alice sends it on: now the admin's.
	if out := call("alice", "POST", "/api/agent-feedback/status?id="+id+"&to=admin", "{}"); out["_code"] != float64(200) {
		t.Fatalf("alice sending it on: %v", out)
	}
	if rows := adminRows("review"); len(rows) != 1 {
		t.Errorf("what alice sent on is not the admin's: %v", rows)
	}
	if strings.Contains(string(T.withFeedback([]byte(`{}`), "alice", "a1")), "DOWN, from") {
		t.Error("Builder still reads feedback alice sent to the admin")
	}

	// gohort's own agent: straight to the admin, a thumbs-up unreviewed.
	if out := flag("bob", "seed-builder", "up", ""); !strings.Contains(out["message"].(string), "for them to keep or not") {
		t.Errorf("a thumbs-up on gohort's own agent: %v", out)
	}
	good := adminRows("good")
	if len(good) != 1 || good[0].(map[string]any)["status"] != flagGood {
		t.Fatalf("the admin's good examples: %v", good)
	}
	if n := len(T.keptGoodExamples()); n != 0 {
		t.Errorf("an unreviewed thumbs-up counts as a good example: %d", n)
	}
	gid, _ := good[0].(map[string]any)["id"].(string)
	call("boss", "POST", "/api/console/reply-flags/status?id="+gid+"&status="+flagKept, "{}")
	if n := len(T.keptGoodExamples()); n != 1 {
		t.Errorf("a good example the admin kept does not count: %d", n)
	}
}
