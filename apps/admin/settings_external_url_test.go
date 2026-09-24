package admin

// The Site Settings GET must return the stored External URL as it is: it once
// blanked it whenever the Ollama proxy was bound to loopback, and the next
// save of any settings panel wrote the blank back.

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestSettingsReturnTheExternalURLUntouched(t *testing.T) {
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	a.db.Set(WebTable, "external_url", "https://gohort.example")
	a.db.Set(WebTable, "ollama_proxy_port", 11434)
	a.db.Set(WebTable, "ollama_proxy_bind", "127.0.0.1")
	w := httptest.NewRecorder()
	a.handleGetSettings(w, httptest.NewRequest("GET", "/api/settings", nil))
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["external_url"] != "https://gohort.example" {
		t.Fatalf("external_url came back as %v", got["external_url"])
	}
	if got["ollama_proxy_url"] != "http://localhost:11434" {
		t.Errorf("a loopback-bound proxy should still show localhost: %v", got["ollama_proxy_url"])
	}
}
