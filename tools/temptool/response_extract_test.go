package temptool

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

const calDAVBody = `<?xml version="1.0" encoding="UTF-8"?>
<multistatus xmlns="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <response><href>/195178399/calendars/</href><propstat><prop>
    <resourcetype><collection/></resourcetype><displayname>Craig Coffee</displayname>
  </prop></propstat></response>
  <response><href>/195178399/calendars/home/</href><propstat><prop>
    <resourcetype><collection/><cal:calendar/></resourcetype><displayname>Home</displayname>
  </prop></propstat></response>
  <response><href>/195178399/calendars/work/</href><propstat><prop>
    <resourcetype><collection/><cal:calendar/></resourcetype><displayname>Work</displayname>
  </prop></propstat></response>
</multistatus>`

// TestResponseExtractEndToEnd is the payoff test for the whole CalDAV saga: an
// api-mode tool with content_type=application/xml + response_extract dispatches
// a PROPFIND, gets a 207 + WebDAV XML, and returns clean JSON — no shell tool,
// no ElementTree, no chaining. This is the exact thing the model failed 10+
// times to do by hand.
func TestResponseExtractEndToEnd(t *testing.T) {
	var gotMethod, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusMultiStatus) // 207
		_, _ = w.Write([]byte(calDAVBody))
	}))
	defer srv.Close()

	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{
		Name: "cal", Type: SecureCredNone, BaseURL: srv.URL,
	}, ""); err != nil {
		t.Fatalf("save cred: %v", err)
	}
	sess := &ToolSession{Username: "alice", ChatSessionID: "s1", WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()}}

	tt := &TempTool{
		Name:            "get_calendars",
		Mode:            TempToolModeAPI,
		Credential:      "cal",
		CommandTemplate: srv.URL + "/195178399/calendars/",
		Method:          "PROPFIND",
		ContentType:     "application/xml",
		BodyTemplate:    `<d:propfind xmlns:d="DAV:"><d:prop><d:displayname/></d:prop></d:propfind>`,
		ResponseExtract: &ExtractSpec{
			Select: "response",
			Where:  &ExtractWhere{Has: "calendar"},
			Fields: map[string]string{"path": "href", "displayname": "displayname"},
		},
	}

	out, err := dispatchAPIModeTempTool(sess, tt, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// PROPFIND with a raw XML body must have reached the server as XML.
	if gotMethod != "PROPFIND" || gotCT != "application/xml" {
		t.Fatalf("request not sent as XML PROPFIND: method=%q ct=%q", gotMethod, gotCT)
	}
	if !strings.Contains(out, `"displayname":"Home"`) || !strings.Contains(out, `"path":"/195178399/calendars/home/"`) {
		t.Fatalf("expected extracted calendar JSON; got:\n%s", out)
	}
	// The non-calendar base collection ("Craig Coffee") must be filtered out.
	if strings.Contains(out, "Craig Coffee") {
		t.Fatalf("where:{has:calendar} should have dropped the base collection; got:\n%s", out)
	}
}

// TestResponseExtractSurvivesUpdate guards the round-trip drop trap (same class
// that bit hook_capabilities + content_type): response_extract must survive an
// unrelated tool_def update.
func TestResponseExtractSurvivesUpdate(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{Name: "cal", Type: SecureCredNone, BaseURL: "https://caldav.example.com"}, ""); err != nil {
		t.Fatalf("cred: %v", err)
	}
	sess := &ToolSession{Username: "alice", ChatSessionID: "s1", WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()}}

	if _, err := createGrouped(map[string]any{
		"action": "create", "name": "get_calendars", "description": "list", "mode": "api",
		"credential": "cal", "url_template": "https://caldav.example.com/x", "method": "PROPFIND",
		"content_type":     "application/xml",
		"body_template":    `<d:propfind xmlns:d="DAV:"><d:prop><d:displayname/></d:prop></d:propfind>`,
		"response_extract": map[string]any{"select": "response", "fields": map[string]any{"path": "href"}},
	}, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	rec, ok := loadExistingToolRecord(sess, "get_calendars")
	if !ok || rec.ResponseExtract == nil || rec.ResponseExtract.Select != "response" {
		t.Fatalf("response_extract not stored on create: %+v", rec.ResponseExtract)
	}
	if _, err := updateGrouped(map[string]any{"name": "get_calendars", "description": "list (edited)"}, sess); err != nil {
		t.Fatalf("update: %v", err)
	}
	rec, _ = loadExistingToolRecord(sess, "get_calendars")
	if rec.ResponseExtract == nil || rec.ResponseExtract.Select != "response" || rec.ResponseExtract.Fields["path"] != "href" {
		t.Fatalf("response_extract dropped on update round-trip: %+v", rec.ResponseExtract)
	}
}
