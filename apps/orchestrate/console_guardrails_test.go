package orchestrate

// The fleet-wide guardrail view. Its whole job is the interleave: per-agent
// logs are already ordered, and collating them by time across agents is the
// thing that makes "is anything being blocked that shouldn't be" answerable
// without opening every agent in turn.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func consoleGuardrailApp(t *testing.T) (*OrchestrateApp, Database, *http.Cookie) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = root
	return app, UserDB(root, "u"), authAs(t)
}

func fetchGuardrailRows(t *testing.T, app *OrchestrateApp, cookie *http.Cookie, query string) []consoleGuardrailRow {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/console/guardrail-blocks"+query, nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	app.handleConsoleGuardrails(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("console view returned %d: %s", w.Code, w.Body.String())
	}
	var rows []consoleGuardrailRow
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode rows: %v (%s)", err, w.Body.String())
	}
	return rows
}

func TestConsoleGuardrailsInterleavesAgentsNewestFirst(t *testing.T) {
	app, udb, cookie := consoleGuardrailApp(t)
	for _, a := range []AgentRecord{
		{ID: "a1", Name: "Wiwee", Owner: "u", OrchestratorPrompt: "p"},
		{ID: "a2", Name: "Scribe", Owner: "u", OrchestratorPrompt: "p"},
	} {
		if _, err := saveAgent(udb, a); err != nil {
			t.Fatalf("save agent: %v", err)
		}
	}
	base := time.Now().Add(-time.Hour)
	// Interleaved in time, but filed under different agents — which is exactly
	// the ordering a per-agent list cannot show.
	appendGuardrailBlock(udb, "a1", GuardrailBlock{At: base, Rule: "oldest", Hook: guardHookPreOutput})
	appendGuardrailBlock(udb, "a2", GuardrailBlock{At: base.Add(2 * time.Minute), Rule: "middle", Hook: guardHookPreAction})
	appendGuardrailBlock(udb, "a1", GuardrailBlock{At: base.Add(4 * time.Minute), Rule: "newest", Hook: guardHookPreInput})

	rows := fetchGuardrailRows(t, app, cookie, "")
	if len(rows) != 3 {
		t.Fatalf("expected three rows, got %d: %+v", len(rows), rows)
	}
	for i, want := range []string{"newest", "middle", "oldest"} {
		if rows[i].Name != want {
			t.Errorf("row %d = %q, want %q (order is the point of this view)", i, rows[i].Name, want)
		}
	}
	// Each row has to say which agent it came from, or the interleave is
	// unreadable.
	if rows[0].Agent != "Wiwee" || rows[1].Agent != "Scribe" {
		t.Errorf("rows lost their agent: %+v / %+v", rows[0], rows[1])
	}
}

func TestConsoleGuardrailsCarriesTheDetail(t *testing.T) {
	app, udb, cookie := consoleGuardrailApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "Wiwee", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	appendGuardrailBlock(udb, "a1", GuardrailBlock{
		At: time.Now(), Rule: "never discuss compensation", Hook: guardHookPreOutput,
		Reason: "the draft named a salary figure", Channel: "channel", Sender: "Alex Rivera",
	})
	rows := fetchGuardrailRows(t, app, cookie, "")
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %d", len(rows))
	}
	if rows[0].Reason != "the draft named a salary figure" {
		t.Errorf("the reason is what makes a row worth reading: %+v", rows[0])
	}
	for _, want := range []string{guardHookPreOutput, "channel", "Alex Rivera"} {
		if !strings.Contains(rows[0].Where, want) {
			t.Errorf("where-line missing %q: %q", want, rows[0].Where)
		}
	}
}

func TestConsoleGuardrailsScopesToTheOwner(t *testing.T) {
	app, udb, cookie := consoleGuardrailApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "Mine", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	appendGuardrailBlock(udb, "a1", GuardrailBlock{At: time.Now(), Rule: "mine"})
	// Another user's agent and block, in their own store.
	other := UserDB(app.DB, "other")
	if _, err := saveAgent(other, AgentRecord{ID: "b1", Name: "Theirs", Owner: "other", OrchestratorPrompt: "p"}); err != nil {
		t.Fatalf("save other agent: %v", err)
	}
	appendGuardrailBlock(other, "b1", GuardrailBlock{At: time.Now(), Rule: "theirs"})

	rows := fetchGuardrailRows(t, app, cookie, "")
	for _, row := range rows {
		if row.Name == "theirs" || row.Agent == "Theirs" {
			t.Fatalf("another user's block leaked into the view: %+v", row)
		}
	}
	if len(rows) != 1 {
		t.Errorf("expected only the owner's block, got %d: %+v", len(rows), rows)
	}
}

func TestConsoleGuardrailsAgentFilter(t *testing.T) {
	app, udb, cookie := consoleGuardrailApp(t)
	for _, a := range []AgentRecord{
		{ID: "a1", Name: "One", Owner: "u", OrchestratorPrompt: "p"},
		{ID: "a2", Name: "Two", Owner: "u", OrchestratorPrompt: "p"},
	} {
		if _, err := saveAgent(udb, a); err != nil {
			t.Fatalf("save agent: %v", err)
		}
	}
	appendGuardrailBlock(udb, "a1", GuardrailBlock{At: time.Now(), Rule: "from-one"})
	appendGuardrailBlock(udb, "a2", GuardrailBlock{At: time.Now(), Rule: "from-two"})

	rows := fetchGuardrailRows(t, app, cookie, "?agent=a2")
	if len(rows) != 1 || rows[0].Name != "from-two" {
		t.Errorf("agent filter ignored: %+v", rows)
	}
}

func TestConsoleGuardrailsEmptyIsAnEmptyList(t *testing.T) {
	app, udb, cookie := consoleGuardrailApp(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "Quiet", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	// A card view renders [] as "nothing yet"; null would render as an error.
	r := httptest.NewRequest(http.MethodGet, "/api/console/guardrail-blocks", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	app.handleConsoleGuardrails(w, r)
	if body := strings.TrimSpace(w.Body.String()); body != "[]" {
		t.Errorf("an empty fleet should serialize as [], got %q", body)
	}
}

func TestGuardrailRowTitleTrimsALongRule(t *testing.T) {
	got := guardrailRowTitle(strings.Repeat("x", 200))
	if n := len([]rune(got)); n > 90 {
		t.Errorf("title not trimmed: %d runes", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a trimmed title should say so: %q", got)
	}
	if guardrailRowTitle("   ") != "(rule not recorded)" {
		t.Error("a blank rule should still render a title")
	}
	// A rule in a non-ASCII script must not be cut mid-character — a byte-wise
	// slice renders the title as replacement glyphs, which reads as corruption
	// rather than as a trim.
	wide := guardrailRowTitle(strings.Repeat("é", 200))
	if !utf8.ValidString(wide) {
		t.Errorf("trim produced invalid UTF-8: %q", wide)
	}
	if n := len([]rune(wide)); n > 90 {
		t.Errorf("wide title not trimmed: %d runes", n)
	}
	// A short rule is returned untouched, ellipsis and all.
	if got := guardrailRowTitle("never discuss pay"); got != "never discuss pay" {
		t.Errorf("short rule was altered: %q", got)
	}
}
