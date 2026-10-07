package customapps

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An app's pipeline runs resume after a restart: the key carries what finds
// the app again, and the restorer is registered at startup.
func TestAppPipelineRunsAreRestorable(t *testing.T) {
	if got := appRunKey(AppSpec{Owner: "owner@example.com", Slug: "weekly-digest"}); got != "owner@example.com/weekly-digest" {
		t.Errorf("restore key = %q, want owner/slug (what loadSpec takes)", got)
	}
	b, err := os.ReadFile("customapps.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "T.registerPipelineRestore()") {
		t.Error("the restorer must be registered when routes are")
	}
	if !strings.Contains(src, "orch.PublicHandleAppPipeline(w, r, def, sub, T.appPipelineLive(spec), appRunKey(spec))") {
		t.Error("an app's pipeline must be served as a restorable app pipeline")
	}
}
