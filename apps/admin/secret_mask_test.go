package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The search, mail, image, embedding and transcription GETs returned their
// keys in the clear to the browser. They read as a placeholder now, and the
// placeholder coming back on a save keeps the stored key.
func TestAdminSecretsAreMaskedAndKept(t *testing.T) {
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	sub := http.NewServeMux()
	a.registerNetConfigRoutes(sub)
	a.registerMediaRoutes(sub)

	const secret = "sk-test-0123456789"
	a.db.Set(SearchTable, "provider", "brave")
	a.db.Set(SearchTable, "api_key", secret)
	a.db.Set(MailTable, "password", secret)
	a.db.Set(ImageTable, "provider", "openai")
	a.db.Set(ImageTable, "api_key", secret)
	a.db.Set(EmbeddingTable, "current", EmbeddingConfig{Endpoint: "http://127.0.0.1:1", APIKey: secret})
	a.db.Set(TranscribeTable, "current", TranscribeConfig{Endpoint: "http://127.0.0.1:1", APIKey: secret})

	for _, path := range []string{"/api/web-search", "/api/mail", "/api/image-gen", "/api/embeddings", "/api/transcribe"} {
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("GET %s returned the stored secret: %s", path, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), secretUnchanged) {
			t.Errorf("GET %s should show that a secret is set: %s", path, w.Body.String())
		}
	}

	post := func(path string, body map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		sub.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(b))))
		if w.Code >= 300 {
			t.Fatalf("POST %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	post("/api/web-search", map[string]any{"provider": "brave", "api_key": secretUnchanged})
	post("/api/mail", map[string]any{"server": "smtp.example.com:587", "password": secretUnchanged})
	post("/api/image-gen", map[string]any{"provider": "openai", "api_key": secretUnchanged})
	for _, row := range [][2]string{{SearchTable, "api_key"}, {MailTable, "password"}, {ImageTable, "api_key"}} {
		if got := a.storedString(row[0], row[1]); got != secret {
			t.Errorf("%s/%s: the placeholder overwrote the key with %q", row[0], row[1], got)
		}
	}

	// A new value replaces it, and clearing the field clears it.
	post("/api/image-gen", map[string]any{"provider": "openai", "api_key": "sk-new"})
	if got := a.storedString(ImageTable, "api_key"); got != "sk-new" {
		t.Errorf("a new key was not stored: %q", got)
	}
	post("/api/image-gen", map[string]any{"provider": "openai", "api_key": ""})
	if got := a.storedString(ImageTable, "api_key"); got != "" {
		t.Errorf("clearing the field kept %q", got)
	}
}
