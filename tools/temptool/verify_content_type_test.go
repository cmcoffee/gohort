package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestVerifyHonorsContentType is the regression guard for the CalDAV verify
// gap: tool_def(action="test") render-validated every body_template as JSON,
// so an XML PROPFIND/REPORT body (content_type=application/xml) FAILED verify
// even though the dispatch path sends it raw correctly. A Builder reasonably
// read that FAIL as "api mode can't send XML" and gave up. Verify must mirror
// dispatch: a non-JSON content_type switches to raw substitution + no JSON
// validation.
func TestVerifyHonorsContentType(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{
		Name: "no_auth", Type: SecureCredNone,
		BaseURL: "https://caldav.example.com",
	}, ""); err != nil {
		t.Fatalf("save credential: %v", err)
	}

	sess := &ToolSession{
		Username:      "alice",
		ChatSessionID: "s1",
		WorkspaceDir:  t.TempDir(),
		DB:            &DBase{Store: kvlite.MemStore()},
	}

	create := map[string]any{
		"action":       "create",
		"name":         "caldav_events",
		"description":  "list caldav events in a date range",
		"mode":         "api",
		"credential":   "no_auth",
		"url_template": "https://caldav.example.com/195178399/calendars/home/",
		"method":       "REPORT",
		"content_type": "application/xml",
		"body_template": `<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">` +
			`<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">` +
			`<c:time-range start="{start_date}T000000Z" end="{end_date}T235959Z"/>` +
			`</c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`,
		"params": map[string]any{
			"start_date": map[string]any{"type": "string", "description": "YYYYMMDD"},
			"end_date":   map[string]any{"type": "string", "description": "YYYYMMDD"},
		},
		"required": []any{"start_date", "end_date"},
	}
	if _, err := createGrouped(create, sess); err != nil {
		t.Fatalf("create: %v", err)
	}

	// REPORT is a write-shaped method, so verify render-validates the body
	// (offline) rather than live-firing it — exactly the path that used to
	// reject the XML.
	report, err := testGrouped(map[string]any{
		"name": "caldav_events",
		"cases": []any{map[string]any{
			"args": map[string]any{"start_date": "20260722", "end_date": "20260722"},
		}},
	}, sess)
	if err != nil {
		t.Fatalf("test/verify: %v", err)
	}

	if strings.Contains(report, "INVALID JSON") {
		t.Fatalf("verify wrongly JSON-validated an XML body:\n%s", report)
	}
	if !strings.Contains(report, "raw, application/xml") {
		t.Fatalf("verify did not report raw-body handling for the XML content_type:\n%s", report)
	}
	if strings.Contains(report, "endpoint(s) FAILED") {
		t.Fatalf("verify FAILED an otherwise-valid XML api tool:\n%s", report)
	}
}
