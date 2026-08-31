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

	saved := map[string]bool{}
	for _, m := range regexp.MustCompile(`global\.db\.(?:Crypt)?Set\(SearchTable, "([a-z_]+)"`).FindAllStringSubmatch(body, -1) {
		saved[m[1]] = true
	}
	if len(saved) == 0 {
		t.Fatal("found no SearchTable writes; the scan pattern has drifted from the code")
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

// The CLI menu offers no way to pick a peer, so a visit that answers nothing
// about peers must change nothing about them. Saving the search block without
// carrying source through would silently unpair a search configured on the web.
func TestTheSetupMenuCarriesSourceThrough(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	at := strings.Index(body, `global.db.Set(SearchTable, "source"`)
	if at < 0 {
		t.Fatal("the setup menu no longer writes source back; a CLI save will unpair a peer search")
	}
	// And it must write back what it read, not a fresh empty string.
	if !strings.Contains(body, `global.db.Get(SearchTable, "source", &searchSource)`) {
		t.Error("source is written by the menu but never read into it — the save would blank it")
	}
}

// The setup menu edits a handful of fields on configs that carry more than it
// shows. Rebuilding one from scratch does not blank a field, it CONVERTS the
// config: for a peer-backed setup, Provider drops while the resolved peer
// endpoint stays behind, and "embed on peer den" silently becomes "embed on
// this URL by hand" — after which nothing overlays the peer's live endpoint or
// swaps in a live credential, because the config no longer mentions a peer, and
// every request 401s while the peer itself is healthy.
//
// Observed exactly that way: a peer-backed embeddings config found sitting on a
// manual endpoint pointing at the peer's own URL.
func TestTheSetupMenuPreservesFieldsItDoesNotShow(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	// Each of these must be built FROM what was stored, not from scratch. A
	// composite literal here is the bug: it says "these are all the fields",
	// and the struct disagrees.
	for _, tc := range []struct{ built, from string }{
		{"newEmbedCfg", "storedEmbed"},
		{"newSTT", "storedSTT"},
	} {
		if strings.Contains(body, tc.built+" := EmbeddingConfig{") ||
			strings.Contains(body, tc.built+" := TranscribeConfig{") {
			t.Errorf("%s is rebuilt from a literal — every field the menu does not show is erased, "+
				"which converts a peer-backed config into a manual one pointed at the peer's URL", tc.built)
		}
		if !strings.Contains(body, tc.built+" := "+tc.from) {
			t.Errorf("%s should start from %s so unshown fields survive the save", tc.built, tc.from)
		}
	}
}
