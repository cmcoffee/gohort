// File stores as a ReferenceSource, so an agent reaches one the way it
// reaches any other attached knowledge.
//
// The alternative was a globally-registered log tool, and the interface
// comment on ReferenceToolProvider says why that loses: "a generic
// pull_reference competes with the framework's own knowledge tools and
// loses; distinctly-named per-item tools don't." An agent attached to
// the "Support bundles" store gets search_support_bundles_logs, which it
// can see and reason about, rather than a generic reader it has to
// discover it should prefer over knowledge_search.
//
// The ITEM is the store, not the bundle. Attaching per bundle would mean
// editing the agent every time somebody drops a new one in; instead the
// agent attaches once and names the bundle per investigation, which is
// also how a person thinks about it.
//
// This app deliberately does NOT run commands. A local command appliance
// (servitor, Type=="command", WorkDir on the bundle) already mints frozen
// command templates behind an owner approval gate, with typed parameters
// that are quoted at runtime so a placeholder can never contribute
// syntax. A second command path here would be a weaker copy of that.

package filestore

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"

	"github.com/cmcoffee/gohort/tools/temptool"
)

type storeSource struct{ app *FileStoreApp }

func (s storeSource) Kind() string  { return "files" }
func (s storeSource) Label() string { return "File stores" }

// HasItems answers the cheap question cheaply: whether this user can reach any
// store at all, without describing them.
//
// List builds each item's description by walking the store's tree for a file
// count, which is right for a picker somebody opened and absurd as the answer
// to "is there anything here" — it was costing seconds on every render of a
// page that showed none of it. See core.ReferenceSourceCounter.
func (s storeSource) HasItems(user string) bool {
	if s.app == nil || s.app.DB == nil {
		return false
	}
	return len(StoresForUser(s.app.DB, user)) > 0
}

func (s storeSource) List(user string) []ReferenceItem {
	if s.app == nil || s.app.DB == nil {
		return nil
	}
	var out []ReferenceItem
	for _, st := range StoresForUser(s.app.DB, user) {
		desc := strings.TrimSpace(st.Description)
		if desc == "" {
			desc = st.Path
		}
		// Say what is actually in there. A store reading "0 folders" or
		// "unreadable" in the picker is a misconfigured path caught
		// before an agent is attached to it rather than after.
		// CountFolders, not ListFolders: this line needs the NUMBER, and
		// ListFolders buys it with a recursive walk of every subfolder whose
		// sizes and mtimes are then thrown away. The picker renders on the
		// agent editor, so that walk was in front of somebody every time they
		// opened one.
		if n, err := CountFolders(st.Path); err == nil {
			desc += " · " + strconv.Itoa(n) + " folder" + plural(n)
		} else {
			desc += " · unreadable"
		}
		out = append(out, ReferenceItem{ID: st.Slug, Name: st.Name, Desc: desc})
	}
	return out
}

// Fetch is the generic pull path — what a consumer gets when it pulls a
// source without going through the tools.
//
// It answers with what a search would find, because a file store has no
// "the document" to inject: the whole point is that nothing here is
// small enough to hand over whole.
func (s storeSource) Fetch(ctx context.Context, user, itemID, query string) string {
	st, ok := LoadStore(s.app.DB, itemID)
	// Checked HERE, not only where the list is built. An agent record can
	// carry a stale attachment from before the restriction, and an item
	// id is a string somebody can supply — filtering the picker is a
	// courtesy, this is the gate.
	if !ok || !st.AllowsUser(user) {
		return ""
	}
	if strings.TrimSpace(query) == "" {
		return s.folderMenu(st)
	}
	// Scope to the newest subfolder when there are any. Searching every
	// folder at once would mix unrelated tickets, runs, or incidents and
	// present the mixture as one finding; the TOOLS let a caller name the
	// folder deliberately, and this path has nobody to ask.
	root := st.Path
	scope := ""
	if folders, err := ListFolders(st.Path); err == nil && len(folders) > 0 {
		scope = folders[0].Name
	}
	dir, err := SubRoot(root, scope)
	if err != nil {
		return ""
	}
	res, err := Search(dir, SearchOpts{Pattern: regexpQuote(query), IgnoreCase: true, Context: 2, Max: 20})
	if err != nil || len(res.Matches) == 0 {
		return ""
	}
	where := st.Name
	if scope != "" {
		where = "the most recent folder of " + st.Name + " (" + scope + ")"
	}
	return "Matches in " + where + ":\n" + renderMatches(res)
}

