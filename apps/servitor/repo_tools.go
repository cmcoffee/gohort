// repo_tools.go — the Type=="repo" worker probe tools. These replace the SSH
// run_command / run_pty / read_log tools: instead of executing commands, the
// repo worker searches and reads the encrypted code store. The recording, plan,
// and file tools servitor already has are reused unchanged.
package servitor

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bundle"
)

// repoMaxSearchHits is how many hits one code search keeps: the log search's
// limit. The reply arrives as its first page whatever it holds, so keeping
// more costs the agent nothing until it reads on, and read_output reads on or
// greps within all of them where a short limit made it search again.
const repoMaxSearchHits = bundle.MaxSearchHits

// repoPerFile is one file's share of a code search's hits, so one file
// that uses a name everywhere cannot fill the reply before the others are
// reached.
const repoPerFile = 10

// repoSearchParams adds the options every search_code shares to a tool's own
// parameters (its query, and the repo or member it names), so the variants
// describe the same option the same way. They are the log search's options
// (core/bundle), less the ones that need timestamps.
func repoSearchParams(own map[string]ToolParam) map[string]ToolParam {
	own["patterns"] = ToolParam{Type: "array", Items: &ToolParam{Type: "string"}, Description: "Several texts to find in ONE pass, instead of query or with it, up to 8, e.g. [\"GetUser(\", \"users_table\", \"/api/v1/users\"]. Each is answered in its own section as if searched alone, with its own count and hits. Literal unless regex is set. Use it instead of searching several times in a row."}
	own["regex"] = ToolParam{Type: "boolean", Description: "Treat query (and patterns) as regular expressions (case-insensitive), e.g. \"func (Get|Set)User\" or \"timeout.*retry\". Default false: literal text, so a dot or a bracket matches itself."}
	own["exclude"] = ToolParam{Type: "string", Description: "Optional regular expression for lines to leave out: a line the query matched is dropped when this matches it too, e.g. \"^\\s*//\" to skip comment lines."}
	own["glob"] = ToolParam{Type: "string", Description: "Optional file filter, e.g. \"*.go\", \"internal/auth/*\", or a fragment of the path such as \"handlers\". Empty searches every file."}
	own["context"] = ToolParam{Type: "integer", Description: "Lines either side of each hit (0-8, default 0)."}
	own["count"] = ToolParam{Type: "boolean", Description: "Return per-file match totals only, no lines, over every file, busiest first: how often and where. Not with group."}
	own["group"] = ToolParam{Type: "boolean", Description: "Group the matching lines by shape instead of listing them: numbers, ids and quoted values are masked, so lines differing only in those are one shape, each with its count and one example, most frequent first. Shows every distinct way a name is used. Not with count."}
	own["per_file"] = ToolParam{Type: "integer", Description: fmt.Sprintf("How many hits one file may keep (default %d), so one file cannot crowd out the rest. Lifted when only one file is in scope.", repoPerFile)}
	return own
}

// repoQueryFromArgs reads a search_code call's arguments.
func repoQueryFromArgs(args map[string]any, maxHits int) repoQuery {
	q := repoQuery{MaxHits: maxHits, PerFile: repoPerFile}
	q.Text = StringArg(args, "query")
	q.Patterns = bundle.PatternsArg(args["patterns"])
	q.Regex = BoolArg(args, "regex")
	q.Exclude = StringArg(args, "exclude")
	q.Glob = strings.TrimSpace(StringArg(args, "glob"))
	q.Context = min(max(IntArg(args, "context"), 0), 8)
	q.Count = BoolArg(args, "count")
	q.Group = BoolArg(args, "group")
	if n := IntArg(args, "per_file"); n > 0 {
		q.PerFile = n
	}
	return q
}

// renderRepoSearch is a code search's reply, the log search's (core/bundle
// render.go) with the texts named as they were asked for. A search of one
// text that found nothing says what that establishes: nothing, where exclude
// hid the matches or glob kept the search to part of the repo, and an absence
// otherwise.
func renderRepoSearch(res bundle.SearchResult, q repoQuery) string {
	texts, err := q.texts()
	if err != nil {
		return err.Error()
	}
	if len(res.Lanes) == 0 && !q.Count && !q.Group && res.Total == 0 && res.Excluded == 0 {
		if q.Glob != "" {
			return fmt.Sprintf("No matches for %q in files matching %q. The rest of the repository was not searched: drop glob to search all of it.\n", texts[0], q.Glob)
		}
		return fmt.Sprintf("No matches for %q in %d files. The text is not in the ingested source: do not infer that it is there anyway.\n", texts[0], res.Scanned)
	}
	shown := q
	shown.Regex = true // named as asked, not as quoted for the search
	return bundle.RenderSearch(res, shown.bundleQuery(texts), "read_file")
}

