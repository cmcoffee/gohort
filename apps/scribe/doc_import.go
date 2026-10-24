// What Scribe brought in from TechWriter that every guide now uses: the
// importer that turns an exported page back into a guide. The header-image
// generator it also brought is header_image.go.
package scribe

import (
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

var (
	htmlTitleRe = regexp.MustCompile(`(?is)<title>([^<]+)</title>`)
	htmlH1Re    = regexp.MustCompile(`(?is)<h1[^>]*>([^<]+)</h1>`)
	htmlMetaRe  = regexp.MustCompile(`(?is)<div class="meta">.*?</div>`)
	htmlHeadRe  = regexp.MustCompile(`(?is)<header class="guide-doc-head">.*?</header>`)
	htmlBrandRe = regexp.MustCompile(`(?is)<div class="guide-brand">.*?</div>`)
	htmlFootRe  = regexp.MustCompile(`(?is)<footer class="guide-foot">.*?</footer>`)
)

// docFromHTML recovers a title and a markdown body from an exported page — one
// of Scribe's own, or TechWriter's — so it can be brought back in as a guide.
// The title comes from <title> (then <h1>); the body is what lies between the
// <body> tags with the title block, meta line, brand and footer stripped, then
// converted back to markdown.
func docFromHTML(html string) (title, body string) {
	if m := htmlTitleRe.FindStringSubmatch(html); m != nil {
		title = strings.TrimSpace(HTMLUnescape(m[1]))
	} else if m := htmlH1Re.FindStringSubmatch(html); m != nil {
		title = strings.TrimSpace(HTMLUnescape(m[1]))
	}
	body = html
	if i := strings.Index(strings.ToLower(body), "<body"); i >= 0 {
		if end := strings.Index(body[i:], ">"); end >= 0 {
			body = body[i+end+1:]
		}
	}
	if i := strings.Index(strings.ToLower(body), "</body>"); i >= 0 {
		body = body[:i]
	}
	body = htmlHeadRe.ReplaceAllString(body, "")
	body = htmlH1Re.ReplaceAllString(body, "")
	body = htmlMetaRe.ReplaceAllString(body, "")
	body = htmlBrandRe.ReplaceAllString(body, "")
	body = htmlFootRe.ReplaceAllString(body, "")
	body = strings.TrimSpace(HTMLToMarkdown(body))
	return title, body
}
