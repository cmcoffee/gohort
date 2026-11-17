package orchestrate

import (
	"net/http/httptest"
	"testing"

	"github.com/cmcoffee/gohort/core/outbound"
)

// A turn outlives its request, but not what the app named as private to it:
// every web search the turn makes is checked against those names.
func TestADetachedTurnKeepsItsPrivateTerms(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/send", nil)
	r = r.WithContext(outbound.WithPrivateTerms(r.Context(), "Kestrel Control"))
	sr := &sendRun{r: r}
	sr.detachContext()
	defer sr.cancel()
	if got := outbound.PrivateTerms(sr.ctx); len(got) != 1 || got[0] != "Kestrel Control" {
		t.Fatalf("the turn's terms = %q, want the request's", got)
	}
}
