package temptool

import (
	"strings"
	"testing"
)

// TestToolboxTopLevelParamsShared is the friction-report case: params put at
// the TOP level of a toolbox create, beside actions, were never read, so every
// action failed with "url_template: placeholder {q} not in params". A top-level
// value is now shared by every action that does not set its own, an action's
// own param of the same name wins, and the reply says which actions took it.
func TestToolboxTopLevelParamsShared(t *testing.T) {
	sess := newTestSession()
	res, err := createGrouped(map[string]any{
		"name": "catalog", "description": "d", "mode": "toolbox", "credential": "no_auth",
		"method":  "GET",
		"headers": map[string]any{"Accept": "application/json", "X-Mode": "shared"},
		"params": map[string]any{
			"q":     map[string]any{"type": "string", "description": "shared query"},
			"limit": map[string]any{"type": "integer", "description": "shared limit"},
		},
		"actions": []any{
			map[string]any{"name": "search", "url_template": "https://x.test/search?q={q}&limit={limit}"},
			map[string]any{"name": "recent", "url_template": "https://x.test/recent?limit={limit}",
				"params":  map[string]any{"limit": map[string]any{"type": "integer", "description": "own limit"}},
				"headers": map[string]any{"X-Mode": "own"}},
		},
	}, sess)
	if err != nil {
		t.Fatalf("create with top-level params must succeed: %v", err)
	}
	if !strings.Contains(res, "applied params to search, recent") || !strings.Contains(res, "method to search, recent") {
		t.Errorf("the reply must say which actions took the shared values; got: %s", res)
	}
	rec, ok := loadExistingToolRecord(sess, "catalog")
	if !ok || len(rec.Actions) != 2 {
		t.Fatalf("toolbox not stored with both actions")
	}
	search, recent := rec.Actions[0], rec.Actions[1]
	if _, has := search.Params["q"]; !has {
		t.Errorf("search did not inherit q: %v", search.Params)
	}
	if got := recent.Params["limit"].Description; got != "own limit" {
		t.Errorf("an action's own param must win over the shared one; recent.limit = %q", got)
	}
	if got := recent.Headers["X-Mode"]; got != "own" {
		t.Errorf("an action's own header must win; got %q", got)
	}
	if got := recent.Headers["Accept"]; got != "application/json" {
		t.Errorf("recent did not inherit the shared Accept header; got %v", recent.Headers)
	}
}

// TestToolboxUpdateSharedAndWording: update applies top-level shared fields
// the same way create does (it used to reject params and method, and drop
// content_type and headers without a word), says plainly when a shared value
// changed no action, and reads "Updated toolbox X in place", never "Created".
func TestToolboxUpdateSharedAndWording(t *testing.T) {
	sess := newTestSession()
	if _, err := createGrouped(map[string]any{
		"name": "svc", "description": "d", "mode": "toolbox", "credential": "no_auth",
		"actions": []any{
			map[string]any{"name": "feed", "url_template": "https://x.test/feed"},
		},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	res, err := updateGrouped(map[string]any{
		"name":   "svc",
		"params": map[string]any{"page": map[string]any{"type": "integer"}},
		"method": "POST", // every stored action already has its own method
	}, sess)
	if err != nil {
		t.Fatalf("update with top-level params: %v", err)
	}
	if !strings.HasPrefix(res, `Updated toolbox "svc" in place with 1 action(s)`) || strings.Contains(res, "Created") {
		t.Errorf("update must read as an update only; got: %s", res)
	}
	if !strings.Contains(res, "Top-level method changed no action") {
		t.Errorf("a shared value every action overrides must be reported as changing nothing; got: %s", res)
	}
	if strings.Contains(res, "DID NOT LAND") {
		t.Errorf("a shared toolbox field is not a top-level field that failed to land; got: %s", res)
	}
	rec, _ := loadExistingToolRecord(sess, "svc")
	if _, has := rec.Actions[0].Params["page"]; !has {
		t.Errorf("update's top-level params did not reach the action: %v", rec.Actions[0].Params)
	}
	if rec.Actions[0].Method != "GET" {
		t.Errorf("the action's own method must win; got %q", rec.Actions[0].Method)
	}
}

// TestUpdatedResultWording pins the other modes: create's "Created ..." under
// an update becomes "Updated ...", with no second verb in front of it.
func TestUpdatedResultWording(t *testing.T) {
	got := updatedResult("w", `Created api tool "w" (wraps credential "no_auth"). It is now in your tool catalog.`)
	if got != `Updated api tool "w" (wraps credential "no_auth"). It is now in your tool catalog.` {
		t.Errorf("got %q", got)
	}
	if got := updatedResult("p", `Pipeline tool "p" registered`); !strings.HasPrefix(got, "Updated p in place. ") {
		t.Errorf("a reply with no verb of its own keeps the prefix; got %q", got)
	}
}
