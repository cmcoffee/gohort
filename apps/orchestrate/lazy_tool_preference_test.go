package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A held-back tool loses an unfair fight. It is a BULLET in a prose section;
// fetch_url is a real entry in the tool-calling API with a full schema, one
// call away. A model comparing "purpose-built, but load it first" against
// "generic, callable right now" takes the generic one nearly every time — and
// the old wording said only that loading was POSSIBLE, never that it was
// preferable.
//
// Reported live: an agent built to talk to a social network reached for
// fetch_url on every turn while its own posting tools sat in this list.
func TestTheLazySectionSaysToPreferItsTools(t *testing.T) {
	section := lazyToolSectionFor([]AgentToolDef{
		{Tool: Tool{Name: "moltbook", Description: "post to and read the moltbook social network"}},
	})

	// The tool is named with enough description to be chosen on.
	if !strings.Contains(section, "moltbook") || !strings.Contains(section, "social network") {
		t.Fatalf("the tool is not described:\n%s", section)
	}
	// It says HOW to reach it.
	if !strings.Contains(section, "load_tool") {
		t.Errorf("the section does not say how to load them:\n%s", section)
	}
	// And, the part that was missing, that it should be preferred over the
	// generic tools it was losing to.
	if !strings.Contains(section, "Prefer these") {
		t.Errorf("the section never says to prefer these tools:\n%s", section)
	}
	for _, generic := range []string{"fetch_url", "browse_page", "web_search"} {
		if !strings.Contains(section, generic) {
			t.Errorf("the section does not name %q as the thing not to reach for instead:\n%s", generic, section)
		}
	}
}

// No held-back tools, no section — an agent whose tools are all directly
// callable must not be told to prefer an empty list.
func TestNoLazyToolsMeansNoSection(t *testing.T) {
	if got := lazyToolSectionFor(nil); got != "" {
		t.Errorf("an agent with no held-back tools got a section:\n%s", got)
	}
}
