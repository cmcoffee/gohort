package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// newTestSession builds an in-memory session usable by createGrouped/testGrouped.
func newTestSession() *ToolSession {
	return &ToolSession{
		Username:      "alice",
		ChatSessionID: "s1",
		DB:            &DBase{Store: kvlite.MemStore()},
	}
}

// injectBrokenMoltbook persists a toolbox with a comment POST action that has
// NO body_template — the shape a real tool had before the create gate existed.
// Injected straight into the session pool so it bypasses createToolboxGrouped's
// authoring gate (which now rejects this shape), letting the testGrouped tests
// exercise how the verifier reports a legacy broken tool.
func injectBrokenMoltbook(t *testing.T, sess *ToolSession) {
	t.Helper()
	tool := &TempTool{
		Name:       "moltbook",
		Mode:       TempToolModeToolbox,
		Credential: "no_auth",
		Actions: []TempToolAction{
			{
				Name:        "feed",
				Description: "list posts",
				URLTemplate: "https://x.test/api/v1/feed?limit={limit}&sort={sort}",
				Method:      "GET",
				Params: map[string]ToolParam{
					"limit": {Type: "integer"},
					"sort":  {Type: "string"},
				},
				Required: []string{"limit", "sort"},
			},
			{
				Name:        "comment",
				Description: "comment on a post",
				URLTemplate: "https://x.test/api/v1/posts/{post_id}/comments",
				Method:      "POST",
				Params: map[string]ToolParam{
					"post_id": {Type: "string"},
					"content": {Type: "string"},
				},
				Required: []string{"post_id", "content"}, // content sent nowhere — the bug
			},
		},
	}
	if err := sess.AppendTempTool(tool); err != nil {
		t.Fatalf("inject: %v", err)
	}
}

// TestVerifyCatchesUnsentRequiredParam reproduces the live moltbook failure:
// a POST "comment" action with content+post_id required but NO body_template,
// so content is never sent and the API 400s with "content must be a string".
// action="test" must FAIL that endpoint offline (no network) and point at the
// missing body_template. post_id, which lives in the URL, must NOT be flagged.
func TestVerifyCatchesUnsentRequiredParam(t *testing.T) {
	sess := newTestSession()
	injectBrokenMoltbook(t, sess)

	report, err := testGrouped(map[string]any{"name": "moltbook"}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !strings.Contains(report, "[FAIL] comment") {
		t.Fatalf("expected comment endpoint to FAIL; report:\n%s", report)
	}
	if !strings.Contains(report, "content") || !strings.Contains(report, "body_template") {
		t.Fatalf("expected the failure to name the unsent 'content' param and the missing body_template; report:\n%s", report)
	}
	// post_id lives in the URL — the unsent-param list must be [content] only,
	// not [content post_id].
	if strings.Contains(report, "[content post_id]") || strings.Contains(report, "[post_id content]") {
		t.Fatalf("post_id is carried in the URL and must not be reported as unsent; report:\n%s", report)
	}
}

// TestVerifyPassesWiredComment confirms the fixed shape passes check A: the
// same comment action WITH a body_template that carries content no longer
// reports an unsent required param.
func TestVerifyPassesWiredComment(t *testing.T) {
	sess := newTestSession()

	create := map[string]any{
		"action":      "create",
		"name":        "moltbook",
		"description": "social toolbox",
		"mode":        "toolbox",
		"credential":  "no_auth",
		"actions": []any{
			map[string]any{
				"name":          "comment",
				"description":   "comment on a post",
				"url_template":  "https://x.test/api/v1/posts/{post_id}/comments",
				"method":        "POST",
				"body_template": `{"content": {content}}`,
				"params": map[string]any{
					"post_id": map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []any{"post_id", "content"},
			},
		},
	}
	if _, err := createGrouped(create, sess); err != nil {
		t.Fatalf("create: %v", err)
	}

	report, err := testGrouped(map[string]any{
		"name":  "moltbook",
		"cases": []any{map[string]any{"action": "comment", "args": map[string]any{"post_id": "p1", "content": "hi"}}},
	}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if strings.Contains(report, "sent NOWHERE") {
		t.Fatalf("wired comment should not report an unsent param; report:\n%s", report)
	}
	if !strings.Contains(report, "body_template renders valid JSON") {
		t.Fatalf("expected body render to pass; report:\n%s", report)
	}
	// A write endpoint is never auto-fired — the report must say so.
	if !strings.Contains(report, "NOT auto-fired") {
		t.Fatalf("expected write endpoint to be flagged as not auto-fired; report:\n%s", report)
	}
}

// TestVerifyDegradesOfflineInPrivateMode confirms that when the network is
// blocked (private mode), test runs offline checks only — it must still FAIL
// the unsent-param bug but must NOT try to live-probe the read endpoint (no
// spurious "live probe errored").
func TestVerifyDegradesOfflineInPrivateMode(t *testing.T) {
	sess := newTestSession()
	sess.Network = NewNetworkConnector(true) // block network — private mode
	injectBrokenMoltbook(t, sess)

	report, err := testGrouped(map[string]any{
		"name":  "moltbook",
		"cases": []any{map[string]any{"action": "feed", "args": map[string]any{"limit": 5, "sort": "new"}}},
	}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !strings.Contains(report, "OFFLINE checks only") {
		t.Fatalf("expected the offline-mode banner; report:\n%s", report)
	}
	// The read endpoint must be skipped, not errored.
	if strings.Contains(report, "live probe errored") || strings.Contains(report, "live GET") {
		t.Fatalf("read endpoint must NOT be live-probed in private mode; report:\n%s", report)
	}
	if !strings.Contains(report, "network is blocked this turn") {
		t.Fatalf("expected the read endpoint to note network-blocked; report:\n%s", report)
	}
	// Offline checks still run: the comment bug must still FAIL.
	if !strings.Contains(report, "[FAIL] comment") {
		t.Fatalf("offline unsent-param check must still FAIL comment; report:\n%s", report)
	}
}

// TestParseTestCases covers the cases normalizer used by action="test".
func TestParseTestCases(t *testing.T) {
	got := parseTestCases([]any{
		map[string]any{"action": "Feed", "args": map[string]any{"limit": 5}},
		map[string]any{"args": map[string]any{"x": 1}}, // unlabeled → ""
	})
	if _, ok := got["feed"]; !ok {
		t.Fatalf("expected lowercased 'feed' key, got %v", got)
	}
	if _, ok := got[""]; !ok {
		t.Fatalf("expected unlabeled case under empty key, got %v", got)
	}
}
