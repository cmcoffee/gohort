package orchestrate

// A publish through an integration sends the document by reference: the
// model writes its placeholder, the call goes out with the whole document,
// and the model never had the document in front of it to type.

import (
	"context"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

func TestAPublishSendsTheDocumentByReference(t *testing.T) {
	T, _, _ := newTestOrchestrate(t)
	md := "# Runbook\n\n## Restart\n\n" + strings.Repeat("Restart the service and watch it come back. ", 400) + "\n\nTHE-LAST-LINE"
	req := docs.PublishRequest{Title: "Runbook", Doc: docs.PublishDoc{Title: "Runbook", Markdown: md}, Nav: docs.AnalyzeNav(md)}

	var mu sync.Mutex
	var sent []map[string]any
	wiki := AgentToolDef{
		Tool: Tool{Name: "fetch_url_wiki", Description: "Call the wiki API.", Parameters: map[string]ToolParam{
			"method": {Type: "string"}, "url": {Type: "string"}, "body": {Type: "string"}}},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			mu.Lock()
			sent = append(sent, args)
			mu.Unlock()
			return `HTTP 200 OK` + "\n" + `{"id":"9","_links":{"webui":"https://wiki.example.com/9"}}`, nil
		},
	}
	prevTools := publishIntegrationToolsFor
	publishIntegrationToolsFor = func(*ToolSession, string) ([]AgentToolDef, error) { return []AgentToolDef{wiki}, nil }
	t.Cleanup(func() { publishIntegrationToolsFor = prevTools; docs.RegisterCredentialPublisher(nil) })
	registerCredentialPublisher(T)

	run := func(body string) (string, error) {
		sent = nil
		fake := &FakeLLM{Turns: []FakeTurn{
			{ToolCalls: []ToolCall{{ID: "1", Name: "fetch_url_wiki", Args: map[string]any{"method": "POST", "url": "/pages", "body": body}}}},
			{ToolCalls: []ToolCall{{ID: "2", Name: "report_published", Args: map[string]any{"ok": true, "url": "https://wiki.example.com/9", "note": "made the page"}}}},
			{Content: "made the page"},
		}}
		T.LLM = fake
		_, url, err := docs.PublishViaCredential(docs.WithPublishBody(context.Background(), req), "alice", "wiki",
			"Post it to the wiki.\n\nTitle: Runbook\n\n"+docs.ReferenceInstruction(req))
		for i := 0; i < fake.Calls(); i++ {
			for _, m := range fake.Sent(i) {
				if strings.Contains(m.Content, "THE-LAST-LINE") {
					t.Errorf("the model was shown the document (call %d, %s)", i, m.Role)
				}
			}
		}
		return url, err
	}

	// With the placeholder: the call carries all of it, inside the JSON.
	if _, err := run(`{"title":"Runbook","body":"{{document_html}}"}`); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(sent) != 1 || !strings.Contains(sent[0]["body"].(string), "THE-LAST-LINE") || strings.Contains(sent[0]["body"].(string), "{{document") {
		t.Fatalf("the call did not carry the document: %.200v", sent)
	}

	// A success that names no page is not taken: the model is sent back for
	// the address, and a run that never gives one did not publish anywhere
	// the next update could find.
	sent = nil
	T.LLM = &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "fetch_url_wiki", Args: map[string]any{"method": "POST", "url": "/pages", "body": `{"body":"{{document}}"}`}}}},
		{ToolCalls: []ToolCall{{ID: "2", Name: "report_published", Args: map[string]any{"ok": true, "note": "made the page"}}}},
		{Content: "made the page", Repeat: true},
	}}
	if _, url, err := docs.PublishViaCredential(docs.WithPublishBody(context.Background(), req), "alice", "wiki", "Post it.\n\n"+docs.ReferenceInstruction(req)); err == nil {
		t.Errorf("a success that named no page was taken (url %q)", url)
	}

	// Without it: whatever the model typed went out, so it is not a success.
	if _, err := run(`{"title":"Runbook","body":"A short summary of the runbook."}`); err == nil || !strings.Contains(err.Error(), "none of its calls carried the document") {
		t.Errorf("a publish that never sent the document reads as done: %v", err)
	}
}
