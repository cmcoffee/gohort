// What Scribe brought in from TechWriter that every guide now uses: the
// header-image generator and the importer that turns an exported page back
// into a guide.
package scribe

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// generateHeaderImage produces a banner for a guide from its title through
// the deployment's image generator and returns something a browser can show:
// the generator's remote URL, or a data URL when it wrote a local file. The
// prompt asks for no rendered text — image models still misspell typography —
// so the title is a subject descriptor; the page shows the title itself.
func generateHeaderImage(ctx context.Context, title string) (string, error) {
	prompt := `A wide banner-style header image evoking the topic: "` + strings.TrimSpace(title) + `". ` +
		"Visual style: clean, professional, editorial / magazine quality, suitable as a top-of-article banner. " +
		"Wide aspect ratio (around 3:1). Do NOT render any text, words, letters, captions, or typography in the image: purely visual."
	result, err := GenerateImageLandscape(ctx, "", prompt)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(result.URL, "http://") || strings.HasPrefix(result.URL, "https://") {
		return result.URL, nil
	}
	data, err := os.ReadFile(result.URL)
	os.Remove(result.URL)
	if err != nil {
		return "", fmt.Errorf("could not read the generated image: %w", err)
	}
	mime := "image/png"
	if strings.HasSuffix(result.URL, ".jpg") || strings.HasSuffix(result.URL, ".jpeg") {
		mime = "image/jpeg"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

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
