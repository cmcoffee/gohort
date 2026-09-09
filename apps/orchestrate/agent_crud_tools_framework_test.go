package orchestrate

import (
	"strings"
	"testing"
)

// The live failure this exists for: an agent could not reach knowledge_search,
// its owner added the name to allowed_tools, and the save answered "match no
// known tool and were dropped — the agent will NOT have them". Both halves
// misled. The name is framework-injected, so adding it was a no-op rather than
// a fix, and the real cause was elsewhere.
func TestAFrameworkInjectedNameIsNotReportedAsATypo(t *testing.T) {
	rec := &AgentRecord{AllowedTools: []string{"knowledge_search", "fetch_knowledge_doc"}}
	msg := unresolvedToolsWarning(nil, rec)

	if strings.Contains(msg, "were dropped") || strings.Contains(msg, "will NOT have them") {
		t.Errorf("a framework tool must not be reported as a dropped typo: %q", msg)
	}
	if !strings.Contains(msg, "provided by the framework") {
		t.Errorf("say where it actually comes from: %q", msg)
	}
	// The part that would have saved the session: point at the narrowing.
	if !strings.Contains(msg, "machine phase") {
		t.Errorf("an unreachable framework tool is usually a phase narrowing; say so: %q", msg)
	}
	if !strings.Contains(msg, "are provided") {
		t.Errorf("two names should agree the verb: %q", msg)
	}
}

// A genuine typo must still be reported as one, or the fix above has traded a
// misleading warning for a missing one.
func TestARealTypoIsStillReportedAsDropped(t *testing.T) {
	rec := &AgentRecord{AllowedTools: []string{"knowledge_search", "kw_logtrceee"}}
	msg := unresolvedToolsWarning(nil, rec)
	if !strings.Contains(msg, "kw_logtrceee") || !strings.Contains(msg, "were dropped") {
		t.Errorf("a name nothing answers to is still a typo: %q", msg)
	}
	if !strings.Contains(msg, "provided by the framework") {
		t.Errorf("and the framework note should still appear alongside it: %q", msg)
	}
}

func TestOneFrameworkNameAgreesTheVerb(t *testing.T) {
	rec := &AgentRecord{AllowedTools: []string{"knowledge_search"}}
	if msg := unresolvedToolsWarning(nil, rec); !strings.Contains(msg, "is provided") {
		t.Errorf("one name takes a singular verb: %q", msg)
	}
}