// repoReply pages a code search's reply: the first page arrives, and
// read_output reads on or greps within it without searching again.
func repoReply(text string) string {
	return SpillOutput(text, bundle.SearchPageChars, "read_output")
}

// errRepoNotLoaded is returned by the repo tools when the encrypted store is
// empty — an unambiguous signal so the LLM treats it as "the code isn't
// available" (stop and report) rather than "no such code exists" (fabricate).
// The disambiguation only runs on the empty-result path, so it costs nothing
// when the store is populated.
const errRepoNotLoaded = "REPOSITORY NOT LOADED: this repository's files are not currently ingested, so search and read cannot run. This is NOT evidence the code is absent. Stop here and tell the user to run Refresh (which re-clones and re-maps). Do not guess file names, paths, or structure from prior knowledge."

// repoCodeTools builds the search/read tools bound to one (user, repo appliance),
// decrypting the store in memory (no plaintext leaves it).
func repoCodeTools(user, applianceID string) []AgentToolDef {
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "search_code",
				Description: "Search every file in the repository for a string or symbol (case-insensitive; literal, or a regular expression with regex). Your first move for almost any question: find where a name, table, route, config key, or an exact log-line string appears. Narrow with glob to a part of the tree. Returns matching lines with file path and line number.",
				Parameters: repoSearchParams(map[string]ToolParam{
					"query": {Type: "string", Description: "The text to find: a function/type/table name, a route, a config key, or an exact string from a log line."},
				}),
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				q := repoQueryFromArgs(args, repoMaxSearchHits)
				res, err := searchRepo(ctx, user, applianceID, q)
				if err != nil {
					return "", err
				}
				if res.Scanned == 0 && repoFileCount(user, applianceID) == 0 {
					return errRepoNotLoaded, nil
				}
				return repoReply(renderRepoSearch(res, q)), nil
			},
		},
		{
			Tool: Tool{
				Name:        "read_file",
				Description: "Read a file from the repository by its path (as shown in search_code results). Optionally limit to a line range. Use this to see the real code around a hit before answering.",
				Parameters: map[string]ToolParam{
					"path":       {Type: "string", Description: "Repo-relative file path, e.g. 'internal/auth/token.go'."},
					"start_line": {Type: "integer", Description: "Optional 1-based first line to return."},
					"end_line":   {Type: "integer", Description: "Optional 1-based last line to return."},
				},
				Required: []string{"path"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				path, _ := args["path"].(string)
				content, ok := readRepoFile(user, applianceID, path)
				if !ok {
					if repoFileCount(user, applianceID) == 0 {
						return errRepoNotLoaded, nil
					}
					return "", fmt.Errorf("file not found: %s", path)
				}
				start := repoIntArg(args, "start_line")
				end := repoIntArg(args, "end_line")
				if start <= 0 && end <= 0 {
					return content, nil
				}
				lines := strings.Split(content, "\n")
				if start <= 0 {
					start = 1
				}
				if end <= 0 || end > len(lines) {
					end = len(lines)
				}
				if start > len(lines) {
					return "", fmt.Errorf("start_line %d is past end of file (%d lines)", start, len(lines))
				}
				var b strings.Builder
				for i := start; i <= end; i++ {
					fmt.Fprintf(&b, "%d: %s\n", i, lines[i-1])
				}
				return b.String(), nil
			},
		},
		{
			Tool: Tool{
				Name:        "list_dir",
				Description: "List the immediate contents (files and subdirectories) of a directory in the repository. Use it to orient yourself in the layout. Empty path lists the repository root.",
				Parameters: map[string]ToolParam{
					"path": {Type: "string", Description: "Repo-relative directory path, e.g. 'internal/auth'. Empty for the root."},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				path, _ := args["path"].(string)
				entries := listRepoDir(user, applianceID, path)
				if len(entries) == 0 {
					if repoFileCount(user, applianceID) == 0 {
						return errRepoNotLoaded, nil
					}
					return "(empty or no such directory)", nil
				}
				return strings.Join(entries, "\n"), nil
			},
		},
	}
}

// linkedRepo is one repo appliance resolved for attachment to a SYSTEM
// investigation: whose encrypted store, which repo, and what to call it.
type linkedRepo struct {
	Owner string
	ID    string
	Name  string
}

