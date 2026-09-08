package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// A peer-backed web search lives or dies on Source. It is the pointer at the
// peer, and BOTH mechanisms that keep such a search working key off it:
// resolveSearchPeer overlays the peer's current endpoint and credential, and
// searchSearXNG picks the peer TRANSPORT, which authenticates with a freshly
// resolved token and replays a refused request.
//
// The admin page wrote "source" and read it back, so testing a peer there
// passed; the runtime loader in this file never read it, so every search the
// agents ran went out on a key snapshotted when the peer was first selected and
// came back 401 "unrecognized or disabled peer key". Two surfaces disagreeing
// about which fields a config has, each looking correct on its own.
//
// Source-scanned because the failure is not a crash or a bad value — it is a
// field quietly missing from one of two readers, which no type checks.
func TestSearchConfigReadsEveryFieldItIsSaved(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	// The WRITES moved to the admin app when search configuration left the CLI
	// menu (the terminal only sets what has to be right before a browser can
	// reach you). The loader stayed here. The invariant is unchanged and spans
	// both files: every field written must be read by whatever serves a search.
	writers, err := os.ReadFile("apps/admin/api_net_config.go")
	if err != nil {
		t.Fatal(err)
	}
	saved := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?:global\.db|a\.db)\.(?:Crypt)?Set\(SearchTable, "([a-z_]+)"`).FindAllStringSubmatch(body+string(writers), -1) {
		saved[m[1]] = true
	}
	if len(saved) == 0 {
		t.Fatal("found no SearchTable writes in config.go or apps/admin/api_net_config.go; the scan has drifted from the code")
	}

	// Scoped to the RUNTIME loader, not the file. Counting reads file-wide
	// passes while the bug is live: the setup menu reads "source" too, and the
	// setup menu is not what serves a search.
	start := strings.Index(body, "func (d dbCFG) search() WebSearchConfig {")
	if start < 0 {
		t.Fatal("the runtime search-config loader has been renamed; this scan is pointed at nothing")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of the loader")
	}
	loader := body[start : start+end]

	for field := range saved {
		if !strings.Contains(loader, `"`+field+`"`) {
			t.Errorf("SearchTable %q is saved but the runtime loader never reads it — a peer-backed search that depends on it 401s in production while testing green in admin", field)
		}
	}
	if !saved["source"] {
		t.Error("source must be persisted; it is what selects the peer transport")
	}
}

// The two tests that used to live here — "the setup menu carries source
// through" and "the setup menu preserves fields it does not show" — are gone
// with their subject: the CLI menu no longer configures search, embeddings or
// transcription, so it can no longer erase a field it does not display.
//
// The RISK they described did not disappear, it moved: the admin API decodes a
// whole config from the request, so a form that omits a field still posts a
// zero for it. Nothing pins that today. If it bites, the guard belongs in
// apps/admin, written against how that surface actually saves (whole-record
// POST), not copied from a menu that edited a loaded struct in place.
