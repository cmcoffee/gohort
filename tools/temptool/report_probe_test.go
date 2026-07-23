package temptool

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestVerifyLiveProbesReport is the fix for the CalDAV list_events verify punt:
// REPORT is a read-only WebDAV query, but the classifier treated it as a write,
// so verify refused to fire it and told the user to make a manual call for a
// plain read. Now verify live-fires a REPORT (with its raw XML body via
// content_type) and asserts a 2xx.
func TestVerifyLiveProbesReport(t *testing.T) {
	var gotMethod, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusMultiStatus) // 207
		_, _ = w.Write([]byte(`<multistatus xmlns="DAV:"/>`))
	}))
	defer srv.Close()

	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{Name: "cal", Type: SecureCredNone, BaseURL: srv.URL}, ""); err != nil {
		t.Fatalf("cred: %v", err)
	}
	sess := &ToolSession{Username: "alice", ChatSessionID: "s1", WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()}}

	if _, err := createGrouped(map[string]any{
		"action": "create", "name": "list_events", "description": "d", "mode": "api",
		"credential": "cal", "url_template": srv.URL + "/calendars/work/", "method": "REPORT",
		"content_type":  "application/xml",
		"body_template": `<c:calendar-query xmlns:c="urn:ietf:params:xml:ns:caldav"><c:time-range start="{start}" end="{end}"/></c:calendar-query>`,
		"params":        map[string]any{"start": map[string]any{"type": "string"}, "end": map[string]any{"type": "string"}},
		"required":      []any{"start", "end"},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}

	report, err := testGrouped(map[string]any{
		"name":  "list_events",
		"cases": []any{map[string]any{"args": map[string]any{"start": "20260722T000000Z", "end": "20260722T235959Z"}}},
	}, sess)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if strings.Contains(report, "write endpoint NOT auto-fired") {
		t.Fatalf("REPORT must be live-probed as a read, not punted as a write:\n%s", report)
	}
	if !strings.Contains(report, "live REPORT returned") {
		t.Fatalf("expected a live REPORT probe result:\n%s", report)
	}
	// The probe must have actually sent a REPORT with the raw XML body.
	if gotMethod != "REPORT" || gotCT != "application/xml" {
		t.Fatalf("probe didn't send a raw-XML REPORT: method=%q ct=%q", gotMethod, gotCT)
	}
}
