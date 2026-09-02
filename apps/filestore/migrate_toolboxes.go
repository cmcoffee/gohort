// Folding the toolboxes table back onto the commands it was mapped from.
//
// See command_tools.go for why the third noun went away. This is what keeps a
// deployment that already mapped something from losing it: a mapping
// conversation is real work, and coming up to find the folder empty with no
// error is how an operator rebuilds by hand what was already there.

package filestore

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// legacyToolboxesTable held mapped toolboxes, keyed by name, when a mapping was
// a record of its own. Kept only so the migration can empty it.
const legacyToolboxesTable = "file_store_toolboxes"

// legacyToolbox is the shape those records were stored in.
type legacyToolbox struct {
	Tool        TempTool `json:"tool"`
	FromStore   string   `json:"from_store,omitempty"`
	FromCommand string   `json:"from_command,omitempty"`
}

// MigrateToolboxesOntoCommands moves each mapped toolbox onto the command it
// was mapped from, carrying the attachment across as the approval — because
// that is what an attachment MEANT, and an admin who approved something should
// not have to approve it again under a new name.
//
// Idempotent: the old row is dropped only after the command is written, so a
// crash between the two leaves the record in both places and the next boot
// finishes the job. A toolbox whose command has since been deleted is dropped —
// there is nothing left for it to be part of.
func MigrateToolboxesOntoCommands(db Database) int {
	if db == nil {
		return 0
	}
	moved := 0
	for _, k := range db.Keys(legacyToolboxesTable) {
		var tb legacyToolbox
		if !db.Get(legacyToolboxesTable, k, &tb) {
			continue
		}
		cmd, ok := LoadStoreCommand(db, tb.FromStore, tb.FromCommand)
		if ok && !cmd.Mapped() {
			cmd.Tools = tb.Tool.Actions
			cmd.ToolDesc = strings.TrimSpace(tb.Tool.Description)
			// The attachment was the approval. Read it off the store it was
			// attached to, not off the toolbox — a toolbox attached nowhere
			// was mapped and never approved.
			if st, found := LoadStore(db, tb.FromStore); found {
				for _, n := range st.Toolboxes {
					if strings.EqualFold(RefToolSlug(strings.TrimSpace(n)), RefToolSlug(tb.Tool.Name)) {
						cmd.Approved = true
						break
					}
				}
			}
			db.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
			moved++
		}
		db.Unset(legacyToolboxesTable, k)
	}
	// The attachment lists have nothing left to point at.
	for _, st := range ListStores(db) {
		if len(st.Toolboxes) == 0 {
			continue
		}
		st.Toolboxes = nil
		db.Set(storesTable, st.Slug, st)
	}
	if moved > 0 {
		Log("[filestore] folded %d mapped toolbox(es) onto the commands they came from", moved)
	}
	return moved
}
