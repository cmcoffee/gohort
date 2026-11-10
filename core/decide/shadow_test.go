package decide

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A router shadow asks System One both ways round with the destinations as
// options, compares with the step's own choice, and tallies per step: sure
// and agreeing, sure and choosing otherwise, unsure. Off, it asks nothing.
// decide fills core's hook.
func TestARouterShadowTalliesHowSystemOneDid(t *testing.T) {
	privateWorld(t)
	if ShadowRoute == nil {
		t.Fatal("decide does not fill core.ShadowRoute")
	}
	left := 0.95 // System One's share for the first option shown
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"answers":{"d":{"probabilities":{"o00":%g,"o01":%g}}},"diagnostics":{"d":{"label_mass":1}}}`, left, 1-left)
	}))
	defer srv.Close()
	systemOneConnector(t, "home", srv.URL+"/v1")
	choices, descs := []string{"left", "right"}, []string{"Go to left: the left side", "Go to right"}
	if shadowRoute(context.Background(), "m", "route", "pick", choices, descs) != nil {
		t.Fatal("a shadow was asked with System One off")
	}
	RootDB.Set(ConfigTable, "provider", "home")
	// The server answers whatever is shown first, so both ways round it
	// cancels to even: unsure.
	if got := shadowRoute(context.Background(), "m", "route", "pick", choices, descs)("left"); !strings.Contains(got, "unsure") {
		t.Errorf("an even answer: %q", got)
	}
	tl := Shadows()
	if len(tl) != 1 || tl[0].Unsure != 1 || tl[0].Machine != "m" || tl[0].Step != "route" {
		t.Fatalf("the tally: %+v", tl)
	}
	// A server that favours "left" wherever it is shown is sure.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		b, _ := io.ReadAll(r.Body)
		pLeftFirst := `{"o00":0.95,"o01":0.05}`
		if strings.Index(string(b), "Go to right") < strings.Index(string(b), "Go to left") {
			pLeftFirst = `{"o00":0.05,"o01":0.95}`
		}
		fmt.Fprintf(w, `{"answers":{"d":{"probabilities":%s}},"diagnostics":{"d":{"label_mass":1}}}`, pLeftFirst)
	})
	if got := shadowRoute(context.Background(), "m", "route", "pick", choices, descs)("left"); !strings.Contains(got, "sure, and the step chose the same") {
		t.Errorf("sure and agreeing: %q", got)
	}
	if got := shadowRoute(context.Background(), "m", "route", "pick", choices, descs)("right"); !strings.Contains(got, "sure, but the step chose right") {
		t.Errorf("sure and differing: %q", got)
	}
	tl = Shadows()
	if tl[0].Agreed != 1 || tl[0].Differed != 1 || tl[0].Unsure != 1 || !strings.Contains(tl[0].Line(), "sure 2 of 3 times, and agreed with the step 1 of those 2") {
		t.Errorf("the tally: %+v (%s)", tl[0], tl[0].Line())
	}
}
