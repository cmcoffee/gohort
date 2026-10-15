package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
)

// Builder's survey shows the file stores and the commands registered on them,
// and the other sources an agent can attach, so a request naming one starts
// from what is there instead of rebuilding it.
func TestSurveyShowsFileStoresAndSources(t *testing.T) {
	prev := appstores.Provider
	appstores.Provider = fakeStoreCatalog{}
	t.Cleanup(func() { appstores.Provider = prev })
	RegisterReferenceSource(fakeRefSource{kind: "testfiles", label: "Test files"})
	_, _, user := newTestOrchestrate(t)
	pinRootDB(t)
	out := surveyWorkspace(user)
	for _, want := range []string{
		"FILE STORES (2)",
		`- Intake (store "intake"), 3 folder(s) | commands: extract (two steps)`,
		`- Analysis (store "analysis"), 0 folder(s), pulls from intake`,
		"SOURCES (", "- Test files: Prod logs (attach as testfiles:prod)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("survey is missing %q:\n%s", want, out)
		}
	}
}
