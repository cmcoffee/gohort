package extensions

// A person's own webhooks, on their Extensions page: the watches it may
// wake are theirs and woken by a post, the secret it is added with never
// comes back, how it checks stays as it was added, and its row says when
// its watch is gone.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/webhooks"
)

func TestAPersonsWebhooks(t *testing.T) {
	app, req := authedExtensions(t)
	do := func(method, path, body string) (int, string) {
		r := req(method, path)
		if body != "" {
			r.Body = readCloser(body)
		}
		w := httptest.NewRecorder()
		serve(app)(w, r)
		return w.Code, w.Body.String()
	}
	SaveEventMonitor(RootDB, EventMonitor{Name: "repo", Owner: "alice", Kind: EventKindWebhook, Token: NewEventToken()})
	SaveEventMonitor(RootDB, EventMonitor{Name: "checker", Owner: "alice", Kind: EventKindPoll})
	SaveEventMonitor(RootDB, EventMonitor{Name: "bobs", Owner: "bob", Kind: EventKindWebhook, Token: NewEventToken()})

	_, opts := do("GET", "/api/webhooks?options=feeds", "")
	if !strings.Contains(opts, `"watch:repo"`) || strings.Contains(opts, "checker") || strings.Contains(opts, "bobs") {
		t.Errorf("the watches offered: %s", opts)
	}
	_, fresh := do("GET", "/api/webhooks?options=new", "")
	var start map[string]string
	json.Unmarshal([]byte(fresh), &start)
	if len(start["secret"]) < 32 || start["verify"] != webhooks.VerifyHMAC {
		t.Errorf("a new form starts with: %s", fresh)
	}

	code, body := do("POST", "/api/webhooks", `{"label":"Repo","verify":"hmac","secret":"sekret-value","header":"X-Hub-Signature-256","prefix":"sha256=","kind":"header:X-GitHub-Event","kinds":"push, release","feeds":"watch:repo","reach":"direct"}`)
	if code >= 300 || !strings.Contains(body, "/hook/repo") {
		t.Fatalf("add: %d %s", code, body)
	}
	if code, _ := do("POST", "/api/webhooks", `{"label":"Theirs","verify":"none","feeds":"watch:bobs"}`); code != 400 {
		t.Errorf("waking someone else's watch: %d", code)
	}
	_, list := do("GET", "/api/webhooks", "")
	if strings.Contains(list, "sekret-value") || !strings.Contains(list, "an HMAC SHA-256 signature in X-Hub-Signature-256") || !strings.Contains(list, "/hook/repo") {
		t.Errorf("the list: %s", list)
	}
	_, form := do("GET", "/api/webhooks?name=repo", "")
	// Posted as the old comma text, read back as the list the tag box edits.
	if strings.Contains(form, "sekret-value") || !strings.Contains(form, `"kinds":["push","release"]`) {
		t.Errorf("the edit form: %s", form)
	}
	if h, ok := webhooks.Get("repo"); !ok || strings.Join(h.Kinds, "|") != "push|release" {
		t.Errorf("kinds saved from comma text: %+v", h.Kinds)
	}
	if code, _ := do("POST", "/api/webhooks?name=repo", `{"label":"Repo events","verify":"none","header":"X-Hub-Signature-256","algorithm":"sha256","prefix":"sha256=","feeds":"watch:repo","reach":"direct"}`); code >= 300 {
		t.Errorf("edit: %d", code)
	}
	if h, _ := webhooks.Get("repo"); h.Label != "Repo events" || h.Verify != webhooks.VerifyHMAC || len(h.Kinds) != 0 {
		t.Errorf("after the edit: %+v", h)
	}
	// The tag box sends a list.
	if code, _ := do("POST", "/api/webhooks?name=repo", `{"label":"Repo events","verify":"none","header":"X-Hub-Signature-256","algorithm":"sha256","prefix":"sha256=","kind":"header:X-GitHub-Event","kinds":["push"," tag ",""],"feeds":"watch:repo","reach":"direct"}`); code >= 300 {
		t.Errorf("edit with a list of kinds: %d", code)
	}
	if h, _ := webhooks.Get("repo"); strings.Join(h.Kinds, "|") != "push|tag" {
		t.Errorf("kinds saved from the tag box: %+v", h.Kinds)
	}
	if code, _ := do("POST", "/api/webhooks?action=secret&name=repo", `{"secret":""}`); code != 400 {
		t.Errorf("a blank new secret: %d", code)
	}
	if code, _ := do("GET", "/api/webhooks?calls=nope", ""); code != 404 {
		t.Errorf("calls of a webhook not theirs: %d", code)
	}
	RootDB.Unset("event_monitors", "alice:repo")
	if _, list := do("GET", "/api/webhooks", ""); !strings.Contains(list, "is gone") {
		t.Errorf("its watch gone: %s", list)
	}
	if code, _ := do("DELETE", "/api/webhooks?name=repo", ""); code >= 300 {
		t.Errorf("delete: %d", code)
	}
	if _, ok := webhooks.Get("repo"); ok {
		t.Error("still there")
	}
}

// Linked services are offered beside watches, and a webhook feeding one
// says so on its row, and that it is no longer linked when it is not.
func TestAWebhookMayFeedALinkedService(t *testing.T) {
	app, req := authedExtensions(t)
	linked := []string{"slack"}
	webhooks.LinkedServices = func(owner string) []string {
		if owner == "alice" {
			return linked
		}
		return nil
	}
	t.Cleanup(func() { webhooks.LinkedServices = nil })
	do := func(method, path, body string) (int, string) {
		r := req(method, path)
		if body != "" {
			r.Body = readCloser(body)
		}
		w := httptest.NewRecorder()
		serve(app)(w, r)
		return w.Code, w.Body.String()
	}
	if _, opts := do("GET", "/api/webhooks?options=feeds", ""); !strings.Contains(opts, `"channel:slack"`) {
		t.Errorf("feeds offered: %s", opts)
	}
	code, body := do("POST", "/api/webhooks", `{"label":"Slack","verify":"slack","secret":"sig","text":"event.text","chat":"event.channel","from":"event.user","feeds":"channel:slack"}`)
	if code >= 300 {
		t.Fatalf("add: %d %s", code, body)
	}
	_, list := do("GET", "/api/webhooks", "")
	if !strings.Contains(list, `"feeds":"the channel slack"`) || !strings.Contains(list, "Slack's request signing") || strings.Contains(list, `"problem"`) {
		t.Errorf("the list: %s", list)
	}
	if _, form := do("GET", "/api/webhooks?name=slack", ""); !strings.Contains(form, `"chat":"event.channel"`) {
		t.Errorf("the edit form: %s", form)
	}
	linked = nil
	if _, list := do("GET", "/api/webhooks", ""); !strings.Contains(list, "no longer one of your linked services") {
		t.Errorf("unlinked: %s", list)
	}
}

// The kinds a webhook passes on are a tag box, one kind each, on both the add
// and the edit form.
func TestAWebhooksKindsAreATagBox(t *testing.T) {
	for _, adding := range []bool{true, false} {
		found := false
		for _, f := range webhookFields(adding) {
			if f.Field == "kinds" {
				found = true
				if f.Type != "tags" {
					t.Errorf("adding=%v: kinds is a %q field", adding, f.Type)
				}
			}
		}
		if !found {
			t.Errorf("adding=%v: no kinds field", adding)
		}
	}
}
