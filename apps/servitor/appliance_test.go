package servitor

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A write-only secret must survive a save that never carried it.
//
// Every read path blanks Password and RepoToken before the record leaves the
// server, so the edit form always loads with them empty — which means an empty
// field on save says "you were not shown this", never "clear it".
//
// The guard used to read the INCOMING type: `req.Type == "repo" && ...`. Any
// save whose body did not carry that type wrote a blank straight over a stored
// token, and it presented as "no token configured" rather than as a token that
// had stopped working, because by then there genuinely was none.
func TestAWriteOnlySecretIsNotBlankedByASaveThatOmitsIt(t *testing.T) {
	aliceAndBob(t)
	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "alice")
	// RepoFiles is set so that no save here starts a clone.
	udb.Set(applianceTable, "box", Appliance{ID: "box", Name: "box", Type: "ssh", Host: "box.example.com", Owner: "alice", Password: "pw-secret", RepoFiles: 1})
	udb.Set(applianceTable, "code", Appliance{ID: "code", Name: "code", Type: "repo", RepoURL: "https://example.com/code.git", Owner: "alice", RepoToken: "tok-secret", RepoFiles: 1})
	stored := func(id string) Appliance {
		var a Appliance
		udb.Get(applianceTable, id, &a)
		return a
	}
	save := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		routed(app)(w, reqAs(t, http.MethodPost, "/api/appliances", "alice", body))
		if w.Code != http.StatusOK {
			t.Fatalf("save answered %d %s", w.Code, w.Body.String())
		}
	}
	save(`{"id":"box","name":"box renamed","host":"box.example.com"}`)
	save(`{"id":"box","type":"ssh","name":"box","host":"box.example.com","password":""}`)
	if stored("box").Password != "pw-secret" {
		t.Error("a save that did not carry the password blanked it")
	}
	save(`{"id":"code","type":"repo","name":"code renamed","repo_url":"https://example.com/code.git"}`)
	if stored("code").RepoToken != "tok-secret" {
		t.Error("a save that did not carry the repo token blanked it")
	}
	// A real type change still drops the secret that belongs to the kind this
	// appliance no longer is — it can never be used again, and leaving it is
	// secret material sitting in a record nobody thinks holds one.
	save(`{"id":"box","type":"repo","name":"box","repo_url":"https://example.com/box.git"}`)
	if stored("box").Password != "" {
		t.Error("a system that left SSH kept its password")
	}
}

