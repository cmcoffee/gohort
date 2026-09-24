package admin

// Several admin panels share the api/settings record, and the GET fills every
// tunable in at its EFFECTIVE value. A panel that posted the whole record back
// wrote each of those as an explicit setting, so saving one unrelated field
// pinned every knob at today's default, and a later default never arrived.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// settingsAdmin is an admin app on an empty store, with the tunables read
// from the same store and the settings routes mounted.
func settingsAdmin(t *testing.T) (*AdminApp, func(method, body string) *httptest.ResponseRecorder) {
	t.Helper()
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	SetTunablesDB(a.db)
	t.Cleanup(func() { SetTunablesDB(nil) })
	mux := http.NewServeMux()
	a.registerSystemRoutes(mux)
	call := func(method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/api/settings", strings.NewReader(body)))
		return w
	}
	return a, call
}

// overridable returns n numeric knobs that can be set one above their default.
func overridable(t *testing.T, n int) []TunableSpec {
	t.Helper()
	var out []TunableSpec
	for _, s := range AllTunableSpecs() {
		if s.Kind != KindBool && s.Default+1 <= s.Max && s.Default+1 >= s.Min {
			out = append(out, s)
		}
		if len(out) == n {
			return out
		}
	}
	t.Skipf("need %d numeric tunables with room above the default, have %d", n, len(out))
	return nil
}

func TestSavingOneSettingDoesNotPinEveryTunable(t *testing.T) {
	a, call := settingsAdmin(t)
	knobs := overridable(t, 3)
	kept, pinned, changed := knobs[0], knobs[1], knobs[2]
	// One real override, and one knob pinned at its default the way the old
	// whole-record save left them.
	a.db.Set(WebTable, kept.Key, kept.Default+1)
	a.db.Set(WebTable, pinned.Key, pinned.Default)
	InvalidateTunables()

	// A panel loads the record and posts it all back with one field edited.
	var rec map[string]any
	if err := json.Unmarshal(call("GET", "").Body.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	rec["service_name"] = "Workshop"
	body, _ := json.Marshal(rec)
	if w := call("POST", string(body)); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}

	var name string
	if a.db.Get(WebTable, "service_name", &name); name != "Workshop" {
		t.Errorf("the edited field did not save: %q", name)
	}
	for _, s := range AllTunableSpecs() {
		var v float64
		has := a.db.Get(WebTable, s.Key, &v)
		switch s.Key {
		case kept.Key:
			if !has || v != kept.Default+1 {
				t.Errorf("the real override on %s was lost: %v %v", s.Key, has, v)
			}
		default:
			if has {
				t.Errorf("saving service_name pinned %s at %v", s.Key, v)
			}
		}
	}

	// A knob the operator does change is stored, and sent through the verb the
	// panels use.
	if w := call("PATCH", `{"`+changed.Key+`":`+jsonNum(changed.Default+1)+`}`); w.Code != 200 {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
	}
	if got := TunableEffectiveValue(changed.Key); got != changed.Default+1 {
		t.Errorf("a changed knob did not take: %v", got)
	}
	// And setting it back to the default stores no value, so it follows the
	// default from then on.
	call("PATCH", `{"`+changed.Key+`":`+jsonNum(changed.Default)+`}`)
	var v float64
	if a.db.Get(WebTable, changed.Key, &v) {
		t.Errorf("setting %s back to its default pinned it at %v", changed.Key, v)
	}
}

// Every panel on the shared record saves only the field that changed. A POST
// sends back the whole record as loaded, which also puts back whatever another
// panel changed since the page opened.
func TestSettingsPanelsSendOnlyTheChangedField(t *testing.T) {
	sections := append(everyAdminSection(t), buildTunableSections()...)
	n := 0
	for _, sec := range sections {
		fp, ok := sec.Body.(ui.FormPanel)
		if !ok || fp.Source != "api/settings" {
			continue
		}
		n++
		if fp.Method != "PATCH" {
			t.Errorf("%q saves the whole settings record with %q", sec.Title, fp.Method)
		}
	}
	if n == 0 {
		t.Fatal("found no settings panels; the walk is broken")
	}
}

func jsonNum(f float64) string { b, _ := json.Marshal(f); return string(b) }
