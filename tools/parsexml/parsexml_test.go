package parsexml

import (
	"strings"
	"testing"
)

func TestParseXMLTool(t *testing.T) {
	tool := new(ParseXMLTool)
	xml := `<multistatus xmlns="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
	  <response><href>/cal/home/</href><propstat><prop>
	    <resourcetype><collection/><cal:calendar/></resourcetype><displayname>Home</displayname>
	  </prop></propstat></response>
	  <response><href>/cal/</href><propstat><prop>
	    <resourcetype><collection/></resourcetype><displayname>Root</displayname>
	  </prop></propstat></response>
	</multistatus>`

	out, err := tool.Run(map[string]any{
		"xml": xml,
		"spec": map[string]any{
			"select": "response",
			"where":  map[string]any{"has": "calendar"},
			"fields": map[string]any{"path": "href", "name": "displayname"},
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, `"name":"Home"`) || !strings.Contains(out, `"path":"/cal/home/"`) {
		t.Fatalf("expected extracted calendar JSON; got %s", out)
	}
	if strings.Contains(out, "Root") {
		t.Fatalf("where:{has:calendar} should drop the non-calendar collection; got %s", out)
	}

	// Missing xml / spec are clear errors.
	if _, err := tool.Run(map[string]any{"spec": map[string]any{"fields": map[string]any{"a": "b"}}}); err == nil {
		t.Fatal("missing xml should error")
	}
	if _, err := tool.Run(map[string]any{"xml": "<x/>"}); err == nil {
		t.Fatal("missing spec should error")
	}
}
