package account

// A person exports and imports their own work from the Account page. The
// export is theirs alone; the import takes only their kinds of artifact.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func accountFixture(t *testing.T) (*Account, map[string]string) {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	sessions := map[string]string{
		"alice": AuthCreateSession(adb, "alice"),
		"bob":   AuthCreateSession(adb, "bob"),
	}
	return &Account{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}, sessions
}

func as(r *http.Request, token string) *http.Request {
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
	return r
}

func TestExportEverythingIsOnlyYourOwn(t *testing.T) {
	T, sess := accountFixture(t)
	for owner, name := range map[string]string{"alice": "alices-skill", "bob": "bobs-skill"} {
		if _, err := SaveSkill(RootDB, owner, SkillRecord{Name: name, Description: "d", Instructions: "i"}); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	T.handleArtifactExport(w, as(httptest.NewRequest(http.MethodGet, "/account/api/artifacts/export?all=1", nil), sess["alice"]))
	if w.Code != http.StatusOK {
		t.Fatalf("export: %d %s", w.Code, w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("the export should download: %q", cd)
	}
	var b ArtifactBundle
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range b.Artifacts {
		names = append(names, a.Name)
	}
	if len(names) != 1 || names[0] != "alices-skill" {
		t.Fatalf("alice's export should hold only her own work, got %v", names)
	}

	// A credential is an administrator's to export.
	w = httptest.NewRecorder()
	T.handleArtifactExport(w, as(httptest.NewRequest(http.MethodGet, "/account/api/artifacts/export?type=credential&name=x", nil), sess["alice"]))
	if w.Code != http.StatusBadRequest {
		t.Errorf("a user exported a credential: %d", w.Code)
	}
}

func TestImportTakesYourKindsAndReportsTheRest(t *testing.T) {
	T, sess := accountFixture(t)
	skill, _ := json.Marshal(SkillRecord{Name: "imported", Description: "d", Instructions: "i"})
	bundle, _ := json.Marshal(ArtifactBundle{Bundle: ArtifactBundleFormat, Artifacts: []PortableArtifact{
		{Type: "skill", Name: "imported", Recipe: skill},
		{Type: "connector", Name: "hook", Recipe: json.RawMessage(`{"name": "hook", "kind": "rest_poll"}`)},
	}})
	body, _ := json.Marshal(map[string]string{"pack": string(bundle)})

	w := httptest.NewRecorder()
	T.handleArtifactPreview(w, as(httptest.NewRequest(http.MethodPost, "/account/api/artifacts/preview", bytes.NewReader(body)), sess["bob"]))
	var pv ArtifactPreviewResult
	_ = json.Unmarshal(w.Body.Bytes(), &pv)
	if w.Code != http.StatusOK || pv.WouldImport != 1 || pv.WouldSkip != 1 {
		t.Fatalf("preview: %d %+v", w.Code, pv)
	}

	w = httptest.NewRecorder()
	T.handleArtifactImport(w, as(httptest.NewRequest(http.MethodPost, "/account/api/artifacts/import", bytes.NewReader(body)), sess["bob"]))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "only an administrator") {
		t.Fatalf("import: %d %s", w.Code, w.Body.String())
	}
	s, ok := FindSkillByName(RootDB, "bob", "imported")
	if !ok || !s.Disabled {
		t.Fatalf("the skill should land in bob's account, switched off: ok=%v %+v", ok, s)
	}
	if _, ok := GetConnector(RootDB, "hook"); ok {
		t.Error("a user's import created a deployment-wide connector")
	}
}

func TestAnonymousCannotReachTheDataDoors(t *testing.T) {
	T, _ := accountFixture(t)
	for _, h := range []http.HandlerFunc{T.handleArtifactExport, T.handleArtifactPreview, T.handleArtifactImport} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodPost, "/account/api/artifacts/import", strings.NewReader("{}")))
		if w.Code == http.StatusOK {
			t.Error("a signed-out request reached an account data door")
		}
	}
}
