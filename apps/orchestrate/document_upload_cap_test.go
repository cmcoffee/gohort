package orchestrate

// A document arrives base64 in JSON, so the server-wide body cap would refuse
// one of about 46 MB. The upload routes raise their own cap before reading.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/netgate"
)

func TestDocumentUploadRaisesTheServerBodyCap(t *testing.T) {
	T := &OrchestrateApp{}
	// A server cap far below the body: without the raise, the decode fails
	// on size before the handler ever sees the (deliberately blank) name.
	h := netgate.LimitRequestBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		T.handleCollectionUpload(w, r, "alice", Collection{ID: "c1", Owner: "alice"})
	}), 16)
	body := `{"name":"","data":"` + strings.Repeat("A", 4096) + `"}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/collections/c1/upload", strings.NewReader(body)))
	if !strings.Contains(w.Body.String(), "name required") {
		t.Fatalf("the upload route did not raise the body cap: %d %s", w.Code, w.Body.String())
	}
}
