package ui

// A finished pipeline's buttons can run an action the page registered, with
// what the run produced, and can be shown only when the run produced the
// thing they act on. A link or a bare POST could not open a picker.

import (
	"strings"
	"testing"
)

func TestAWatchPanelFinishButtonCanRunAPageAction(t *testing.T) {
	b, err := PipelineWatchPanel{OnDoneActions: []WatchAction{{Label: "Pick", URL: "app_pick", Method: "client", ShowIfField: "post_id"}}}.MarshalJSON()
	if err != nil || !strings.Contains(string(b), `"method":"client"`) || !strings.Contains(string(b), `"show_if_field":"post_id"`) {
		t.Fatalf("the action does not reach the runtime: %s %v", b, err)
	}
	js := readRuntimeFile(t, "10_basics.js")
	start := strings.Index(js, "function renderDoneActions(")
	if start < 0 {
		t.Fatal("no renderDoneActions")
	}
	body := js[start:]
	body = body[:strings.Index(body, "\n    }\n")]
	for _, want := range []string{
		"if (act.show_if_field && !(lastEvent || {})[act.show_if_field]) return;",
		"var fn = window.UIClientActions && window.UIClientActions[act.url];",
		"fn({event: lastEvent || {}, article: lastArticle, button: cbtn});",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("renderDoneActions missing %q", want)
		}
	}
	if !strings.Contains(js, "lastArticle = {title: title || '', content: content || ''};") {
		t.Error("the article shown is not kept for the action")
	}
}
