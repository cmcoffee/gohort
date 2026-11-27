package decide

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// The worker's identity carries the weights a local server serves under its
// name, so new weights behind the same served name ("local") are a new
// model to the judge map. The answer is kept a minute, then fetched again
// in the background; a server that names no weights leaves the identity as
// it was.
func TestNewWeightsBehindTheSameNameAreANewModel(t *testing.T) {
	var mu sync.Mutex
	root, asked := "/opt/vllm/models/old-weights", 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		asked++
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"data":[{"id":"local","root":"` + root + `","max_model_len":262144}]}`))
	}))
	defer srv.Close()

	db := privateWorld(t)
	db.Set(LLMTable, "provider", "vllm")
	db.Set(LLMTable, "endpoint", srv.URL+"/v1")
	db.Set(LLMTable, "model", "local")
	served.Lock()
	served.m = map[string]servedEntry{}
	served.Unlock()

	before := Identity(ProviderWorker)
	if !strings.HasSuffix(before, "|local|/opt/vllm/models/old-weights") {
		t.Fatalf("the identity does not carry the weights: %q", before)
	}
	if again := Identity(ProviderWorker); again != before {
		t.Fatalf("the identity moved with nothing changed: %q then %q", before, again)
	}
	mu.Lock()
	if asked != 1 {
		t.Errorf("the server was asked %d times within a minute", asked)
	}
	root = "/opt/vllm/models/new-weights"
	mu.Unlock()

	// A minute on: the stale answer is used once while a fresh one is fetched.
	key := srv.URL + "/v1\x00local"
	served.Lock()
	e := served.m[key]
	e.at = time.Now().Add(-2 * servedFresh)
	served.m[key] = e
	served.Unlock()
	if stale := Identity(ProviderWorker); stale != before {
		t.Errorf("a stale answer was not used while refreshing: %q", stale)
	}
	var after string
	for i := 0; i < 100; i++ {
		if after = Identity(ProviderWorker); after != before {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.HasSuffix(after, "|local|/opt/vllm/models/new-weights") {
		t.Fatalf("new weights behind the same name kept the old identity: %q", after)
	}

	// A provider that serves by its own model names is left as it was.
	db.Set(LLMTable, "provider", "anthropic")
	if id := Identity(ProviderWorker); strings.Count(id, "|") != 2 {
		t.Errorf("a hosted provider's identity gained a part: %q", id)
	}
}

// A server that names no weights, or cannot be reached, gives none.
func TestAServerThatNamesNoWeightsGivesNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"local"},{"id":"other","root":"/x"}]}`))
	}))
	defer srv.Close()
	if w := fetchServedWeights(srv.URL+"/v1", "local", ""); w != "" {
		t.Errorf("weights from a server that named none: %q", w)
	}
	if w := fetchServedWeights("http://127.0.0.1:1/v1", "local", ""); w != "" {
		t.Errorf("weights from a server that is not there: %q", w)
	}
	if w := fetchServedWeights(srv.URL+"/v1/chat/completions", "other", ""); w != "/x" {
		t.Errorf("the model list was not found from a chat endpoint: %q", w)
	}
}
