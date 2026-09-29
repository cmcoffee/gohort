package recipes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func testDB(t *testing.T) core.Database {
	t.Helper()
	db := &core.DBase{Store: kvlite.MemStore()}
	prevAuth, prevRoot := core.AuthDB, core.RootDB
	core.AuthDB = func() core.Database { return db }
	core.RootDB = db
	t.Cleanup(func() { core.AuthDB, core.RootDB = prevAuth, prevRoot })
	return db
}

// Every built-in template is valid, and the starters are all there.
func TestTheBuiltInTemplatesAreValid(t *testing.T) {
	want := map[string]bool{"github": false, "slack": false, "jira-cloud": false, "open-meteo": false, "system-info": false, "scout": false}
	for _, r := range builtins() {
		if err := Validate(r); err != nil {
			t.Errorf("%s: %v", r.ID, err)
		}
		want[r.ID] = true
	}
	for id, found := range want {
		if !found {
			t.Errorf("built-in %s is missing or did not load", id)
		}
	}
}

// Filling a template puts the answers where they are marked, checks a url
// answer, holds a secret answer apart for its credential, and refuses a
// required question left blank.
func TestFillingATemplatePutsTheAnswersIn(t *testing.T) {
	r, _, ok := Get(nil, "jira-cloud")
	if !ok {
		t.Fatal("no jira-cloud template")
	}
	b, secrets, err := Fill(r, map[string]string{"site": "https://acme.atlassian.net/", "email": "someone@example.com", "api_token": "tok"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(b)
	for _, want := range []string{`"base_url":"https://acme.atlassian.net"`, `"username":"someone@example.com"`, `https://acme.atlassian.net/rest/api/3/issue`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the filled bundle should carry %s", want)
		}
	}
	if strings.Contains(string(raw), "{{") || strings.Contains(string(raw), `"tok"`) {
		t.Error("no placeholder is left and the secret is not in the pieces")
	}
	if secrets["jira"] != "tok" {
		t.Errorf("the secret is held for its credential: %v", secrets)
	}
	if _, _, err := Fill(r, map[string]string{"site": "http://acme.atlassian.net", "email": "x", "api_token": "t"}); err == nil {
		t.Error("a url answer must be https")
	}
	if _, _, err := Fill(r, map[string]string{"site": "https://acme.atlassian.net", "api_token": "t"}); err == nil {
		t.Error("a required question left blank is refused")
	}
}

// Adding a template imports its pieces as drafts and writes the secret answer
// into its credential, which stays disabled until the admin enables it.
func TestAddingATemplateInstallsDrafts(t *testing.T) {
	db := testDB(t)
	res, err := Install(db, "github", "admin", map[string]string{"token": "ghp_test_token_value"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 5 {
		t.Fatalf("the credential and four tools land: %+v", res)
	}
	c, ok := core.Secure().Load("github")
	if !ok || !c.Disabled {
		t.Fatalf("the credential lands disabled: %+v", c)
	}
	if _, _, hasSecret := core.Secure().CredentialStatus("github"); !hasSecret {
		t.Error("the secret answer is stored in the credential")
	}
	again, _ := Install(db, "github", "admin", map[string]string{"token": "another"})
	if again.Imported != 0 || len(again.Warnings) == 0 {
		t.Errorf("added again, nothing is overwritten and it says so: %+v", again)
	}
}

// A template file round-trips: exported, it imports under another id; a
// built-in id is refused; a secret-shaped value written into a piece is
// refused; a placeholder no question asks for is refused.
func TestATemplateFileImportsAndExports(t *testing.T) {
	db := testDB(t)
	data, err := Export(db, "slack")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Import(db, data, "admin"); err == nil {
		t.Error("a built-in's id is refused")
	}
	var r Recipe
	json.Unmarshal(data, &r)
	r.ID = "slack-ops"
	data, _ = json.Marshal(r)
	if _, err := Import(db, data, "admin"); err != nil {
		t.Fatalf("imported under its own id: %v", err)
	}
	if _, src, ok := Get(db, "slack-ops"); !ok || src != Imported {
		t.Error("the imported template is listed as imported")
	}
	if err := Delete(db, "slack"); err == nil {
		t.Error("a built-in cannot be deleted")
	}
	if err := Delete(db, "slack-ops"); err != nil {
		t.Errorf("an imported one can: %v", err)
	}

	bad := r
	bad.ID = "leaky"
	bad.Bundle.Artifacts = append([]core.PortableArtifact{}, bad.Bundle.Artifacts...)
	bad.Bundle.Artifacts[1].Recipe = json.RawMessage(`{"name":"x","mode":"api","url_template":"https://slack.com/api/x","headers":{"Authorization":"Bearer xoxb12345678901234567890"}}`)
	if err := Validate(bad); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Errorf("a secret written into a piece is refused: %v", err)
	}
	stray := r
	stray.ID = "stray"
	stray.Description = "x"
	stray.Bundle.Artifacts = append([]core.PortableArtifact{}, r.Bundle.Artifacts...)
	stray.Bundle.Artifacts[1].Recipe = json.RawMessage(`{"name":"x","mode":"api","url_template":"https://slack.com/api/{{team}}"}`)
	if err := Validate(stray); err == nil || !strings.Contains(err.Error(), "team") {
		t.Errorf("a placeholder no question asks for is refused: %v", err)
	}
}

// Saving things already built as a template turns the chosen values into
// questions, keeping them out of the file.
func TestSavingAsATemplateTurnsValuesIntoQuestions(t *testing.T) {
	db := testDB(t)
	if err := core.Secure().Save(core.SecureCredential{Name: "wiki", Type: core.SecureCredBearer, BaseURL: "https://wiki.acme.example", AllowedEndpoints: []string{"/api/**"}}, "s3cret"); err != nil {
		t.Fatal(err)
	}
	r, err := Save(db, Recipe{ID: "acme-wiki", Title: "Acme wiki"}, []core.ArtifactSel{{Type: "credential", Name: "wiki"}},
		[]SaveQuestion{
			{Question: Question{Name: "site", Label: "Wiki address", Kind: "url", Required: true}, Value: "https://wiki.acme.example"},
			{Question: Question{Name: "token", Label: "Wiki token", Secret: true, Credential: "wiki", Required: true}},
		}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r.Bundle)
	if !strings.Contains(string(raw), "{{site}}") || strings.Contains(string(raw), "wiki.acme.example") || strings.Contains(string(raw), "s3cret") {
		t.Errorf("the value became a question and no secret travelled: %s", raw)
	}
	if len(r.Questions) != 2 || r.Questions[0].Name != "site" {
		t.Errorf("the questions keep their order: %+v", r.Questions)
	}
}
