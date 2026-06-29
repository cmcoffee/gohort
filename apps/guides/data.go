// Guides data model + HTML rendering.
//
// A Guide is a living document: an ordered list of Sections, each authored as
// markdown but RENDERED to a styled HTML document (table of contents + sections)
// for the viewer and for export. Markdown is the storage format (natural for the
// LLM to produce + cheap to diff/export); HTML is the presentation.
package guides

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const guidesTable = "guides"

// Section is one page of a guide. Markdown is the authored body; Order positions
// it in the document.
type Section struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
	Order    int    `json:"order"`
}

// Guide is the living document.
type Guide struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Subtitle string    `json:"subtitle,omitempty"`
	Sections []Section `json:"sections"`
	Created  string    `json:"created"`
	Updated  string    `json:"updated"`
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// sorted returns the guide's sections in display order.
func (g Guide) sorted() []Section {
	s := append([]Section(nil), g.Sections...)
	sort.SliceStable(s, func(i, j int) bool { return s[i].Order < s[j].Order })
	return s
}

// nextOrder returns an Order value that appends after the last section.
func (g Guide) nextOrder() int {
	max := 0
	for _, s := range g.Sections {
		if s.Order > max {
			max = s.Order
		}
	}
	return max + 1
}

// --- storage (per-user) ------------------------------------------------------

func loadGuide(udb Database, id string) (Guide, bool) {
	var g Guide
	ok := udb.Get(guidesTable, id, &g)
	return g, ok
}

func saveGuide(udb Database, g Guide) Guide {
	if g.Created == "" {
		g.Created = now()
	}
	g.Updated = now()
	udb.Set(guidesTable, g.ID, g)
	return g
}