// linkedRepoTools builds the code tools a system investigation gets from its
// LinkedRepos. One linked repo binds the standard tools directly, with the
// repo named in each description. Several share ONE set of tools: search_code
// sweeps all of them (hits prefixed with the repo name), and read_file /
// list_dir take a `repo` argument to say which one — same tool NAMES either
// way, so the worker allow-list and the worker's habits hold unchanged.
func linkedRepoTools(repos []linkedRepo) []AgentToolDef {
	switch len(repos) {
	case 0:
		return nil
	case 1:
		r := repos[0]
		tools := repoCodeTools(r.Owner, r.ID)
		for i := range tools {
			tools[i].Tool.Description = strings.TrimSuffix(tools[i].Tool.Description, ".") +
				fmt.Sprintf(". The repository is %q: the source code this system runs.", r.Name)
		}
		return tools
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		names = append(names, r.Name)
	}
	nameList := strings.Join(names, "; ")
	// findLinked resolves the `repo` argument by name or id, case-insensitively.
	findLinked := func(ref string) (linkedRepo, bool) {
		ref = strings.TrimSpace(ref)
		for _, r := range repos {
			if strings.EqualFold(r.Name, ref) || strings.EqualFold(r.ID, ref) {
				return r, true
			}
		}
		return linkedRepo{}, false
	}
	return []AgentToolDef{
		{
			Tool: Tool{
				Name: "search_code",
				Description: "Search the source code this system runs (repositories: " + nameList + ") for a string or symbol (case-insensitive; literal, or a regular expression with regex). " +
					"Your first move for tracing behavior to source: an exact log-line string, a config key, a function or table name. Narrow with glob to a part of the tree. Returns matching lines as repo, file path, and line number.",
				Parameters: repoSearchParams(map[string]ToolParam{
					"query": {Type: "string", Description: "The text to find: a function/type/table name, a route, a config key, or an exact string from a log line."},
				}),
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				q := repoQueryFromArgs(args, repoMaxSearchHits)
				if _, err := q.texts(); err != nil {
					return "", err
				}
				// One section per repository: each is searched, limited and
				// cut on its own, and says so on its own.
				var b strings.Builder
				loaded := 0
				for _, r := range repos {
					if repoFileCount(r.Owner, r.ID) == 0 {
						continue // not ingested; reported below only if NOTHING is
					}
					loaded++
					res, err := searchRepo(ctx, r.Owner, r.ID, q)
					if err != nil {
						return "", err
					}
					fmt.Fprintf(&b, "=== repository %s ===\n%s\n", r.Name, renderRepoSearch(res, q))
				}
				if loaded == 0 {
					return errRepoNotLoaded, nil
				}
				return repoReply(b.String()), nil
			},
		},
		{
			Tool: Tool{
				Name:        "read_file",
				Description: "Read a file from one of this system's linked repositories (" + nameList + ") by its path, as shown in search_code results. Optionally limit to a line range.",
				Parameters: map[string]ToolParam{
					"repo":       {Type: "string", Description: "Which repository, by the name shown in search_code results: " + nameList + "."},
					"path":       {Type: "string", Description: "Repo-relative file path, e.g. 'internal/auth/token.go'."},
					"start_line": {Type: "integer", Description: "Optional 1-based first line to return."},
					"end_line":   {Type: "integer", Description: "Optional 1-based last line to return."},
				},
				Required: []string{"repo", "path"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				ref, _ := args["repo"].(string)
				r, ok := findLinked(ref)
				if !ok {
					return "", fmt.Errorf("no linked repository called %q, the linked repositories are: %s", ref, nameList)
				}
				path, _ := args["path"].(string)
				content, found := readRepoFile(r.Owner, r.ID, path)
				if !found {
					if repoFileCount(r.Owner, r.ID) == 0 {
						return errRepoNotLoaded, nil
					}
					return "", fmt.Errorf("file not found in %s: %s", r.Name, path)
				}
				start := repoIntArg(args, "start_line")
				end := repoIntArg(args, "end_line")
				if start <= 0 && end <= 0 {
					return content, nil
				}
				lines := strings.Split(content, "\n")
				if start <= 0 {
					start = 1
				}
				if end <= 0 || end > len(lines) {
					end = len(lines)
				}
				if start > len(lines) {
					return "", fmt.Errorf("start_line %d is past end of file (%d lines)", start, len(lines))
				}
				var b strings.Builder
				for i := start; i <= end; i++ {
					fmt.Fprintf(&b, "%d: %s\n", i, lines[i-1])
				}
				return b.String(), nil
			},
		},
		{
			Tool: Tool{
				Name:        "list_dir",
				Description: "List the immediate contents of a directory in one of this system's linked repositories (" + nameList + "). Empty path lists that repository's root.",
				Parameters: map[string]ToolParam{
					"repo": {Type: "string", Description: "Which repository, by name: " + nameList + "."},
					"path": {Type: "string", Description: "Repo-relative directory path. Empty for the root."},
				},
				Required: []string{"repo"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				ref, _ := args["repo"].(string)
				r, ok := findLinked(ref)
				if !ok {
					return "", fmt.Errorf("no linked repository called %q, the linked repositories are: %s", ref, nameList)
				}
				path, _ := args["path"].(string)
				entries := listRepoDir(r.Owner, r.ID, path)
				if len(entries) == 0 {
					if repoFileCount(r.Owner, r.ID) == 0 {
						return errRepoNotLoaded, nil
					}
					return "(empty or no such directory)", nil
				}
				return strings.Join(entries, "\n"), nil
			},
		},
	}
}

func repoIntArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
