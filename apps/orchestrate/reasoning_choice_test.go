package orchestrate

// The editor's Reasoning section had Think mode and Effort, and a set Effort
// decides Think, so Effort Off and Think Off were the same control twice. It
// is one choice now, over the same two stored fields: Default, Off, or a
// level. A forced On with no level was Default unless the admin turned the
// route off, so it reads as Default and saves as following the route.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTheReasoningChoiceReadsAsTheAgentRuns(t *testing.T) {
	for _, c := range []struct {
		think, effort, want string
	}{
		{"", "", ""},
		{"on", "", ""},
		{"off", "", "off"},
		{"off", "high", "high"}, // a set effort wins, as thinkMode has it
		{"on", "off", "off"},
	} {
		if got := reasoningOf(AgentRecord{Think: c.think, Effort: c.effort}); got != c.want {
			t.Errorf("think=%q effort=%q reads %q, want %q", c.think, c.effort, got, c.want)
		}
	}

	// A form sending the choice back with the fields it loaded runs as it
	// did, and a value that is no choice changes nothing.
	a := AgentRecord{Think: "off", Effort: "high"}
	applyEditorChoices(nil, &a, map[string]json.RawMessage{"reasoning": json.RawMessage(`"high"`)})
	if a.thinkMode() != "on" || a.Effort != "high" {
		t.Errorf("an unchanged choice changed how it runs: %+v", a)
	}
	applyEditorChoices(nil, &a, map[string]json.RawMessage{"reasoning": json.RawMessage(`"bogus"`)})
	if a.Effort != "high" {
		t.Errorf("a value that is no choice changed the record: %+v", a)
	}

	if m := withEditorChoices(nil, AgentRecord{Think: "off"}); string(m["reasoning"]) != `"off"` {
		t.Errorf("the form's reasoning: %s", m["reasoning"])
	}
}

func TestTheReasoningChoiceSavesThroughPATCH(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	a, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Agent", OrchestratorPrompt: "Help.", Think: "on"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ choice, mode, effort string }{
		{"", "", ""}, // a forced on saved as Default follows the route
		{"off", "off", "off"},
		{"high", "on", "high"},
		{"low", "on", "low"},
		{"", "", ""},
	} {
		w := httptest.NewRecorder()
		body := `{"reasoning":"` + c.choice + `"}`
		routed(T)(w, as(t, httptest.NewRequest(http.MethodPatch, "/api/agents?id="+a.ID, strings.NewReader(body)), user))
		if w.Code != http.StatusOK {
			t.Fatalf("reasoning=%q answered %d: %s", c.choice, w.Code, w.Body.String())
		}
		got, _ := loadAgent(udb, a.ID)
		if got.thinkMode() != c.mode || got.Effort != c.effort || reasoningOf(got) != c.choice {
			t.Errorf("chose %q: think=%q effort=%q runs %q", c.choice, got.Think, got.Effort, got.thinkMode())
		}
	}
}
