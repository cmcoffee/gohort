package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// captureVerify records every verification verdict for the test's duration.
func captureVerify(t *testing.T) *[]string {
	t.Helper()
	var got []string
	prev := ToolVerifyRecorder
	ToolVerifyRecorder = func(_ *ToolSession, name string, passed bool, _ string) {
		v := "fail"
		if passed {
			v = "pass"
		}
		got = append(got, name+":"+v)
	}
	t.Cleanup(func() { ToolVerifyRecorder = prev })
	return &got
}

// TestDirectWriteCountsAsFired is wrap-translate-post's failure on every
// build: test tells the author to make one direct call of the write, the
// author makes it and gets a 2xx, and nothing counted it, so the tool stayed
// unverified and the next test said "never fired" again. Now the direct call
// of a toolbox write verifies the tool when it was the last thing the test
// was waiting on, and a re-run of test keeps the fired write.
func TestDirectWriteCountsAsFired(t *testing.T) {
	f, sess := newFakeAPI(t, "tr")
	verdicts := captureVerify(t)
	if _, err := createGrouped(map[string]any{
		"name": "translate", "description": "d", "mode": "toolbox", "credential": "tr",
		"actions": []any{
			map[string]any{"name": "languages", "url_template": f.srv.URL + "/languages"},
			map[string]any{"name": "post", "url_template": f.srv.URL + "/translate", "method": "POST",
				"body_template": `{"text": {text}}`,
				"params":        map[string]any{"text": map[string]any{"type": "string"}}},
		},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := testGrouped(map[string]any{"name": "translate"}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !strings.Contains(first, "[UNPROVEN] post") || !strings.Contains(first, "still need ONE manual live call") {
		t.Fatalf("the write should be waiting on a direct call; report:\n%s", first)
	}

	*verdicts = nil
	tt := sess.LookupTempTool("translate")
	out, err := dispatchTempTool(sess, tt, map[string]any{"action": "post", "text": "hola"})
	if err != nil || !strings.HasPrefix(out, "HTTP 201") {
		t.Fatalf("direct call: %q, %v", out, err)
	}
	if len(*verdicts) != 1 || (*verdicts)[0] != "translate:pass" {
		t.Errorf("the direct 2xx of the last unfired write must verify the tool; verdicts %v", *verdicts)
	}

	again, err := testGrouped(map[string]any{"name": "translate", "rerun": true}, sess)
	if err != nil {
		t.Fatalf("re-test: %v", err)
	}
	if !strings.Contains(again, "counted as fired") || !strings.Contains(again, "Tool verified") {
		t.Errorf("a re-run must keep the earlier direct 2xx; report:\n%s", again)
	}

	// An edit to the write makes it a different endpoint: it has to fire again.
	if _, err := updateGrouped(map[string]any{"name": "translate", "actions": []any{
		map[string]any{"name": "post", "body_template": `{"q": {text}}`},
	}}, sess); err != nil {
		t.Fatalf("update: %v", err)
	}
	edited, err := testGrouped(map[string]any{"name": "translate"}, sess)
	if err != nil {
		t.Fatalf("test after edit: %v", err)
	}
	if strings.Contains(edited, "counted as fired") {
		t.Errorf("an edited write must not inherit the old fire; report:\n%s", edited)
	}
}

// TestPipedAPICallVerifies: an api tool whose response_pipe replaces the
// status line could never be verified by a direct call, since its output no
// longer says whether the call succeeded. Dispatch now reads the status first.
func TestPipedAPICallVerifies(t *testing.T) {
	sess := newTestSession()
	verdicts := captureVerify(t)
	tt := &TempTool{Name: "post_it", Mode: TempToolModeAPI, CommandTemplate: "https://x.test/p", Method: "POST", ResponsePipe: "jq .id"}
	noteEndpointStatus(sess, tt, "HTTP 201 Created")
	recordCleanRun(sess, tt, "42", nil)
	if len(*verdicts) != 1 || (*verdicts)[0] != "post_it:pass" {
		t.Errorf("a piped call that answered 2xx must verify; verdicts %v", *verdicts)
	}
	*verdicts = nil
	noteEndpointStatus(sess, tt, "HTTP 500 Internal Server Error")
	recordCleanRun(sess, tt, "", nil)
	if len(*verdicts) != 0 {
		t.Errorf("a piped call that answered 500 must not verify; verdicts %v", *verdicts)
	}
}

// TestUnprobedReadNotVerified: a read endpoint test could not call (no value
// for a required param) used to show [PASS] with a note, and the run could
// end "all endpoints passed. Tool verified." It is UNPROVEN now, with the
// case to pass spelled out.
func TestUnprobedReadNotVerified(t *testing.T) {
	f, sess := newFakeAPI(t, "itemsvc")
	verdicts := captureVerify(t)
	if _, err := createGrouped(map[string]any{
		"name": "items", "description": "d", "mode": "toolbox", "credential": "itemsvc",
		"actions": []any{map[string]any{
			"name": "get_item", "url_template": f.srv.URL + "/items/{id}",
			"params": map[string]any{"id": map[string]any{"type": "string"}},
		}},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	report, err := testGrouped(map[string]any{"name": "items"}, sess)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !strings.Contains(report, "[UNPROVEN] get_item") || strings.Contains(report, "Tool verified") {
		t.Errorf("an unprobed read must not pass or verify; report:\n%s", report)
	}
	if !strings.Contains(report, `cases=[{action:"get_item", args:{id:"<id>"}}]`) {
		t.Errorf("the line must say what to pass to probe it; report:\n%s", report)
	}
	for _, v := range *verdicts {
		if v == "items:pass" {
			t.Errorf("recorded as verified: %v", *verdicts)
		}
	}
}

// TestGetBuiltinTool: get on a framework tool said "no tool found", which
// sent the model looking for it in every pool. It is named as a built-in.
func TestGetBuiltinTool(t *testing.T) {
	RegisterReservedToolName("create_standing_agent")
	out, err := getGrouped(map[string]any{"name": "create_standing_agent"}, newTestSession())
	if err != nil {
		t.Fatalf("get of a built-in must answer, not error: %v", err)
	}
	if !strings.Contains(out, "built-in framework tool") || !strings.Contains(out, "Call it directly") {
		t.Errorf("got %q", out)
	}
}