// folderMenu is the "what is in here" answer: the store's subfolders,
// newest first. Newest first because whoever is asking is almost always
// asking about the most recent thing that landed.
func (s storeSource) folderMenu(st Store) string {
	folders, err := ListFolders(st.Path)
	if err != nil {
		return "The file store " + st.Name + " cannot be read: " + err.Error()
	}
	if len(folders) == 0 {
		// A flat store is legitimate: the files are the store. Say which
		// case this is rather than reporting an emptiness that isn't one.
		files, ferr := List(st.Path, "")
		if ferr == nil && len(files) > 0 {
			return "The file store " + st.Name + " has no subfolders — its " +
				strconv.Itoa(len(files)) + " file" + plural(len(files)) +
				" sit at the top level. Search it without naming `within`."
		}
		return "The file store " + st.Name + " is empty."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Subfolders in %s, newest first:\n", st.Name)
	for i, f := range folders {
		if i >= 40 {
			fmt.Fprintf(&b, "…and %d more.\n", len(folders)-i)
			break
		}
		fmt.Fprintf(&b, "- %s — %d file%s, %s, last written %s\n",
			f.Name, f.Files, plural(f.Files), HumanSize(f.Bytes), f.Modified.Format("2006-01-02 15:04"))
	}
	return b.String()
}

// ItemTools mints the per-store tools. Names fold in the store's slug so
// two attached stores cannot collide in one catalog.
//
// Three tools, and the shape is the same whether the store is flat or
// grouped: `within` is optional everywhere, meaning "the whole store"
// when omitted and one subfolder when given.
// storeToolNames is the set of tools a store mints, in catalog order.
//
// One definition, used by ItemTools below and by the admin table, because
// the table's whole job is to state the names in force — a column that
// prints a name nothing answers to is worse than no column. (It printed
// "read_<slug>_file" within a minute of being written.)
func storeToolNames(slug string) []string {
	return []string{"list_" + slug, "search_" + slug, "read_" + slug, "list_" + slug + "_commands"}
}

// ItemToolsWithSession is ItemTools plus the store's bound tool bundle. It needs
// a session because a bound tool runs with one — a credential to resolve, a
// workspace to write into, a context to be cancelled by.
func (s storeSource) ItemToolsWithSession(sess *ToolSession, user, itemID string) []AgentToolDef {
	tools := s.ItemTools(user, itemID)
	if sess == nil || len(tools) == 0 {
		return tools
	}
	st, ok := LoadStore(s.app.DB, itemID)
	if !ok {
		return tools
	}
	return append(tools, s.boundToolDefs(sess, user, st)...)
}

func (s storeSource) ItemTools(user, itemID string) []AgentToolDef {
	st, ok := LoadStore(s.app.DB, itemID)
	// No tools at all for a store this user may not reach, so a stale
	// attachment degrades to the store simply not being there rather
	// than to a set of tools that refuse on every call.
	if !ok || !st.AllowsUser(user) {
		return nil
	}
	slug := st.Slug
	label := st.Name
	about := strings.TrimSpace(st.Description)
	if about != "" {
		about = " " + about
	}

	// What an admin has registered against this store, and what each one is
	// for. Read-only and instant — it names the commands, it does not run them.
	//
	// The gap it closes: a store whose folders arrive encrypted or packed reads
	// as EMPTY to an agent. It searches, finds nothing, and reports nothing
	// found — when the truth is that a Decrypt bundle command is registered and
	// nobody has clicked it. The agent could not say that, because the commands
	// were invisible to it.
	//
	// Mirrors servitor's shape deliberately: the cheap read-only tool that maps
	// what is THERE (search_<system>_knowledge / get_<system>_facts) is separate
	// from the slow one that reaches the live thing (investigate_<system>). This
	// is the first half. Naming a command is safe in a way running one is not,
	// and it is most of the value: an agent that can say "this folder needs
	// Decrypt bundle run on it first" has turned a dead end into an instruction.
	commandsTool := AgentToolDef{
		Tool: Tool{
			Name:        "list_" + slug + "_commands",
			Description: fmt.Sprintf("List the commands an admin has registered against the %q file store — what each one does to a folder, and whether it asks a person for input. Read-only and instant: this NAMES them, it does not run them. Reach for it when a folder looks empty, unreadable, or still packaged: the usual reason is that one of these has to be run on it first, and telling the user WHICH one is the useful answer. Running one is a person's click in Files, not a tool call.", label),
			Caps:        []Capability{CapRead},
		},
		Handler: func(args map[string]any) (string, error) {
			return describeStoreCommands(s.app.DB, st), nil
		},
	}

	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "list_" + slug,
				Description: fmt.Sprintf("List what is in the %q file store: its subfolders (newest first, with file counts and sizes), or the files inside one of them.%s Call this when you do not already know what is there — the search and read tools take a subfolder name.", label, about),
				Parameters: map[string]ToolParam{
					"within": {Type: "string", Description: "Optional subfolder to list the FILES of. Omit to list the subfolders themselves."},
				},
				Caps: []Capability{CapRead},
			},
			Handler: func(args map[string]any) (string, error) {
				within := stringArg(args, "within")
				if within == "" {
					return s.folderMenu(st), nil
				}
				dir, err := SubRoot(st.Path, within)
				if err != nil {
					return "", err
				}
				files, err := List(dir, "")
				if err != nil {
					return "", err
				}
				if len(files) == 0 {
					return "That subfolder has no files in it.", nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "Files in %s, newest first:\n", within)
				for i, f := range files {
					if i >= 200 {
						fmt.Fprintf(&b, "…and %d more.\n", len(files)-i)
						break
					}
					fmt.Fprintf(&b, "- %s — %s, %s\n", f.Rel, HumanSize(f.Size), f.Modified.Format("2006-01-02 15:04"))
				}
				return b.String(), nil
			},
		},
		{
			Tool: Tool{
				Name:        "search_" + slug,
				Description: fmt.Sprintf("Search the %q file store for a pattern, returning matching lines with a few lines of context.%s This is a regular-expression search over raw lines, NOT a semantic one: search for what would literally BE in the text (an error string, an id, a stack frame, a config key), not for a description of it. Compressed .gz files are searched too. Results are capped, and the reply says so when the cap was hit — which matters, because a capped result cannot tell you how OFTEN something occurs.", label, about),
				Parameters: map[string]ToolParam{
					"pattern":     {Type: "string", Description: "A regular expression. A plain string is a valid one. Prefer something distinctive over something common."},
					"within":      {Type: "string", Description: "Optional subfolder to search. Omit to search the whole store. Use list_" + slug + " to see what is there."},
					"file_glob":   {Type: "string", Description: "Optional filename filter, e.g. \"*.log\" or \"catalina*\". Narrows a large tree."},
					"ignore_case": {Type: "boolean", Description: "Match case-insensitively. Default false — use it when hunting a word, not when hunting an identifier."},
					"context":     {Type: "number", Description: "Lines either side of each hit (0-8, default 2)."},
				},
				Required: []string{"pattern"},
				Caps:     []Capability{CapRead},
			},
			Handler: func(args map[string]any) (string, error) {
				dir, err := SubRoot(st.Path, stringArg(args, "within"))
				if err != nil {
					return "", err
				}
				ctxLines := 2
				if n, ok := numArg(args, "context"); ok {
					ctxLines = n
				}
				res, err := Search(dir, SearchOpts{
					Pattern:    stringArg(args, "pattern"),
					Glob:       stringArg(args, "file_glob"),
					IgnoreCase: boolArg(args, "ignore_case"),
					Context:    ctxLines,
				})
				if err != nil {
					return "", err
				}
				if len(res.Matches) == 0 {
					// A search that ran out of time and found nothing is
					// NOT "nothing matched" — saying so would report an
					// absence nobody established.
					if res.Stopped != "" {
						return "No matches in what was read, but the search did not finish: " + res.Stopped +
							". Narrow it with `within` or `file_glob` and try again — do not conclude this pattern is absent.", nil
					}
					return "Nothing matched. The pattern is a regular expression over raw lines — check it appears literally, try ignore_case, widen file_glob, or drop `within` to search the whole store.", nil
				}
				return renderMatches(res), nil
			},
		},
		{
			Tool: Tool{
				Name:        "read_" + slug,
				Description: fmt.Sprintf("Read a window of lines from one file in the %q store — use it after a search to see what surrounds a hit. Bounded: it returns a window, never a whole file.", label),
				Parameters: map[string]ToolParam{
					"file":   {Type: "string", Description: "Path of the file, exactly as a search result or listing reported it."},
					"within": {Type: "string", Description: "The subfolder the file is in, if the search reported one."},
					"around": {Type: "number", Description: "Line number to centre the window on. Omit to read from the top."},
					"lines":  {Type: "number", Description: "How many lines to return (default and maximum 400)."},
				},
				Required: []string{"file"},
				Caps:     []Capability{CapRead},
			},
			Handler: func(args map[string]any) (string, error) {
				dir, err := SubRoot(st.Path, stringArg(args, "within"))
				if err != nil {
					return "", err
				}
				around, _ := numArg(args, "around")
				lines, _ := numArg(args, "lines")
				out, start, err := Read(dir, stringArg(args, "file"), around, lines)
				if err != nil {
					return "", err
				}
				if len(out) == 0 {
					return "That file is empty, or the window starts past its end.", nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%s, lines %d-%d:\n", stringArg(args, "file"), start, start+len(out)-1)
				for i, line := range out {
					fmt.Fprintf(&b, "%d: %s\n", start+i, line)
				}
				return b.String(), nil
			},
		},
		commandsTool,
	}
}

