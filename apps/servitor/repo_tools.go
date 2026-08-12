// repo_tools.go — the Type=="repo" worker probe tools. These replace the SSH
// run_command / run_pty / read_log tools: instead of executing commands, the
// repo worker searches and reads the encrypted code store. The recording, plan,
// and file tools servitor already has are reused unchanged.
package servitor

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const repoMaxSearchHits = 60

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
				Description: "Search every file in the repository for a string or symbol (case-insensitive substring). Your first move for almost any question: find where a name, table, route, config key, or an exact log-line string appears. Returns matching lines with file path and line number.",
				Parameters: map[string]ToolParam{
					"query": {Type: "string", Description: "The text to find — a function/type/table name, a route, a config key, or an exact string from a log line."},
				},
				Required: []string{"query"},
			},
			Handler: func(args map[string]any) (string, error) {
				query, _ := args["query"].(string)
				if strings.TrimSpace(query) == "" {
					return "", fmt.Errorf("query is required")
				}
				hits := searchRepo(user, applianceID, query, repoMaxSearchHits)
				if len(hits) == 0 {
					if repoFileCount(user, applianceID) == 0 {
						return errRepoNotLoaded, nil
					}
					return "No matches.", nil
				}
				var b strings.Builder
				for _, h := range hits {
					fmt.Fprintf(&b, "%s:%d: %s\n", h.Path, h.Line, h.Text)
				}
				if len(hits) >= repoMaxSearchHits {
					fmt.Fprintf(&b, "(showing first %d matches — narrow the query for more)\n", repoMaxSearchHits)
				}
				return b.String(), nil
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
			Handler: func(args map[string]any) (string, error) {
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
			Handler: func(args map[string]any) (string, error) {
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
				fmt.Sprintf(". The repository is %q — the source code this system runs.", r.Name)
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
				Description: "Search the source code this system runs (repositories: " + nameList + ") for a string or symbol (case-insensitive substring). " +
					"Your first move for tracing behavior to source: an exact log-line string, a config key, a function or table name. Returns matching lines as repo, file path, and line number.",
				Parameters: map[string]ToolParam{
					"query": {Type: "string", Description: "The text to find — a function/type/table name, a route, a config key, or an exact string from a log line."},
				},
				Required: []string{"query"},
			},
			Handler: func(args map[string]any) (string, error) {
				query, _ := args["query"].(string)
				if strings.TrimSpace(query) == "" {
					return "", fmt.Errorf("query is required")
				}
				var b strings.Builder
				total, loaded := 0, 0
				for _, r := range repos {
					if repoFileCount(r.Owner, r.ID) == 0 {
						continue // not ingested; reported below only if NOTHING is
					}
					loaded++
					for _, h := range searchRepo(r.Owner, r.ID, query, repoMaxSearchHits) {
						fmt.Fprintf(&b, "[%s] %s:%d: %s\n", r.Name, h.Path, h.Line, h.Text)
						total++
					}
				}
				if loaded == 0 {
					return errRepoNotLoaded, nil
				}
				if total == 0 {
					return "No matches.", nil
				}
				return b.String(), nil
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
			Handler: func(args map[string]any) (string, error) {
				ref, _ := args["repo"].(string)
				r, ok := findLinked(ref)
				if !ok {
					return "", fmt.Errorf("no linked repository called %q — the linked repositories are: %s", ref, nameList)
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
			Handler: func(args map[string]any) (string, error) {
				ref, _ := args["repo"].(string)
				r, ok := findLinked(ref)
				if !ok {
					return "", fmt.Errorf("no linked repository called %q — the linked repositories are: %s", ref, nameList)
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
