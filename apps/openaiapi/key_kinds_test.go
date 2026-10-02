package openaiapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// /v1 takes a personal access token. A desktop key resolves to its owner too,
// but it carries no scope, so it is refused rather than let through unscoped.
func TestV1TakesOnlyAPersonalAccessToken(t *testing.T) {
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return RootDB }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	desktop, _ := MintDesktopKey("alice")
	pat := MintAccountTokenScoped("alice", "voice", &TokenScope{Features: []string{OpenAIFeatureKey}, Targets: []string{"worker"}})

	app := &OpenAIAPI{}
	app.DB = RootDB.(*DBase).Bucket("openai_api")
	models := func(key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		app.handleModels(w, r)
		return w
	}
	if w := models(desktop); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "personal access token") {
		t.Errorf("a desktop key on /v1: %d %s", w.Code, w.Body.String())
	}
	if w := models(pat.Token); w.Code != http.StatusOK {
		t.Errorf("a scoped personal token on /v1: %d %s", w.Code, w.Body.String())
	}
}

// A caller's system message rides as context. As an override it replaced the
// agent's prompt and skipped its machine, which is what narrows its tools.
func TestACallersSystemMessageDoesNotReplaceTheAgent(t *testing.T) {
	src, err := os.ReadFile("openaiapi.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "SystemPromptOverride") {
		t.Error("/v1 hands a request's system message to the agent as a prompt override")
	}
}

// Threads are kept per key: two keys sending the same session id do not land
// in one thread.
func TestV1ThreadsArePerKey(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	a := MintAccountTokenScoped("alice", "voice", &TokenScope{})
	b := MintAccountTokenScoped("alice", "bot", &TokenScope{})
	key := func(secret string) string {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		r.Header.Set("X-API-Key", secret)
		r.Header.Set("X-Session-Id", "same")
		return sessionKey(r, chatReq{})
	}
	if key(a.Token) == key(b.Token) {
		t.Error("two keys share a thread by naming the same session id")
	}
}
