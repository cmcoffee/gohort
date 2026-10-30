package scribe

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/agenthost"
)

// A send carrying what the author highlighted puts that passage in the turn,
// and the send still reads its message from the body.
func TestASendCarriesTheHighlightedPassage(t *testing.T) {
	body := `{"message":"make this shorter","selection":{"text":"Restart the service before upgrading."}}`
	r := withHighlight(httptest.NewRequest("POST", "/scribe/chat/send", strings.NewReader(body)))
	if got := agenthost.TurnContext(r.Context()); !strings.Contains(got, "Restart the service before upgrading.") {
		t.Errorf("the turn is not about the passage:\n%s", got)
	}
	if b, _ := io.ReadAll(r.Body); string(b) != body {
		t.Errorf("the body the send reads: %q", b)
	}
	plain := withHighlight(httptest.NewRequest("POST", "/scribe/chat/send", strings.NewReader(`{"message":"hi"}`)))
	if agenthost.TurnContext(plain.Context()) != "" {
		t.Error("a send with nothing highlighted carries no passage")
	}
}
