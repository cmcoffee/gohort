package temptool

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// withNoAuthCred registers the bootstrapped open-pattern credential the api
// create path resolves when none is named, so these tests exercise authoring
// rather than credential registration.
func withNoAuthCred(t *testing.T) {
	t.Helper()
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	t.Cleanup(func() { AuthDB = prev })
	if err := Secure().Save(SecureCredential{
		Name: "no_auth", Type: SecureCredNone, BaseURL: "https://x.test",
	}, ""); err != nil {
		t.Fatalf("cred: %v", err)
	}
}

// TestApiToolCarriesHeaders is the fix for the iCloud CalDAV read that could
// never work: a calendar-query REPORT needs "Depth: 1" to match the
// collection's CHILD resources. api-mode tools had no way to send a header at
// all (only the sandbox hook did), so the server applied the query at Depth 0,
// matched nothing, and answered 207 with an empty multistatus.
func TestApiToolCarriesHeaders(t *testing.T) {
	withNoAuthCred(t)
	sess := newTestSession()
	create := map[string]any{
		"action":       "create",
		"name":         "caldav_list",
		"description":  "list events",
		"mode":         "api",
		"url_template": "https://x.test/1234/calendars/abc/",
		"method":       "REPORT",
		"content_type": "application/xml",
		"body_template": "<c:calendar-query xmlns:c=\"urn:ietf:params:xml:ns:caldav\">" +
			"<c:filter><c:comp-filter name=\"VCALENDAR\"><c:comp-filter name=\"VEVENT\">" +
			"<c:time-range start=\"{start_date}\" end=\"{end_date}\"/>" +
			"</c:comp-filter></c:comp-filter></c:filter></c:calendar-query>",
		"headers": map[string]any{"Depth": "1"},
		"params": map[string]any{
			"start_date": map[string]any{"type": "string"},
			"end_date":   map[string]any{"type": "string"},
		},
	}
	if _, err := createGrouped(create, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	tt, ok := loadExistingToolRecord(sess, "caldav_list")
	if !ok {
		t.Fatal("tool not found after create")
	}
	if tt.Headers["Depth"] != "1" {
		t.Fatalf("headers not persisted: %v", tt.Headers)
	}

	// The update round-trip is where new api fields historically got dropped:
	// a field has to be wired into BOTH tempToolToCreateArgs and the update
	// whitelist, or an unrelated edit silently erases it.
	if _, err := updateGrouped(map[string]any{
		"action": "update", "name": "caldav_list", "description": "list events (v2)",
	}, sess); err != nil {
		t.Fatalf("update: %v", err)
	}
	tt2, _ := loadExistingToolRecord(sess, "caldav_list")
	if tt2.Headers["Depth"] != "1" {
		t.Fatalf("headers lost across an unrelated update: %v", tt2.Headers)
	}
	if tt2.Description != "list events (v2)" {
		t.Fatalf("update didn't apply: %q", tt2.Description)
	}

	// And an explicit header change replaces them.
	if _, err := updateGrouped(map[string]any{
		"action": "update", "name": "caldav_list",
		"headers": map[string]any{"Depth": "infinity", "Accept": "text/calendar"},
	}, sess); err != nil {
		t.Fatalf("update headers: %v", err)
	}
	tt3, _ := loadExistingToolRecord(sess, "caldav_list")
	if tt3.Headers["Depth"] != "infinity" || tt3.Headers["Accept"] != "text/calendar" {
		t.Fatalf("headers not replaced: %v", tt3.Headers)
	}
}

// TestToolboxActionCarriesHeaders covers the per-action half — a CalDAV
// toolbox needs Depth on its REPORT action but not on its PUT.
func TestToolboxActionCarriesHeaders(t *testing.T) {
	sess := newTestSession()
	create := map[string]any{
		"action": "create", "name": "caldav", "description": "caldav box",
		"mode": "toolbox", "credential": "no_auth",
		"actions": []any{
			map[string]any{
				"name": "list_events", "description": "list",
				"url_template": "https://x.test/cal/", "method": "REPORT",
				"headers": map[string]any{"Depth": "1"},
			},
			map[string]any{
				"name": "put_event", "description": "create",
				"url_template": "https://x.test/cal/{uid}.ics", "method": "PUT",
				"body_template": "BEGIN:VCALENDAR{uid}END:VCALENDAR",
				"content_type":  "text/calendar",
				"params":        map[string]any{"uid": map[string]any{"type": "string"}},
			},
		},
	}
	if _, err := createGrouped(create, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	tt, ok := loadExistingToolRecord(sess, "caldav")
	if !ok {
		t.Fatal("toolbox not found")
	}
	for _, a := range tt.Actions {
		switch a.Name {
		case "list_events":
			if a.Headers["Depth"] != "1" {
				t.Errorf("list_events headers = %v, want Depth:1", a.Headers)
			}
		case "put_event":
			if len(a.Headers) != 0 {
				t.Errorf("put_event should carry no headers, got %v", a.Headers)
			}
		}
	}
}

// TestStringMapArg covers the shapes an LLM actually emits for an object arg.
func TestStringMapArg(t *testing.T) {
	got := stringMapArg(map[string]any{"headers": map[string]any{"Depth": "1"}}, "headers")
	if got["Depth"] != "1" {
		t.Errorf("object form: %v", got)
	}
	// Quoted JSON object — models do this often enough to matter.
	got = stringMapArg(map[string]any{"headers": `{"Depth": "1"}`}, "headers")
	if got["Depth"] != "1" {
		t.Errorf("string form: %v", got)
	}
	// The round-trip shape: tempToolToCreateArgs emits map[string]string, and
	// missing this case is how headers survived create then vanished on the
	// next unrelated update.
	got = stringMapArg(map[string]any{"headers": map[string]string{"Depth": "1"}}, "headers")
	if got["Depth"] != "1" {
		t.Errorf("string-map form: %v", got)
	}
	// Non-string value stringified rather than dropped.
	got = stringMapArg(map[string]any{"headers": map[string]any{"Depth": 1}}, "headers")
	if got["Depth"] != "1" {
		t.Errorf("numeric value: %v", got)
	}
	for _, empty := range []any{nil, "", map[string]any{}, "not json"} {
		if got := stringMapArg(map[string]any{"headers": empty}, "headers"); got != nil {
			t.Errorf("%v should yield nil, got %v", empty, got)
		}
	}
	if got := stringMapArg(map[string]any{}, "headers"); got != nil {
		t.Errorf("absent key should yield nil, got %v", got)
	}
}

// TestPathPlaceholderMustBeRequired is the "{uid}.ics" bug: a path placeholder
// declared optional can't be substituted, so the tool dies at DISPATCH with
// `url template: missing arg "uid"` — long after authoring, reading like a
// framework fault. Reject it at create instead.
func TestPathPlaceholderMustBeRequired(t *testing.T) {
	withNoAuthCred(t)
	sess := newTestSession()
	_, err := createGrouped(map[string]any{
		"action": "create", "name": "cal_put", "description": "create event",
		"mode": "api",
		"url_template": "https://x.test/cal/{uid}.ics", "method": "PUT",
		"content_type": "text/calendar", "body_template": "BEGIN:VEVENT{summary}END:VEVENT",
		"params": map[string]any{
			"uid":     map[string]any{"type": "string"},
			"summary": map[string]any{"type": "string"},
		},
		"required": []any{"summary"}, // uid deliberately left optional
	}, sess)
	if err == nil {
		t.Fatal("expected create to reject an optional PATH placeholder")
	}
	if !strings.Contains(err.Error(), "uid") || !strings.Contains(err.Error(), "required") {
		t.Fatalf("error should name the param and the fix; got: %v", err)
	}
}

// TestQueryPlaceholderMayBeOptional guards the other direction: an optional
// query placeholder is the documented drop-when-omitted pattern and must
// still be allowed.
func TestQueryPlaceholderMayBeOptional(t *testing.T) {
	withNoAuthCred(t)
	sess := newTestSession()
	if _, err := createGrouped(map[string]any{
		"action": "create", "name": "feed_list", "description": "list feed",
		"mode": "api",
		"url_template": "https://x.test/feed?limit={limit}&since={cursor}", "method": "GET",
		"params": map[string]any{
			"limit":  map[string]any{"type": "integer"},
			"cursor": map[string]any{"type": "string"},
		},
		"required": []any{"limit"},
	}, sess); err != nil {
		t.Fatalf("optional QUERY placeholder must be allowed: %v", err)
	}
}

// TestPathPlaceholderParams unit-covers the split.
func TestPathPlaceholderParams(t *testing.T) {
	got := pathPlaceholderParams("https://x.test/c/{uid}.ics", nil)
	if len(got) != 1 || got[0] != "uid" {
		t.Errorf("path placeholder: %v", got)
	}
	if got := pathPlaceholderParams("https://x.test/c/{uid}.ics", []string{"uid"}); got != nil {
		t.Errorf("required placeholder should not be flagged: %v", got)
	}
	if got := pathPlaceholderParams("https://x.test/f?since={cursor}", nil); got != nil {
		t.Errorf("query placeholder should not be flagged: %v", got)
	}
	// Path placeholder alongside a query one — only the path is flagged.
	got = pathPlaceholderParams("https://x.test/c/{id}/items?after={cursor}", nil)
	if len(got) != 1 || got[0] != "id" {
		t.Errorf("mixed template: %v", got)
	}
}

// TestEmptyResultBody covers the "2xx but nothing came back" detector — the
// shape that let a list tool ship as "verified" while returning no rows.
func TestEmptyResultBody(t *testing.T) {
	empty := []string{
		"",
		"   ",
		"[]",
		"{}",
		"<?xml version='1.0' encoding='UTF-8' ?>\n<multistatus xmlns='DAV:'/>",
		"<D:multistatus xmlns:D=\"DAV:\"></D:multistatus>",
	}
	for _, b := range empty {
		if !emptyResultBody(b, TempToolAction{}) {
			t.Errorf("should read as empty: %q", b)
		}
	}
	nonEmpty := []string{
		`[{"id":1}]`,
		`{"events":[{"uid":"x"}]}`,
		"<multistatus xmlns='DAV:'><response><href>/c/x.ics</href></response></multistatus>",
		"<D:multistatus xmlns:D=\"DAV:\"><D:response><D:href>/c/x.ics</D:href></D:response></D:multistatus>",
	}
	for _, b := range nonEmpty {
		if emptyResultBody(b, TempToolAction{}) {
			t.Errorf("should NOT read as empty: %q", b)
		}
	}
}

// TestEmptyResultBodyJudgesExtractedOutput: when the endpoint declares a
// response_extract, what matters is what the EXTRACTOR yields — a body full of
// XML that extracts to [] is still zero records for the caller.
func TestEmptyResultBodyJudgesExtractedOutput(t *testing.T) {
	ep := TempToolAction{ResponseExtract: &ExtractSpec{
		Select: "response",
		Where:  &ExtractWhere{Has: "calendar-data"},
		Fields: map[string]string{"href": "href"},
	}}
	// Collection-only multistatus (no calendar-data anywhere) → extracts to [].
	body := "<multistatus xmlns='DAV:'><response><href>/c/</href>" +
		"<propstat><prop><resourcetype><collection/></resourcetype></prop></propstat>" +
		"</response></multistatus>"
	if !emptyResultBody(body, ep) {
		t.Error("an extract yielding no rows must read as empty")
	}
}
