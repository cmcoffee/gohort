package core

import (
	"context"
	"testing"
)

// A tool's output can only suggest a recovery path the caller can actually
// take. fetch_url's binary-response message hardcoded "read_file/run_local",
// both servitor-scoped, so a research pipeline stage holding only web_search +
// fetch_url + browse_page was handed a door that wasn't there — and re-fetched
// the same 4.2MB PDF five times instead of concluding it couldn't read it.

func TestHasTool_UnknownFailsClosed(t *testing.T) {
	// Nobody called SetAvailableTools: we do not know the catalog, so we must
	// not claim anything is present. Answering true here is what produced the
	// imaginary-tool suggestion in the first place.
	var unset ToolSession
	if unset.HasTool("read_file") {
		t.Error("an unstamped session must not claim to have tools")
	}
	var nilSess *ToolSession
	if nilSess.HasTool("read_file") {
		t.Error("a nil session must not claim to have tools")
	}
}

func TestHasTool_ReportsWhatResolved(t *testing.T) {
	var s ToolSession
	s.SetAvailableTools([]string{"web_search", "fetch_url", "browse_page"})
	if !s.HasTool("fetch_url") {
		t.Error("a resolved tool must report present")
	}
	if s.HasTool("read_file") {
		t.Error("an unresolved tool must report absent")
	}
}

func TestSetAvailableTools_EmptyIsKnownEmpty(t *testing.T) {
	// Distinct from unset: we know the catalog and it has nothing we want.
	var s ToolSession
	s.SetAvailableTools(nil)
	if s.HasTool("read_file") {
		t.Error("empty catalog must report absent")
	}
}

func TestFirstAvailableTool_PrefersEarlier(t *testing.T) {
	var s ToolSession
	s.SetAvailableTools([]string{"python", "shell"})
	// Order in the call expresses preference, not the order in the catalog.
	if got := s.FirstAvailableTool("read_file", "shell", "python"); got != "shell" {
		t.Errorf("got %q, want shell", got)
	}
	if got := s.FirstAvailableTool("read_file", "run_local"); got != "" {
		t.Errorf("no match must return empty, got %q", got)
	}
}

func TestFirstAvailableTool_NilSafe(t *testing.T) {
	var nilSess *ToolSession
	if got := nilSess.FirstAvailableTool("read_file", "shell"); got != "" {
		t.Errorf("nil session must return empty, got %q", got)
	}
}

func TestSetAvailableTools_NilSessionDoesNotPanic(t *testing.T) {
	var nilSess *ToolSession
	nilSess.SetAvailableTools([]string{"web_search"}) // must not panic
}

// A tool an app hands the loop reads the turn's session off its context,
// and gets nothing outside a run.
func TestAToolReadsTheSessionOffItsContext(t *testing.T) {
	sess := &ToolSession{}
	if got := ToolSessionFromContext(sess.ContextWithSession(context.Background())); got != sess {
		t.Error("the session did not come back off the context")
	}
	if ToolSessionFromContext(context.Background()) != nil || ToolSessionFromContext(nil) != nil {
		t.Error("a context outside a run should carry no session")
	}
	var none *ToolSession
	if ToolSessionFromContext(none.ContextWithSession(context.Background())) != nil {
		t.Error("a nil session should not be put on the context")
	}
}
