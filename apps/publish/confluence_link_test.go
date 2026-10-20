package publish

import (
	"strings"
	"testing"
)

// The page id is read out of the address a browser shows, the old viewpage
// form, or a bare id; anything else names no page.
func TestAConfluenceLinkNamesItsPage(t *testing.T) {
	for link, want := range map[string]string{
		"https://example.atlassian.net/wiki/spaces/OPS/pages/123456/Runbook":  "123456",
		"https://example.atlassian.net/wiki/pages/viewpage.action?pageId=987": "987",
		"  4242 ": "4242",
		"https://example.atlassian.net/wiki/x/AbCd":              "",
		"https://example.atlassian.net/wiki/spaces/OPS/overview": "",
	} {
		if got := confluencePageID(link); got != want {
			t.Errorf("%q read as %q, want %q", link, got, want)
		}
	}
}

// A page's storage format comes in as Markdown a guide can hold: code blocks
// stay code, panels become quotes, links to pages keep their titles, and the
// table of contents is dropped because the guide makes its own.
func TestConfluenceStorageComesInAsMarkdown(t *testing.T) {
	storage := `<h2>Restart</h2><p>Run it as <strong>root</strong>.</p>` +
		`<ac:structured-macro ac:name="toc" ac:schema-version="1"/>` +
		`<ac:structured-macro ac:name="code"><ac:parameter ac:name="language">bash</ac:parameter><ac:plain-text-body><![CDATA[systemctl restart app && echo "<ok>"]]></ac:plain-text-body></ac:structured-macro>` +
		`<ac:structured-macro ac:name="warning"><ac:rich-text-body><p>Never on Fridays.</p></ac:rich-text-body></ac:structured-macro>` +
		`<p>See <ac:link><ri:page ri:content-title="Logs"/></ac:link> next.</p>`
	md := confluenceStorageMarkdown(storage)
	for _, want := range []string{"## Restart", "**root**", "```", `systemctl restart app && echo "<ok>"`, "> Never on Fridays.", "See Logs next."} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	if strings.Contains(md, "ac:") || strings.Contains(md, "ri:") || strings.Contains(md, "toc") {
		t.Errorf("Confluence markup survived:\n%s", md)
	}
}
