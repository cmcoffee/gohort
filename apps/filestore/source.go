// File stores as a ReferenceSource, so an agent reaches one the way it
// reaches any other attached knowledge.
//
// The alternative was a globally-registered log tool, and the interface
// comment on ReferenceToolProvider says why that loses: "a generic
// pull_reference competes with the framework's own knowledge tools and
// loses; distinctly-named per-item tools don't." An agent attached to
// the "Build logs" store gets search_build_logs_logs, which it
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
	"github.com/cmcoffee/gohort/core/bundle"
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
			desc += " - " + strconv.Itoa(n) + " folder" + plural(n)
		} else {
			desc += " - unreadable"
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
	res, err := Search(ctx, dir, SearchOpts{Pattern: regexpQuote(query), IgnoreCase: true, Context: 2, Max: 20})
	if err != nil || len(res.Matches) == 0 {
		return ""
	}
	where := st.Name
	if scope != "" {
		where = "the most recent folder of " + st.Name + " (" + scope + ")"
	}
	return "Matches in " + where + ":\n" + renderMatches(res, "")
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
			return "The file store " + st.Name + " has no subfolders: its " +
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
		fmt.Fprintf(&b, "- %s: %d file%s, %s, last written %s%s\n",
			f.Name, f.Files, plural(f.Files), HumanSize(f.Bytes), f.Modified.Format("2006-01-02 15:04"), st.expiryNote(f.Name))
	}
	if st.Ephemeral {
		fmt.Fprintf(&b, "This is a temporary store: each folder is deleted %s after its last change, unless something is using it then.\n", fmtLifetime(st.lifetime()))
	}
	return b.String()
}

// expiryNote is ", deleted in about 5h" for a folder of a temporary store, so
// an agent (and the person reading its answer) knows how long a folder stays.
func (st Store) expiryNote(folder string) string {
	if !st.Ephemeral {
		return ""
	}
	dir, err := SubRoot(st.Path, folder)
	if err != nil {
		return ""
	}
	newest, err := bundleNewest(dir)
	if err != nil {
		return ""
	}
	left := time.Until(newest.Add(st.lifetime()))
	if left <= 0 {
		return ", due for deletion at the next sweep"
	}
	return ", deleted in about " + fmtLifetime(left)
}

// fmtLifetime is a duration in whole hours, or minutes under an hour.
func fmtLifetime(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes())+1)
	}
	return fmt.Sprintf("%dh", int(d.Round(time.Hour).Hours()))
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

// storeToolsInForce is every tool name a store gives an attached agent before
// its mapped commands: the four of every store, and the evidence tools when it
// indexes evidence.
func storeToolsInForce(st Store) []string {
	names := storeToolNames(st.Slug)
	if st.Evidence {
		names = append(names, evidenceToolNames(st.Slug)...)
	}
	return names
}

// ItemToolsWithSession is ItemTools plus the approved commands mapped on this
// folder. It needs a session because a mapped action runs with one — a
// workspace to work in, a context to be cancelled by.
func (s storeSource) ItemToolsWithSession(sess *ToolSession, user, itemID string) []AgentToolDef {
	tools := s.itemTools(sess, user, itemID)
	if sess == nil || len(tools) == 0 {
		return tools
	}
	st, ok := LoadStore(s.app.DB, itemID)
	if !ok {
		return tools
	}
	// Commands and pull change what the store holds, or act on its behalf:
	// Full Access for the agent's owner, and an attachment that allows
	// changes (both kinds of tool declare CapWrite).
	if !st.FullAccessFor(user) {
		return tools
	}
	tools = append(tools, s.commandToolDefs(sess, user, st)...)
	if pull, ok := s.app.pullToolDef(sess, user, st); ok {
		tools = append(tools, pull)
	}
	return tools
}

