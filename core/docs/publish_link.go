package docs

// Taking in a page that is already published somewhere.
//
// A document's home at a destination (its PublishRecord) was only ever made
// by publishing it there, so a page that existed before had no way in: pasting
// its link and publishing made a SECOND page, and the first one drifted on its
// own. LinkPage turns the link somebody pastes into the record a publish
// would have left, and reads the page as it stands, so a producer can start
// the document from it and every later update goes to that page. What it read
// is the copy's baseline, so the first check finds no edits that are not
// there.

import (
	"context"
	"errors"
	"strings"

	"github.com/cmcoffee/gohort/core/textutil"
)

// LinkedPage is a page taken in from its link.
type LinkedPage struct {
	// Record is where it lives, as a publish of it would have recorded.
	Record PublishRecord
	// Copy is the page as read now: the baseline for later reads.
	Copy PublishedCopy
	// Title is the page's own title, when the destination says.
	Title string
	// Markdown is its content as Markdown, to start a document from.
	Markdown string
}

// PageLinker is a destination that can resolve a pasted link to one of its
// pages itself: Confluence reads the page id out of the link, and the space it
// is in from the page. A destination without one is linked by the address as
// given, which is what its own publishes record anyway.
type PageLinker interface {
	LinkPage(ctx context.Context, user, target, link string) (LinkedPage, error)
}

// LinkPage takes in the page at link through destination kind (and target,
// when the destination needs to be told where it is).
func LinkPage(ctx context.Context, user, kind, target, link string) (LinkedPage, error) {
	link = strings.TrimSpace(link)
	if link == "" {
		return LinkedPage{}, errors.New("paste the page's address")
	}
	d := lookupDest(kind)
	if d == nil {
		return LinkedPage{}, errors.New("that destination is not set up here")
	}
	if ok, reason := d.Available(user); !ok {
		return LinkedPage{}, errors.New(d.Label() + " is not available: " + reason)
	}
	if l, ok := d.(PageLinker); ok {
		return l.LinkPage(ctx, user, strings.TrimSpace(target), link)
	}
	rec := PublishRecord{Kind: strings.TrimSpace(kind), Target: strings.TrimSpace(target), ExternalID: link, URL: link}
	cur, err := ReadPublished(ctx, user, rec)
	if errors.Is(err, ErrCannotReadBack) {
		return LinkedPage{}, errors.New(d.Label() + " cannot read a page, so one cannot be taken in from it")
	}
	if err != nil {
		return LinkedPage{}, err
	}
	return LinkedPage{Record: rec, Copy: cur, Markdown: CopyMarkdown(cur.Body)}, nil
}

// CopyMarkdown is a read copy's body as Markdown: HTML converted, Markdown as
// it is.
func CopyMarkdown(body string) string {
	if looksHTMLRE.MatchString(body) {
		return strings.TrimSpace(textutil.HTMLToMarkdown(cdataRE.ReplaceAllString(body, "$1")))
	}
	return strings.TrimSpace(body)
}

// CanLinkPages reports whether destination kind can take a page in: it
// resolves links itself, or it can read a page.
func CanLinkPages(kind string) bool {
	d := lookupDest(kind)
	if d == nil {
		return false
	}
	_, links := d.(PageLinker)
	_, reads := d.(PublishReader)
	return links || reads
}
