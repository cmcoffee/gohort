package websearch

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A refusal gets a try in the browser; a page's own answer does not.
func TestBlockedFetchTellsARefusalFromAMiss(t *testing.T) {
	for _, c := range []struct {
		err  error
		text string
		want string
	}{
		{&httpStatusError{code: 403}, "", "HTTP 403"},
		{&httpStatusError{code: 429}, "", "HTTP 429"},
		{&httpStatusError{code: 522}, "", "HTTP 522"},
		{&httpStatusError{code: 404}, "", ""},
		{&httpStatusError{code: 500}, "", ""},
		{nil, "Verify you are human by completing the action below.", "a wall instead of the page"},
		{nil, `{"status":"ok"}`, ""},
		{nil, "", ""},
	} {
		if got := blockedFetch(c.err, c.text); got != c.want {
			t.Errorf("blockedFetch(%v, %q) = %q, want %q", c.err, c.text, got, c.want)
		}
	}
}

// A page answered with a refusal keeps its status code, so the caller can
// tell it from a miss.
func TestARefusedFetchKeepsItsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	_, _, err := fetchArticleInternal(srv.URL+"/page", 8000)
	if got := blockedFetch(err, ""); got != "HTTP 403" {
		t.Errorf("a 403 read as %q (err %v)", got, err)
	}
}
