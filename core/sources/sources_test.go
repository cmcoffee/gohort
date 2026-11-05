package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The citation lookup reaches public addresses only: doi.org and the like
// redirect wherever a record says, which can be inward.
func TestACitationIsNotFetchedFromInside(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><meta name="citation_title" content="Inside"><meta name="citation_author" content="Someone"></head></html>`))
	}))
	defer srv.Close()
	if _, title, _ := fetchCitationMetadata(context.Background(), srv.URL); title != "" {
		t.Fatalf("read citation metadata from a loopback address: %q", title)
	}
}
