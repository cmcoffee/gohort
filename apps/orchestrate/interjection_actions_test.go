package orchestrate

// A queued note whose turn ended unread is out of the queue: its Edit and
// Delete would lock a note no queue holds, so they go, and a lock that finds
// no queue says the turn ended rather than showing the raw error.

import (
	"os"
	"strings"
	"testing"
)

func TestAnEndedTurnsNoteOffersNoEdit(t *testing.T) {
	b, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, ".ui-agent-interjection-undelivered .ui-orch-interject-actions { display: none; }") {
		t.Error("a note whose turn ended still offers Edit and Delete")
	}
	if !strings.Contains(s, "r.status === 410 || r.status === 404") {
		t.Error("a lock that finds no queue shows the raw error")
	}
}