func listGuides(udb Database) []Guide {
	var out []Guide
	for _, k := range udb.Keys(guidesTable) {
		if g, ok := loadGuide(udb, k); ok {
			out = append(out, g)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	return out
}

func deleteGuide(udb Database, id string) {
	udb.Unset(guidesTable, id)
	udb.Unset(revisionsTable, id)
}

// --- revisions ---------------------------------------------------------------

const revisionsTable = "guide_revisions"
const maxRevisions = 50

// GuideRevision is a point-in-time snapshot of a guide + a note describing the
// change that produced it.
type GuideRevision struct {
	ID    string `json:"id"`
	At    string `json:"at"`
	Note  string `json:"note"`
	Guide Guide  `json:"guide"`
}

// guideRevisions is the stored revision list for one guide (newest last).
type guideRevisions struct {
	Revisions []GuideRevision `json:"revisions"`
}

// saveGuideRev saves the guide AND records a revision snapshot of the resulting
// state with a note. The revision timeline is the undo history for the destructive
// co-author tools. Capped at maxRevisions (oldest dropped).
func saveGuideRev(udb Database, g Guide, note string) Guide {
	saved := saveGuide(udb, g)
	var rl guideRevisions
	udb.Get(revisionsTable, saved.ID, &rl)
	rl.Revisions = append(rl.Revisions, GuideRevision{
		ID:    newID(),
		At:    now(),
		Note:  strings.TrimSpace(note),
		Guide: saved,
	})
	if len(rl.Revisions) > maxRevisions {
		rl.Revisions = rl.Revisions[len(rl.Revisions)-maxRevisions:]
	}
	udb.Set(revisionsTable, saved.ID, rl)
	return saved
}

func listRevisions(udb Database, guideID string) []GuideRevision {
	var rl guideRevisions
	udb.Get(revisionsTable, guideID, &rl)
	return rl.Revisions
}

func loadRevision(udb Database, guideID, revID string) (GuideRevision, bool) {
	for _, r := range listRevisions(udb, guideID) {
		if r.ID == revID {
			return r, true
		}
	}
	return GuideRevision{}, false
}

// --- HTML rendering ----------------------------------------------------------

// renderGuideHTML builds the full document: a table of contents followed by each
// section rendered from markdown. Trusted output (server-built from
// MarkdownToHTML) — the viewer drops it in via innerHTML. Styling lives in
// guideDocCSS (injected via the page's ExtraHeadHTML) so it reads like a
// formatted document / PDF.
func renderGuideHTML(g Guide) string {
	secs := g.sorted()
	var b strings.Builder
	b.WriteString(`<article class="guide-doc">`)
	b.WriteString(`<header class="guide-doc-head"><h1>` + HTMLEscape(g.Title) + `</h1>`)
	if strings.TrimSpace(g.Subtitle) != "" {
		b.WriteString(`<p class="guide-doc-sub">` + HTMLEscape(g.Subtitle) + `</p>`)
	}
	b.WriteString(`</header>`)

	if len(secs) == 0 {
		b.WriteString(`<p class="guide-doc-empty">This guide has no sections yet. Ask the assistant on the right to draft one — for example, "Add an introduction section."</p></article>`)
		return b.String()
	}

	// Table of contents.
	b.WriteString(`<nav class="guide-toc"><div class="guide-toc-title">Contents</div><ol>`)
	for i, s := range secs {
		anchor := fmt.Sprintf("sec-%d", i+1)
		b.WriteString(`<li><a href="#` + anchor + `">` + HTMLEscape(sectionHeading(s, i)) + `</a></li>`)
	}
	b.WriteString(`</ol></nav>`)

	// Sections.
	for i, s := range secs {
		anchor := fmt.Sprintf("sec-%d", i+1)
		b.WriteString(`<section class="guide-section" id="` + anchor + `">`)
		b.WriteString(`<h2><span class="guide-section-num">` + fmt.Sprint(i+1) + `.</span> ` + HTMLEscape(sectionHeading(s, i)) + `</h2>`)
		b.WriteString(`<div class="guide-section-body">` + MarkdownToHTML(s.Markdown) + `</div>`)
		b.WriteString(`</section>`)
	}
	b.WriteString(`</article>`)
	return b.String()
}

func sectionHeading(s Section, i int) string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	return fmt.Sprintf("Section %d", i+1)
}

// renderGuideMarkdown assembles the whole guide as one markdown document — for
// export (PDF/markdown). Includes a numbered Table of Contents so the PDF and the
// markdown carry the same structure the HTML viewer shows.
func renderGuideMarkdown(g Guide) string {
	var b strings.Builder
	b.WriteString("# " + g.Title + "\n\n")
	if strings.TrimSpace(g.Subtitle) != "" {
		b.WriteString("_" + g.Subtitle + "_\n\n")
	}
	secs := g.sorted()
	if len(secs) > 0 {
		b.WriteString("## Contents\n\n")
		for i, s := range secs {
			b.WriteString(fmt.Sprintf("%d. %s\n", i+1, sectionHeading(s, i)))
		}
		b.WriteString("\n")
	}
	for i, s := range secs {
		b.WriteString(fmt.Sprintf("## %d. %s\n\n", i+1, sectionHeading(s, i)))
		b.WriteString(strings.TrimSpace(s.Markdown) + "\n\n")
	}
	return b.String()
}

// renderGuideMarkdownPlain is the assembled guide WITHOUT the Contents block —
// for feeding the audit (the agent doesn't need a ToC, and numbered "## N." would
// just be noise it might echo).
func renderGuideMarkdownPlain(g Guide) string {
	var b strings.Builder
	b.WriteString("# " + g.Title + "\n\n")
	if strings.TrimSpace(g.Subtitle) != "" {
		b.WriteString("_" + g.Subtitle + "_\n\n")
	}
	for i, s := range g.sorted() {
		b.WriteString("## " + sectionHeading(s, i) + "\n\n")
		b.WriteString(strings.TrimSpace(s.Markdown) + "\n\n")
	}
	return b.String()
}
