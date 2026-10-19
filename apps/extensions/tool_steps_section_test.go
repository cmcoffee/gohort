package extensions

// "Replies may ask for" on a toolbox's Security panel: the owner declares, as
// a pair of the tool's own actions, the next step a reply asks for (a post the
// service answers with a verification challenge to send back), so the
// injection check reads it as the service's protocol. Held to the tool's own
// actions; drops what it added.

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestTheOwnerDeclaresAToolboxsNextSteps(t *testing.T) {
	app, req := authedExtensions(t)
	if err := AdminPersistTempTool(RootDB, "alice", TempTool{Name: "social", Description: "d", Mode: TempToolModeToolbox, Actions: []TempToolAction{
		{Name: "create_post", URLTemplate: "https://social.example/api/v1/posts", Method: "POST"},
		{Name: "verify_post", URLTemplate: "https://social.example/api/v1/verify", Method: "POST"},
	}}); err != nil {
		t.Fatal(err)
	}
	set := func(action, follow string, on bool) (int, string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"action": action, "follow": follow, "on": on})
		r := req("POST", "/api/tool-access?section=steps&name=social")
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		app.handleUserToolAccess(w, r)
		return w.Code, w.Body.String()
	}
	stored := func() []string {
		row, _ := UserToolByName(AuthDB(), "alice", "social")
		for _, a := range row.Tool.Actions {
			if a.Name == "create_post" {
				return a.FollowUps
			}
		}
		return nil
	}
	if code, _ := set("create_post", "verify_post", true); code != 204 {
		t.Fatalf("declaring a step answered %d", code)
	}
	if got := stored(); !slices.Equal(got, []string{"verify_post"}) {
		t.Fatalf("the step was not stored: %v", got)
	}
	w := httptest.NewRecorder()
	app.handleUserToolAccess(w, req("GET", "/api/tool-access?section=steps&name=social"))
	if !strings.Contains(w.Body.String(), `{"action":"create_post","follow":"verify_post"}`) {
		t.Errorf("the panel does not read back the step: %s", w.Body.String())
	}
	for _, bad := range [][2]string{{"create_post", "elsewhere"}, {"create_post", "create_post"}} {
		if code, body := set(bad[0], bad[1], true); code != 400 {
			t.Errorf("%v should be refused: %d %s", bad, code, body)
		}
	}
	if code, _ := set("create_post", "verify_post", false); code != 204 || stored() != nil {
		t.Errorf("removing the step left %v", stored())
	}
}
