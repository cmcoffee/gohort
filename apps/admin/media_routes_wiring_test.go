package admin

// Each media API path reaches its own handler, pinned by an answer only that
// handler gives on an empty store.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestEachMediaPathReachesItsOwnHandler(t *testing.T) {
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerMediaRoutes(mux)
	for _, tc := range []struct{ method, path, want string }{
		{"GET", "/api/embeddings", `"provider":"local"`},
		{"GET", "/api/embeddings/models", "[]"},
		{"GET", "/api/transcribe", `"enabled":false}`},
		{"GET", "/api/image-gen", `"provider":"gemini"`},
		{"POST", "/api/embeddings/test", "embeddings are disabled"},
		{"POST", "/api/transcribe/test", "transcription is disabled"},
		{"POST", "/api/image-gen/test", "pick a provider first"},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}")))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s %s answered %d %q, want 200 with %q", tc.method, tc.path, w.Code, w.Body.String(), tc.want)
		}
	}
}
