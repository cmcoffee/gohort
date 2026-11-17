package customapps

// An app's own scripts and stylesheets, put into its page as it is served.
//
// An app's page runs in a sandboxed frame with no origin (isolateAppHTML), so
// a request the frame makes by itself carries no session: the runtime's shim
// relays its fetches, XHRs, images and sounds through the page around it,
// which does. A <script src> or <link rel="stylesheet"> is loaded by the
// browser while it parses, before any shim can step in, and came back as the
// sign-in page's redirect, which the browser refused (measured: a classic
// script and a stylesheet blocked by ORB, a module script by CORS). So the
// page's references to its own .js, .mjs and .css assets are replaced, at
// serve time, by the files' text: a built front end shipped as assets runs as
// if it had been pasted into page.html, which is the only way it could run
// before.
//
// A module that imports another of the app's files (a code-split chunk) still
// cannot load it: the build has to be one file per script, which the folder
// guide says.

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxInlinedAssetBytes caps what one page takes in, across its assets.
const maxInlinedAssetBytes = 24 << 20

var (
	scriptSrcTag = regexp.MustCompile(`(?is)<script\b([^>]*?)\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))([^>]*)>\s*</script\s*>`)
	linkTag      = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	tagAttr      = regexp.MustCompile(`(?is)([a-z][a-z0-9-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	deferAttr    = regexp.MustCompile(`(?i)\bdefer\b`)
	closeScript  = regexp.MustCompile(`(?i)</script`)
	closeStyle   = regexp.MustCompile(`(?i)</style`)
	bodyEnd      = regexp.MustCompile(`(?i)</body\s*>`)
)

// ownAssetName is the asset a page reference names, when it is one of the
// app's own (assets/<name>, ./assets/<name>) of a type that is inlined.
func ownAssetName(ref string, exts ...string) (string, bool) {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "./")
	if !strings.HasPrefix(ref, "assets/") {
		return "", false
	}
	name := strings.TrimPrefix(ref, "assets/")
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	if !ValidAppAssetName(name) {
		return "", false
	}
	ext := strings.ToLower(path.Ext(name))
	for _, e := range exts {
		if ext == e {
			return name, true
		}
	}
	return "", false
}

// inlineAppAssets rewrites each isolated card and frame of page so its own
// script and stylesheet assets are in the HTML.
func inlineAppAssets(page json.RawMessage, owner, slug string) json.RawMessage {
	read := func(name string) (string, bool) {
		data, _, err := ReadAppAsset(owner, slug, name)
		if err != nil {
			return "", false
		}
		return string(data), true
	}
	return inlineAssetsWith(page, read)
}

// inlineAssetsWith is inlineAppAssets with the asset reader given.
func inlineAssetsWith(page json.RawMessage, read func(name string) (string, bool)) json.RawMessage {
	if len(page) == 0 || !strings.Contains(string(page), "assets/") {
		return page
	}
	var v any
	if json.Unmarshal(page, &v) != nil {
		return page
	}
	budget := maxInlinedAssetBytes
	changed := false
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			if typ, _ := t["type"].(string); typ == "card" || typ == "frame" {
				if h, ok := t["html"].(string); ok && strings.Contains(h, "assets/") {
					if out := inlineHTMLAssets(h, read, &budget); out != h {
						t["html"], changed = out, true
					}
				}
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(v)
	if !changed {
		return page
	}
	out, err := json.Marshal(v)
	if err != nil {
		return page
	}
	return out
}

// inlineHTMLAssets is html with its own scripts and stylesheets in it. A
// deferred classic script goes to the end of the body, where it still runs
// after the document is parsed; a module is deferred by itself. A reference
// to a missing asset, or one past the budget, is left as written.
func inlineHTMLAssets(html string, read func(string) (string, bool), budget *int) string {
	var tail []string
	html = scriptSrcTag.ReplaceAllStringFunc(html, func(tag string) string {
		m := scriptSrcTag.FindStringSubmatch(tag)
		ref := m[2] + m[3] + m[4]
		name, ok := ownAssetName(ref, ".js", ".mjs")
		if !ok {
			return tag
		}
		src, ok := read(name)
		if !ok || len(src) > *budget {
			return tag
		}
		*budget -= len(src)
		attrs := strings.TrimSpace(m[1] + " " + m[5])
		isModule := strings.Contains(strings.ToLower(attrs), "module")
		deferred := !isModule && deferAttr.MatchString(attrs)
		attrs = strings.TrimSpace(deferAttr.ReplaceAllString(attrs, ""))
		if attrs != "" {
			attrs = " " + attrs
		}
		inlined := "<script" + attrs + " data-asset=\"" + attrEscape(name) + "\">" + closeScript.ReplaceAllString(src, `<\/script`) + "</script>"
		if deferred {
			tail = append(tail, inlined)
			return ""
		}
		return inlined
	})
	html = linkTag.ReplaceAllStringFunc(html, func(tag string) string {
		attrs := map[string]string{}
		for _, a := range tagAttr.FindAllStringSubmatch(tag, -1) {
			attrs[strings.ToLower(a[1])] = a[2] + a[3] + a[4]
		}
		if !strings.Contains(strings.ToLower(attrs["rel"]), "stylesheet") {
			return tag
		}
		name, ok := ownAssetName(attrs["href"], ".css")
		if !ok {
			return tag
		}
		css, ok := read(name)
		if !ok || len(css) > *budget {
			return tag
		}
		*budget -= len(css)
		media := ""
		if m := strings.TrimSpace(attrs["media"]); m != "" {
			media = ` media="` + attrEscape(m) + `"`
		}
		return "<style data-asset=\"" + attrEscape(name) + "\"" + media + ">" + closeStyle.ReplaceAllString(css, `<\/style`) + "</style>"
	})
	if len(tail) > 0 {
		end := strings.Join(tail, "")
		if loc := bodyEnd.FindStringIndex(html); loc != nil {
			html = html[:loc[0]] + end + html[loc[0]:]
		} else {
			html += end
		}
	}
	return html
}

// attrEscape is s fit for a double-quoted attribute.
func attrEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(s)
}
