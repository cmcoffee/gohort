package scribe

// What a chat turn changed in the guide, as one card in the conversation.
//
// Deleting a section used to ask first, one card per section. A restructure is
// deletes and adds, so reorganizing a guide meant approving every section on
// its way out, while the same turn's edits and adds went through unasked and
// every change was already a revision in History. The approvals guarded the
// one change that was no harder to undo than the rest.
//
// So nothing asks during the turn. The turn's changes are listed as they land,
// on one card that grows, with one Undo that puts the guide back the way the
// turn found it. The undo is a restore, which is itself a revision, so it can
// be undone too.
//
// Only the chat send has a person watching the card, so only it tracks. The
// curator's unattended runs keep their own rule: they never delete a section.

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
)

// guideWriteTools are the co-author tools that change the open guide. Each
// saves a revision with a note saying what it did, and the card lists those.
var guideWriteTools = map[string]bool{
	"add_section": true, "edit_section": true, "draft_section": true, "delete_section": true,
	"rename_section": true, "move_section": true, "add_image": true,
}

// turnChangesBlock is the card's renderer name (page.go).
const turnChangesBlock = "scribe_changes"

// turnChanges follows one turn's changes to its guide.
type turnChanges struct {
	pub *agenthost.Publisher
	// open is the turn's guide and its owner's store, read fresh each call.
	open func() (Guide, Database, bool)
	id   string // the card's block id, one per turn

	mu     sync.Mutex
	guide  string
	before string // the revision holding the guide as the turn found it
	seen   string // the last revision already on the card
	notes  []string
}

func newTurnChanges(pub *agenthost.Publisher, open func() (Guide, Database, bool)) *turnChanges {
	return &turnChanges{pub: pub, open: open, id: "scribe-changes-" + newID()}
}

// wrap has each guide-writing tool note its change on the card. Other tools,
// and a source's own tools, pass through as they are.
func (tc *turnChanges) wrap(tools []AgentToolDef) []AgentToolDef {
	out := make([]AgentToolDef, len(tools))
	for i, td := range tools {
		if inner := td.Handler; inner != nil && guideWriteTools[td.Tool.Name] {
			td.Handler = func(ctx context.Context, args map[string]any) (string, error) {
				tc.begin()
				res, err := inner(ctx, args)
				if err == nil {
					tc.record()
				}
				return res, err
			}
		}
		out[i] = td
	}
	return out
}

// begin pins the guide as the turn found it, before its first change. The
// newest revision is that already when nothing has saved the guide since; a
// save made outside History (an editor's) gets a revision of its own here, so
// Undo never goes back past it. Compared by content: Updated is to the second,
// and a hand edit saved in the same second as the last revision read as that
// revision.
func (tc *turnChanges) begin() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.before != "" {
		return
	}
	g, udb, ok := tc.open()
	if !ok {
		return
	}
	revs := listRevisions(udb, g.ID)
	if n := len(revs); n > 0 && sameGuide(revs[n-1].Guide, g) {
		tc.before = revs[n-1].ID
	} else {
		tc.before = appendRevision(udb, g, "Before the Author's changes")
	}
	tc.guide, tc.seen = g.ID, tc.before
}

// record adds the revisions saved since the last call to the card and
// publishes it again under the same id, so the card grows in place.
func (tc *turnChanges) record() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.before == "" {
		return
	}
	_, udb, ok := tc.open()
	if !ok {
		return
	}
	revs := listRevisions(udb, tc.guide)
	start := 0
	for i, rv := range revs {
		if rv.ID == tc.seen {
			start = i + 1
		}
	}
	if start >= len(revs) {
		return
	}
	for _, rv := range revs[start:] {
		if note := strings.TrimSpace(rv.Note); note != "" {
			tc.notes = append(tc.notes, note)
		}
	}
	tc.seen = revs[len(revs)-1].ID
	tc.pub.Publish(tc.block())
}

// block is the card as it stands: the changes in order, and what Undo
// restores.
func (tc *turnChanges) block() agenthost.Block {
	n := len(tc.notes)
	text := "1 change"
	if n != 1 {
		text = strconv.Itoa(n) + " changes"
	}
	return agenthost.Block{
		Type:  turnChangesBlock,
		ID:    tc.id,
		Title: "Changes this turn",
		Text:  text,
		Data: map[string]string{
			"guide":   tc.guide,
			"rev":     tc.before,
			"changes": strings.Join(tc.notes, "\n"),
		},
	}
}

// sameGuide reports whether two copies of a guide hold the same document.
func sameGuide(a, b Guide) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// withoutDelete is the kit for a run nobody is watching: a finding worked in
// or superseded by the curator. delete_section asked first when it had a
// card, and a run with nobody to ask was refused, so these runs never
// deleted a section. With no card to ask on, they are not given the tool.
func withoutDelete(tools []AgentToolDef) []AgentToolDef {
	return withoutTools(tools, "delete_section")
}
