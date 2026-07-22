package temptool

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// TestSubstituteURLPathVsQuery guards the CalDAV url-encoding bug: a path-valued
// param (calendar_path="/195178399/calendars/home/") was percent-encoded to
// "%2F195178399%2F..." when substituted into url_template, so the credential
// allowlist rejected it as off-base_url — blocking the final event-fetch tool.
// Path-position placeholders must keep "/" as a separator; query-position ones
// must still fully encode.
func TestSubstituteURLPathVsQuery(t *testing.T) {
	params := map[string]ToolParam{
		"calendar_path": {Type: "string"},
		"q":             {Type: "string"},
	}

	// Path-position: slashes preserved so it resolves against base_url.
	got, err := substituteURL("{calendar_path}", params, []string{"calendar_path"},
		map[string]any{"calendar_path": "/195178399/calendars/home/"})
	if err != nil {
		t.Fatalf("path substitute: %v", err)
	}
	if got != "/195178399/calendars/home/" {
		t.Errorf("path param: got %q, want slashes preserved", got)
	}

	// Path-position but with a space / reserved char — still encoded, slashes kept.
	got, _ = substituteURL("/x/{calendar_path}", params, []string{"calendar_path"},
		map[string]any{"calendar_path": "a b/c?d"})
	if got != "/x/a%20b/c%3Fd" {
		t.Errorf("path param encoding: got %q, want space+? encoded, / kept", got)
	}

	// Query-position: full encoding, including "/".
	got, err = substituteURL("/search?path={q}", params, []string{"q"},
		map[string]any{"q": "a/b c"})
	if err != nil {
		t.Fatalf("query substitute: %v", err)
	}
	if got != "/search?path=a%2Fb%20c" {
		t.Errorf("query param: got %q, want / and space fully encoded", got)
	}
}
