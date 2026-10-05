package temptool

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// newStatusAPI is newFakeAPI with a server that answers every call with the
// given status, for a probe that has to fail.
func newStatusAPI(t *testing.T, cred string, status int) (*httptest.Server, *ToolSession) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	t.Cleanup(srv.Close)
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	t.Cleanup(func() { AuthDB = prev })
	if err := Secure().Save(SecureCredential{Name: cred, Type: SecureCredNone, BaseURL: srv.URL}, ""); err != nil {
		t.Fatalf("cred: %v", err)
	}
	return srv, &ToolSession{Username: "alice", ChatSessionID: "s1", WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()}}
}

// branchToolbox is one toolbox create call on url, with the action's fields
// and the top-level fields overridden by the caller.
func branchToolbox(url string, action, top map[string]any) map[string]any {
	act := map[string]any{
		"name": "get_card", "url_template": url + "/repos/{branch}/cards/{card}",
		"params": map[string]any{
			"branch": map[string]any{"type": "string"},
			"card":   map[string]any{"type": "string"},
		},
	}
	for k, v := range action {
		act[k] = v
	}
	args := map[string]any{"name": "cards", "description": "d", "mode": "toolbox", "credential": "cards", "actions": []any{act}}
	for k, v := range top {
		args[k] = v
	}
	return args
}

func actionRequired(t *testing.T, sess *ToolSession, tool, action string) []string {
	t.Helper()
	tt := sess.LookupTempTool(tool)
	if tt == nil {
		t.Fatalf("no tool %q", tool)
	}
	for _, a := range tt.Actions {
		if a.Name == action {
			out := append([]string{}, a.Required...)
			sort.Strings(out)
			return out
		}
	}
	t.Fatalf("no action %q", action)
	return nil
}

// A {branch} not declared at all was reported as "not required". The error
// now names the real problem.
func TestUndeclaredPathParamSaysNotInParams(t *testing.T) {
	f, sess := newFakeAPI(t, "cards")
	_, err := createGrouped(branchToolbox(f.srv.URL, map[string]any{
		"params": map[string]any{"card": map[string]any{"type": "string"}},
	}, nil), sess)
	if err == nil || !strings.Contains(err.Error(), "branch") || strings.Contains(err.Error(), "not required") {
		t.Fatalf("want the undeclared {branch} named, not a required complaint: %v", err)
	}
}

// required as a comma string or a JSON-array string is read as the list it
// spells; true is an error naming the shape, never an empty list.
func TestRequiredStringShapes(t *testing.T) {
	f, sess := newFakeAPI(t, "cards")
	for _, req := range []any{"branch,card", `["branch", "card"]`, " branch , card "} {
		if _, err := createGrouped(branchToolbox(f.srv.URL, map[string]any{"required": req}, nil), sess); err != nil {
			t.Fatalf("required=%q: %v", req, err)
		}
		if got := actionRequired(t, sess, "cards", "get_card"); !reflect.DeepEqual(got, []string{"branch", "card"}) {
			t.Errorf("required=%q gave %v", req, got)
		}
	}
	_, err := createGrouped(branchToolbox(f.srv.URL, map[string]any{"required": true}, nil), sess)
	if err == nil || !strings.Contains(err.Error(), "list of the param names") || strings.Contains(err.Error(), "not required") {
		t.Fatalf("required=true must name the expected shape: %v", err)
	}
	if _, err := createAPIForTest(sess, f.srv.URL, map[string]any{"required": "id"}); err != nil {
		t.Fatalf("api tool with required=\"id\": %v", err)
	}
	if tt := sess.LookupTempTool("one"); tt == nil || !reflect.DeepEqual(tt.Required, []string{"id"}) {
		t.Fatalf("api tool required = %+v", tt)
	}
}

func createAPIForTest(sess *ToolSession, url string, extra map[string]any) (string, error) {
	args := map[string]any{
		"name": "one", "description": "d", "mode": "api", "credential": "cards",
		"url_template": url + "/items/{id}?q={q}",
		"params": map[string]any{
			"id": map[string]any{"type": "string"},
			"q":  map[string]any{"type": "string"},
		},
	}
	for k, v := range extra {
		args[k] = v
	}
	return createGrouped(args, sess)
}

