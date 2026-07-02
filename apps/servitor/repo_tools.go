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
const errRepoNotLoaded = "REPOSITORY NOT LOADED: this repository's files are not currently ingested, so search and read cannot run. This is NOT evidence the code is absent. Stop here and tell the user to run Map System (which re-clones) or Refresh Repo. Do not guess file names, paths, or structure from prior knowledge."

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

func repoIntArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