// TestApplianceTierParsing — an unrecognized value follows routing rather than
// pinning a tier nobody chose. Records written by a future version, or edited by
// hand, must degrade to the deployment's own decision.
func TestApplianceTierParsing(t *testing.T) {
	cases := map[string]LLMTier{
		"lead": LEAD, "LEAD": LEAD, " lead ": LEAD,
		"worker": WORKER, "Worker": WORKER,
		"": TierUnset, "precision": TierUnset, "true": TierUnset,
	}
	for in, want := range cases {
		if got := applianceTierOverride(in); got != want {
			t.Errorf("applianceTierOverride(%q) = %v, want %v", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"lead": "lead", "LEAD": "lead", "worker": "worker", "": "", "nonsense": "",
	} {
		if got := normalizeApplianceTier(in); got != want {
			t.Errorf("normalizeApplianceTier(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTheModalGatesLeadOnTheServerFlag — the form is hand-written JS in the
// chat page's asset, so the gate has to be checked THERE.
//
// This is the second half of a lesson: the per-appliance tier fields were first
// added to applianceFields() in page.go, which read like the appliance form and
// was called by nothing at all. They rendered nowhere. That file is gone; the
// modal below is the form, and these assertions are against the thing that
// actually ships.
func TestTheModalGatesLeadOnTheServerFlag(t *testing.T) {
	src, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "if (rec.lead_tier_available) opts.push(['lead', 'Lead (always)']);") {
		t.Error("the modal no longer gates the Lead option on lead_tier_available — it would " +
			"offer a tier the server refuses to save and the runtime ignores")
	}
	for _, field := range []string{"orchestrator_tier: orchTierIn.value", "worker_tier:       workTierIn.value"} {
		if !strings.Contains(body, field) {
			t.Errorf("the save payload does not carry %q — the select would render and never persist", field)
		}
	}
	// Both selects must be in the modal body, or one of them is unreachable.
	if !strings.Contains(body, "tierSection,") {
		t.Error("the tier section is built but never added to the modal body")
	}
}

// TestTheAvailabilityFlagIsComputedNotStored — it is a deployment fact, so
// persisting it would freeze a snapshot that goes stale the moment Model
// Privacy changes.
func TestTheAvailabilityFlagIsComputedNotStored(t *testing.T) {
	body := webSource(t)
	if n := strings.Count(body, "LeadTierAvailable = AllLLMsPrivate()"); n < 3 {
		t.Errorf("only %d of the appliance read paths set the availability flag — a form "+
			"reached through one of the others would silently hide the Lead option", n)
	}
	if !strings.Contains(body, "req.LeadTierAvailable = false") {
		t.Error("the save path does not clear the computed flag, so it would be persisted")
	}
}

// A remote stub is stored under the far side's kind, so the editor has to
// open a record with a peer_name as "remote", or saving it demands a host and
// typing one silently turns it into a local system.
func TestTheEditorOpensAPeerSystemAsRemote(t *testing.T) {
	src, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	fix := strings.Index(body, "if (rec.peer_name) rec.type = 'remote';")
	first := strings.Index(body, "setType(rec.type || 'ssh');")
	if fix < 0 || first < 0 || fix > first {
		t.Fatal("the editor must switch a peer system to remote before its first setType")
	}
}

// "Tools run as" applies to a toolset only. The editor sets it from the stored
// type when it opens, and switching type has to move it too, or a new toolset
// never shows it and a system switched away from one keeps it.
func TestToolsRunAsFollowsTheTypeSwitch(t *testing.T) {
	src, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "function setType(t) {")
	if start < 0 {
		t.Fatal("the editor's setType is gone")
	}
	end := strings.Index(body[start:], "\n      }\n")
	if end < 0 {
		t.Fatal("could not find the end of setType")
	}
	if !strings.Contains(body[start:start+end], "runAsSection.style.display = t === 'toolset' ? '' : 'none';") {
		t.Error("setType does not show Tools run as exactly for a toolset")
	}
}

// A save keeps its gates: a system shared with someone, even with Full
// access, is changed only by its owner; the reply never carries the password
// it was sent; and a system is not pinned to the lead model while a model the
// deployment uses is not private.
func TestASystemSaveKeepsItsGates(t *testing.T) {
	aliceAndBob(t)
	app := &Servitor{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "alice")
	box := Appliance{ID: "box", Name: "box", Type: "ssh", Host: "box.example.com", Owner: "alice",
		AllowedUsers: []string{"bob"}, FullAccess: []string{"bob"}, LevelsSet: true}
	udb.Set(applianceTable, box.ID, box)
	app.indexNamedShares("alice", box.ID, box.AllowedUsers)
	save := func(user, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		routed(app)(w, reqAs(t, http.MethodPost, "/api/appliances", user, body))
		return w
	}
	if w := save("bob", `{"id":"box","name":"bob's now","host":"elsewhere.example.com"}`); w.Code != http.StatusForbidden {
		t.Errorf("a Full-access user's edit answered %d", w.Code)
	}
	var a Appliance
	if udb.Get(applianceTable, "box", &a); a.Name != "box" || a.Host != "box.example.com" {
		t.Errorf("a Full-access user changed the system: %+v", a)
	}

	w := save("alice", `{"name":"new","host":"new.example.com","password":"pw-sent"}`)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "pw-sent") {
		t.Errorf("the save's reply carries the password: %d %s", w.Code, w.Body.String())
	}

	if AllLLMsPrivate() {
		t.Skip("every model reads as private here")
	}
	if w := save("alice", `{"name":"pinned","host":"pinned.example.com","orchestrator_tier":"lead"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a lead pin with a model that is not private answered %d", w.Code)
	}
	for _, k := range udb.Keys(applianceTable) {
		var a Appliance
		if udb.Get(applianceTable, k, &a); a.Name == "pinned" {
			t.Error("the refused lead pin was saved")
		}
	}
}