// A top-level required list is shared with an action that sets none, like the
// other top-level fields; a name no action has is refused.
func TestToolboxTopLevelRequiredShared(t *testing.T) {
	f, sess := newFakeAPI(t, "cards")
	args := branchToolbox(f.srv.URL, map[string]any{
		"url_template": f.srv.URL + "/cards?branch={branch}&card={card}",
	}, map[string]any{"required": []any{"branch"}})
	out, err := createGrouped(args, sess)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := actionRequired(t, sess, "cards", "get_card"); !reflect.DeepEqual(got, []string{"branch"}) {
		t.Errorf("shared required gave %v", got)
	}
	if !strings.Contains(out, "required to get_card") {
		t.Errorf("the reply must say the action took it: %s", out)
	}
	args["required"] = []any{"nope"}
	if _, err := createGrouped(args, sess); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("a top-level required name no action has must be refused: %v", err)
	}
}

// required: true inside a param's own object joins the required list.
func TestParamMarkedRequiredJoinsList(t *testing.T) {
	f, sess := newFakeAPI(t, "cards")
	if _, err := createGrouped(branchToolbox(f.srv.URL, map[string]any{
		"params": map[string]any{
			"branch": map[string]any{"type": "string", "required": true},
			"card":   map[string]any{"type": "string", "required": true},
			"note":   map[string]any{"type": "string"},
		},
		"required": []any{},
	}, nil), sess); err != nil {
		t.Fatalf("marked path params must count as required: %v", err)
	}
	if got := actionRequired(t, sess, "cards", "get_card"); !reflect.DeepEqual(got, []string{"branch", "card"}) {
		t.Errorf("marked params gave required %v", got)
	}
	if _, err := createAPIForTest(sess, f.srv.URL, map[string]any{
		"required": []any{"id"},
		"params": map[string]any{
			"id": map[string]any{"type": "string"},
			"q":  map[string]any{"type": "string", "required": "true"},
		},
	}); err != nil {
		t.Fatalf("api create: %v", err)
	}
	if tt := sess.LookupTempTool("one"); !reflect.DeepEqual(tt.Required, []string{"id", "q"}) {
		t.Errorf("api tool required = %v", tt.Required)
	}
}

