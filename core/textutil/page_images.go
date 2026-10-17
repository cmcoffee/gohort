package textutil

// The images on a web page, for a tool that returns the page as text.
//
// fetch_url and browse_page extract a page's readable text and drop its
// images, so an answer that is IN an image (a chart, a photo of a board, a
// table published as a picture) was unreachable: the agent could not see it,
// and could not even find its address to fetch it. Listing them at the end of
// the text, with their alt text, lets the agent fetch the one that matters and
// look at it, at full resolution.

import (
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// PageImage is one image a page shows: its absolute address and what its alt
// text says it is.
type PageImage struct {
	URL string
	Alt string
}

// pageImageMinSide is the smallest declared width or height an image may have
// and still be content rather than an icon, a spacer or a tracking pixel.
const pageImageMinSide = 48

// pageImageChrome are words in an image's address or alt that mark it as page
// furniture rather than content.
var pageImageChrome = []string{"logo", "icon", "sprite", "spacer", "pixel", "avatar", "badge", "emoji", "favicon", "tracking", "blank.gif"}

// PageImages lists the content images in htmlStr, in page order, resolved
// against base, at most max of them (0 is no limit). Data URIs, SVGs, images
// declared smaller than an icon, and furniture (logos, icons, avatars,
// tracking pixels) are left out, and an address appears once. A lazy-loaded
// image is found by its data-src or the first srcset entry.
func PageImages(htmlStr string, base *url.URL, max int) []PageImage {
	z := html.NewTokenizer(strings.NewReader(htmlStr))
	seen := map[string]bool{}
	var out []PageImage
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return out
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		if tok.Data != "img" {
			continue
		}
		attrs := map[string]string{}
		for _, a := range tok.Attr {
			attrs[strings.ToLower(a.Key)] = strings.TrimSpace(a.Val)
		}
		src := ""
		for _, k := range []string{"src", "data-src", "data-lazy-src", "data-original"} {
			if v := attrs[k]; v != "" && !strings.HasPrefix(strings.ToLower(v), "data:") {
				src = v
				break
			}
		}
		if src == "" {
			if set := attrs["srcset"]; set != "" {
				src = strings.Fields(strings.Split(set, ",")[0] + " ")[0]
			}
		}
		if src == "" || strings.HasPrefix(strings.ToLower(src), "data:") {
			continue
		}
		if tooSmall(attrs["width"]) || tooSmall(attrs["height"]) {
			continue
		}
		u, err := url.Parse(src)
		if err != nil {
			continue
		}
		if base != nil {
			u = base.ResolveReference(u)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			continue
		}
		// Tracking parameters change nothing about the image and cost the
		// reader a line's worth of tokens each.
		if q := u.Query(); len(q) > 0 {
			for k := range q {
				if strings.HasPrefix(strings.ToLower(k), "utm_") {
					q.Del(k)
				}
			}
			u.RawQuery = q.Encode()
		}
		abs := u.String()
		low := strings.ToLower(u.Path)
		if strings.HasSuffix(low, ".svg") || seen[abs] {
			continue
		}
		alt := strings.Join(strings.Fields(attrs["alt"]), " ")
		if isPageChrome(strings.ToLower(abs + " " + alt)) {
			continue
		}
		if r := []rune(alt); len(r) > 120 {
			alt = string(r[:120]) + "..."
		}
		seen[abs] = true
		out = append(out, PageImage{URL: abs, Alt: alt})
		if max > 0 && len(out) >= max {
			return out
		}
	}
}

func tooSmall(v string) bool {
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(v), "px"))
	return err == nil && n > 0 && n < pageImageMinSide
}

func isPageChrome(s string) bool {
	for _, w := range pageImageChrome {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// PageImagesNote renders the list for the end of a page's text, or "" when
// there is none or no way to look at one. look is how the reader fetches an
// image to see it (`image(action="fetch", url=...)`), from the tools it
// actually has: a list of addresses it cannot open is tokens spent on a door
// that is not there, so with no look there is no list.
func PageImagesNote(imgs []PageImage, look string) string {
	if len(imgs) == 0 || strings.TrimSpace(look) == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nImages on the page (the text above does not include them; if the answer may be IN one, fetch it with " + look + " to see it):\n")
	for _, im := range imgs {
		alt := im.Alt
		if alt == "" {
			alt = "(no description)"
		}
		b.WriteString("- " + alt + ": " + im.URL + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ImageLookHint is how a reader with these tools fetches an image to see it,
// for PageImagesNote, or "" when it has none that would. has reports whether a
// tool resolved for the reader (ToolSession.HasTool).
func ImageLookHint(has func(string) bool) string {
	switch {
	case has == nil:
		return ""
	case has("image"):
		return `image(action="fetch", url=...)`
	case has("fetch_image"):
		return `fetch_image(url=...)`
	}
	return ""
}
