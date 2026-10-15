package orchestrate

// app_def(action="stores"): the file stores an app could be built on.
//
// An app that handles files (downloads, extracts, moves them on) should use
// the stores and commands an admin already set up, and Builder could not see
// them: get lists an app's grants, and a new app has none, so a request that
// named the stores and their commands was built from scratch, with its own
// download and extraction code. This lists every store the owner can reach,
// what it holds, the commands registered on it (as an app calls them, with the
// admin's guide to each), where it pulls from, and, for an app that exists,
// what it is granted.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
)

func (t *chatTurn) appDefStores(args map[string]any) (string, error) {
	cat, ok := appstores.Provider.(appstores.Catalog)
	if appstores.Provider == nil || !ok {
		return "File stores are not available on this deployment.", nil
	}
	slug := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug")))
	if slug != "" {
		if _, ok := LoadAppSpec(t.user, slug); !ok {
			slug = ""
		}
	}
	stores := cat.Stores(t.user, slug)
	if len(stores) == 0 {
		return "There are no file stores you can reach. An admin registers one under Admin, Files.", nil
	}
	var b strings.Builder
	b.WriteString("File stores on this deployment. Build on these rather than writing your own download, extraction or copying code: an app reaches a store only when an admin grants it one, on the store's page (Admin, Files, the store's row, Apps that may use it), choosing write, run commands and pull separately.\n")
	b.WriteString("In a script: from gohort import store_path, store_command, store_pull. store_path(\"<store>\") is its folder (read-only, or read-write with write). store_command(\"<store>\", \"<folder>\", \"<command>\") runs a registered command on one folder as `<binary> <folder>`; a two-phase one returns its challenge, and store_command(..., input=<response>) finishes it as `<binary> <folder> <input>`. store_pull(\"<store>\", \"<item>\") copies a file or folder in from the store it pulls from.\n")
	if slug == "" {
		b.WriteString("A new app has no grants yet: create it first, then tell the person exactly which store and which rights to grant it, and test once they have.\n")
	}
	for _, st := range stores {
		fmt.Fprintf(&b, "\n## %s (store \"%s\")\n", st.Name, st.Store)
		if st.About != "" {
			b.WriteString(st.About + "\n")
		}
		fmt.Fprintf(&b, "%d folder(s) in it now.", st.FolderCount)
		if st.Temporary {
			b.WriteString(" Temporary: each folder is removed after its lifetime.")
		}
		if st.PullsFrom != "" {
			fmt.Fprintf(&b, " Pulls from %q (a folder's %s/ part).", st.PullsFrom, st.PullPart)
		}
		b.WriteString("\n")
		switch {
		case slug == "":
		case st.Granted == nil:
			fmt.Fprintf(&b, "Not granted to %s.\n", slug)
		default:
			var r []string
			for _, x := range []struct {
				on   bool
				name string
			}{{true, "read"}, {st.Granted.Write, "write"}, {st.Granted.Commands, "run commands"}, {st.Granted.Pull, "pull"}} {
				if x.on {
					r = append(r, x.name)
				}
			}
			fmt.Fprintf(&b, "Granted to %s: %s.\n", slug, strings.Join(r, ", "))
		}
		if len(st.Commands) == 0 {
			b.WriteString("No commands registered.\n")
			continue
		}
		b.WriteString("Commands:\n")
		for _, c := range st.Commands {
			fmt.Fprintf(&b, "- %q", c.Name)
			if c.Label != "" && c.Label != c.Name {
				fmt.Fprintf(&b, " (%s)", c.Label)
			}
			if c.TwoPhase {
				fmt.Fprintf(&b, ": two phases, the second takes %s", firstNonEmptyStr(c.InputLabel, "an input"))
			}
			if c.Note != "" {
				b.WriteString(". " + strings.TrimSuffix(c.Note, "."))
			}
			b.WriteString("\n")
			if c.Guide != "" {
				b.WriteString("  The admin's guide to it:\n")
				for _, l := range strings.Split(c.Guide, "\n") {
					b.WriteString("  " + l + "\n")
				}
			}
		}
	}
	return b.String(), nil
}