// ItemTurnWriteTools describes the store's tools that make changes, which
// ItemToolsWithSession builds only inside a turn: its mapped commands and its
// pull, under the same Full Access rule. Definitions only, for the Sources
// picker and the lists of attachable tool names (core TurnWriteToolDescriber);
// the handlers are dropped, so nothing here can run.
func (s storeSource) ItemTurnWriteTools(user, itemID string) []Tool {
	st, ok := LoadStore(s.app.DB, itemID)
	if !ok || !st.AllowsUser(user) || !st.FullAccessFor(user) {
		return nil
	}
	var out []Tool
	for _, td := range s.commandToolDefs(nil, user, st) {
		out = append(out, td.Tool)
	}
	if pull, ok := s.app.pullToolDef(nil, user, st); ok {
		out = append(out, pull.Tool)
	}
	return out
}

// ItemTools is the session-less form, for a caller that only wants to know
// what this store contributes. Its search then cannot be cancelled, which is
// the honest consequence of having nothing to cancel it WITH — every caller
// inside a turn goes through ItemToolsWithSession.
func (s storeSource) ItemTools(user, itemID string) []AgentToolDef {
	return s.itemTools(nil, user, itemID)
}

// itemTools builds the per-store tools, carrying the session so the one long
// operation among them — a regex scan of the whole store, budgeted in MINUTES —
// can be stopped when the turn is. Nil-safe: sess.Context() falls back to an
// uncancellable one.
func (s storeSource) itemTools(sess *ToolSession, user, itemID string) []AgentToolDef {
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
	// found — when the truth is that a Unpack archive command is registered and
	// nobody has clicked it. The agent could not say that, because the commands
	// were invisible to it.
	//
	// Mirrors servitor's shape deliberately: the cheap read-only tool that maps
	// what is THERE (search_<system>_knowledge / get_<system>_facts) is separate
	// from the slow one that reaches the live thing (investigate_<system>). This
	// is the first half. Naming a command is safe in a way running one is not,
	// and it is most of the value: an agent that can say "this folder needs
	// Unpack archive run on it first" has turned a dead end into an instruction.
	commandsTool := AgentToolDef{
		Tool: Tool{
			Name:        "list_" + slug + "_commands",
			Description: fmt.Sprintf("List the commands an admin has registered against the %q file store: what each one does to a folder, and whether it asks a person for input. Read-only and instant: this NAMES them, it does not run them. Reach for it when a folder looks empty, unreadable, or still packaged: the usual reason is that one of these has to be run on it first, and telling the user WHICH one is the useful answer. A command an admin has mapped and approved is run through its own tools, which this list names with the exact line each runs; any other is a person's click in Files.", label),
			Caps:        []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			// Asked from inside a turn, the listing reports only the command
			// tools this agent actually holds, and names the setting that
			// keeps the rest out.
			reach := commandsReach{fullAccess: st.FullAccessFor(user)}
			if turn := TurnToolsFromContext(ctx); turn != nil {
				reach.known, reach.tools = true, map[string]bool{}
				for _, t := range turn {
					reach.tools[t.Name] = true
				}
				if sess != nil && sess.LoadableTools != nil {
					for _, t := range sess.LoadableTools() {
						reach.tools[t.Name] = true
					}
				}
			}
			return describeStoreCommandsFor(s.app.DB, st, reach), nil
		},
	}

	return append([]AgentToolDef{
		{
			Tool: Tool{
				Name:        "list_" + slug,
				Description: fmt.Sprintf("List what is in the %q file store: its subfolders (newest first, with file counts and sizes), or the files inside one of them.%s Call this when you do not already know what is there, the search and read tools take a subfolder name.", label, about),
				Parameters: map[string]ToolParam{
					"within": {Type: "string", Description: "Optional subfolder to list the FILES of. Omit to list the subfolders themselves."},
				},
				Caps: []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
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
				fmt.Fprintf(&b, "Files in %s, newest first, with the period each one covers:\n", within)
				// What period each file covers, from its first and last
				// timestamp, so a question about one day can go to the files
				// that hold it. Bounded by a clock: a head and a tail per file
				// is cheap, but not free across hundreds of files on a slow
				// disk, and the listing must come back.
				spanBudget := time.Now().Add(10 * time.Second)
				spansCut := false
				for i, f := range files {
					if i >= 200 {
						fmt.Fprintf(&b, "…and %d more.\n", len(files)-i)
						break
					}
					span := ""
					if time.Now().Before(spanBudget) {
						span = describeSpan(dir, f)
					} else {
						spansCut = true
					}
					fmt.Fprintf(&b, "- %s: %s, %s%s\n", f.Rel, HumanSize(f.Size), f.Modified.Format("2006-01-02 15:04"), span)
				}
				if spansCut {
					b.WriteString("(Covered periods were not read for the files after the time budget ran out; list a narrower subfolder for those.)\n")
				}
				return b.String(), nil
			},
		},
		{
			Tool: Tool{
				Name:        "search_" + slug,
				Description: fmt.Sprintf("Search the %q file store for a pattern, returning matching lines with a few lines of context.%s This is a regular-expression search over raw lines, NOT a semantic one: search for what would literally BE in the text (an error string, an id, a stack frame, a config key), not for a description of it. Compressed .gz files are searched too. Results are capped: each file contributes at most per_file matches so one noisy file cannot crowd out the rest, and the reply says where anything was cut. To learn how OFTEN something occurs, or whether it occurs at all, use count=true: per-file totals over every file, no lines.", label, about),
				Parameters: map[string]ToolParam{
					"pattern":     {Type: "string", Description: "A regular expression. A plain string is a valid one. Prefer something distinctive over something common."},
					"patterns":    {Type: "array", Items: &ToolParam{Type: "string"}, Description: PatternsHelp},
					"exclude":     {Type: "string", Description: "Optional regular expression for lines to leave out: a line the pattern matched is dropped, not shown or counted, when this matches it too. The way to say \"but not\", which a pattern cannot: pattern \"ERROR\" with exclude \"healthcheck|/status\". Follows ignore_case."},
					"within":      {Type: "string", Description: "Optional subfolder to search. Omit to search the whole store. Use list_" + slug + " to see what is there."},
					"file_glob":   {Type: "string", Description: "Optional filename filter, e.g. \"*.log\" or \"catalina*\". Narrows a large tree."},
					"ignore_case": {Type: "boolean", Description: "Match case-insensitively. Default false: use it when hunting a word, not when hunting an identifier."},
					"context":     {Type: "number", Description: "Lines either side of each hit (0-8, default 2)."},
					"record":      {Type: "boolean", Description: RecordHelp},
					"level":       {Type: "string", Description: LevelHelp},
					"since":       {Type: "string", Description: "Optional start of a time window, e.g. \"2026-08-17\" or \"2026-08-17 02:00:00\". Applied to each line's own timestamp; a line with none (a traceback line) takes the time of the last dated line above it. Use this rather than putting dates in the pattern."},
					"until":       {Type: "string", Description: "Optional end of the time window, same formats as since."},
					"over_time":   {Type: "boolean", Description: OverTimeHelp},
					"bucket":      {Type: "string", Description: BucketHelp},
					"group":       {Type: "boolean", Description: "Group the matching lines by shape instead of listing them: numbers, ids, addresses and times are masked, so lines differing only in those are one shape, each returned with its count, the files and times it spans, and one example line, most frequent first and the rarest after. The first call on an unfamiliar folder: pattern \".\" groups every line, \"ERROR|WARN\" with ignore_case just the trouble. Not with count; context and paging do not apply."},
					"count":       {Type: "boolean", Description: "Return per-file match totals only, no lines, over every file with no match cap. The cheap way to answer how often, or whether at all."},
					"per_file":    {Type: "number", Description: fmt.Sprintf("How many matches one file may keep (default %d), so a noisy file cannot crowd out the rest. Lifted automatically when only one file is searched.", defaultPerFile)},
				},
				Caps: []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				dir, err := SubRoot(st.Path, stringArg(args, "within"))
				if err != nil {
					return "", err
				}
				ctxLines := 2
				if n, ok := numArg(args, "context"); ok {
					ctxLines = n
				}
				opts := SearchOpts{
					Pattern:    stringArg(args, "pattern"),
					Patterns:   bundle.PatternsArg(args["patterns"]),
					Exclude:    stringArg(args, "exclude"),
					Glob:       stringArg(args, "file_glob"),
					IgnoreCase: boolArg(args, "ignore_case"),
					Context:    ctxLines,
					CountOnly:  boolArg(args, "count"),
					Group:      boolArg(args, "group"),
					OverTime:   boolArg(args, "over_time"),
					Records:    boolArg(args, "record"),
					Level:      stringArg(args, "level"),
					PerFile:    defaultPerFile,
				}
				if n, ok := numArg(args, "per_file"); ok && n > 0 {
					opts.PerFile = n
				}
				if opts.Bucket, err = bundle.ParseBucket(stringArg(args, "bucket")); err != nil {
					return "", err
				}
				if opts.Since, err = ParseBundleArgTime(stringArg(args, "since")); err != nil {
					return "", err
				}
				if opts.Until, err = ParseBundleArgTime(stringArg(args, "until")); err != nil {
					return "", err
				}
				res, err := Search(sess.Context(), dir, opts)
				if err != nil {
					return "", err
				}
				return searchReply(res, opts, "read_"+slug), nil
			},
		},
		{
			Tool: Tool{
				Name:        "read_" + slug,
				Description: fmt.Sprintf("Read a window of lines from one file in the %q store: use it after a search to see what surrounds a hit. Bounded: it returns a window, never a whole file.", label),
				Parameters: map[string]ToolParam{
					"file":   {Type: "string", Description: "Path of the file, exactly as a search result or listing reported it."},
					"within": {Type: "string", Description: "The subfolder the file is in, if the search reported one."},
					"around": {Type: "number", Description: "Line number to centre the window on. Omit to read from the top."},
					"lines":  {Type: "number", Description: "How many lines to return (default and maximum 400)."},
				},
				Required: []string{"file"},
				Caps:     []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
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
	}, s.evidenceToolDefs(sess, st)...)
}

// describeSpan is the covered-period suffix of one listing line.
func describeSpan(dir string, f LogFile) string {
	first, last, lastKnown, ok := FileSpan(dir, f)
	switch {
	case !ok:
		return ", no timestamps"
	case !lastKnown:
		return ", from " + first.Format("2006-01-02 15:04:05") + " (compressed: its end was not read)"
	default:
		return ", covers " + first.Format("2006-01-02 15:04:05") + " to " + last.Format("2006-01-02 15:04:05")
	}
}

// defaultPerFile is how many matches one file keeps when the caller does not
// say: the same share a bundle search uses (bundle.DefaultPerFile says why).
const defaultPerFile = bundle.DefaultPerFile

// searchReply is the search tool's whole answer, in the shape its mode asks
// for, paged: a reply longer than a page is kept and arrives as its first
// page, and read_output reads on or greps within it without searching again
// (core paged_output.go). The search keeps up to maxMatches for exactly that.
func searchReply(res SearchResult, opts SearchOpts, readTool string) string {
	return SpillOutput(searchText(res, opts, readTool), bundle.SearchPageChars, "read_output")
}

// searchText is searchReply before paging. Several patterns get a line each up
// top, then each one's answer as its own search's would read, the rarest first
// (core/bundle lanes.go).
func searchText(res SearchResult, opts SearchOpts, readTool string) string {
	if len(res.Lanes) > 1 {
		order := bundle.LaneOrder(res.Lanes)
		var b strings.Builder
		b.WriteString(bundle.LanesHead(res.Lanes, order))
		for _, i := range order {
			lo := opts
			lo.Pattern, lo.Patterns = res.Lanes[i].Pattern, nil
			b.WriteString(bundle.LaneSection(lo.Pattern))
			b.WriteString(searchText(res.Lane(i), lo, readTool))
		}
		return b.String()
	}
	var out string
	switch {
	case opts.CountOnly:
		out = renderCounts(res, opts.Pattern)
	case opts.Group && res.Groups.Lines > 0:
		out = renderGroups(res)
	case opts.OverTime && res.Histogram.Lines+res.Histogram.Undated > 0:
		out = renderOverTime(res, opts)
	case len(res.Matches) == 0:
		out = renderNoMatches(res) + bundle.LevelNote(opts.Level)
	default:
		out = renderMatches(res, readTool)
	}
	return out
}

// renderCounts is the count-only answer: per-file totals, busiest first.
func renderCounts(res SearchResult, pattern string) string {
	var b strings.Builder
	b.WriteString(res.Budget.Counts(pattern, res.Scanned, res.Considered))
	if res.Untimed > 0 {
		fmt.Fprintf(&b, "%d file(s) carry no timestamps, so the time window left them out.\n", res.Untimed)
	}
	b.WriteString(renderExcluded(res))
	b.WriteString(renderGaps(res))
	if res.Stopped != "" {
		b.WriteString("INCOMPLETE: " + res.Stopped + ". Totals cover only what was read.\n")
	}
	return b.String()
}

// renderNoMatches is the reply to a search that shows nothing, which is never
// just "nothing matched" when something else explains the empty page: paging
// past the end, a window over untimed files, a search that did not finish, an
// exclude that hid every match, or files that could not be read to the end.
// Each of those is a reason the pattern may still be there.
func renderNoMatches(res SearchResult) string {
	if res.Untimed > 0 && res.Untimed == res.Scanned {
		return fmt.Sprintf("Nothing matched inside the time window, but none of the %d file(s) searched carry timestamps the window could be applied to. Search again without since/until.", res.Untimed)
	}
	// A search that ran out of time and found nothing is NOT "nothing
	// matched": saying so would report an absence nobody established.
	if res.Stopped != "" {
		return "No matches in what was read, but the search did not finish: " + res.Stopped +
			". Narrow it with `within` or `file_glob` and try again: do not conclude this pattern is absent.\n" + renderGaps(res)
	}
	// Matches that exclude took out are not an absence: the pattern is
	// there, the filter hid it.
	if res.Excluded > 0 {
		return fmt.Sprintf("Nothing left after exclude: %d line(s) matched the pattern and every one also matched exclude. Narrow exclude, or drop it, to see them.\n", res.Excluded) + renderGaps(res)
	}
	if gaps := renderGaps(res); gaps != "" {
		return "Nothing matched in what was read, but not all of it could be:\n" + gaps +
			"Do not conclude this pattern is absent from those files."
	}
	return "Nothing matched. The pattern is a regular expression over raw lines: check it appears literally, try ignore_case, widen file_glob, or drop `within` to search the whole store."
}

// renderGroups is a grouped search's reply: the shapes, then everything that
// says they are not the whole folder.
func renderGroups(res SearchResult) string {
	var b strings.Builder
	b.WriteString(res.Groups.Render(maxLineRunes))
	fmt.Fprintf(&b, "(%d of %d files read.)\n", res.Scanned, res.Considered)
	b.WriteString(renderExcluded(res))
	if res.Untimed > 0 {
		fmt.Fprintf(&b, "%d file(s) carry no timestamps, so the time window left them out.\n", res.Untimed)
	}
	b.WriteString(renderGaps(res))
	if res.Stopped != "" {
		b.WriteString("INCOMPLETE: " + res.Stopped + ". The shapes and counts cover only what was read.\n")
	}
	return b.String()
}

// renderOverTime is an over-time search's reply: the rows of counts, then
// everything that says they are not the whole folder.
func renderOverTime(res SearchResult, opts SearchOpts) string {
	var b strings.Builder
	b.WriteString(res.Histogram.Render(fmt.Sprintf("%q", opts.Pattern), opts.Bucket))
	fmt.Fprintf(&b, "(%d of %d files read.)\n", res.Scanned, res.Considered)
	b.WriteString(renderExcluded(res))
	if res.Untimed > 0 {
		fmt.Fprintf(&b, "%d file(s) carry no timestamps, so the time window left them out.\n", res.Untimed)
	}
	b.WriteString(renderGaps(res))
	if res.Stopped != "" {
		b.WriteString("INCOMPLETE: " + res.Stopped + ". The counts cover only what was read.\n")
	}
	return b.String()
}

// renderExcluded says how many matches the exclude pattern removed, so the
// totals beside it read as what is left after the filter rather than as every
// occurrence. Empty when it removed none.
func renderExcluded(res SearchResult) string {
	if res.Excluded == 0 {
		return ""
	}
	return fmt.Sprintf("%d more line(s) matched the pattern but were left out by exclude, and are not in these totals.\n", res.Excluded)
}

// renderGaps says what a search could not see inside the files it did read: a
// line too long to search whole, and a file whose read failed partway (a
// truncated .gz, an I/O error). Either is a place a match could be that the
// search never looked at, so it goes out with every kind of answer, "nothing
// matched" most of all. Empty when there were none.
func renderGaps(res SearchResult) string {
	var b strings.Builder
	if res.LongLines > 0 {
		fmt.Fprintf(&b, "%d line(s) were over %d MB and were searched only in their first %d MB.\n",
			res.LongLines, maxLineBytes>>20, maxLineBytes>>20)
	}
	if n := len(res.Unreadable); n > 0 {
		list, more := res.Unreadable, ""
		if n > 8 {
			list, more = list[:8], fmt.Sprintf("; and %d more", n-8)
		}
		fmt.Fprintf(&b, "INCOMPLETE: %d file(s) could not be read to the end, so nothing past that point was searched: %s%s.\n",
			n, strings.Join(list, "; "), more)
	}
	return b.String()
}

// renderMatches formats hits for a model: grouped by file, line-numbered,
// and explicit about truncation.
func renderMatches(res SearchResult, readTool string) string {
	var b strings.Builder
	// Everything that qualifies the matches comes first: a long reply
	// arrives as its first page, and these must be on it.
	b.WriteString(res.Budget.Summary(res.Scanned, res.Considered, "file_glob"))
	if res.Untimed > 0 {
		fmt.Fprintf(&b, "%d file(s) carry no timestamps, so the time window left them out.\n", res.Untimed)
	}
	b.WriteString(renderExcluded(res))
	b.WriteString(renderGaps(res))
	// Different sentence from the cap, deliberately. The cap means there
	// are more matches; this means part of the store was never read, so
	// an absence here is not evidence of absence.
	if res.Stopped != "" {
		b.WriteString("INCOMPLETE: " + res.Stopped + ". Files not read may contain matches. Narrow with `within` or `file_glob` rather than treating this as the whole picture.\n")
	}
	// What it cost, when it cost something. A search that took a minute
	// and said nothing about why reads as a system that hangs at random;
	// "3140 files, 12 GB, 71s" reads as a big bundle, which is what it
	// is. Under five seconds this is noise, so it is omitted.
	if res.Elapsed > 5*time.Second {
		fmt.Fprintf(&b, "(read %d files, %d MB, in %s)\n", res.Scanned, res.Bytes>>20, res.Elapsed.Round(time.Second))
	}
	file := ""
	for _, m := range res.Matches {
		if m.File != file {
			file = m.File
			fmt.Fprintf(&b, "\n%s:\n", file)
		}
		above, below := bundle.RecordNotes(m.Record, readTool)
		b.WriteString(indentNote(above))
		for i, before := range m.Before {
			fmt.Fprintf(&b, "  %d- %s\n", m.Line-len(m.Before)+i, before)
		}
		fmt.Fprintf(&b, "  %d> %s\n", m.Line, m.Text)
		for i, after := range m.After {
			fmt.Fprintf(&b, "  %d- %s\n", m.Line+i+1, after)
		}
		b.WriteString(indentNote(below))
	}
	return b.String()
}

// indentNote sets a note under a file's matches at their indent.
func indentNote(note string) string {
	if note == "" {
		return ""
	}
	return "  " + strings.ReplaceAll(strings.TrimSuffix(note, "\n"), "\n", "\n  ") + "\n"
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
