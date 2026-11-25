package customapps

// An app's own scripts and stylesheets, loaded by its page as files.
//
// An app's page runs in a sandboxed frame with no origin (isolateAppHTML), so
// a request the frame makes by itself carries no session: the runtime's shim
// relays its fetches, XHRs, images and sounds through the page around it,
// which does. A <script src> or <link rel="stylesheet"> is loaded by the
// browser while it parses, before any shim can step in, and came back as the
// sign-in page's redirect, which the browser refused (measured: a classic
// script and a stylesheet blocked by ORB, a module script by CORS). So they
// used to be put into the page as it was served, which re-sent every one of
// them on every open and could not load a code-split build.
//
// Now they are served at a path that needs no session:
//
//	/apps/_files/<app>/<version>/<name>
//
// <app> names whose app it is (owner and slug), and <version> is a hash of it
// and of every script and stylesheet the app has, so it changes whenever any
// of them changes and cannot be guessed without having them. A file is served
// only under its app's current version, so the browser keeps it for a year
// (immutable) and a changed app is a new folder. A module's relative import
// resolves inside the same folder, so a build split into chunks loads.
//
// What this lends: the app's scripts and stylesheets, to anyone holding the
// link, which is what everyone who opens the app already receives. Nothing a
// person stored (records, settings, their view, uploaded files) is reachable
// here; those still go through the shim and the session.
// (docs/design/apps-feel-native.md, item 3.)

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// appFilesPath is where an app's scripts and stylesheets are served, under
// the app host's own path. An app slugged _files would be shadowed by it, as
// one slugged _app or _admin is by theirs.
const appFilesPath = "/_files/"

// appFileTypes are the kinds of asset served there, with their types.
var appFileTypes = map[string]string{
	".js":  "text/javascript; charset=utf-8",
	".mjs": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8",
}

var (
	scriptSrcTag = regexp.MustCompile(`(?is)<script\b([^>]*?)\bsrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))([^>]*)>\s*</script\s*>`)
	linkTag      = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	tagAttr      = regexp.MustCompile(`(?is)([a-z][a-z0-9-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	hrefAttr     = regexp.MustCompile(`(?is)\bhref\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)`)
)

// ownAssetName is the asset a page reference names, when it is one of the
// app's own (assets/<name>, ./assets/<name>) of a type served as a file.
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

// appFilesID names owner's app slug in a files path.
func appFilesID(owner, slug string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(owner + "\x00" + slug))
}

// parseAppFilesID is appFilesID read back.
func parseAppFilesID(id string) (owner, slug string, ok bool) {
	b, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", "", false
	}
	owner, slug, ok = strings.Cut(string(b), "\x00")
	return owner, slug, ok && owner != "" && slug != ""
}

// appFilesVersion is the current version of owner's app slug's scripts and
// stylesheets, and their names. ok is false when it has none.
func appFilesVersion(owner, slug string) (version string, names map[string]bool, ok bool) {
	list, err := ListAppAssets(owner, slug)
	if err != nil {
		return "", nil, false
	}
	sort.Strings(list)
	h := sha256.New()
	h.Write([]byte(owner + "\x00" + slug + "\x00"))
	names = map[string]bool{}
	for _, name := range list {
		if _, served := appFileTypes[strings.ToLower(path.Ext(name))]; !served || !ValidAppAssetName(name) {
			continue
		}
		data, _, err := ReadAppAsset(owner, slug, name)
		if err != nil {
			continue
		}
		h.Write([]byte(name + "\x00" + strconv.Itoa(len(data)) + "\x00"))
		h.Write(data)
		names[name] = true
	}
	if len(names) == 0 {
		return "", nil, false
	}
	return hex.EncodeToString(h.Sum(nil))[:32], names, true
}

// linkAppFiles points each isolated card and frame of page at owner's app
// slug's scripts and stylesheets as files. A page with none, or an app with
// none, is served as it was.
func (T *CustomApps) linkAppFiles(page json.RawMessage, owner, slug string) json.RawMessage {
	if len(page) == 0 || !strings.Contains(string(page), "assets/") {
		return page
	}
	version, names, ok := appFilesVersion(owner, slug)
	if !ok {
		return page
	}
	base := T.WebPath() + appFilesPath + appFilesID(owner, slug) + "/" + version + "/"
	return linkAssetsWith(page, base, func(name string) bool { return names[name] })
}

// linkAssetsWith is linkAppFiles with the folder and the app's files given.
func linkAssetsWith(page json.RawMessage, base string, have func(name string) bool) json.RawMessage {
	if len(page) == 0 || !strings.Contains(string(page), "assets/") {
		return page
	}
	var v any
	if json.Unmarshal(page, &v) != nil {
		return page
	}
	changed := false
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			if typ, _ := t["type"].(string); typ == "card" || typ == "frame" {
				if h, ok := t["html"].(string); ok && strings.Contains(h, "assets/") {
					if out := linkHTMLAssets(h, base, have); out != h {
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

// linkHTMLAssets is html with each of the app's own scripts and stylesheets
// referenced at its file, every other attribute (type="module", defer, media)
// as written. A reference to a missing asset is left as written.
func linkHTMLAssets(html, base string, have func(string) bool) string {
	html = scriptSrcTag.ReplaceAllStringFunc(html, func(tag string) string {
		m := scriptSrcTag.FindStringSubmatch(tag)
		name, ok := ownAssetName(m[2]+m[3]+m[4], ".js", ".mjs")
		if !ok || !have(name) {
			return tag
		}
		return "<script" + m[1] + `src="` + attrEscape(base+name) + `"` + m[5] + "></script>"
	})
	return linkTag.ReplaceAllStringFunc(html, func(tag string) string {
		attrs := map[string]string{}
		for _, a := range tagAttr.FindAllStringSubmatch(tag, -1) {
			attrs[strings.ToLower(a[1])] = a[2] + a[3] + a[4]
		}
		if !strings.Contains(strings.ToLower(attrs["rel"]), "stylesheet") {
			return tag
		}
		name, ok := ownAssetName(attrs["href"], ".css")
		if !ok || !have(name) {
			return tag
		}
		return hrefAttr.ReplaceAllLiteralString(tag, `href="`+attrEscape(base+name)+`"`)
	})
}

// handleAppFiles serves one of an app's scripts or stylesheets, without a
// session: GET /_files/<app>/<version>/<name>. Anything else, an app that is
// gone or switched off, or a version that is not the app's current one, is a
// 404.
func (T *CustomApps) handleAppFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := r.URL.Path
	if i := strings.Index(rest, appFilesPath); i >= 0 {
		rest = rest[i+len(appFilesPath):]
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	owner, slug, ok := parseAppFilesID(parts[0])
	name := parts[2]
	ctype, served := appFileTypes[strings.ToLower(path.Ext(name))]
	if !ok || !served || !ValidAppAssetName(name) {
		http.NotFound(w, r)
		return
	}
	if spec, ok := loadSpec(owner, slug); !ok || spec.Disabled {
		http.NotFound(w, r)
		return
	}
	version, names, ok := appFilesVersion(owner, slug)
	if !ok || !names[name] || subtle.ConstantTimeCompare([]byte(version), []byte(parts[1])) != 1 {
		http.NotFound(w, r)
		return
	}
	data, _, err := ReadAppAsset(owner, slug, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	// A module script is fetched with CORS from the frame's null origin.
	h.Set("Access-Control-Allow-Origin", "*")
	// Opened as a page of its own, it is a document with no origin and no
	// reach. As a script or stylesheet this does not apply.
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(data)
}

// attrEscape is s fit for a double-quoted attribute.
func attrEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(s)
}
