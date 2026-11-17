package knowledge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The documents list redraws, once, when something under its collection's
// address changes (a curation run ending, a held page added), and not for
// another collection's change or anything else on the page.
func TestTheDocumentsListHearsWhatChangesItsCollection(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := strings.TrimSuffix(strings.TrimPrefix(collectionWatchJS, "<script>"), "</script>")
	harness := `var listeners = [];
var window = {addEventListener: function(type, fn) { if (type === 'ui-data-changed') listeners.push(fn); }};
function fire(sources) { listeners.forEach(function(fn) { fn({detail: {sources: sources}}); }); }
` + script + `
var reloads = 0;
window.knowledgeWatchCollection('/orchestrate/api/collections/c1/', function() { reloads++; });
fire(['/orchestrate/api/collections/c2/curate']);
fire(['api/projects']);
fire([]);
setTimeout(function() {
  if (reloads !== 0) { console.log('another collection or page source redrew the list: ' + reloads); process.exit(1); }
  fire(['/orchestrate/api/collections/c1/curate']);
  fire(['/orchestrate/api/collections/c1/held', 'x']);
  setTimeout(function() {
    if (reloads !== 1) { console.log('two changes in a row should redraw once, got ' + reloads); process.exit(1); }
    console.log('ok');
  }, 700);
}, 700);
`
	f := filepath.Join(t.TempDir(), "watch.js")
	if err := os.WriteFile(f, []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("%v: %s", err, out)
	}
	if !strings.Contains(documentsDetailAssets, "window.knowledgeWatchCollection('/orchestrate/api/collections/' + encodeURIComponent(cid) + '/'") {
		t.Error("the collection page's script does not watch its collection")
	}
}
