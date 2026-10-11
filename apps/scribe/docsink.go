package scribe

// Scribe as a place to save a finished document: the artifact pane's "Save to
// Scribe". The page arrives as HTML and becomes a document through the same
// import an exported HTML file takes (docFromHTML), so a saved artifact and a
// re-imported export land the same way: text, headings, tables and pictures,
// split into sections at its ## headings. Anything a script drew (a chart on a
// canvas) has no text to keep; Print and Download carry those.

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docsink"
)

// scriptStyleRe matches a script or style element, contents included.
var scriptStyleRe = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</(script|style)\s*>`)

func (T *Scribe) registerDocSink() {
	docsink.Register(docsink.Sink{
		Name:  "scribe",
		Label: "Save to Scribe",
		Available: func(r *http.Request) bool {
			return AppEnabledHere(T.WebPath()) && UserHasAppAccess(r, T.WebPath())
		},
		Save: func(user, title, html string) (string, string, error) {
			return T.saveFromHTML(user, title, html)
		},
	})
}

// saveFromHTML makes a Private document of user's from a page. title, when
// given, wins over the page's own <title> or <h1>.
func (T *Scribe) saveFromHTML(user, title, html string) (url, stored string, err error) {
	udb := UserDB(T.DB, user)
	if udb == nil {
		return "", "", fmt.Errorf("no store for this user")
	}
	// An artifact's scripts and styles are its machinery, not its text: left
	// in, the converter keeps their source as paragraphs.
	pageTitle, body := docFromHTML(scriptStyleRe.ReplaceAllString(html, ""))
	if strings.TrimSpace(body) == "" {
		return "", "", fmt.Errorf("this page has no text a document can keep (it is drawn by script); use Print or Download instead")
	}
	g := newDocument(user, firstNonEmpty(strings.TrimSpace(title), pageTitle), body, true)
	T.storeEmbeddedImages(&g, udb)
	g = saveGuideRev(udb, g, "Saved from an artifact")
	return T.WebPath() + "/", g.Title, nil
}
