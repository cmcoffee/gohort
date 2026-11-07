package bridges

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The page answers only at the bare path: a mistyped API path under it is a
// 404, not the page with a 200.
func TestThePageAnswersOnlyAtItsOwnPath(t *testing.T) {
	T := &Bridges{}
	for path, notFound := range map[string]bool{"/api/typo": true, "/nope/deeper": true, "/": false} {
		w := httptest.NewRecorder()
		T.handleDashboard(w, httptest.NewRequest("GET", path, nil))
		if (w.Code == http.StatusNotFound) != notFound {
			t.Errorf("GET %s answered %d", path, w.Code)
		}
	}
}

// Message bridges carries a way to add one, not just a sentence naming where.
func TestMessageBridgesLinksToWhereOneIsAdded(t *testing.T) {
	if !strings.Contains(addBridgeLinkHTML, `href="/extensions"`) || !strings.Contains(addBridgeLinkHTML, "Add a Teams bot") {
		t.Errorf("the add link: %s", addBridgeLinkHTML)
	}
	src, err := os.ReadFile("page.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "ui.Card{HTML: addBridgeLinkHTML}") {
		t.Error("the link is not on the Message bridges section")
	}
}
