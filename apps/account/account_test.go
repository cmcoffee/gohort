package account

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The page answers only at the bare path: a mistyped API path under it is a
// 404, not the page with a 200.
func TestThePageAnswersOnlyAtItsOwnPath(t *testing.T) {
	T := &Account{}
	for path, notFound := range map[string]bool{"/api/typo": true, "/nope/deeper": true, "/": false} {
		w := httptest.NewRecorder()
		T.servePage(w, httptest.NewRequest("GET", path, nil))
		if (w.Code == http.StatusNotFound) != notFound {
			t.Errorf("GET %s answered %d", path, w.Code)
		}
	}
}
