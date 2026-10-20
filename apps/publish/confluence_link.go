package publish

// Taking in a Confluence page that already exists (docs.LinkPage), so a
// document started from it updates that page rather than making another.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cmcoffee/gohort/core/docs"
)

var (
	confluencePageIDRE = regexp.MustCompile(`(?:/pages/|[?&]pageId=)(\d+)`)
	allDigitsRE        = regexp.MustCompile(`^\d+$`)
)

// confluencePageID is the page id in a pasted link: the /pages/<id>/ of the
// address a browser shows, a viewpage.action?pageId=<id>, or the bare id.
func confluencePageID(link string) string {
	link = strings.TrimSpace(link)
	if allDigitsRE.MatchString(link) {
		return link
	}
	if m := confluencePageIDRE.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	return ""
}

// LinkPage reads the page the link names and records it as this
// destination's page: its id, the space it is in (the target an update is
// checked against), its version and its address.
func (d *confluenceDest) LinkPage(ctx context.Context, user, target, link string) (docs.LinkedPage, error) {
	id := confluencePageID(link)
	if id == "" {
		return docs.LinkedPage{}, fmt.Errorf("that link does not name a Confluence page: open the page and copy its address from the browser, the one with /pages/<number>/ in it")
	}
	cfg := d.app.config()
	docs.PublishStep(ctx, "Reading Confluence page %s", id)
	body, err := callAPI(user, cfg.ConfluenceCredential, "GET", d.apiURL(user, "/pages/"+id+"?body-format=storage"), "")
	if err != nil {
		return docs.LinkedPage{}, err
	}
	var page struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		SpaceID string `json:"spaceId"`
		Version struct {
			Number int `json:"number"`
		} `json:"version"`
		Body struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
		Links struct {
			Base  string `json:"base"`
			WebUI string `json:"webui"`
		} `json:"_links"`
	}
	if err := json.Unmarshal(body, &page); err != nil || strings.TrimSpace(page.ID) == "" {
		return docs.LinkedPage{}, fmt.Errorf("Confluence answered, but not with a page")
	}
	url := strings.TrimSpace(link)
	if page.Links.WebUI != "" {
		base := strings.TrimRight(page.Links.Base, "/")
		if base == "" {
			base = d.siteURL(user) + "/wiki"
		}
		url = base + page.Links.WebUI
	}
	rec := docs.PublishRecord{Kind: ConfluenceKind, Target: page.SpaceID, Title: page.Title,
		ExternalID: page.ID, URL: url, Version: page.Version.Number}
	// The space by name, for the Publish dialog's row. Best effort: the id is
	// what an update needs.
	if spaces, err := d.Targets(ctx, user); err == nil {
		for _, s := range spaces {
			if s.ID == page.SpaceID {
				rec.TargetTitle = s.Title
			}
		}
	}
	storage := page.Body.Storage.Value
	docs.PublishStep(ctx, "Read %q (version %d, %d characters)", page.Title, page.Version.Number, len(storage))
	return docs.LinkedPage{
		Record:   rec,
		Copy:     docs.PublishedCopy{Body: storage, Version: strconv.Itoa(page.Version.Number)},
		Title:    page.Title,
		Markdown: confluenceStorageMarkdown(storage),
	}, nil
}

var (
	cfCodeMacroRE  = regexp.MustCompile(`(?s)<ac:structured-macro[^>]*ac:name="code"[^>]*>.*?<ac:plain-text-body><!\[CDATA\[(.*?)\]\]></ac:plain-text-body>.*?</ac:structured-macro>`)
	cfPanelMacroRE = regexp.MustCompile(`(?s)<ac:structured-macro[^>]*ac:name="(?:info|note|warning|tip|panel)"[^>]*>.*?<ac:rich-text-body>(.*?)</ac:rich-text-body>.*?</ac:structured-macro>`)
	cfTocMacroRE   = regexp.MustCompile(`(?s)<ac:structured-macro[^>]*ac:name="toc"[^>]*(?:/>|>.*?</ac:structured-macro>)`)
	cfPageLinkRE   = regexp.MustCompile(`(?s)<ac:link[^>]*>.*?<ri:page[^>]*ri:content-title="([^"]*)"[^>]*/>.*?</ac:link>`)
	cfTagRE        = regexp.MustCompile(`</?(?:ac|ri):[^>]*>`)
)

// confluenceStorageMarkdown is a page's storage format as Markdown. Code
// blocks, panels (as quotes) and links to other pages (as their titles) are
// kept; a table of contents is dropped, since the guide makes its own; any
// other macro keeps only its words.
func confluenceStorageMarkdown(s string) string {
	// Code goes around the HTML conversion, not through it: a command's own
	// <angle brackets> and quotes are text, which the conversion would read
	// as markup and drop. Each block is held under a marker and put back.
	var codes []string
	s = cfCodeMacroRE.ReplaceAllStringFunc(s, func(m string) string {
		codes = append(codes, cfCodeMacroRE.FindStringSubmatch(m)[1])
		return "<p>" + cfCodeMarker + strconv.Itoa(len(codes)-1) + "X</p>"
	})
	s = cfPanelMacroRE.ReplaceAllString(s, "<blockquote>$1</blockquote>")
	s = cfTocMacroRE.ReplaceAllString(s, "")
	s = cfPageLinkRE.ReplaceAllString(s, "$1")
	s = cfTagRE.ReplaceAllString(s, "")
	md := docs.CopyMarkdown(s)
	for i, code := range codes {
		md = strings.Replace(md, cfCodeMarker+strconv.Itoa(i)+"X", "\n```\n"+strings.Trim(code, "\n")+"\n```\n", 1)
	}
	return strings.TrimSpace(md)
}

// cfCodeMarker holds a code block's place: plain letters, which no
// conversion touches.
const cfCodeMarker = "GOHORTCODEBLOCK"