// renderMatches formats hits for a model: grouped by file, line-numbered,
// and explicit about truncation.
func renderMatches(res SearchResult) string {
	var b strings.Builder
	file := ""
	for _, m := range res.Matches {
		if m.File != file {
			file = m.File
			fmt.Fprintf(&b, "\n%s:\n", file)
		}
		for i, before := range m.Before {
			fmt.Fprintf(&b, "  %d- %s\n", m.Line-len(m.Before)+i, before)
		}
		fmt.Fprintf(&b, "  %d> %s\n", m.Line, m.Text)
		for i, after := range m.After {
			fmt.Fprintf(&b, "  %d- %s\n", m.Line+i+1, after)
		}
	}
	// What it cost, when it cost something. A search that took a minute
	// and said nothing about why reads as a system that hangs at random;
	// "3140 files, 12 GB, 71s" reads as a big bundle, which is what it
	// is. Under five seconds this is noise, so it is omitted.
	if res.Elapsed > 5*time.Second {
		fmt.Fprintf(&b, "\n(read %d files, %d MB, in %s)\n", res.Scanned, res.Bytes>>20, res.Elapsed.Round(time.Second))
	}
	if res.Capped {
		b.WriteString("\n(result cap reached — these are the first matches, not all of them. Narrow the pattern or the file_glob before concluding anything about how often this occurs.)\n")
	}
	// Different sentence from the cap, deliberately. The cap means there
	// are more matches; this means part of the store was never read, so
	// an absence here is not evidence of absence.
	if res.Stopped != "" {
		b.WriteString("\n(INCOMPLETE — " + res.Stopped + ". Files not read may contain matches. Narrow with `within` or `file_glob` rather than treating this as the whole picture.)\n")
	}
	return strings.TrimLeft(b.String(), "\n")
}

