// hand_to_builder — an agent passes something it MADE to Builder to be turned
// into a durable artifact (a tool, an agent, a pipeline) or repaired.
//
// The gap this fills: an agent could already dispatch Builder with prose
// (agents action="run"), but prose is where a working script goes to die. The
// agent would paraphrase what it wrote, Builder would reconstruct it from the
// description, and the thing that actually ran was never handed over. This
// passes the artifact itself.
//
// Workspaces are shared per user by default, so Builder can already READ the
// files — "handing over" is about naming them, proving they exist, and framing
// what should happen to them. That is the whole job, and it is why this stays
// a thin composer over the existing dispatch rather than a transport.
//
// Gate: identical to agents(run, agent="Builder") — canDispatchBuilder. This
// adds no reach, only a better shape for reach the agent already had.

package orchestrate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxHandoffFiles caps how many files ride one handoff. Builder reads these
// into its context; a runaway list would crowd out the brief that explains
// them.
const maxHandoffFiles = 8

// handoffPreviewBytes is how much of each file is inlined into the brief.
// Enough for Builder to recognize the shape and decide, not so much that a
// large script eats the turn — it can read the full file from the workspace.
const handoffPreviewBytes = 1200

// handToBuilderTool builds the tool def, or nil when this agent may not reach
// Builder. Returning nil keeps the catalog honest: an agent that cannot
// dispatch Builder never sees a tool whose entire purpose is dispatching
// Builder.
func handToBuilderTool(t *chatTurn) *AgentToolDef {
	if t == nil || !t.canDispatchBuilder() {
		return nil
	}
	return &AgentToolDef{
		Tool: Tool{
			Name:        "hand_to_builder",
			Description: "Hand something you MADE to Builder so it becomes durable — a script you wrote and verified turned into a reusable tool, a working request shape turned into an API tool, or a broken tool repaired. Pass the workspace FILES themselves, not a description of them: Builder needs the code that actually ran, not a paraphrase. Use after the thing works, not instead of making it work. For a build with nothing to hand over yet, dispatch Builder normally instead.",
			Parameters: map[string]ToolParam{
				"brief": {
					Type:        "string",
					Description: "What you want built or fixed, and what the files are. State what the artifact DOES, how you verified it, and anything Builder would otherwise have to rediscover (an endpoint that needed a specific header, a parameter that must be a string). Be concrete — this is the whole handover.",
				},
				"files": {
					Type:        "array",
					Items:       &ToolParam{Type: "string"},
					Description: "Workspace filenames to hand over (e.g. [\"fetch_stories.py\"]). Flat names in your workspace root; paths with / are refused. Omit only if there is genuinely no file.",
				},
				"repairs": {
					Type:        "string",
					Description: "(optional) Name of an EXISTING tool this is meant to fix, when the handoff repairs something rather than creating it. Builder edits that tool in place instead of minting a near-duplicate.",
				},
			},
			Required: []string{"brief"},
		},
		Handler: func(args map[string]any) (string, error) {
			return t.handToBuilder(args)
		},
	}
}

