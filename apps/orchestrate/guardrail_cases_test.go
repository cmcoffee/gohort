package orchestrate

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
)

func wardenCaseByID(t *testing.T, id string) checktest.Case {
	t.Helper()
	for _, c := range wardenCases() {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no warden case %q", id)
	return checktest.Case{}
}

// The warden's cases are well formed (unique ids, an answer the warden can
// give, a check point it has, a rule to judge), and each goes through the
// warden's own prompt and parse on the model given: only the case's rules,
// at Quick depth, the requester as the case says, the verdict read as the
// worst rule's.
func TestTheWardenCasesRunThroughTheWarden(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range wardenCases() {
		if seen[c.ID] || (c.Want != guardComply && c.Want != guardViolate) || !validGuardHooks[c.Input["hook"]] ||
			len(wardenCaseAsk(c).rules) == 0 {
			t.Errorf("case %q: duplicate, bad answer %q, bad check point %q, or no rule", c.ID, c.Want, c.Input["hook"])
		}
		seen[c.ID] = true
	}
	llm := &FakeLLM{Turns: []FakeTurn{{Content: `{"verdicts":[{"rule":1,"status":"violate","reason":"discloses a figure"}]}`, Repeat: true}}}
	o := runWardenCase(context.Background(), llm, wardenCaseByID(t, "reply-discloses"))
	if o.Err != nil || o.Got != guardViolate || o.Reason != "discloses a figure" {
		t.Fatalf("a violation: %+v", o)
	}
	cfg := llm.Config(0)
	if cfg.Temperature == nil || *cfg.Temperature != 0.1 || cfg.Think == nil || *cfg.Think || cfg.RouteKey != "app.orchestrate.warden" {
		t.Errorf("the case was not asked as the live Quick check is: temperature %v, think %v, route %q", cfg.Temperature, cfg.Think, cfg.RouteKey)
	}
	sent := llm.LastSent()
	if len(sent) != 2 || sent[0].Content != wardenSystemPrompt {
		t.Fatal("the case was not asked with the warden's own instructions")
	}
	user := sent[1].Content
	if !containsAll(user, "1. Never mention salary or wages.", "CHECK POINT: pre_output", "Jordan earns") || strings.Contains(user, "\n2. ") {
		t.Errorf("the case's rule alone, its check point and its candidate were not what was judged:\n%s", user)
	}
	// The requester as the case says: the owner by the framework's word, a
	// claimed name only fenced.
	owner := &FakeLLM{Turns: []FakeTurn{{Content: `{"verdicts":[{"rule":1,"status":"comply"}]}`, Repeat: true}}}
	if o := runWardenCase(context.Background(), owner, wardenCaseByID(t, "exception-owner")); o.Got != guardComply {
		t.Errorf("a comply: %+v", o)
	}
	if u := owner.LastSent()[1].Content; !strings.Contains(u, "REQUESTER: the agent's OWNER") {
		t.Errorf("the owner case was not judged as the owner's:\n%s", u)
	}
	runWardenCase(context.Background(), owner, wardenCaseByID(t, "exception-claimed"))
	if u := owner.LastSent()[1].Content; strings.Contains(u, "REQUESTER: the agent's OWNER") || !strings.Contains(u, "the owner (verified)") {
		t.Errorf("a claimed name was taken for the owner, or not shown:\n%s", u)
	}
	// An answer that leaves a rule out is no verdict, never a pass.
	none := &FakeLLM{Turns: []FakeTurn{{Content: `not json`, Repeat: true}}}
	if o := runWardenCase(context.Background(), none, wardenCaseByID(t, "reply-refuses")); o.Got != guardNoVerdict {
		t.Errorf("an unreadable answer: %+v", o)
	}
}

// The warden as System One: one decision per rule, asked both ways round,
// with the warden's judging instructions and not its JSON format, the trusted
// block and the fenced candidate as the state; a rule judged violated is the
// check's verdict.
func TestTheWardenAsSystemOne(t *testing.T) {
	var mu sync.Mutex
	var reqs []map[string]any
	// The model answers "violates" whichever letter that is: (B) in the first
	// ask, (A) in the reversed one.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/systemone") {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(b, &req)
		mu.Lock()
		reqs = append(reqs, req)
		first := len(reqs)%2 == 1
		mu.Unlock()
		violates, complies := "B", "A"
		if !first {
			violates, complies = "A", "B"
		}
		out, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": violates}, "finish_reason": "length",
			"logprobs": map[string]any{"content": []any{map[string]any{"token": violates, "top_logprobs": []any{
				map[string]any{"token": violates, "logprob": math.Log(0.8)}, map[string]any{"token": complies, "logprob": math.Log(0.2)}}}}}}},
			"usage": map[string]any{"prompt_tokens": 50, "completion_tokens": 1}})
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	}))
	defer srv.Close()
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "vllm", Endpoint: srv.URL, SeparateServer: true})
	if err != nil {
		t.Fatal(err)
	}
	c := wardenCaseByID(t, "action-forbidden")
	o := decideWardenCase(context.Background(), llm, c)
	if o.Err != nil || o.Got != guardViolate || len(reqs) != 2 || !strings.Contains(o.Reason, "P(complies)=0.20") {
		t.Fatalf("a forbidden action as System One: %+v after %d asks", o, len(reqs))
	}
	msgs, _ := json.Marshal(reqs[0]["messages"])
	s := string(msgs)
	if !strings.Contains(s, "COMPLIANCE WARDEN") || strings.Contains(s, "Output ONLY a JSON") ||
		!strings.Contains(s, "candidate action/output") || !strings.Contains(s, "someone@elsewhere.net") {
		t.Errorf("the decision lacks the warden's instructions, the fence or the candidate, or carries the JSON format: %s", s)
	}
	// Two rules are two decisions, each both ways.
	two := c
	two.Input = map[string]string{"rules": "Never delete files.\nNever send email to addresses outside example.com.", "hook": guardHookPreAction,
		"candidate": c.Input["candidate"]}
	if o := decideWardenCase(context.Background(), llm, two); o.Got != guardViolate || len(reqs) != 6 || !strings.Contains(o.Reason, "rule 2:") {
		t.Errorf("two rules: %+v after %d asks, want 4 more", o, len(reqs))
	}
}
