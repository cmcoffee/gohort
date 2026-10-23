package docs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const bodyTestDoc = "# Runbook\n\n## Contents\n\n- [Restart](#restart)\n- [Logs](#logs)\n\nIntro words \"quoted\" <b>here</b>.\n\n## Restart\n\nSee [the logs](#logs).\n\n## Logs\n\nTail them.\n"

func bodyTestReq() PublishRequest {
	return PublishRequest{Title: "Runbook", Doc: PublishDoc{Title: "Runbook", Markdown: bodyTestDoc}, Nav: AnalyzeNav(bodyTestDoc)}
}

// The model is told about the document, not given it, and the instruction
// stays small whatever the document's size.
func TestTheInstructionCarriesTheOutlineNotTheDocument(t *testing.T) {
	req := bodyTestReq()
	req.Doc.Markdown += strings.Repeat("A long paragraph of the guide. ", 5000)
	got := ReferenceInstruction(req)
	if strings.Count(got, "A long paragraph") > 20 || len(got) > 4000 {
		t.Errorf("the instruction carries the document (%d chars)", len(got))
	}
	for _, want := range []string{BodyMarkdownPlaceholder, BodyHTMLPlaceholder, "- Restart", "set_document_links", `"#logs"`[1:6]} {
		if !strings.Contains(got, want) {
			t.Errorf("the instruction is missing %q:\n%s", want, got)
		}
	}
}

// A placeholder is replaced with the document as the call goes out: in a
// plain value, and inside a body written as JSON text, escaped as JSON needs.
func TestThePlaceholderCarriesTheDocument(t *testing.T) {
	ctx := WithPublishBody(context.Background(), bodyTestReq())
	b := PublishBodyFrom(ctx)

	out, hit := b.ExpandArgs(map[string]any{"title": "Runbook", "content": BodyMarkdownPlaceholder})
	if !hit || out["content"] != bodyTestDoc || out["title"] != "Runbook" {
		t.Errorf("a plain value: %v %v", hit, out)
	}

	raw := `{"title":"Runbook","body":{"storage":{"value":"<p>top</p>` + BodyHTMLPlaceholder + `","representation":"storage"}}}`
	out, hit = b.ExpandArgs(map[string]any{"method": "PUT", "body": raw})
	if !hit {
		t.Fatal("a JSON body was not expanded")
	}
	var sent struct {
		Body struct {
			Storage struct{ Value string } `json:"storage"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(out["body"].(string)), &sent); err != nil {
		t.Fatalf("the expanded body is not JSON any more: %v\n%s", err, out["body"])
	}
	if !strings.HasPrefix(sent.Body.Storage.Value, "<p>top</p>") || !strings.Contains(sent.Body.Storage.Value, "Tail them.") || !strings.Contains(sent.Body.Storage.Value, "<h2") {
		t.Errorf("the HTML went out as %q", sent.Body.Storage.Value)
	}

	if _, hit := b.ExpandArgs(map[string]any{"q": "no placeholder"}); hit {
		t.Error("a call without the placeholder counted as carrying the document")
	}
	if b.Expanded() != 2 {
		t.Errorf("counted %d calls carrying it, want 2", b.Expanded())
	}
}

// The model names the destination's anchors and the framework rewrites the
// links, and the contents list can give way to the destination's own.
func TestLinksAreRewrittenAsTheDocumentGoesOut(t *testing.T) {
	b := PublishBodyFrom(WithPublishBody(context.Background(), bodyTestReq()))
	toc := `<ac:structured-macro ac:name="toc"/>`
	b.SetLinks(ParseLinkLines("#restart -> #Runbook-Restart\n#logs ->"), &toc)
	md := b.Markdown()
	if !strings.Contains(md, "See the logs.") || strings.Contains(md, "(#logs)") {
		t.Errorf("a link to keep as words was not: %s", md)
	}
	if !strings.Contains(md, toc) || strings.Contains(md, "- [Restart]") {
		t.Errorf("the contents list was not replaced: %s", md)
	}
	html := b.HTML()
	if !strings.Contains(html, toc) || strings.Contains(html, contentsMarker) || strings.Contains(html, "&lt;ac:") {
		t.Errorf("the HTML's contents: %s", html)
	}
}

// What the model sees of a long reply keeps its shape and short fields, which
// an update needs (the version), and says where the whole of it is.
func TestALongReplyIsClippedForTheModel(t *testing.T) {
	long := strings.Repeat("page text ", 2000)
	out := "HTTP 200 OK\n" + `{"id":"123","version":{"number":7},"body":{"storage":{"value":"` + long + `"}}}`
	got := ClipForModel(out, 3, 4000)
	if !strings.HasPrefix(got, "HTTP 200 OK\n") || !strings.Contains(got, `"number":7`) || !strings.Contains(got, `"id":"123"`) {
		t.Errorf("the short fields are gone: %.300s", got)
	}
	if len(got) > 3000 || !strings.Contains(got, "kept whole as result 3") {
		t.Errorf("not clipped (%d chars): %.300s", len(got), got)
	}
	if ClipForModel("short", 1, 4000) != "short" {
		t.Error("a short reply was changed")
	}
}

// What a destination named for the document's navigation last time is where
// an update starts: its table-of-contents markup goes in place of the
// document's own list without the run naming it again, and is reported back
// for the next update. An update that named nothing used to send the plain
// list and leave the destination's table of contents behind.
func TestAnUpdateStartsFromLastTimesNavigation(t *testing.T) {
	md := "# Guide\n\n## Contents\n\n1. Setup\n   - 1.1 Install\n2. Use\n\n## 1. Setup\n\nSee [Use](#2-use).\n\n## 2. Use\n\nGo."
	macro := "<toc-block/>"
	req := PublishRequest{Doc: PublishDoc{Title: "Guide", Markdown: md}, Nav: AnalyzeNav(md),
		NavContents: &macro, NavLinks: map[string]string{"2-use": "#Use"}}
	ctx := WithPublishBody(context.Background(), req)
	b := PublishBodyFrom(ctx)
	out := b.Markdown()
	if !strings.Contains(out, macro) || strings.Contains(out, "1. Setup\n   - 1.1 Install") {
		t.Errorf("the saved markup replaces the list, nested entries and all:\n%s", out)
	}
	if !strings.Contains(out, "[Use](#Use)") {
		t.Errorf("the saved links are used:\n%s", out)
	}
	links, contents := b.Navigation()
	if contents == nil || *contents != macro || links["2-use"] != "#Use" {
		t.Errorf("reported back for the next update: %v %v", links, contents)
	}
	// Named again, the new markup wins.
	again := "<toc-block depth=2/>"
	b.SetLinks(nil, &again)
	if _, c := b.Navigation(); c == nil || *c != again {
		t.Error("a run that names it again changes it")
	}
	// Nothing saved, nothing named: the list goes out as written, and nothing
	// is reported.
	plain := PublishBodyFrom(WithPublishBody(context.Background(), PublishRequest{Doc: PublishDoc{Markdown: md}, Nav: AnalyzeNav(md)}))
	if l, c := plain.Navigation(); l != nil || c != nil || !strings.Contains(plain.Markdown(), "1. Setup") {
		t.Error("with nothing saved the document goes out as written")
	}
}
