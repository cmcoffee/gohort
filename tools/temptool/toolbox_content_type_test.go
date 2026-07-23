package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestToolboxActionContentType is the regression guard for the CalDAV-as-a-
// toolbox gap: a toolbox action with a non-JSON content_type (a REPORT/xml
// action, a PUT/text/calendar action) must carry that content_type through
// create, the update round-trip, verify, and dispatch — otherwise its XML/
// iCalendar body is JSON-validated and dies with "invalid character '<'", the
// same failure single api tools hit before Fix A.
func TestToolboxActionContentType(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{
		Name: "apple_caldav", Type: SecureCredNone, BaseURL: "https://caldav.example.com",
	}, ""); err != nil {
		t.Fatalf("save cred: %v", err)
	}
	sess := &ToolSession{
		Username: "alice", ChatSessionID: "s1",
		WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()},
		// Offline-only: this test targets the render/content_type path; REPORT is
		// now live-probed and the fake host would fail DNS.
		Network: NewNetworkConnector(true),
	}

	create := map[string]any{
		"action":      "create",
		"name":        "apple_calendar_manager",
		"description": "caldav toolbox",
		"mode":        "toolbox",
		"credential":  "apple_caldav",
		"actions": []any{
			map[string]any{
				"name":         "list_events",
				"description":  "report events",
				"url_template": "/195178399/calendars/home/",
				"method":       "REPORT",
				"content_type": "application/xml",
				"body_template": `<c:calendar-query xmlns:c="urn:ietf:params:xml:ns:caldav">` +
					`<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">` +
					`<c:time-range start="{start}" end="{end}"/></c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`,
				"params":   map[string]any{"start": map[string]any{"type": "string"}, "end": map[string]any{"type": "string"}},
				"required": []any{"start", "end"},
			},
		},
	}
	if _, err := createGrouped(create, sess); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The action's content_type must be persisted.
	rec, ok := loadExistingToolRecord(sess, "apple_calendar_manager")
	if !ok || len(rec.Actions) != 1 {
		t.Fatalf("toolbox not stored with its action")
	}
	if rec.Actions[0].ContentType != "application/xml" {
		t.Fatalf("action content_type dropped on create; got %q", rec.Actions[0].ContentType)
	}

	// It must survive an unrelated update (edit the description only).
	if _, err := updateGrouped(map[string]any{
		"name":        "apple_calendar_manager",
		"description": "caldav toolbox (edited)",
	}, sess); err != nil {
		t.Fatalf("update: %v", err)
	}
	rec, _ = loadExistingToolRecord(sess, "apple_calendar_manager")
	if rec.Actions[0].ContentType != "application/xml" {
		t.Fatalf("action content_type dropped on update round-trip; got %q", rec.Actions[0].ContentType)
	}

	// Verify must treat the XML body as raw, not fail it as invalid JSON.
	report, err := testGrouped(map[string]any{
		"name":  "apple_calendar_manager",
		"cases": []any{map[string]any{"action": "list_events", "args": map[string]any{"start": "20260722T000000Z", "end": "20260728T235959Z"}}},
	}, sess)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if strings.Contains(report, "INVALID JSON") {
		t.Fatalf("verify JSON-validated an XML toolbox action:\n%s", report)
	}
	if !strings.Contains(report, "raw, application/xml") {
		t.Fatalf("verify did not report raw-body handling for the action:\n%s", report)
	}
}
