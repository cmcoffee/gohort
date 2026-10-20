package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"
)

// A live artifact reads only a custom app's data. The pane fetches each
// declared path with the viewer's cookies and hands the body to a document
// the model wrote, so anything wider is everything the viewer can read.
func TestALiveArtifactMayNameOnlyAnAppsData(t *testing.T) {
	for _, p := range []string{
		"/apps/metrics/data/daily",
		"/apps/metrics/shared/scores",
		"/apps/metrics/records",
	} {
		if why := artifactDataPathProblem(p); why != "" {
			t.Errorf("%s refused: %s", p, why)
		}
	}
	for _, p := range []string{
		"/api/desktop/key",
		"/api/account/tokens",
		"/apps/_admin/review",
		"/apps/_apps",
		"/apps/metrics/",
		"/apps/metrics/_settings/values",
		"/apps/metrics/assets/logo.png",
		"/apps/metrics/data/",
		"/apps/metrics/data/a/b",
		"/apps/metrics/data/../../../api/desktop/key",
		"/apps/metrics/data/%2e%2e",
		"/apps/metrics/data/x?y=1",
		"/apps/metrics/data/x#frag",
		"/apps/metrics/data\\x",
		"//evil.example/apps/x/records",
		"apps/metrics/records",
	} {
		if artifactDataPathProblem(p) == "" {
			t.Errorf("%s was allowed", p)
		}
	}
}

// A block saved before the check replays its stored list to the pane, so the
// filter applies wherever a block is encoded, not only when one is made.
func TestAStoredArtifactLosesItsWiderPaths(t *testing.T) {
	blk := UIBlock{Type: "html_artifact", ID: "a1", HTML: "<p>x</p>",
		DataURLs: []string{"/api/desktop/key", "/apps/metrics/records"}}
	b, err := json.Marshal(blk)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "/api/desktop/key") {
		t.Errorf("a stored block still sends the desktop key path: %s", b)
	}
	if !strings.Contains(string(b), "/apps/metrics/records") {
		t.Errorf("an allowed path was dropped: %s", b)
	}
	// Inside a session record too, which is how a reload sends it.
	b, _ = json.Marshal(struct {
		UIBlocks []UIBlock `json:"ui_blocks"`
	}{[]UIBlock{blk}})
	if strings.Contains(string(b), "/api/desktop/key") {
		t.Errorf("a session's blocks still send the desktop key path: %s", b)
	}
}
