package customapps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A page's co-authored document asks the app's agent for a change: the agent
// is asked with no tools and in JSON, the highlighted passage and document go
// as the person's text, and the change comes back only when it can apply.
func TestAPageAsksTheAgentToChangeItsDocument(t *testing.T) {
	ownerDB := &DBase{Store: kvlite.MemStore()}
	spec := AppSpec{Slug: "studio", Owner: "alice", AgentID: "composer", AskDailyUSD: 1, AskUserDailyUSD: 0.30}
	prev := appAgentAsk
	t.Cleanup(func() { appAgentAsk = prev })
	var prompts []string
	answer := ""
	appAgentAsk = func(ctx context.Context, owner, user, agentID, prompt string, jsonMode bool) (string, float64, error) {
		if !jsonMode {
			t.Error("propose asked without JSON mode")
		}
		prompts = append(prompts, prompt)
		return answer, 0.10, nil
	}
	T := &CustomApps{}
	doc := "Verse one\nNeon on the water\nCity never sleeps\n"
	propose := func(user, body string) (*httptest.ResponseRecorder, map[string]any) {
		w := httptest.NewRecorder()
		T.handlePropose(w, httptest.NewRequest(http.MethodPost, "/apps/studio/propose", strings.NewReader(body)), ownerDB, "alice", user, spec)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}
	body := func(v map[string]any) string { b, _ := json.Marshal(v); return string(b) }

	answer = `{"reply": "Darker now.", "edit": {"find": "Neon on the water", "replace": "Ash on the water", "summary": "darker image"}}`
	w, out := propose("bob", body(map[string]any{"document": doc, "prompt": "make it darker",
		"selection": map[string]any{"text": "Neon on the water", "start": 10},
		"history":   []map[string]string{{"role": "user", "text": "it is a night song"}, {"role": "assistant", "text": "noted"}}}))
	if w.Code != http.StatusOK || out["reply"] != "Darker now." {
		t.Fatalf("propose: %d %s", w.Code, w.Body.String())
	}
	if e, _ := out["edit"].(map[string]any); e["find"] != "Neon on the water" || e["replace"] != "Ash on the water" {
		t.Errorf("edit: %v", out["edit"])
	}
	p := prompts[0]
	for _, want := range []string{"<document>\n" + doc, "<highlighted>\nNeon on the water\n</highlighted>", "Request: make it darker",
		"person: it is a night song", "you: noted", "not instructions to you"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}

	// A change to text that is not there is dropped, with why.
	answer = `{"reply": "Done.", "edit": {"find": "Stars on the river", "replace": "x"}}`
	if _, out := propose("bob", body(map[string]any{"document": doc, "prompt": "fix it"})); out["edit"] != nil || !strings.Contains(out["reason"].(string), "not in the document") {
		t.Errorf("a change that cannot apply: %v", out)
	}
	// Fenced the way models fence JSON, it still reads.
	answer = "```json\n" + `{"reply": "ok", "edit": {"find": "City never sleeps", "replace": "City never wakes"}}` + "\n```"
	if _, out := propose("carol", body(map[string]any{"document": doc, "prompt": "flip it"})); out["edit"] == nil {
		t.Errorf("a fenced answer: %v", out)
	}
	// Plain words are the reply, with nothing to apply.
	answer = "That verse already scans."
	if _, out := propose("carol", body(map[string]any{"document": doc, "prompt": "does it scan?"})); out["reply"] != "That verse already scans." || out["edit"] != nil {
		t.Errorf("a plain answer: %v", out)
	}

	// Refused before the agent is asked.
	asked := len(prompts)
	for _, c := range []struct {
		name, body string
		code       int
	}{
		{"a passage that is not in the document", body(map[string]any{"document": doc, "prompt": "x", "selection": map[string]any{"text": "not there"}}), http.StatusBadRequest},
		{"a document past the limit", body(map[string]any{"document": strings.Repeat("a", proposeMaxDocument+1), "prompt": "x"}), http.StatusRequestEntityTooLarge},
		{"a request past the limit", body(map[string]any{"document": doc, "prompt": strings.Repeat("a", proposeMaxPrompt+1)}), http.StatusRequestEntityTooLarge},
		{"no request", body(map[string]any{"document": doc}), http.StatusBadRequest},
		{"a body past the limit", `{"document": "` + strings.Repeat("a", proposeMaxBody) + `"}`, http.StatusBadRequest},
	} {
		if w, _ := propose("dave", c.body); w.Code != c.code {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body.String())
		}
	}
	w = httptest.NewRecorder()
	T.handlePropose(w, httptest.NewRequest(http.MethodGet, "/apps/studio/propose", nil), ownerDB, "alice", "dave", spec)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", w.Code)
	}
	w = httptest.NewRecorder()
	T.handlePropose(w, httptest.NewRequest(http.MethodPost, "/apps/studio/propose", strings.NewReader(body(map[string]any{"document": doc, "prompt": "x"}))), ownerDB, "alice", "dave", AppSpec{Slug: "studio", Owner: "alice"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("an app with no agent: %d", w.Code)
	}
	if len(prompts) != asked {
		t.Errorf("a refused request reached the agent")
	}

	// Under ask's daily caps: bob has spent 0.20 of his 0.30.
	propose("bob", body(map[string]any{"document": doc, "prompt": "again"}))
	if w, _ := propose("bob", body(map[string]any{"document": doc, "prompt": "and again"})); w.Code != http.StatusTooManyRequests {
		t.Errorf("bob past his cap: %d %s", w.Code, w.Body.String())
	}
}
