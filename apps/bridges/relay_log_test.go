package bridges

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A relay's log lines are kept for its bridge and shown only to the bridge's
// owner, newest first. A request without the bridge's key keeps nothing.
func TestARelaysLogIsKeptForItsOwner(t *testing.T) {
	f := newOwnershipFixture(t)
	f.T.DB.Set(bridgeKeysTable, "k-relay", BridgeKey{ID: "k-relay", Name: "Mac", Key: "relay-secret", Owner: "alice", Service: "imessage", Enabled: true})
	post := func(key, body string) int {
		r := httptest.NewRequest(http.MethodPost, "/bridges/api/relay-log", strings.NewReader(body))
		r.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		f.T.handleRelayLogPost(w, r)
		return w.Code
	}
	lines := `{"lines":[{"at":"2026-10-23T21:37:02Z","text":"attachment candidate: state=5"},{"at":"2026-10-23T21:37:03Z","text":"attachment not found:\n IMG_2.heic"}]}`
	if code := post("wrong", lines); code != http.StatusUnauthorized {
		t.Errorf("a wrong key: %d, want 401", code)
	}
	if code := post("relay-secret", lines); code != http.StatusNoContent {
		t.Fatalf("the relay's post: %d, want 204", code)
	}

	view := func(user string) (int, []map[string]string) {
		w := f.call(user, http.MethodGet, "/api/relay-tail?id=k-relay", "", f.routes())
		var rows []map[string]string
		json.Unmarshal(w.Body.Bytes(), &rows)
		return w.Code, rows
	}
	code, rows := view("alice")
	if code != http.StatusOK || len(rows) != 2 {
		t.Fatalf("the owner: %d, %d row(s)", code, len(rows))
	}
	if rows[0]["line"] != "attachment not found: IMG_2.heic" || rows[1]["line"] != "attachment candidate: state=5" {
		t.Errorf("want newest first, each on one line: %v", rows)
	}
	if rows[0]["at"] == "2026-10-23T21:37:03Z" || rows[0]["at"] == "" {
		t.Errorf("the time should be shown in the viewer's zone, not raw: %q", rows[0]["at"])
	}
	if code, _ := view("bob"); code != http.StatusNotFound {
		t.Errorf("another user: %d, want 404", code)
	}
}
