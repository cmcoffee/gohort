package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The routes are ServeMux patterns with wildcards, and the binary must serve
// them as patterns however it was built. A GOPATH-mode build took an old Go's
// GODEBUG defaults (httpmuxgo121=1), every "{id}" route answered 404, and no
// session could be opened. gohort.go pins the mux; this fails without it when
// run in GOPATH mode (GO111MODULE=off go test github.com/cmcoffee/gohort).
func TestTheMuxServesWildcardPatterns(t *testing.T) {
	mux := http.NewServeMux()
	var got string
	mux.HandleFunc("/api/sessions/{sid}", func(w http.ResponseWriter, r *http.Request) { got = r.PathValue("sid") })
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/sessions/s1", nil))
	if w.Code != http.StatusOK || got != "s1" {
		t.Fatalf("a {sid} pattern answered %d with sid %q: the mux is the Go 1.21 one (httpmuxgo121=1)", w.Code, got)
	}
}
