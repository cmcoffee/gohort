// Which tools are bound to the directory they were authored in.
//
// A shell tool can reach its script two ways. Either the script lives in the
// tool RECORD (ScriptBody), and the framework rewrites it into whatever
// workspace the tool runs in at every dispatch — that tool travels, survives a
// workspace wipe, and does not care which directory it lands in. Or the
// command_template points at {workspace_dir}/<file> and the file exists only on
// disk, in which case the tool works exactly as long as it keeps running in the
// directory it was authored in.
//
// Authoring closed most of that gap: tool_def now refuses a {workspace_dir}
// reference whose file isn't there, and captures the script into the record
// when it is. Two kinds of record predate or escape that:
//
//   - written before the capture existed, and never re-saved since
//   - a MULTI-FILE or sub-path reference ({workspace_dir}/lib/foo.py), which
//     the single ScriptBody slot cannot represent, so it stays disk-resident
//     by design
//
// Those are the tools that would break if a workspace ever moved beneath them
// — the question that has to be answered BEFORE changing what workspace a run
// gets, not discovered afterwards by a tool that stopped working.
//
// This is a read-only survey. It changes nothing, and it deliberately reports
// names and templates rather than script contents.
package core

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// WorkspaceBoundTool is one tool that depends on its directory.
type WorkspaceBoundTool struct {
	Owner    string   // username whose pool holds it ("" for the shared pool)
	Name     string   // tool name
	Template string   // the command_template that names the path
	Refs     []string // the {workspace_dir} paths it references
	Reason   string   // why it is bound: "no script in record" | "multi-file or sub-path reference"
	Shared   bool     // published to the deployment-wide pool
}

// workspaceRefPattern finds {workspace_dir}/<path> references in a template.
// The path stops at whitespace or a quote — a template is a shell line, so a
// following argument is not part of the filename.
var workspaceRefPattern = regexp.MustCompile(`\{workspace_dir\}/([^\s"']+)`)

// WorkspaceBoundTools surveys every user's persistent pool and returns the
// tools that would not survive their workspace changing. db is the root store.
func WorkspaceBoundTools(db Database) []WorkspaceBoundTool {
	if db == nil {
		return nil
	}
	var out []WorkspaceBoundTool
	seen := map[string]bool{} // owner+name, so the shared-pool walk can't double-report
	for _, u := range AuthListUsers(db) {
		user := strings.TrimSpace(u.Username)
		if user == "" {
			continue
		}
		for _, p := range LoadPersistentTempTools(UserDB(db, user), user) {
			if b, ok := workspaceBinding(p.Tool); ok {
				b.Owner, b.Shared = user, p.Shared
				if key := user + "\x00" + b.Name; !seen[key] {
					seen[key] = true
					out = append(out, b)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Owner != out[j].Owner {
			return out[i].Owner < out[j].Owner
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// workspaceBinding decides whether one tool is bound to its directory, and why.
// A tool whose script is in the record is NOT bound, however many
// {workspace_dir} references its template carries for output paths — the
// framework redeploys the script wherever it runs.
func workspaceBinding(t TempTool) (WorkspaceBoundTool, bool) {
	refs := workspaceRefs(t.CommandTemplate)
	if len(refs) == 0 {
		return WorkspaceBoundTool{}, false
	}
	scripts := scriptLikeRefs(refs)
	if len(scripts) == 0 {
		// Only scratch paths (data.json, out.png). Those are outputs, not the
		// tool's own code, and they follow the workspace by design.
		return WorkspaceBoundTool{}, false
	}
	b := WorkspaceBoundTool{Name: t.Name, Template: t.CommandTemplate, Refs: scripts}
	switch {
	case strings.TrimSpace(t.ScriptBody) == "":
		b.Reason = "no script in record"
	case len(scripts) > 1 || strings.ContainsAny(scripts[0], "/\\"):
		// Capture handles exactly one flat filename; anything else stays on
		// disk even though the record carries a body for the one it did catch.
		b.Reason = "multi-file or sub-path reference"
	default:
		return WorkspaceBoundTool{}, false // the record carries it; it travels
	}
	return b, true
}

func workspaceRefs(template string) []string {
	var out []string
	for _, m := range workspaceRefPattern.FindAllStringSubmatch(template, -1) {
		if len(m) > 1 && m[1] != "" {
			out = append(out, m[1])
		}
	}
	return out
}

// scriptLikeRefs keeps the references that look like CODE. A template pointing
// at {workspace_dir}/out.png is naming where to write, not what to run, and a
// tool is not bound to its directory for that.
func scriptLikeRefs(refs []string) []string {
	var out []string
	for _, r := range refs {
		switch strings.ToLower(r[strings.LastIndex(r, ".")+1:]) {
		case "py", "sh", "bash", "jq", "awk", "pl", "rb", "js", "ts", "php", "r", "lua":
			out = append(out, r)
		}
	}
	return out
}

// FormatWorkspaceBoundTools renders the survey for a human. Empty when nothing
// is bound, so a caller can print it unconditionally.
func FormatWorkspaceBoundTools(list []WorkspaceBoundTool) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d tool(s) depend on the directory they were authored in:\n", len(list))
	for _, t := range list {
		owner := t.Owner
		if t.Shared {
			owner += " (shared)"
		}
		fmt.Fprintf(&b, "  %-28s %-22s %s\n      refs: %s\n      %s\n",
			t.Name, owner, t.Reason, strings.Join(t.Refs, ", "), t.Template)
	}
	b.WriteString("\nEach would stop working if its run moved to a different workspace. " +
		"Re-saving one through tool_def captures a single flat script into the record; " +
		"a multi-file reference needs its files moved into the record another way.")
	return b.String()
}

func init() {
	RegisterMaintenanceFunc(
		"survey_workspace_bound_tools",
		"Survey workspace-bound tools",
		"Read-only. Lists tools whose command_template runs a script from "+
			"{workspace_dir} that is not carried in the tool record — the ones that "+
			"would break if a run were given a different workspace. Reports names, "+
			"owners and templates to the log; changes nothing.",
		func(ctx context.Context) int {
			list := WorkspaceBoundTools(RootDB)
			if len(list) == 0 {
				Log("[workspace-bound] no tools depend on their authoring directory")
				return 0
			}
			Log("[workspace-bound]\n%s", FormatWorkspaceBoundTools(list))
			return len(list)
		},
	)
}
