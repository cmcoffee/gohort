package servitor

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func mkTool(name string) AgentToolDef {
	return AgentToolDef{Tool: Tool{Name: name}}
}

// TestAssertOnlyAllowedTools_AcceptsAllowed verifies the guard is
// silent when every tool in the slice is present in the allow set.
func TestAssertOnlyAllowedTools_AcceptsAllowed(t *testing.T) {
	allowed := map[string]bool{"a": true, "b": true}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("expected no panic for allowed-only tool set, got: %v", r)
		}
	}()
	assertOnlyAllowedTools("test", []AgentToolDef{mkTool("a"), mkTool("b")}, allowed)
}

// TestAssertOnlyAllowedTools_RejectsDisallowed verifies the guard
// panics with a useful message when an outside-allow-list tool sneaks
// in. The panic message is what catches a future refactor at boot.
func TestAssertOnlyAllowedTools_RejectsDisallowed(t *testing.T) {
	allowed := map[string]bool{"a": true}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for disallowed tool, got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value was not a string: %v", r)
		}
		if !strings.Contains(msg, "fetch_url") {
			t.Errorf("panic message missing offending tool name: %q", msg)
		}
		if !strings.Contains(msg, "tool_guard.go") {
			t.Errorf("panic message missing pointer to allow-list file: %q", msg)
		}
	}()
	assertOnlyAllowedTools("test", []AgentToolDef{mkTool("a"), mkTool("fetch_url")}, allowed)
}

// TestServitorAllowLists_NoOutboundTools is the privacy invariant:
// neither allow-list contains any tool name commonly used to reach
// third-party services. If gohort grows a new outbound tool with a
// name matching one of these, this test will catch the moment it's
// (mis-)added to the servitor allow-list.
func TestServitorAllowLists_NoOutboundTools(t *testing.T) {
	forbidden := []string{
		"web_search", "fetch_url", "browse_page", "screenshot_page",
		"fetch_image", "find_image", "generate_image",
		"download_video", "view_video",
		"send_email", "delegate",
	}
	for _, name := range forbidden {
		if servitorWorkerToolAllowList[name] {
			t.Errorf("servitorWorkerToolAllowList must not contain outbound tool %q", name)
		}
		if servitorOrchestratorToolAllowList[name] {
			t.Errorf("servitorOrchestratorToolAllowList must not contain outbound tool %q", name)
		}
	}
	// Any name beginning with call_ is a secure-API credential dispatch
	// (defined dynamically). Servitor must never expose any of them.
	for name := range servitorWorkerToolAllowList {
		if strings.HasPrefix(name, "call_") {
			t.Errorf("servitorWorkerToolAllowList must not contain credential-dispatch tool %q", name)
		}
	}
	for name := range servitorOrchestratorToolAllowList {
		if strings.HasPrefix(name, "call_") {
			t.Errorf("servitorOrchestratorToolAllowList must not contain credential-dispatch tool %q", name)
		}
	}
}