// handToBuilder validates the named files, composes the brief, and dispatches
// Builder through the ordinary run path so every existing guard (depth cap,
// dispatch policy, cap accounting) applies unchanged.
func (t *chatTurn) handToBuilder(args map[string]any) (string, error) {
	brief := strings.TrimSpace(stringArg(args, "brief"))
	if brief == "" {
		return "", fmt.Errorf("brief is required — say what you want built or fixed and what the files are")
	}
	names := stringSliceFromArgs(args, "files")
	repairs := strings.TrimSpace(stringArg(args, "repairs"))

	files, warnings, err := t.collectHandoffFiles(names)
	if err != nil {
		return "", err
	}
	if len(files) == 0 && repairs == "" {
		// Nothing concrete to hand over is the case this tool exists to
		// prevent. Redirect rather than silently degrading to a prose
		// dispatch the agent could have made itself.
		return "", fmt.Errorf("no readable files and no `repairs` target — there is nothing to hand over. Write the working file to your workspace first (or name the tool to repair); to ask for a build with nothing made yet, dispatch Builder with agents(action=\"run\", agent=\"Builder\") instead")
	}

	var b strings.Builder
	b.WriteString("A handoff from ")
	b.WriteString(t.agent.Name)
	b.WriteString(".\n\n## What is wanted\n\n")
	b.WriteString(brief)
	b.WriteString("\n")
	if repairs != "" {
		fmt.Fprintf(&b, "\n## Repair an existing tool\n\nThis fixes the EXISTING tool %q. Use tool_def(action=\"update\", name=%q) so its working actions and credential wiring survive — do NOT create a near-duplicate under a new name.\n", repairs, repairs)
	}
	if len(files) > 0 {
		b.WriteString("\n## Files handed over\n\n")
		b.WriteString("These are in the shared workspace and readable now. Previews below are TRUNCATED — read the file for the full contents before building from it.\n")
		for _, f := range files {
			fmt.Fprintf(&b, "\n### %s (%d bytes)\n\n%s\n%s\n%s\n",
				f.name, f.size, DocFence(f.preview), f.preview, DocFence(f.preview))
			if f.truncated {
				fmt.Fprintf(&b, "\n(truncated — read %s for the rest)\n", f.name)
			}
		}
	}
	if len(warnings) > 0 {
		fmt.Fprintf(&b, "\n## Not handed over\n\n%s\n", strings.Join(warnings, "\n"))
	}

	res, err := t.agentsRunAction(map[string]any{
		"agent":   "Builder",
		"message": b.String(),
	})
	if err != nil {
		return "", err
	}
	note := fmt.Sprintf("Handed %d file(s) to Builder.", len(files))
	if len(warnings) > 0 {
		note += fmt.Sprintf(" %d skipped.", len(warnings))
	}
	return note + "\n\n" + res, nil
}

type handoffFile struct {
	name      string
	size      int64
	preview   string
	truncated bool
}

// collectHandoffFiles resolves each name against the caller's workspace,
// returning what could be read plus a human-readable note per rejection.
//
// A missing file is reported, never silently dropped: an agent that thinks it
// handed over a script and did not would let Builder build from the brief
// alone, which is exactly the failure this tool exists to stop.
func (t *chatTurn) collectHandoffFiles(names []string) ([]handoffFile, []string, error) {
	if len(names) == 0 {
		return nil, nil, nil
	}
	dir, err := EnsureWorkspaceDir(t.user)
	if err != nil {
		return nil, nil, fmt.Errorf("workspace unavailable: %w", err)
	}
	var out []handoffFile
	var warn []string
	seen := map[string]bool{}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	for _, raw := range sorted {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		// Flat names only. A path could otherwise walk out of the workspace
		// and hand Builder a file the agent was never meant to reach.
		if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
			warn = append(warn, fmt.Sprintf("- %s: refused (workspace-root filenames only, no paths)", name))
			continue
		}
		if len(out) >= maxHandoffFiles {
			warn = append(warn, fmt.Sprintf("- %s: skipped (only %d files ride one handoff)", name, maxHandoffFiles))
			continue
		}
		full := filepath.Join(dir, name)
		info, serr := os.Stat(full)
		if serr != nil || info.IsDir() {
			warn = append(warn, fmt.Sprintf("- %s: not found in your workspace", name))
			continue
		}
		data, rerr := os.ReadFile(full)
		if rerr != nil {
			warn = append(warn, fmt.Sprintf("- %s: unreadable (%v)", name, rerr))
			continue
		}
		if len(data) == 0 {
			warn = append(warn, fmt.Sprintf("- %s: empty", name))
			continue
		}
		f := handoffFile{name: name, size: info.Size(), preview: string(data)}
		if len(f.preview) > handoffPreviewBytes {
			f.preview = f.preview[:handoffPreviewBytes]
			f.truncated = true
		}
		out = append(out, f)
	}
	return out, warn, nil
}
