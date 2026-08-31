package websearch

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every place that SENDS a search must refresh a peer credential first.
//
// The config is resolved without blocking — that call also answers "is search
// enabled" during a page render — so for a peer it can carry the static pairing
// key rather than a live token, and the far side answers 401 "unrecognized or
// disabled peer key". The transport-based peer paths recover by reading that
// string and retrying; a config seam cannot, because it holds a string rather
// than a round trip.
//
// Scanned from source rather than listed here: a new search entry point is
// exactly the kind of thing that gets added without reading this file, and it
// fails intermittently — only after a token expires — which is the hardest
// possible way to notice.
func TestEverySearchSendRefreshesThePeerCredential(t *testing.T) {
	src, err := os.ReadFile("websearch.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	sends := regexp.MustCompile(`SearchWithProvider\(SearchRequest\{`).FindAllStringIndex(body, -1)
	if len(sends) == 0 {
		t.Fatal("found no search send sites; the scan pattern has drifted from the code")
	}
	for _, at := range sends {
		// The refresh belongs in the same function, above the call. Looking
		// back a bounded window keeps this honest about WHERE rather than just
		// whether the file mentions it somewhere.
		start := at[0] - 800
		if start < 0 {
			start = 0
		}
		if !strings.Contains(body[start:at[0]], "RefreshPeerCredential(") {
			t.Errorf("a SearchWithProvider call at offset %d is not preceded by RefreshPeerCredential — "+
				"a peer-backed search from here will 401 the first time the token has expired", at[0])
		}
	}
}
