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
	"github.com/cmcoffee/gohort/core/promotion"
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
	b.WriteString(appStoresScriptUse + "\n")
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
			if len(c.Actions) > 0 {
				fmt.Fprintf(&b, "  Mapped actions: run each with store_action(%q, %q, \"<action>\", {<value>: ...}), never store_command:\n", st.Store, c.Name)
				for _, a := range c.Actions {
					fmt.Fprintf(&b, "  - %q", a.Name)
					if a.Description != "" {
						b.WriteString(": " + strings.TrimSuffix(a.Description, "."))
					}
					fmt.Fprintf(&b, ". Runs: %s\n", a.Line)
					var vals []string
					for _, v := range a.Values {
						d := v.Name
						var notes []string
						if v.Type != "" {
							notes = append(notes, v.Type)
						}
						if v.Required {
							notes = append(notes, "required")
						} else {
							notes = append(notes, "optional, left out when not given")
						}
						if v.Folder {
							notes = append(notes, "a folder or file in this store, by its name there")
						}
						d += " (" + strings.Join(notes, ", ") + ")"
						if v.Description != "" {
							d += ": " + strings.TrimSuffix(v.Description, ".")
						}
						vals = append(vals, d)
					}
					if len(vals) > 0 {
						b.WriteString("    values: " + strings.Join(vals, "; ") + "\n")
					}
				}
			} else if c.Mapped {
				b.WriteString("  Mapped, but an admin has not approved the mapping yet (Agents on its row): store_action refuses it until then.\n")
			}
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

// appDefRequestStore files an app's request for access to a file store
// (app_def request_store), and, when the person building is an admin and is
// watching, asks them on the spot: Approve applies the grant at once. Anyone
// else's request waits for an admin under Admin, Governance, Pending
// promotions, which is also where an unanswered card leaves it.
func (t *chatTurn) appDefRequestStore(args map[string]any) (string, error) {
	cat, ok := appstores.Provider.(appstores.Catalog)
	if appstores.Provider == nil || !ok {
		return "", fmt.Errorf("file stores are not available on this deployment")
	}
	slug := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug")))
	spec, ok := LoadAppSpec(t.user, slug)
	if !ok {
		return "", fmt.Errorf("no app %q of yours: create it first, then ask for its stores", slug)
	}
	want := strings.TrimSpace(stringArg(args, "store"))
	var st *appstores.StoreInfo
	var names []string
	for _, s := range cat.Stores(t.user, slug) {
		s := s
		names = append(names, s.Store)
		if s.Store == want || strings.EqualFold(s.Name, want) {
			st = &s
		}
	}
	if st == nil {
		return "", fmt.Errorf("no file store %q (there are: %s)", want, strings.Join(names, ", "))
	}
	write, commands, pull := boolArg(args, "write"), boolArg(args, "commands"), boolArg(args, "pull")
	if g := st.Granted; g != nil && (!write || g.Write) && (!commands || g.Commands) && (!pull || g.Pull) {
		return fmt.Sprintf("%s already has that on %s.", spec.Name, st.Name), nil
	}
	name := appstores.GrantRequestName(st.Store, slug, write, commands, pull)
	why := strings.TrimSpace(stringArg(args, "why"))
	if err := CreatePromotionRequest(AuthDB(), t.user, appstores.GrantRequestKind, name, why); err != nil {
		return "", err
	}
	_, rights, _ := strings.Cut(name, ": ")
	waiting := fmt.Sprintf("Requested %s on %s for %s. It is waiting for an admin under Admin, Governance, Pending promotions; scripts reach the store once it is approved.", rights, st.Name, spec.Name)
	if !UserIsAdmin(t.user) || (t.sse == nil && t.askVia == nil) {
		return waiting, nil
	}
	detail := "The app's scripts get " + rights + " on " + st.Name + ", for everyone who uses the app."
	if why != "" {
		detail += "\n\nWhy: " + why
	}
	if t.askInChat(fmt.Sprintf("Grant %s %s on the file store %s?", spec.Name, rights, st.Name), detail, []string{"Approve"}) != "Approve" {
		return "Not granted now. " + waiting, nil
	}
	if err := promotion.Approve(AuthDB(), promotion.RequestKey(appstores.GrantRequestKind, t.user, name), t.user); err != nil {
		return "", fmt.Errorf("approving the request: %w", err)
	}
	return fmt.Sprintf("Granted: %s now has %s on %s. Test its scripts against the store.", spec.Name, rights, st.Name), nil
}

// appStoresScriptUse is how an app's script reaches its stores, for Builder.
const appStoresScriptUse = "In a script: from gohort import store_path, store_action, store_command, store_pull. store_path(\"<store>\") is its folder (read-only, or read-write with write). " +
	"A command with mapped actions is run ONLY through them: store_action(\"<store>\", \"<command>\", \"<action>\", {\"<value>\": ...}) runs the line an admin approved, with your values in its {placeholders} (a list fills its placeholder once per item; an optional value left out takes its option with it), and nothing else about the command can change. " +
	"store_command(\"<store>\", \"<folder>\", \"<command>\") is for a command with no mapped actions: it runs `<binary> <folder>`; a two-phase one returns its challenge, and store_command(..., input=<response>) finishes it as `<binary> <folder> <input>`. " +
	"store_pull(\"<store>\", \"<item>\") copies a file or folder in from the store it pulls from."
