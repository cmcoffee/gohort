package temptool

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestUpdatePreservesContentType is the regression guard for the CalDAV
// 148-round failure: an api tool created with content_type=application/xml
// worked, but the FIRST tool_def(action="update") — even one re-passing
// content_type — reverted it to the JSON body path, so the XML body then
// failed dispatch as "invalid character '<'". Two holes caused it:
//   1. tempToolToCreateArgs didn't round-trip ContentType (lost on any edit).
//   2. updateGrouped's overlay whitelist didn't include content_type (a
//      re-passed value was ignored).
// Both are covered here: an edit that omits content_type must preserve it, and
// an edit that changes it must apply the new value.
func TestUpdatePreservesContentType(t *testing.T) {
	secStore := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return secStore }
	defer func() { AuthDB = prev }()
	if err := Secure().Save(SecureCredential{
		Name: "no_auth", Type: SecureCredNone, BaseURL: "https://caldav.example.com",
	}, ""); err != nil {
		t.Fatalf("save credential: %v", err)
	}

	sess := &ToolSession{
		Username: "alice", ChatSessionID: "s1",
		WorkspaceDir: t.TempDir(), DB: &DBase{Store: kvlite.MemStore()},
	}

	create := map[string]any{
		"action":        "create",
		"name":          "caldav_probe",
		"description":   "propfind against caldav",
		"mode":          "api",
		"credential":    "no_auth",
		"url_template":  "https://caldav.example.com/195178399/calendars/",
		"method":        "PROPFIND",
		"content_type":  "application/xml",
		"body_template": `<d:propfind xmlns:d="DAV:"><d:prop><d:displayname/></d:prop></d:propfind>`,
	}
	if _, err := createGrouped(create, sess); err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec, ok := loadExistingToolRecord(sess, "caldav_probe"); !ok {
		t.Fatal("tool missing after create")
	} else if rec.ContentType != "application/xml" {
		t.Fatalf("create dropped content_type; got %q", rec.ContentType)
	}

	// Edit only the description — content_type NOT re-passed. It must survive.
	if _, err := updateGrouped(map[string]any{
		"name":        "caldav_probe",
		"description": "propfind against caldav (edited)",
	}, sess); err != nil {
		t.Fatalf("update (desc only): %v", err)
	}
	rec, ok := loadExistingToolRecord(sess, "caldav_probe")
	if !ok {
		t.Fatal("tool missing after description update")
	}
	if rec.ContentType != "application/xml" {
		t.Fatalf("update dropped content_type when it wasn't re-passed; got %q", rec.ContentType)
	}

	// Edit that DOES re-pass content_type with a new value — must apply.
	if _, err := updateGrouped(map[string]any{
		"name":         "caldav_probe",
		"content_type": "text/xml",
	}, sess); err != nil {
		t.Fatalf("update (content_type change): %v", err)
	}
	rec, _ = loadExistingToolRecord(sess, "caldav_probe")
	if rec.ContentType != "text/xml" {
		t.Fatalf("update ignored a re-passed content_type; got %q", rec.ContentType)
	}
}
