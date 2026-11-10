package decide

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// A check's quick choice stands on System One only for a mapped check, only
// for an answer that needs no reason, and only when System One is sure of
// it; anything else asks the full question.
func TestAQuickChoiceStandsOnlyWhenItShould(t *testing.T) {
	privateWorld(t)
	first := 0.97
	// Answers by the option's text, wherever it is shown: "Clean" gets first.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		probs := map[string]float64{}
		for k, v := range req.Questions["d"].Criteria {
			probs[k] = 1 - first
			if strings.HasPrefix(v, "Clean") {
				probs[k] = first
			}
		}
		out, _ := json.Marshal(map[string]any{"answers": map[string]any{"d": map[string]any{"probabilities": probs}},
			"diagnostics": map[string]any{"d": map[string]any{"label_mass": 1}}})
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	}))
	defer srv.Close()
	systemOneConnector(t, "home", srv.URL+"/v1")
	RootDB.Set(ConfigTable, "provider", "home")
	mapped := false
	prevGate := JudgeMapGate
	JudgeMapGate = func(key, tier string) bool { return mapped && key == "test.quick" && tier == MapSystemOne }
	t.Cleanup(func() { JudgeMapGate = prevGate })

	q := QuickChoice{Key: "test.quick", Ask: "Clean?", Answers: []QuickAnswer{
		{Key: "clean", Means: "Clean: nothing to flag"}, {Key: "flagged", Means: "Flagged: something to flag", Reason: true}}}
	if _, ok := AskQuick(context.Background(), q); ok {
		t.Error("an unmapped check was settled by System One")
	}
	mapped = true
	if ans, ok := AskQuick(context.Background(), q); !ok || ans != "clean" {
		t.Errorf("a sure reasonless answer on a mapped check: %q %v", ans, ok)
	}
	first = 0.03
	if _, ok := AskQuick(context.Background(), q); ok {
		t.Error("an answer that needs a reason stood on System One")
	}
	first = 0.5
	if _, ok := AskQuick(context.Background(), q); ok {
		t.Error("an even split stood on System One")
	}
	if !QuickStands(q, 0, QuickSure) || QuickStands(q, 0, QuickSure-0.01) || QuickStands(q, 1, 1) {
		t.Error("QuickStands")
	}
}

// A System One that fails is set aside: the next decision does not ask it
// (Model reports none), with one rest per failure run, until the rest ends.
// A stop from the caller is not its failure.
func TestSystemOneRestsAfterAFailure(t *testing.T) {
	privateWorld(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	systemOneConnector(t, "home", srv.URL+"/v1")
	RootDB.Set(ConfigTable, "provider", "home")
	prevGate := JudgeMapGate
	JudgeMapGate = func(key, tier string) bool { return true }
	t.Cleanup(func() {
		JudgeMapGate = prevGate
		rest.Lock()
		rest.until, rest.why = time.Time{}, ""
		rest.Unlock()
	})
	q := QuickChoice{Key: "test.quick", Ask: "Clean?", Answers: []QuickAnswer{{Key: "clean", Means: "Clean"}, {Key: "flagged", Means: "Flagged", Reason: true}}}

	Rest(context.Canceled)
	if _, _, resting := Resting(); resting {
		t.Fatal("a caller's stop set System One aside")
	}
	if _, ok := AskQuick(context.Background(), q); ok || calls == 0 {
		t.Fatalf("a failing System One settled a check, or was not asked (%d calls)", calls)
	}
	asked := calls
	if until, why, resting := Resting(); !resting || why == "" || time.Until(until) < 2*time.Minute {
		t.Errorf("not resting after a failure: %v %q %v", until, why, resting)
	}
	if Model() != nil {
		t.Error("a resting System One is still offered")
	}
	if _, ok := AskQuick(context.Background(), q); ok || calls != asked {
		t.Errorf("a resting System One was asked again (%d calls, was %d)", calls, asked)
	}
}
