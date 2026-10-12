package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Notes over the cap name where to cut: a changelog goes, since revisions keep
// it; otherwise the largest section.
func TestNotesOverTheCapSayWhereToCut(t *testing.T) {
	logged := "# App\n\nWhat it is.\n\n## Changelog\n- 2026-10-11: hot take\n- 2026-10-10: description\n\n## Open\n- history"
	if h := notesTrimHint(logged); !strings.Contains(h, `"## Changelog"`) || !strings.Contains(h, "note=") {
		t.Errorf("a changelog should be named to drop: %s", h)
	}
	plain := "# App\n\nShort.\n\n## Decisions\n" + strings.Repeat("A long reason. ", 40) + "\n\n## Open\n- one"
	if h := notesTrimHint(plain); !strings.Contains(h, `"## Decisions"`) {
		t.Errorf("the largest section should be named: %s", h)
	}
}

// A publish that says what changed in note= is not sent back to write it into
// NOTES.md: that prompt grew the notes into a changelog until the cap refused
// a publish.
func TestAPublishWithANoteIsNotAskedToLogItInNotes(t *testing.T) {
	turn, ws := folderTestTurn(t)
	if _, err := turn.appDefCheckout(map[string]any{"name": "Wx"}); err != nil {
		t.Fatal(err)
	}
	if _, err := turn.appDefPublish(map[string]any{"dir": "wx.app", "notes": "Shows the weather for one city."}); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(ws, "wx.app", "page.html")
	b, _ := os.ReadFile(page)
	os.WriteFile(page, append(b, []byte("\n<!-- v2 -->\n")...), 0o644)
	out, err := turn.appDefPublish(map[string]any{"dir": "wx.app", "note": "marks v2"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "NOTES.md did not") || strings.Contains(out, "no note=") {
		t.Errorf("a publish with a note was sent to log it in NOTES.md:\n%s", out)
	}
	b, _ = os.ReadFile(page)
	os.WriteFile(page, append(b, []byte("\n<!-- v3 -->\n")...), 0o644)
	out, err = turn.appDefPublish(map[string]any{"dir": "wx.app"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Nothing to redo now") || strings.Contains(out, "publish again") {
		t.Errorf("a publish without a note should get the one-line reminder, not a republish:\n%s", out)
	}
}
