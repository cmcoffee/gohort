package orchestrate

// The access page: one home for "how much can this agent reach".
//
// Two of its four groups already existed as sections near the bottom of the
// editor, behind everything you scroll past. Reviewing what you granted is its
// own errand and you are usually not editing when you do it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func accessRowsFor(t *testing.T, app *OrchestrateApp, id, view string) []map[string]any {
	t.Helper()
	url := "/api/agent-access?id=" + id
	if view != "" {
		url += "&view=" + view
	}
	r := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	app.handleAgentAccess(w, asUser(r, "alice"))
	if w.Code != http.StatusOK {
		t.Fatalf("view %q: %d %s", view, w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("view %q not rows: %v (%s)", view, err, w.Body.String())
	}
	return rows
}

func accessFixture(t *testing.T, rec AgentRecord) (*OrchestrateApp, Database) {
	t.Helper()
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	rec.Owner, rec.OrchestratorPrompt = "alice", "p"
	if _, err := saveAgent(udb, rec); err != nil {
		t.Fatal(err)
	}
	return app, udb
}

// The workspace group answers for the SANDBOX, which is not a tool and is
// shared by every tool that runs a command.
func TestTheWorkspaceGroupAnswersForTheSandbox(t *testing.T) {
	app, _ := accessFixture(t, AgentRecord{ID: "a1", Name: "Plain"})
	byName := map[string]map[string]any{}
	for _, row := range accessRowsFor(t, app, "a1", "workspace") {
		byName[row["name"].(string)] = row
	}
	for _, want := range []string{"Runs commands", "Writes files", "Opens network connections"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("the workspace group never mentions %q", want)
		}
	}
	// Default deployment, no restrictions: it reaches out, and says why.
	net := byName["Opens network connections"]
	if net["policy"] != "yes" {
		t.Errorf("an unrestricted workspace reads as %v", net["policy"])
	}

	// The agent's own ceiling shows, and says what it does NOT take away.
	app2, _ := accessFixture(t, AgentRecord{ID: "a2", Name: "Held", WorkspaceNoNetwork: true})
	for _, row := range accessRowsFor(t, app2, "a2", "workspace") {
		if row["name"] == "Opens network connections" {
			if row["policy"] != "no" {
				t.Errorf("the ceiling is not reflected: %v", row)
			}
			if !strings.Contains(row["detail"].(string), "tools and its model are unaffected") {
				t.Errorf("the row does not say what it leaves alone: %v", row["detail"])
			}
		}
	}
}

// Force Private cuts more than the workspace, and the row says so rather than
// reporting the narrower setting that happens to also be off.
func TestForcePrivateIsNamedAsTheWiderReason(t *testing.T) {
	app, _ := accessFixture(t, AgentRecord{ID: "a3", Name: "Sealed", ForcePrivate: true})
	for _, row := range accessRowsFor(t, app, "a3", "workspace") {
		if row["name"] != "Opens network connections" {
			continue
		}
		if !strings.Contains(row["detail"].(string), "not only the workspace") {
			t.Errorf("the wider reason is not named: %v", row["detail"])
		}
		if !strings.Contains(row["where"].(string), "Force Private") {
			t.Errorf("the row points at the wrong control: %v", row["where"])
		}
	}
}

// A switched-off sub-action shows as the capability it removes, not as a
// setting name somebody has to decode.
func TestASwitchedOffSubActionShowsAsTheCapability(t *testing.T) {
	app, _ := accessFixture(t, AgentRecord{ID: "a4", Name: "NoShell",
		DisabledToolActions: []string{"workspace/run"}})
	for _, row := range accessRowsFor(t, app, "a4", "workspace") {
		if row["name"] == "Runs commands" {
			if row["policy"] != "no" {
				t.Errorf("the shell reads as available: %v", row)
			}
			if !strings.Contains(row["detail"].(string), "file actions still work") {
				t.Errorf("the row does not say what survives: %v", row["detail"])
			}
		}
	}
}

// Every row says where its setting lives, because a reader who disagrees with
// one wants to change it rather than hunt for which surface owns it.
func TestEveryRowNamesWhereItIsSet(t *testing.T) {
	app, _ := accessFixture(t, AgentRecord{ID: "a5", Name: "Full",
		AttachedCollections: []string{"runbooks"}})
	for _, view := range []string{"workspace", "knowledge"} {
		for _, row := range accessRowsFor(t, app, "a5", view) {
			if w, _ := row["where"].(string); strings.TrimSpace(w) == "" {
				t.Errorf("%s row %v does not say where it is set", view, row["name"])
			}
		}
	}
}

// An unknown view is refused rather than silently answering with the tool
// list, which would look like a working group that reports the wrong thing.
func TestAnUnknownViewFallsBackToTheToolList(t *testing.T) {
	app, _ := accessFixture(t, AgentRecord{ID: "a6", Name: "X"})
	rows := accessRowsFor(t, app, "a6", "")
	if rows == nil {
		t.Error("the default view returned null rather than an empty list")
	}
}
