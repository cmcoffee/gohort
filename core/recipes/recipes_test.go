package recipes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/messaging"
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
	b, secrets, _, err := Fill(r, map[string]string{"site": "https://acme.atlassian.net/", "email": "someone@example.com", "api_token": "tok"})
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
	if secrets.Credentials["jira"] != "tok" {
		t.Errorf("the secret is held for its credential: %v", secrets)
	}
	if _, _, _, err := Fill(r, map[string]string{"site": "http://acme.atlassian.net", "email": "x", "api_token": "t"}); err == nil {
		t.Error("a url answer must be https")
	}
	if _, _, _, err := Fill(r, map[string]string{"site": "https://acme.atlassian.net", "api_token": "t"}); err == nil {
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

// A helper question's outputs fill the pieces: a placeholder standing alone
// takes the output whole (an object), one inside text takes it as text; the
// helper's inputs are filled from the other answers first.
func TestAHelperFillsItsOutputsIn(t *testing.T) {
	var gotWith map[string]string
	RegisterHelper("test_echo", Helper{Outputs: []string{"obj"}, Run: func(answer string, with map[string]string) (map[string]any, []string, error) {
		gotWith = with
		return map[string]any{"obj": map[string]any{"said": answer}}, []string{"a note"}, nil
	}})
	r := Recipe{ID: "t", Title: "T", Questions: []Question{
		{Name: "host", Label: "Host", Kind: "http_url", Required: true},
		{Name: "thing", Label: "Thing", Helper: "test_echo", With: map[string]string{"at": "{{host}}/x"}},
	}, Bundle: core.ArtifactBundle{Bundle: core.ArtifactBundleFormat, Artifacts: []core.PortableArtifact{{Type: "connector", Name: "c",
		Recipe: json.RawMessage(`{"name":"c","spec":"{{thing.obj}}","desc":"at {{host}}: {{thing.obj}}"}`)}}}}
	if err := Validate(r); err != nil {
		t.Fatal(err)
	}
	b, _, warns, err := Fill(r, map[string]string{"host": "http://box.lan:8188/", "thing": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if gotWith["at"] != "http://box.lan:8188/x" || len(warns) != 1 {
		t.Errorf("the helper's inputs are filled and its warnings returned: %v %v", gotWith, warns)
	}
	var got struct {
		Spec map[string]any `json:"spec"`
		Desc string         `json:"desc"`
	}
	if err := json.Unmarshal(b.Artifacts[0].Recipe, &got); err != nil || got.Spec["said"] != "hi" || got.Desc != `at http://box.lan:8188: {"said":"hi"}` {
		t.Errorf("whole placeholder takes the object, inline takes text: %s %v", b.Artifacts[0].Recipe, err)
	}
	if _, _, _, err := Fill(r, map[string]string{"host": "ftp://box"}); err == nil {
		t.Error("an http_url answer must be http or https")
	}

	bad := r
	bad.Bundle.Artifacts = []core.PortableArtifact{{Type: "connector", Name: "c", Recipe: json.RawMessage(`{"spec":"{{thing.nope}}"}`)}}
	if Validate(bad) == nil {
		t.Error("an output the helper does not make is refused")
	}
	bad = r
	bad.Questions = []Question{r.Questions[0], {Name: "thing", Label: "Thing", Helper: "no_such_helper"}}
	if Validate(bad) == nil {
		t.Error("a helper this oddjob does not have is refused")
	}
	bad = r
	bad.Bundle.Artifacts = []core.PortableArtifact{{Type: "connector", Name: "c", Recipe: json.RawMessage(`{"spec":"{{host.obj}}"}`)}}
	if Validate(bad) == nil {
		t.Error("a question with no helper has no outputs")
	}
	bad = r
	bad.Questions = []Question{{Name: "host", Label: "Host", Kind: "uri"}, r.Questions[1]}
	if Validate(bad) == nil {
		t.Error("a kind oddjob does not know is refused, not read as text")
	}
}

// The ComfyUI starter wires the default graph into an image connector that
// lands unapproved, pointed at the address given.
func TestTheComfyUITemplateAddsAWiredConnector(t *testing.T) {
	db := testDB(t)
	res, err := Install(db, "comfyui", "admin", map[string]string{"base_url": "http://192.168.1.20:8188/"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 1 {
		t.Fatalf("one connector lands: %+v", res)
	}
	c, ok := core.GetConnector(db, "comfyui")
	if !ok || c.Kind != core.RestImageConnectorKind || c.Template != "comfyui" {
		t.Fatalf("a comfyui image connector: %+v", c)
	}
	var spec core.RestImageSpec
	if err := json.Unmarshal(c.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(spec.PollURLTemplate, "http://192.168.1.20:8188/view?") || spec.ComfyMap.OutputNode != "9" || len(spec.ComfyMap.PromptNodes) == 0 {
		t.Errorf("the spec is wired to the server: %+v", spec)
	}
	if _, err := Install(db, "comfyui", "admin", map[string]string{"base_url": "http://box:8188", "name": "comfy2", "workflow": "{not json"}); err == nil {
		t.Error("a workflow that cannot be read is refused before anything lands")
	}
}

// A bridge is a template too: the Slack bridge adds its credential and a
// polling connector for the channel; the Mattermost bridge adds a generic
// webhook connector and hands its token to the webhook secret store.
func TestBridgeTemplatesAddConnectors(t *testing.T) {
	db := testDB(t)
	res, err := Install(db, "slack-bridge", "admin", map[string]string{"channel_id": "C0123", "bot_token": "bot-token-for-test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 2 {
		t.Fatalf("the credential and the connector land: %+v", res)
	}
	c, ok := core.GetConnector(db, "slack_bridge")
	if !ok || c.Kind != core.RestMessagingConnectorKind || c.Approved {
		t.Fatalf("an unapproved rest_messaging connector: %+v", c)
	}
	var spec core.RestMessagingSpec
	if err := json.Unmarshal(c.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.ChatIDConst != "C0123" || !strings.HasSuffix(spec.PollURL, "channel=C0123") || len(spec.Skip) != 2 {
		t.Errorf("the channel is filled in and the bot's own posts are skipped: %+v", spec)
	}

	got := map[string]string{}
	messaging.RegisterWebhookSecretSetter(func(conn, secret string) error { got[conn] = secret; return nil })
	t.Cleanup(func() { messaging.RegisterWebhookSecretSetter(nil) })
	res, err = Install(db, "mattermost-bridge", "admin", map[string]string{"site": "https://chat.example.com/", "name": "mm", "bot_token": "tok-a", "webhook_token": "tok-b"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 2 || got["mm"] != "tok-b" {
		t.Fatalf("both land, and the webhook token goes to the connector's secret: %+v %v", res, got)
	}
	c, _ = core.GetConnector(db, "mm")
	spec = core.RestMessagingSpec{}
	json.Unmarshal(c.Spec, &spec)
	if spec.WebhookProvider != "generic" || spec.Webhook == nil || spec.Webhook.TokenPath != "token" ||
		spec.SendURL != "https://chat.example.com/api/v4/posts" || spec.Skip[0].Values[0] != "oddjob" {
		t.Errorf("a generic webhook spec with the answers in: %+v", spec)
	}
	if strings.Contains(string(c.Spec), "tok-") {
		t.Error("no secret is written into the spec")
	}
}

// A webhook secret question must point at a connector the template adds, and
// only a secret question says where a secret goes.
func TestWebhookSecretQuestionsAreChecked(t *testing.T) {
	r, _, _ := Get(nil, "mattermost-bridge")
	bad := r
	bad.Questions = append([]Question(nil), r.Questions...)
	bad.Questions[4].WebhookSecret = "other"
	if Validate(bad) == nil {
		t.Error("a webhook secret for a connector the template does not add is refused")
	}
	bad.Questions[4].WebhookSecret, bad.Questions[4].Secret = "{{name}}", false
	if Validate(bad) == nil {
		t.Error("a plain answer cannot name a secret's destination")
	}
}
