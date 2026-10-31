package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/snugforge/kvlite"
)

// gohort's own providers are listed with the other connectors, read from
// their settings: each says what it wraps, whether it is in use (and if not,
// what is), and where it is set up. One in use is tested by its settings' own
// test; one that is not says why instead. None can be approved, exported or
// deleted.
func TestGohortsOwnProvidersAreConnectors(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer srv.Close()
	a := &AdminApp{}
	a.db = &DBase{Store: kvlite.MemStore()}
	a.db.Set(EmbeddingTable, "current", EmbeddingConfig{Enabled: true, Provider: "local", Endpoint: srv.URL + "/v1", Model: "nomic-embed-text"})
	a.db.Set(media.SpeechTable, "current", media.SpeechConfig{Enabled: true, Provider: "service:voice", Endpoint: "http://localhost:8880/v1"})
	a.db.Set(ImageTable, "provider", "openai")

	got := map[string]builtinConnector{}
	for _, b := range a.builtinConnectors() {
		got[b.name] = b
	}
	host := strings.TrimPrefix(srv.URL, "http://")
	if b := got["built-in:embeddings"]; b.wrapper != "Embeddings ⇐ embeddings (model nomic-embed-text) ⇐ "+host+" (no key)" ||
		!strings.HasPrefix(b.status, "In use for Embeddings") || b.test == nil {
		t.Errorf("embeddings: %+v", b)
	}
	if b := got["built-in:speech"]; b.status != "Not in use: speech is made by the connector voice. Set up under Admin > Capabilities > Speech (TTS)." || b.test != nil {
		t.Errorf("speech while a service speaks: %+v", b)
	}
	if b := got["built-in:transcription"]; !strings.HasPrefix(b.status, "Off.") || b.test != nil {
		t.Errorf("transcription, never set up: %+v", b)
	}
	if b := got["built-in:gemini-images"]; !strings.Contains(b.status, "set to the built-in OpenAI") {
		t.Errorf("gemini while openai is chosen: %+v", b)
	}
	if b := got["built-in:openai-images"]; !strings.HasPrefix(b.status, "In use for Image Generation") || !strings.Contains(b.wrapper, "api.openai.com") {
		t.Errorf("openai: %+v", b)
	}

	c := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/connectors/test", nil)}
	res, err := a.builtinTest(c, "built-in:embeddings")
	if body, _ := res.(map[string]any); err != nil || !strings.Contains(body["message"].(string), "3-dim embedding") || body["warnings"] != nil {
		t.Errorf("testing the embedder in use: %v %v", res, err)
	}
	res, _ = a.builtinTest(c, "built-in:speech")
	if body, _ := res.(map[string]any); !strings.HasPrefix(body["message"].(string), "Not in use") || body["warnings"] == nil {
		t.Errorf("testing a built-in not in use: %v", res)
	}
	res, _ = a.builtinTest(c, "built-in:openai-images")
	if body, _ := res.(map[string]any); !strings.Contains(body["message"].(string), "set one under Admin > Capabilities > Image Generation") {
		t.Errorf("an image key test from the Connectors page: %v", res)
	}
	if _, err := a.builtinTest(c, "built-in:nothing"); err == nil {
		t.Error("an unknown built-in was tested")
	}

	rowsAny, _ := a.handleConnectors(c)
	b, _ := json.Marshal(rowsAny)
	var rows []map[string]any
	json.Unmarshal(b, &rows)
	builtins := 0
	for _, r := range rows {
		if strings.HasPrefix(r["name"].(string), builtinPrefix) {
			builtins++
			if r["stored"] != false || r["state"] != "built-in" || r["approved"] != false {
				t.Errorf("a built-in row could be approved, edited or deleted: %v", r)
			}
		}
	}
	if builtins != 5 {
		t.Errorf("%d built-in rows, want 5", builtins)
	}
}
