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
	// Collections are knowledge-collection IDs attached to this guide. The Guide
	// Author searches them (search_knowledge tool) to ground sections in the
	// user's own curated knowledge, alongside web research.
	Collections []string `json:"collections,omitempty"`
	// References are cross-app reference-source selections ({kind, item_id})
	// attached to this guide via the Sources picker — servitor Systems, connected
	// document sources (Confluence), etc. The Guide Author pulls them
	// (pull_reference) to build the guide from internal knowledge; list_reference_
	// sources flags which are attached so it knows the user's chosen scope.
	References []ReferenceSelection `json:"references,omitempty"`
	// Owner is the username that created + owns this guide (stamped on create).
	// Shared publishes it to every authenticated user; ShareMode sets what they may
	// do: "view" (default — read + export only) or "edit" (any signed-in user may
	// also edit sections / co-author). Either way, only the owner (or an admin) can
	// change sharing, delete the guide, or manage its knowledge/sources. When Shared
	// is set the guide is ALSO listed in the app-wide sharedGuidesIndex so any
	// request can resolve it in the owner's store. See resolveGuide.
	Owner     string `json:"owner,omitempty"`
	Shared    bool   `json:"shared,omitempty"`
	ShareMode string `json:"share_mode,omitempty"` // "" | "view" | "edit"
	// Private locks the Guide Author to NO internet access on this guide: its
	// turns run ForcePrivate (network tools stripped, worker-tier routing) and the
	// web-research tool is withheld, so answers/edits are grounded only in the
	// guide's own attached knowledge — nothing reaches the open web.
	Private bool   `json:"private,omitempty"`
	Created string `json:"created"`
	Updated string `json:"updated"`
}

// ShareModeEdit is the ShareMode value that lets any authenticated user edit a
// shared guide (vs the default view-only share).
const ShareModeEdit = "edit"

// sharedForEdit reports whether this guide is shared with EDIT permission — any
// authenticated user may change its content, not just view it.
func (g Guide) sharedForEdit() bool { return g.Shared && g.ShareMode == ShareModeEdit }

// sharedGuidesIndex lives in the app-wide store (T.DB, NOT a per-user UserDB) and
// maps a shared guide ID -> its owner username. Presence IS the shared flag.
const sharedGuidesIndex = "shared_guides"

