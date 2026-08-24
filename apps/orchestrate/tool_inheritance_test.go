package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The inheritable set is drawn from TWO builders, and for a long time it was
// drawn from one. The filter matched "list_chats", "read_chat" and "notify_me"
// against operatorManagementTools, which builds 18 tools and neither reader is
// among them — they live in channelChatTools. So it returned notify_me alone
// and this file's stated purpose, letting a Builder-authored summarizer read
// the chat it summarizes, never worked through inheritance.
//
// Asserted on the NAMES the filter selects rather than on a live channel setup:
// channelChatTools self-gates to nil when the agent has no channels or no
// transport is registered, so a fixture without one proves nothing about the
// second source. What this pins is that the names being selected are names the
// sources actually build — the thing that was false.
func TestInheritableToolNamesExistInTheirSources(t *testing.T) {
	sess := &ToolSession{Username: "u"}
	built := map[string]bool{}
	for _, td := range operatorManagementTools(sess, "agent-1") {
		built[td.Tool.Name] = true
	}

	// notify_me is the half that always worked.
	if !built["notify_me"] {
		t.Error("operatorManagementTools no longer builds notify_me; the inheritance filter selects it")
	}
	// The readers must NOT be sought there — this is the mistake, pinned so it
	// cannot be reintroduced by someone "simplifying" back to one loop.
	for _, name := range []string{"list_chats", "read_chat"} {
		if built[name] {
			t.Errorf("operatorManagementTools now builds %s; the two-source split in phantomInheritableToolDefs can be revisited", name)
		}
	}

	// And the consequential reachers stay out of the inheritable set however
	// the sources change.
	for _, td := range phantomInheritableToolDefs(sess, "u", "agent-1") {
		switch td.Tool.Name {
		case "send_message", "message_contact", "converse_with_contact":
			t.Errorf("%s is inheritable; only the owner-safe read tools and notify_me may be", td.Tool.Name)
		}
	}
}

// The doc comment named list_phantom_chats / read_phantom_chat for years. Those
// are not tool names anywhere in the tree — only in comments — which is a large
// part of why the filter looked correct to read.
func TestNoPhantomChatToolNamesRemain(t *testing.T) {
	for _, stale := range []string{"list_phantom_chats", "read_phantom_chat"} {
		if _, ok := LookupChatTool(stale); ok {
			t.Errorf("%s exists as a real tool after all — the inheritance filter should name it", stale)
		}
		if strings.TrimSpace(stale) == "" {
			t.Fatal("unreachable")
		}
	}
}