// itemsToolbox is a toolbox with one read that needs an id and one write.
func itemsToolbox(t *testing.T, sess *ToolSession, url string, extra map[string]any) {
	t.Helper()
	read := map[string]any{
		"name": "get_item", "url_template": url + "/items/{id}",
		"params": map[string]any{"id": map[string]any{"type": "string"}},
	}
	for k, v := range extra {
		read[k] = v
	}
	if _, err := createGrouped(map[string]any{
		"name": "items", "description": "d", "mode": "toolbox", "credential": "items",
		"actions": []any{read, map[string]any{
			"name": "add_item", "url_template": url + "/items", "method": "POST",
			"body_template": `{"title": {title}}`,
			"params":        map[string]any{"title": map[string]any{"type": "string"}},
		}},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
}

// test_args on test was ignored; a case's "name" and args.action became
// unlabeled cases. All three now reach the endpoint they name.
func TestTestArgsAndCaseAliases(t *testing.T) {
	f, sess := newFakeAPI(t, "items")
	itemsToolbox(t, sess, f.srv.URL, nil)
	for i, args := range []map[string]any{
		{"test_args": map[string]any{"action": "get_item", "id": "7"}},
		{"cases": []any{map[string]any{"name": "get_item", "args": map[string]any{"id": "7"}}}},
		{"cases": []any{map[string]any{"args": map[string]any{"action": "get_item", "id": "7"}}}},
	} {
		args["name"] = "items"
		args["rerun"] = true
		before := f.count()
		report, err := testGrouped(args, sess)
		if err != nil {
			t.Fatalf("shape %d: %v", i, err)
		}
		if f.count() != before+1 || !strings.Contains(report, "[PASS] get_item") {
			t.Errorf("shape %d did not probe get_item; report:\n%s", i, report)
		}
	}
}

// create/update with cases or a toolbox's test_args naming its action runs the
// test in the same call instead of answering NOT VERIFIED.
func TestSaveRunsCasesAndToolboxTestArgs(t *testing.T) {
	f, sess := newFakeAPI(t, "items")
	itemsToolbox(t, sess, f.srv.URL, nil)
	out := verifyWithTestArgs(map[string]any{"name": "items", "cases": []any{
		map[string]any{"action": "get_item", "args": map[string]any{"id": "7"}},
	}}, sess, "Created.")
	if strings.Contains(out, "NOT VERIFIED") || !strings.Contains(out, "Verification with cases") || !strings.Contains(out, "[PASS] get_item") {
		t.Errorf("cases on a save must run; got:\n%s", out)
	}
	forgetToolTest(sess, "items")
	out = verifyWithTestArgs(map[string]any{"name": "items", "test_args": map[string]any{"action": "get_item", "id": "8"}}, sess, "Updated.")
	if !strings.Contains(out, "Verification with test_args") || !strings.Contains(out, "[PASS] get_item") {
		t.Errorf("a toolbox test_args naming its action must run; got:\n%s", out)
	}
	out = verifyWithTestArgs(map[string]any{"name": "items", "test_args": map[string]any{"id": "8"}}, sess, "Updated.")
	if !strings.Contains(out, "names no action") {
		t.Errorf("a toolbox test_args with no action must say so; got:\n%s", out)
	}
}

// A PASS line for a read carries an excerpt of the body; a read that passed
// stays PASS on a later test whose cases leave it out; the write's RESULT
// leads with the verdict.
func TestReadPassKeepsBodyAndVerdict(t *testing.T) {
	f, sess := newFakeAPI(t, "items")
	itemsToolbox(t, sess, f.srv.URL, nil)
	first, err := testGrouped(map[string]any{"name": "items", "cases": []any{
		map[string]any{"action": "get_item", "args": map[string]any{"id": "7"}},
	}}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, `returned "HTTP 200 OK": {"ok":true,"items":[1]}`) {
		t.Errorf("the PASS line must show the body; report:\n%s", first)
	}
	if !strings.Contains(first, "RESULT: not verified yet: 1 write endpoint still needs ONE manual live call") || !strings.Contains(first, "Everything else passed.") {
		t.Errorf("the RESULT must lead with the verdict; report:\n%s", first)
	}
	second, err := testGrouped(map[string]any{"name": "items", "cases": []any{
		map[string]any{"action": "add_item", "args": map[string]any{"title": "x"}},
	}}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second, "[PASS] get_item") || !strings.Contains(second, "from an earlier probe") {
		t.Errorf("the earlier probe must be kept; report:\n%s", second)
	}
	// An edit makes it a different endpoint: unproven again.
	if _, err := updateGrouped(map[string]any{"name": "items", "actions": []any{
		map[string]any{"name": "get_item", "url_template": f.srv.URL + "/v2/items/{id}"},
	}}, sess); err != nil {
		t.Fatal(err)
	}
	third, err := testGrouped(map[string]any{"name": "items"}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(third, "[UNPROVEN] get_item") {
		t.Errorf("an edited read must not keep the old probe; report:\n%s", third)
	}
}

// A pipe that runs clean and prints null over a body with data fails.
func TestPipeNullOnRealBodyFails(t *testing.T) {
	f, sess := newFakeAPI(t, "items")
	itemsToolbox(t, sess, f.srv.URL, map[string]any{"response_pipe": "jq .records"})
	report, err := testGrouped(map[string]any{"name": "items", "cases": []any{
		map[string]any{"action": "get_item", "args": map[string]any{"id": "7"}},
	}}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(report, "response_pipe has a syntax/compile error") {
		t.Skip("no sandbox to run the pipe in")
	}
	if !strings.Contains(report, "[FAIL] get_item") || !strings.Contains(report, "response_pipe produced null (or nothing)") || !strings.Contains(report, `Raw body: {"ok":true`) {
		t.Errorf("a null pipe output must fail with the body shown; report:\n%s", report)
	}
	if _, err := updateGrouped(map[string]any{"name": "items", "actions": []any{
		map[string]any{"name": "get_item", "response_pipe": "jq .items"},
	}}, sess); err != nil {
		t.Fatal(err)
	}
	report, err = testGrouped(map[string]any{"name": "items", "cases": []any{
		map[string]any{"action": "get_item", "args": map[string]any{"id": "7"}},
	}}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "[PASS] get_item") {
		t.Errorf("a pipe that finds the data must pass; report:\n%s", report)
	}
}

// action fields packed into the url's query are refused; real query params
// that share a name are not.
func TestURLQueryActionFieldsRefused(t *testing.T) {
	f, sess := newFakeAPI(t, "cards")
	_, err := createGrouped(map[string]any{
		"name": "todo", "description": "d", "mode": "toolbox", "credential": "cards",
		"actions": []any{map[string]any{
			"name": "create_task", "url_template": f.srv.URL + "/v1/tasks?action=create_task&method=POST&body_template=%7B%22t%22%3A1%7D",
		}},
	}, sess)
	if err == nil || !strings.Contains(err.Error(), "action, method, body_template are fields of the action, not part of the url: set them beside url_template") {
		t.Fatalf("got %v", err)
	}
	_, err = createGrouped(map[string]any{
		"name": "one", "description": "d", "mode": "api", "credential": "cards",
		"url_template": f.srv.URL + "/x?content_type=application/json",
	}, sess)
	if err == nil || !strings.Contains(err.Error(), "content_type is a field of the action") {
		t.Fatalf("api tool: got %v", err)
	}
	for _, ok := range []string{"/2.0/?method=artist.getinfo", "/api.php?action=query&format=json", "/entries?content_type=blogPost"} {
		if err := urlActionFieldError(f.srv.URL + ok); err != nil {
			t.Errorf("%s is a real query: %v", ok, err)
		}
	}
}

// actions on a mode other than toolbox were dropped and the reply said
// "Created api tool".
func TestActionsOutsideToolboxRefused(t *testing.T) {
	f, sess := newFakeAPI(t, "cards")
	for _, mode := range []string{"api", ""} {
		_, err := createGrouped(map[string]any{
			"name": "one", "description": "d", "mode": mode, "credential": "cards",
			"url_template": f.srv.URL + "/x",
			"actions":      []any{map[string]any{"name": "a", "url_template": f.srv.URL + "/a"}},
		}, sess)
		if err == nil || !strings.Contains(err.Error(), `actions are for mode="toolbox"`) {
			t.Errorf("mode %q: got %v", mode, err)
		}
	}
	if sess.LookupTempTool("one") != nil {
		t.Error("nothing should have been created")
	}
}

// A failed probe shows what it sent, with auth values withheld.
func TestFailedProbeShowsRequest(t *testing.T) {
	srv, sess := newStatusAPI(t, "items", http.StatusUnauthorized)
	if _, err := createGrouped(map[string]any{
		"name": "items", "description": "d", "mode": "toolbox", "credential": "items",
		"actions": []any{map[string]any{
			"name": "get_item", "url_template": srv.URL + "/items/{id}?api_key=s3cret&lang=en",
			"params":  map[string]any{"id": map[string]any{"type": "string"}},
			"headers": map[string]any{"X-Api-Key": "hunter2", "Accept": "application/json"},
		}},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	report, err := testGrouped(map[string]any{"name": "items", "cases": []any{
		map[string]any{"action": "get_item", "args": map[string]any{"id": "7"}},
	}}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "Sent: GET /items/7?api_key=[redacted]&lang=en; headers Accept: application/json, X-Api-Key: [redacted]") {
		t.Errorf("the FAIL line must show the request; report:\n%s", report)
	}
	if strings.Contains(report, "hunter2") || strings.Contains(report, "s3cret") {
		t.Errorf("an auth value leaked; report:\n%s", report)
	}
}

// A toolbox create with descriptions only inside its actions is told which
// description is missing.
func TestToolboxMissingDescriptionHint(t *testing.T) {
	err := missingDescription(map[string]any{"name": "x", "mode": "toolbox", "actions": []any{
		map[string]any{"name": "a", "description": "does a"},
	}})
	if err == nil || !strings.Contains(err.Error(), "a toolbox needs its own top-level description") {
		t.Fatalf("got %v", err)
	}
	if err := missingDescription(map[string]any{"name": "x", "mode": "api", "description": "d"}); err != nil {
		t.Fatalf("a description given: %v", err)
	}
}