// resolveGuide finds a guide for a request: the requester's OWN store first, else
// via the shared index (the owner's store). Returns the guide, its owner, the
// OWNER'S UserDB (use for every content op — sections, revisions, the research
// collection — so a shared guide lives in ONE place regardless of who opened it),
// and whether it was found. For a guide the user owns, owner == reqUser and
// ownerUDB == reqUDB, so non-shared flows are unchanged. appDB is the app store
// (T.DB); reqUDB is the requesting user's UserDB.
func resolveGuide(appDB, reqUDB Database, reqUser, id string) (Guide, string, Database, bool) {
	if g, ok := loadGuide(reqUDB, id); ok {
		owner := g.Owner
		if owner == "" {
			owner = reqUser // legacy guide without an owner stamp: the holder owns it
		}
		return g, owner, reqUDB, true
	}
	if owner, ok := LookupSharedOwner(appDB, sharedGuidesIndex, id); ok {
		if oudb := UserDB(appDB, owner); oudb != nil {
			if g, ok := loadGuide(oudb, id); ok {
				return g, owner, oudb, true
			}
		}
	}
	return Guide{}, "", nil, false
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

func deleteGuide(udb Database, user, id string) {
	udb.Unset(guidesTable, id)
	udb.Unset(revisionsTable, id)
	// Vacuum the guide's auto-research collection (metadata + its chunks in
	// VectorDB) so deleting a guide doesn't leave an orphaned collection behind.
	DeleteCollection(UserDB(CollectionsDB(), user), VectorDB, user, guideCollectionID(id))
}

// --- per-guide research collection -------------------------------------------

// guideCollectionID is the deterministic knowledge-collection ID for a guide's
// auto-collected research. One collection per guide, derived from the guide ID.
func guideCollectionID(guideID string) string { return "guide-" + guideID }

// ensureGuideCollection returns the guide's own research collection, creating it
// (user-scoped, in the shared collections home) on first use and AUTO-ATTACHING
// it to the guide so the search_knowledge tool + Knowledge picker include it.
// Returns the collection ID and the (possibly updated) guide. Collections live
// in CollectionsDB(), so this never reaches into orchestrate.
func ensureGuideCollection(udb Database, user string, g Guide) (string, Guide) {
	collID := guideCollectionID(g.ID)
	cdb := UserDB(CollectionsDB(), user)
	if _, ok := LoadCollection(cdb, user, collID); !ok {
		SaveCollection(cdb, Collection{
			ID:          collID,
			Owner:       user,
			Name:        "Guide research: " + firstNonEmpty(g.Title, "Untitled guide"),
			Description: "Cited research the Guide Author gathered while writing this guide. Searched via the guide's search_knowledge tool; grows as you ask it to research topics.",
			Scope:       CollectionScopeUser,
			Created:     time.Now(),
		})
	}
	// Auto-attach so search_knowledge consults it and the picker shows it ticked.
	attached := false
	for _, id := range g.Collections {
		if id == collID {
			attached = true
			break
		}
	}
	if !attached {
		g.Collections = append(g.Collections, collID)
		g = saveGuide(udb, g)
	}
	return collID, g
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
// sanitizeGuideArtifacts strips stray LLM output that leaked into a section body
// during drafting/co-authoring but is NOT part of the guide: reasoning
// delimiters (<think>…</think>), framework markers (<gohort-meta>, [ATTACH:…]),
// and tool-call markup (<tool_call>, <function=…>, <tool_code>). Returns the
// cleaned markdown and whether anything changed. Composes the core strippers so
// the definition of "artifact" stays in one place across the app.
func sanitizeGuideArtifacts(md string) (string, bool) {
	cleaned, _ := StripThinkTags(md)
	cleaned = StripMetaTags(cleaned)
	cleaned = StripToolCallTags(cleaned)
	cleaned = StripEmDashes(cleaned)
	cleaned = strings.TrimSpace(cleaned)
	return cleaned, cleaned != strings.TrimSpace(md)
}

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
// renderGuideHTML renders the guide as an HTML document. When controls is true
// (the live viewer), each section carries inline edit / move / delete buttons and
// data-*-id attributes the guides JS wires up; exports pass false.
func renderGuideHTML(g Guide, controls bool) string {
	secs := g.sorted()
	var b strings.Builder
	b.WriteString(`<article class="guide-doc" data-guide-id="` + HTMLEscape(g.ID) + `">`)
	b.WriteString(`<header class="guide-doc-head"><h1>` + HTMLEscape(g.Title) + `</h1>`)
	if strings.TrimSpace(g.Subtitle) != "" {
		b.WriteString(`<p class="guide-doc-sub">` + HTMLEscape(g.Subtitle) + `</p>`)
	}
	b.WriteString(`</header>`)

	if len(secs) == 0 {
		b.WriteString(`<p class="guide-doc-empty">This guide has no sections yet. Ask the assistant on the right to draft one — for example, "Add an introduction section."`)
		if controls {
			b.WriteString(` Or <button type="button" class="guide-add-link" data-guide-act="add">add one yourself</button>.`)
		}
		b.WriteString(`</p></article>`)
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
		b.WriteString(`<section class="guide-section" id="` + anchor + `" data-section-id="` + HTMLEscape(s.ID) + `">`)
		if controls {
			b.WriteString(`<div class="guide-sec-ctrls">` +
				`<button type="button" class="guide-sec-btn" data-guide-act="edit" title="Edit">Edit</button>` +
				`<button type="button" class="guide-sec-btn" data-guide-act="up" title="Move up">&uarr;</button>` +
				`<button type="button" class="guide-sec-btn" data-guide-act="down" title="Move down">&darr;</button>` +
				`<button type="button" class="guide-sec-btn guide-sec-del" data-guide-act="delete" title="Delete">&times;</button>` +
				`</div>`)
		}
		b.WriteString(`<h2><span class="guide-section-num">` + fmt.Sprint(i+1) + `.</span> ` + HTMLEscape(sectionHeading(s, i)) + `</h2>`)
		b.WriteString(`<div class="guide-section-body">` + MarkdownToHTML(s.Markdown) + `</div>`)
		b.WriteString(`</section>`)
	}
	if controls {
		b.WriteString(`<div class="guide-add-row"><button type="button" class="guide-add-btn" data-guide-act="add">+ Add section</button></div>`)
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
