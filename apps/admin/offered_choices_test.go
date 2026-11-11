package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Names no longer offered, through the real routes: only an administrator
// reaches it; it lists the stale name with where it is used; Change to...
// offers what the provider offers now and changes every use; the section is
// on the Services tab beside Connectors.
func TestNamesNoLongerOfferedThroughItsRoutes(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prevAuth })

	voice := "af_heart"
	service.RegisterOfferedChoices(func() []service.OfferedChoice {
		return []service.OfferedChoice{{ID: "admin-test.voice", Label: "Admin test voice", Page: "Speech (TTS)",
			Offered: func(context.Context) ([]string, bool, error) { return []string{"narrator", "storyteller"}, true, nil },
			Uses: func() []service.ChoiceUse {
				return []service.ChoiceUse{{Where: "the test agent's voice", Value: voice, Set: func(to string) error { voice = to; return nil }}}
			}}}
	})

	mux := http.NewServeMux()
	(&AdminApp{}).RegisterRoutes(mux, "")
	do := func(user, method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	if code, _ := do("alice", "GET", "/api/offered-choices", ""); code < 400 {
		t.Fatalf("a user who is not an administrator read the list: %d", code)
	}
	code, body := do("root", "GET", "/api/offered-choices", "")
	var rows []map[string]string
	json.Unmarshal([]byte(body), &rows)
	var row map[string]string
	for _, r := range rows {
		if r["what"] == "Admin test voice" {
			row = r
		}
	}
	if code != 200 || row == nil || row["name"] != "af_heart" || row["where"] != "the test agent's voice" {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, body := do("root", "GET", "/api/offered-choices/options?id="+row["_id"], ""); code != 200 || !strings.Contains(body, `"narrator"`) || !strings.Contains(body, `"storyteller"`) {
		t.Fatalf("options: %d %s", code, body)
	}
	if code, _ := do("root", "POST", "/api/offered-choices/map?id="+row["_id"], `{"to":"bm_george"}`); code != http.StatusBadRequest || voice != "af_heart" {
		t.Errorf("a name the provider does not offer was accepted: %d, voice %q", code, voice)
	}
	if code, body := do("root", "POST", "/api/offered-choices/map?id="+row["_id"], `{"to":"narrator"}`); code >= 300 || voice != "narrator" || !strings.Contains(body, `"changed":1`) {
		t.Fatalf("map: %d %s, voice %q", code, body, voice)
	}
	if sectionGroup[service.StaleChoicesPage] != "Services" {
		t.Errorf("the section is on %q, want Services", sectionGroup[service.StaleChoicesPage])
	}
}
