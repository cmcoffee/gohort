package anvil

// One card per file per turn. Each write used to put a card of its own in the
// conversation, so a file the assistant edited six times in a turn was six
// cards in a row, each with a sliver of the change. A turn's writes to one
// file now share a card: it shows what the turn did to the file as one diff,
// keeps every write's own diff a click away, and its Revert puts the file
// back as it was before the turn's first write (newest first, through the
// same undo a message's Undo uses, which stops at a write it cannot put back).

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/cmcoffee/gohort/core/agenthost"
)

// fileCards are one turn's edit cards, by file.
type fileCards struct {
	mu     sync.Mutex
	byPath map[string]*fileCard
}

// fileCard is the writes one turn made to one file, oldest first. id is the
// first write's, so the card keeps its place as later writes update it.
type fileCard struct {
	id     string
	writes []EditEntry
}

// add takes a write into its file's card and returns the card as it now
// stands, to publish (a card published again under the same id is updated
// where it is).
func (fc *fileCards) add(kind string, p Project, e EditEntry) agenthost.Block {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.byPath == nil {
		fc.byPath = map[string]*fileCard{}
	}
	c := fc.byPath[e.Path]
	if c == nil {
		c = &fileCard{id: e.ID}
		fc.byPath[e.Path] = c
	}
	c.writes = append(c.writes, e)
	return c.block(kind, p)
}

// block renders the card: the turn's change to the file as one diff, and
// each write behind it.
func (c *fileCard) block(kind string, p Project) agenthost.Block {
	first, last := c.writes[0], c.writes[len(c.writes)-1]
	if len(c.writes) == 1 {
		return editBlock(kind, p, first)
	}
	diff, added, removed := unifiedDiff(first.Path, first.Before, last.After)
	what := EditModify
	switch {
	case first.Kind == EditCreate && last.Kind != EditDelete:
		what = EditCreate
	case last.Kind == EditDelete && first.Kind != EditCreate:
		what = EditDelete
	}
	verb := map[string]string{EditCreate: "Created", EditDelete: "Deleted", EditModify: "Edited"}[what]
	ids := make([]string, 0, len(c.writes))
	each := make([]map[string]any, 0, len(c.writes))
	for _, w := range c.writes {
		ids = append(ids, w.ID)
		each = append(each, map[string]any{"edit_id": w.ID, "diff": w.Diff, "added": w.Added, "removed": w.Removed})
	}
	// A card's data is text; the writes travel as JSON in it.
	eachJSON, _ := json.Marshal(each)
	return agenthost.Block{
		Type:  kind,
		ID:    "anvil-edit-" + c.id,
		Title: first.Path,
		Text:  fmt.Sprintf("%s · %d edits · +%d −%d", verb, len(c.writes), added, removed),
		Data: map[string]string{
			"edit_id":    strings.Join(ids, ","),
			"project_id": p.ID,
			"kind":       what,
			"diff":       diff,
			"edits":      string(eachJSON),
		},
	}
}
