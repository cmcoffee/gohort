package orchestrate

// The chat page is assembled in steps (chatPage): first-run redirect, the
// agent picker and the agent it opens on, the head globals, a Builder brief,
// the toolbar entries with nothing behind them. Each step shows on the page,
// so each is checked there.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// decodeAfter decodes the JSON value that follows marker on the page.
func decodeAfter(t *testing.T, page, marker string, out any) {
	t.Helper()
	i := strings.Index(page, marker)
	if i < 0 {
		t.Fatalf("no %s on the chat page", marker)
	}
	if err := json.NewDecoder(strings.NewReader(page[i+len(marker):])).Decode(out); err != nil {
		t.Fatalf("%s: %v", marker, err)
	}
}

func TestTheChatPageIsAssembledInSteps(t *testing.T) {
	app, udb := familyFixture(t)
	AuthDB().Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	bobToken := AuthCreateSession(AuthDB(), "bob")
	udb.Set(builderBriefTable, "b1", builderBriefRecord{ID: "b1", Text: "fix the writer"})
	get := func(user, url, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, url, nil)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: lastAgentCookie, Value: cookie})
		}
		if user == "bob" {
			r.AddCookie(&http.Cookie{Name: "gohort_session", Value: bobToken})
		} else {
			r = asUser(r, user)
		}
		w := httptest.NewRecorder()
		app.handleChatPage(w, r)
		return w
	}
	opensOn := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		var fields []ui.ChatField
		decodeAfter(t, w.Body.String(), `"extra_fields":`, &fields)
		if len(fields) != 1 || fields[0].Name != "agent_id" {
			t.Fatalf("the agent picker: %+v", fields)
		}
		return fields[0].Default
	}

	// The picker opens on the user's own agent, unless a link or the last
	// one used names another the picker has.
	page := get("alice", "/", "")
	if got := opensOn(page); got != "a-writer" {
		t.Errorf("opens on %q, want the user's own agent", got)
	}
	for _, c := range []struct{ url, cookie, want string }{
		{"/?agent=seed-builder", "", "seed-builder"},
		{"/?agent=bogus", "", "a-writer"},
		{"/", "seed-builder", "seed-builder"},
	} {
		if got := opensOn(get("alice", c.url, c.cookie)); got != c.want {
			t.Errorf("%s (cookie %q) opens on %q, want %q", c.url, c.cookie, got, c.want)
		}
	}
	var opts []ui.SelectOption
	decodeAfter(t, page.Body.String(), `"option_pairs":`, &opts)
	if len(opts) < 2 || opts[0].Value != "" || opts[0].Label != "(select agent)" {
		t.Errorf("the picker should lead with its placeholder: %+v", opts)
	}

	body := page.Body.String()
	for _, want := range []string{"window.ORCH_TOOL_CATALOG = ", "window.ORCH_SUB_AGENTS = {", "window.ORCH_CHANNEL_AGENTS = ", "window.ORCH_RECORD_AGENTS = ", "window.ORCH_PICTURE_DAYS = "} {
		if !strings.Contains(body, want) {
			t.Errorf("the head is missing %s", want)
		}
	}

	// The agent's own menu, then the fleet's.
	nav := navItemsOf(t, body)
	if len(nav) < 2 || nav[0].Label != "Overview" || nav[0].Menu != "Manage" || nav[len(nav)-1].Label != "Broken tools" || nav[len(nav)-1].Menu != "Fleet" {
		t.Errorf("menu order: first %+v, last %+v", nav[0], nav[len(nav)-1])
	}
	var modes []ui.ChatMode
	decodeAfter(t, body, `"modes":`, &modes)
	if len(modes) != 2 || modes[0].Label != "Private" || modes[1].Label != "Fresh" ||
		modes[0].Icon != "globe-off" || modes[1].Icon != "eraser" {
		t.Errorf("modes: %+v", modes)
	}
	var actions []ui.ToolbarAction
	decodeAfter(t, body, `"actions":`, &actions)
	urls := map[string]bool{}
	for _, a := range actions {
		urls[a.URL] = true
	}
	if !urls["orchestrate_edit_agent"] || !urls["orchestrate_send_to_builder"] {
		t.Errorf("the toolbar lost its entries: %+v", actions)
	}
	// Nothing to ask an owner for: that entry goes. Sources go when none is
	// registered, and other tests here register theirs for the whole process.
	if urls["orchestrate_ask_owner"] {
		t.Error("Ask the owner was kept with no one to ask")
	}
	if urls["orchestrate_sources_modal"] != AnyReferenceSource("alice") {
		t.Errorf("the Sources entry (shown %v) should show only when there is a source", urls["orchestrate_sources_modal"])
	}

	// A brief for Builder is sent once.
	if !strings.Contains(get("alice", "/?builder_brief=b1", "").Body.String(), `"auto_send":"fix the writer"`) {
		t.Error("the brief was not handed to the panel")
	}
	if strings.Contains(get("alice", "/?builder_brief=b1", "").Body.String(), `"auto_send"`) {
		t.Error("the brief was sent twice")
	}

	// No agents of their own: the wizard, unless a link points somewhere or
	// they skipped it.
	if w := get("bob", "/?session=x", ""); w.Code != http.StatusOK {
		t.Errorf("a session link should bypass the wizard: %d", w.Code)
	}
	if w := get("bob", "/", ""); w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Location"), "/agent/wizard?kind=assistant&first_run=1") {
		t.Errorf("first run: %d %q", w.Code, w.Header().Get("Location"))
	} else if strings.Contains(w.Body.String(), "ORCH_TOOL_CATALOG") {
		t.Error("the page was drawn after the redirect")
	}
	if w := get("bob", "/?skip_first_run=1", ""); w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Errorf("skip: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := get("bob", "/", ""); w.Code != http.StatusOK {
		t.Errorf("once skipped, the page: %d", w.Code)
	}
}
