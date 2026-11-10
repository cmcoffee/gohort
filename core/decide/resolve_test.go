package decide

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A decision is settled by System One when it is on and as sure of its
// answer as that option needs, each option needing its own confidence; by
// the full answer when System One is off, unsure or failing; and by the safe
// answer when the full answer fails, answers outside the options, or there
// is none.
func TestADecisionGoesTheCheapestWayThatIsSure(t *testing.T) {
	privateWorld(t)
	first, status := 0.95, http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "down", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Asked one way: o00 is option 0.
		fmt.Fprintf(w, `{"answers":{"d":{"probabilities":{"o00":%g,"o01":%g}}},"diagnostics":{"d":{"label_mass":1}}}`, first, 1-first)
	}))
	defer srv.Close()
	systemOneConnector(t, "home", srv.URL+"/v1")
	fulls := 0
	full := func(c int, err error) func(context.Context) (int, string, error) {
		return func(context.Context) (int, string, error) { fulls++; return c, "the full reason", err }
	}
	d := Decision{Name: "test", Question: Question{Ask: "Take it up?", Options: []string{"Take it up: yes", "Leave it"}},
		Sure: []float64{0.6, 0.75}, Full: full(1, nil), Safe: 1, Opts: []ChatOption{WithRouteKey("test.decision")}}
	prevGate := JudgeMapGate
	mapped := false
	JudgeMapGate = func(key, tier string) bool { return mapped && key == "test.decision" && tier == MapSystemOne }
	t.Cleanup(func() { JudgeMapGate = prevGate })
	// On, but not mapped for this check: the full answer, System One unasked.
	RootDB.Set(ConfigTable, "provider", "home")
	if r := Resolve(context.Background(), d); r.By != ByFull || fulls != 1 {
		t.Errorf("System One unmapped: %+v after %d full answers", r, fulls)
	}
	RootDB.Unset(ConfigTable, "provider")
	mapped, fulls = true, 0

	if r := Resolve(context.Background(), d); r.By != ByFull || r.Choice != 1 || r.Reason != "the full reason" || fulls != 1 {
		t.Errorf("System One off: %+v after %d full answers", r, fulls)
	}
	RootDB.Set(ConfigTable, "provider", "home")
	if r := Resolve(context.Background(), d); r.By != BySystemOne || r.Choice != 0 || fulls != 1 || !strings.Contains(r.Reason, "P(take it up)=0.95") {
		t.Errorf("a sure System One: %+v after %d full answers", r, fulls)
	}
	// Option 0 chosen at 0.7 stands (it needs 0.6); option 1 at 0.7 does
	// not (it needs 0.75).
	first = 0.7
	if r := Resolve(context.Background(), d); r.By != BySystemOne || r.Choice != 0 {
		t.Errorf("option 0 at 0.7: %+v", r)
	}
	first = 0.3
	if r := Resolve(context.Background(), d); r.By != ByFull || fulls != 2 {
		t.Errorf("option 1 at 0.7: %+v after %d full answers", r, fulls)
	}
	status = http.StatusInternalServerError
	if r := Resolve(context.Background(), d); r.By != ByFull || fulls != 3 {
		t.Errorf("a failing System One: %+v", r)
	}
	// The failure set System One aside; the cases below ask it again.
	if _, _, resting := Resting(); !resting {
		t.Error("a failing System One was not set aside")
	}
	clearRest()
	d.Full = full(0, errors.New("the worker is down"))
	if r := Resolve(context.Background(), d); r.By != BySafe || r.Choice != 1 || !strings.Contains(r.Reason, "the worker is down") {
		t.Errorf("a failing full answer: %+v", r)
	}
	d.Full = full(5, nil)
	if r := Resolve(context.Background(), d); r.By != BySafe || r.Choice != 1 {
		t.Errorf("a full answer outside the options: %+v", r)
	}
	d.Full = nil
	status, first = http.StatusOK, 0.3
	clearRest() // the server is back; its failures above set it aside
	if r := Resolve(context.Background(), d); r.By != BySafe || r.Choice != 1 || !strings.Contains(r.Reason, "unsure") {
		t.Errorf("an unsure System One with no full answer: %+v", r)
	}
	// An option with no confidence set never stands on System One alone.
	d.Sure, first = []float64{0, 0.6}, 0.99
	if r := Resolve(context.Background(), d); r.By != BySafe {
		t.Errorf("an option with no confidence set stood on System One: %+v", r)
	}
}