// regexpQuote escapes a free-text query so the generic Fetch path treats
// it as a literal. A user's drafting context is prose, and prose is full
// of characters that are regular-expression syntax.
func regexpQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func stringArg(args map[string]any, key string) string {
	if v, ok := args[key]; ok && v != nil {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return false
}

func numArg(args map[string]any, key string) (int, bool) {
	switch v := args[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n, true
		}
	}
	return 0, false
}

var _ = time.Time{}

// boundToolDefs renders the tools bound to a store (Store.Toolset) as agent
// tools, so they arrive with the folder and nowhere else.
//
// The resolution path is deliberately the one servitor's appliance toolset uses
// — the caller's own tool records, carried on a session into
// temptool.BuildAgentToolDefs — so a bound tool behaves at call time exactly as
// it does when its author calls it directly, rather than through a second
// implementation that drifts from the first.
//
// A named tool the user no longer has is skipped rather than reported as an
// error: the binding outliving the tool is an authoring problem to fix where
// tools are edited, and refusing to hand over the OTHER four because one was
// deleted would take a working store down with it.
func (s storeSource) boundToolDefs(sess *ToolSession, user string, st Store) []AgentToolDef {
	if len(st.Toolset) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, n := range st.Toolset {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	// DB is required, not decoration: an api-mode tool resolves its credential
	// through the session and fails at call time without one, on a tool that
	// looks correctly configured everywhere else.
	bound := &ToolSession{Username: user, DB: AuthDB(), Ctx: sess.Context()}
	if ws, err := EnsureWorkspaceDir(user); err == nil {
		bound.WorkspaceDir = ws
	}
	for _, p := range LoadPersistentTempTools(AuthDB(), user) {
		if want[p.Tool.Name] {
			t := p.Tool
			bound.TempTools = append(bound.TempTools, &t)
		}
	}
	return temptool.BuildAgentToolDefs(bound)
}
