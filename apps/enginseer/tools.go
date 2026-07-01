package enginseer

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const maxSearchHits = 60

// repoTools builds the read/search tools bound to one (user, repo). They are
// passed as app tools to the investigator run, so the agent searches and reads
// the encrypted store directly (no path leaves the store as plaintext on disk).
func repoTools(user, repoID string) []AgentToolDef {
	tools := []AgentToolDef{
		{
			Tool: Tool{
				Name:        "search_code",
				Description: "Search every file in the repository for a string or symbol (case-insensitive substring). Your first move for almost any question: find where a name, table, route, config key, or a literal log string appears. Returns matching lines with their file path and line number.",
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
				hits := searchRepo(user, repoID, query, maxSearchHits)
				if len(hits) == 0 {
					return "No matches.", nil
				}
				var b strings.Builder
				for _, h := range hits {
					fmt.Fprintf(&b, "%s:%d: %s\n", h.Path, h.Line, h.Text)
				}
				if len(hits) >= maxSearchHits {
					b.WriteString(fmt.Sprintf("(showing first %d matches — narrow the query for more)\n", maxSearchHits))
				}
				return b.String(), nil
			},
		},
		{
			Tool: Tool{
				Name:        "read_file",
				Description: "Read a file from the repository by its path (as shown in search_code results). Optionally limit to a line range to focus on the relevant section. Use this to see the real code around a hit before answering.",
				Parameters: map[string]ToolParam{
					"path":       {Type: "string", Description: "Repo-relative file path, e.g. 'internal/auth/token.go'."},
					"start_line": {Type: "integer", Description: "Optional 1-based first line to return."},
					"end_line":   {Type: "integer", Description: "Optional 1-based last line to return."},
				},
				Required: []string{"path"},
			},
			Handler: func(args map[string]any) (string, error) {
				path, _ := args["path"].(string)
				content, ok := readRepoFile(user, repoID, path)
				if !ok {
					return "", fmt.Errorf("file not found: %s", path)
				}
				start := intArg(args, "start_line")
				end := intArg(args, "end_line")
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
				Description: "List the immediate contents (files and subdirectories) of a directory in the repository. Use it to orient yourself in the layout. Pass an empty path for the repository root.",
				Parameters: map[string]ToolParam{
					"path": {Type: "string", Description: "Repo-relative directory path, e.g. 'internal/auth'. Empty for the root."},
				},
			},
			Handler: func(args map[string]any) (string, error) {
				path, _ := args["path"].(string)
				entries := listRepoDir(user, repoID, path)
				if len(entries) == 0 {
					return "(empty or no such directory)", nil
				}
				return strings.Join(entries, "\n"), nil
			},
		},
	}
	// Plus the code-map recording + recall tools (per-repo scoped graph).
	return append(tools, mapTools(repoID)...)
}

// intArg reads an integer tool argument (JSON numbers decode as float64).
func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}
