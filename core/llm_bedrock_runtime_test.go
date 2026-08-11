package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// The InvokeModel envelope differs from the Messages API in two specific ways,
// and getting either wrong is a 400 that does not say which.
func TestBedrockInvokeBody(t *testing.T) {
	c := &bedrockRuntimeClient{model: "us.anthropic.claude-opus-4-8"}
	body, err := c.buildBody(
		[]Message{{Role: "user", Content: "hi"}},
		applyOpts(c.model, 1024, nil))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["anthropic_version"] != bedrockAnthropicVersion {
		t.Errorf("anthropic_version = %v, want %q", got["anthropic_version"], bedrockAnthropicVersion)
	}
	// The model rides in the URL here. Leaving it in the body is rejected.
	if _, present := got["model"]; present {
		t.Error("body must not carry a model field on the InvokeModel path")
	}
	if _, present := got["stream"]; present {
		t.Error("body must not carry a stream field — streaming is a separate endpoint")
	}
}

// Versioned model ids carry a colon, which has to survive into the path the
// signature is computed over.
func TestBedrockInvokePathEscaping(t *testing.T) {
	for model, want := range map[string]string{
		"us.anthropic.claude-opus-4-8":              "/model/us.anthropic.claude-opus-4-8/invoke",
		"anthropic.claude-3-5-sonnet-20241022-v2:0": "/model/anthropic.claude-3-5-sonnet-20241022-v2%3A0/invoke",
	} {
		c := &bedrockRuntimeClient{model: model}
		if got := c.invokePath(); got != want {
			t.Errorf("invokePath(%q) = %q, want %q", model, got, want)
		}
	}
}

// The two endpoints sign as DIFFERENT services. Swapping them yields a
// signature mismatch that reads like a bad secret key.
func TestBedrockServiceNamesDiffer(t *testing.T) {
	if bedrockRuntimeService == bedrockService {
		t.Fatalf("both modes sign as %q; the legacy endpoint must sign as \"bedrock\"", bedrockService)
	}
	if bedrockRuntimeService != "bedrock" || bedrockService != "bedrock-mantle" {
		t.Errorf("service names drifted: runtime=%q messages=%q", bedrockRuntimeService, bedrockService)
	}
}

// Bedrock rejects a system-role turn in the message list on both endpoints:
//
//	messages.0: use the top-level 'system' parameter for the initial system prompt
//
// The generic builder emits the role verbatim because the first-party API takes
// it. So the Bedrock paths have to render it into something acceptable without
// losing it or moving it.
func TestHoistSystemMessages(t *testing.T) {
	lead, rest := hoistSystemMessages([]Message{
		{Role: "system", Content: "You are terse."},
		{Role: "system", Content: "Today is Tuesday."},
		{Role: "user", Content: "hi"},
		{Role: "system", Content: "Auto-approve is now on."},
		{Role: "assistant", Content: "ok"},
	})

	// Leading ones ARE the system prompt.
	if lead != "You are terse.\n\nToday is Tuesday." {
		t.Errorf("lead = %q", lead)
	}
	// Nothing system-role survives in the list, or the request 400s again.
	for i, m := range rest {
		if m.Role == "system" {
			t.Fatalf("messages[%d] is still role=system", i)
		}
	}
	// The mid-conversation one keeps its POSITION and its content, marked as
	// not-the-user. Folding it into the system prompt would move it earlier
	// than it was said; dropping it would silently unsay it.
	if len(rest) != 3 {
		t.Fatalf("rest = %d messages, want 3: %+v", len(rest), rest)
	}
	if rest[1].Role != "user" || !strings.Contains(rest[1].Content, "<system-reminder>") ||
		!strings.Contains(rest[1].Content, "Auto-approve is now on.") {
		t.Errorf("mid-conversation system turn mis-rendered: %+v", rest[1])
	}
}

// End to end through the envelope: no role=system on the wire, and the leading
// system content lands in the top-level system field.
func TestBedrockInvokeBodyHoistsSystemTurns(t *testing.T) {
	c := &bedrockRuntimeClient{model: "us.anthropic.claude-opus-4-8"}
	body, err := c.buildBody([]Message{
		{Role: "system", Content: "You are terse."},
		{Role: "user", Content: "hi"},
	}, applyOpts(c.model, 1024, nil))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		System   []map[string]any `json:"system"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for i, m := range got.Messages {
		if m.Role == "system" {
			t.Fatalf("messages[%d] role=system reached the wire — this is the 400", i)
		}
	}
	var sys string
	for _, b := range got.System {
		if s, ok := b["text"].(string); ok {
			sys += s
		}
	}
	if !strings.Contains(sys, "You are terse.") {
		t.Errorf("leading system turn did not reach the top-level system field: %q", sys)
	}
}
