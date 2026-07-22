package core

import "testing"

// The exact iCloud CalDAV calendar-list body the model failed to parse 10+ times
// across ElementTree / xmllint / grep. Namespaces are default-scoped (no prefix)
// on multistatus, which is what broke every xpath attempt.
const caldavCalendarsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<multistatus xmlns="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <response>
    <href>/195178399/calendars/</href>
    <propstat><prop>
      <resourcetype><collection/></resourcetype>
      <displayname>Craig Coffee</displayname>
    </prop><status>HTTP/1.1 200 OK</status></propstat>
  </response>
  <response>
    <href>/195178399/calendars/home/</href>
    <propstat><prop>
      <resourcetype><collection/><cal:calendar/></resourcetype>
      <displayname>Home</displayname>
    </prop><status>HTTP/1.1 200 OK</status></propstat>
  </response>
  <response>
    <href>/195178399/calendars/work/</href>
    <propstat><prop>
      <resourcetype><collection/><cal:calendar/></resourcetype>
      <displayname>Work</displayname>
    </prop><status>HTTP/1.1 200 OK</status></propstat>
  </response>
</multistatus>`

func TestExtractXMLCalDAVCalendars(t *testing.T) {
	spec := ExtractSpec{
		Select: "response",
		Where:  &ExtractWhere{Has: "calendar"},
		Fields: map[string]string{"path": "href", "displayname": "displayname"},
	}
	got, err := ExtractXML([]byte(caldavCalendarsXML), spec)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	want := `[{"displayname":"Home","path":"/195178399/calendars/home/"},{"displayname":"Work","path":"/195178399/calendars/work/"}]`
	if string(got) != want {
		t.Fatalf("calendar list mismatch:\n got %s\nwant %s", got, want)
	}
}

// Namespace-agnostic: D:-prefixed and ns0:-prefixed variants must extract
// IDENTICALLY to the default-namespace form — the whole point of the design.
func TestExtractXMLNamespaceAgnostic(t *testing.T) {
	variants := []string{
		`<multistatus xmlns="DAV:"><response><href>/a/</href></response></multistatus>`,
		`<D:multistatus xmlns:D="DAV:"><D:response><D:href>/a/</D:href></D:response></D:multistatus>`,
		`<ns0:multistatus xmlns:ns0="DAV:"><ns0:response><ns0:href>/a/</ns0:href></ns0:response></ns0:multistatus>`,
	}
	spec := ExtractSpec{Select: "response", Fields: map[string]string{"path": "href"}}
	for _, v := range variants {
		got, err := ExtractXML([]byte(v), spec)
		if err != nil {
			t.Fatalf("variant %q: %v", v, err)
		}
		if string(got) != `[{"path":"/a/"}]` {
			t.Fatalf("variant %q gave %s", v, got)
		}
	}
}

func TestExtractXMLEdgeCases(t *testing.T) {
	spec := ExtractSpec{Select: "response", Fields: map[string]string{"path": "href"}}

	// Empty multistatus (no events) → [] not error — the exact case that made
	// the model's script exit 1.
	got, err := ExtractXML([]byte(`<multistatus xmlns="DAV:"/>`), spec)
	if err != nil || string(got) != `[]` {
		t.Fatalf("empty multistatus should give []; got %s, %v", got, err)
	}

	// Malformed XML → error, no partial output.
	if _, err := ExtractXML([]byte(`<multistatus><response>`), ExtractSpec{Fields: map[string]string{"x": "y"}}); err == nil {
		t.Fatal("malformed XML should error")
	}

	// No select → whole doc is one object; attribute + child-path + missing field.
	obj, err := ExtractXML([]byte(`<root id="7"><a><b>deep</b></a></root>`),
		ExtractSpec{Fields: map[string]string{"id": "@id", "deep": "a/b", "gone": "nope"}})
	if err != nil {
		t.Fatalf("single-object extract: %v", err)
	}
	if string(obj) != `{"deep":"deep","id":"7"}` {
		t.Fatalf("single-object mismatch: %s", obj)
	}
}
