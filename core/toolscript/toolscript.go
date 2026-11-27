// Package toolscript is code mode: one tool call that runs a short Python
// program, and the program calls other tools.
//
// A round of an agent loop is one call to the model. The loop already runs a
// round's independent tool calls in parallel, so reading three known files is
// one round. A chain is not: search, then read what the search found, then
// read what that file imports, is a round per link, and on a local model each
// round is seconds to half a minute. A script does the chain in one round and
// returns what matters from it, not every intermediate result.
//
// Only tools that read are callable from a script: no confirmation, no Write,
// Execute or Network capability. A script has nobody to ask, and a write made
// inside one would lose what the direct call gives it (its card, its record,
// its approval). The script itself runs in the sandbox with the network
// closed, in an empty folder of its own, so the tools it is given are its
// only reach: it reads what they read, and nothing leaves.
//
// See docs/design/code-mode.md.
package toolscript

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/gohort/core/sandbox"
)

// Name is the tool's name.
const Name = "tool_script"

const (
	// MaxCalls is the most tool calls one script may make.
	MaxCalls = 50
	// MaxCode is the longest script accepted, in bytes.
	MaxCode = 20000
	// maxOutput is how much of what the script printed comes back: its start
	// and its end, with a note of what was cut between.
	maxOutput = 30000
	// timeout ends a script that is still running. Reads finish in seconds,
	// so a script past this is stuck rather than busy.
	timeout = 2 * time.Minute
)

// New builds tool_script over tools, which a script may call by name. Each
// must only read: an error names one that does not. The tools are called
// through their own handlers, on the context of the tool_script call, so
// they act exactly as a direct call to them does in the same turn.
func New(tools ...AgentToolDef) (AgentToolDef, error) {
	if len(tools) == 0 {
		return AgentToolDef{}, fmt.Errorf("tool_script needs at least one tool to call")
	}
	byName := map[string]AgentToolDef{}
	for _, t := range tools {
		if why := notReadOnly(t); why != "" {
			return AgentToolDef{}, fmt.Errorf("tool_script cannot call %q: %s", t.Tool.Name, why)
		}
		if t.Handler == nil {
			return AgentToolDef{}, fmt.Errorf("tool_script cannot call %q: it has no handler", t.Tool.Name)
		}
		byName[t.Tool.Name] = t
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        Name,
			Description: description(tools),
			Parameters: map[string]ToolParam{
				"code": {Type: "string", Description: "The Python program. Plain code, no code fence."},
			},
			Required: []string{"code"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			code, _ := args["code"].(string)
			return run(ctx, byName, code)
		},
	}, nil
}

// notReadOnly says why t may not be called from a script, or "" when it may.
func notReadOnly(t AgentToolDef) string {
	if t.NeedsConfirm || t.Confirmation != nil {
		return "it asks before it runs, and a script has nobody to ask"
	}
	if len(t.Tool.Caps) == 0 {
		return "it does not say what it does (no capabilities declared)"
	}
	for _, c := range t.Tool.Caps {
		if c != CapRead {
			return "it does more than read (" + string(c) + ")"
		}
	}
	return ""
}

// description is what the model is told: what a script is for, how it calls
// a tool, and each tool it may call with its arguments. Built in a fixed
// order so the same tools give the same text every turn.
func description(tools []AgentToolDef) string {
	var b strings.Builder
	b.WriteString("Run a short Python program that calls the tools listed below and prints what you need from their results, in one step. ")
	b.WriteString("Use it for a chain of lookups, where each depends on the one before (search, then read the files the search found), or to keep only the relevant part of large results. ")
	b.WriteString("A single lookup is simpler as a direct call.\n\n")
	b.WriteString("call_tool(name, **args) is already imported. It returns the tool's text output and raises an exception saying why when the call fails. ")
	b.WriteString("print() is what comes back to you; nothing else does, and nothing carries over from one script to the next. ")
	b.WriteString(fmt.Sprintf("At most %d tool calls per script. The program has no network and no files of its own: the tools are its only way to read anything.\n\n", MaxCalls))
	b.WriteString("Tools it can call:\n")
	sorted := append([]AgentToolDef(nil), tools...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Tool.Name < sorted[j].Tool.Name })
	for _, t := range sorted {
		b.WriteString("- " + signature(t.Tool))
		if s := firstSentence(t.Tool.Description); s != "" {
			b.WriteString(": " + s)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nExample:\n")
	b.WriteString(example(sorted[0].Tool))
	return b.String()
}

// example is a two-line program around the first tool: a call with its
// required arguments, and printing part of the result.
func example(t Tool) string {
	call := fmt.Sprintf("call_tool(%q", t.Name)
	for _, r := range t.Required {
		if _, ok := t.Parameters[r]; ok {
			call += ", " + r + "=..."
		}
	}
	return "out = " + call + ")\nfor line in out.splitlines()[:20]:\n    print(line)"
}

// signature is call_tool's form for one tool: its name, then its arguments,
// required ones first in their declared order, optional ones after with a
// "?", each with its type and, for a fixed set of values, the values.
func signature(t Tool) string {
	required := map[string]bool{}
	var names []string
	for _, r := range t.Required {
		if _, ok := t.Parameters[r]; ok && !required[r] {
			required[r] = true
			names = append(names, r)
		}
	}
	var optional []string
	for k := range t.Parameters {
		if !required[k] {
			optional = append(optional, k)
		}
	}
	sort.Strings(optional)
	names = append(names, optional...)
	var parts []string
	for _, n := range names {
		p := t.Parameters[n]
		s := n
		if !required[n] {
			s += "?"
		}
		typ := p.Type
		if len(p.Enum) > 0 {
			typ = strings.Join(quoted(p.Enum), "|")
		}
		if typ != "" {
			s += ": " + typ
		}
		parts = append(parts, s)
	}
	return fmt.Sprintf("call_tool(%q", t.Name) + prefixed(parts) + ")"
}

func prefixed(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return ", " + strings.Join(parts, ", ")
}

func quoted(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = fmt.Sprintf("%q", v)
	}
	return out
}

// firstSentence is a tool description's first sentence, which is what it
// does; the rest is advice on using it directly.
func firstSentence(desc string) string {
	desc = strings.TrimSpace(desc)
	if i := strings.Index(desc, ". "); i >= 0 {
		desc = desc[:i+1]
	}
	if i := strings.IndexByte(desc, '\n'); i >= 0 {
		desc = desc[:i]
	}
	return strings.TrimSpace(desc)
}

// launcher runs the model's program (script.py, beside it) with call_tool in
// scope, so a traceback names script.py and its own line numbers. A name
// that is not one of the script's tools is refused here, saying which are:
// the hook would refuse it too, but in words meant for an app's author.
const launcher = `import json, sys
from gohort import call_tool as _call_tool
_names = json.loads(%s)
def call_tool(name, **args):
    if name not in _names:
        raise ValueError("no tool named %%r here; a script can call %%s" %% (name, ", ".join(_names)))
    return _call_tool(name, **args)
sys.argv = ["script.py"]
with open("script.py") as f:
    src = f.read()
exec(compile(src, "script.py", "exec"), {"__name__": "__main__", "call_tool": call_tool})
`

// calls counts and names the tool calls one script makes.
type calls struct {
	mu     sync.Mutex
	n      int
	byName map[string]int
	failed []string
}

func (c *calls) take(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n >= MaxCalls {
		return fmt.Errorf("this script has made %d tool calls, the most one script may", MaxCalls)
	}
	c.n++
	c.byName[name]++
	return nil
}

func (c *calls) fail(name string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failed = append(c.failed, name+": "+oneLine(err.Error()))
}

// summary is the line after the output: how many calls, of which tools.
func (c *calls) summary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == 0 {
		return "(The script made no tool calls.)"
	}
	var names []string
	for k := range c.byName {
		names = append(names, k)
	}
	sort.Strings(names)
	var parts []string
	for _, k := range names {
		parts = append(parts, fmt.Sprintf("%s x%d", k, c.byName[k]))
	}
	s := fmt.Sprintf("(%d tool call", c.n)
	if c.n != 1 {
		s += "s"
	}
	s += ": " + strings.Join(parts, ", ") + ".)"
	for _, f := range c.failed {
		s += "\nFailed: " + f
	}
	return s
}

// run runs code in the sandbox, its tool calls going to tools.
func run(ctx context.Context, tools map[string]AgentToolDef, code string) (string, error) {
	if strings.TrimSpace(code) == "" {
		return "", fmt.Errorf("code is empty: pass the Python program to run")
	}
	if len(code) > MaxCode {
		return "", fmt.Errorf("the program is %d bytes, over the %d a script may be: split the work, or make direct calls", len(code), MaxCode)
	}
	turn := ToolSessionFromContext(ctx)
	if turn == nil || strings.TrimSpace(turn.Username) == "" {
		return "", fmt.Errorf("tool_script runs only inside a conversation")
	}
	dir, cleanup, err := scriptDir(turn)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "script.py"), []byte(code), 0o600); err != nil {
		return "", fmt.Errorf("could not write the script: %v", err)
	}
	names, _ := json.Marshal(sortedNames(tools))
	if err := os.WriteFile(filepath.Join(dir, "run.py"), []byte(fmt.Sprintf(launcher, strconv.Quote(string(names)))), 0o600); err != nil {
		return "", fmt.Errorf("could not write the script: %v", err)
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	made := &calls{byName: map[string]int{}}
	// The hook gets a session of its own holding nothing but the way to
	// these tools: the turn's session is shared with the round's other
	// calls, and one that could reach tools through the hook would let any
	// script in the turn do it.
	hookSess := &ToolSession{Ctx: ctx, Username: turn.Username}
	hookSess.CallTool = func(name string, args map[string]any) (string, error) {
		t, ok := tools[name]
		if !ok {
			return "", fmt.Errorf("no tool named %q here; a script can call %s", name, strings.Join(sortedNames(tools), ", "))
		}
		if err := made.take(name); err != nil {
			return "", err
		}
		out, err := t.Handler(ctx, args)
		if err != nil {
			made.fail(name, err)
		}
		return out, err
	}
	caps := make([]string, 0, len(tools))
	for _, n := range sortedNames(tools) {
		caps = append(caps, "tool:"+n)
	}
	res := sandbox.RunSandboxedShellIn(netgate.WithWorkspaceNetwork(ctx, false), sandbox.ShellRun{
		Command:          "python3 run.py",
		WorkspaceDir:     dir,
		HookCapabilities: caps,
		HookSession:      hookSess,
		MaxOutput:        maxOutput * 4,
	})
	out := trimOutput(strings.TrimRight(res.Output, "\n"))
	switch {
	case res.TimedOut || ctx.Err() == context.DeadlineExceeded:
		out = joinNonEmpty(out, fmt.Sprintf("The script was stopped after %s.", timeout))
	case res.Err != nil && ctx.Err() != nil:
		return "", ctx.Err()
	case res.Err != nil:
		out = joinNonEmpty(out, "The script failed: "+oneLine(res.Err.Error()))
	}
	if out == "" {
		out = "(The script printed nothing. print() what you need.)"
	}
	return out + "\n\n" + made.summary(), nil
}

// scriptDir is an empty folder for one script, under the session's
// workspace (the sandbox does not see the host's temp folder), removed
// afterwards.
func scriptDir(sess *ToolSession) (string, func(), error) {
	base := sess.WorkspaceDir
	if base == "" {
		ws, err := EnsureWorkspaceDir(sess.Username)
		if err != nil {
			return "", nil, fmt.Errorf("no folder to run the script in: %v", err)
		}
		base = ws
	}
	dir, err := os.MkdirTemp(base, ".tool_script-")
	if err != nil {
		return "", nil, fmt.Errorf("no folder to run the script in: %v", err)
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}

func sortedNames(tools map[string]AgentToolDef) []string {
	names := make([]string, 0, len(tools))
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// trimOutput keeps the start and end of a long output.
func trimOutput(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	head, tail := strings.ToValidUTF8(s[:maxOutput*2/3], ""), strings.ToValidUTF8(s[len(s)-maxOutput/3:], "")
	return head + fmt.Sprintf("\n\n[... %d bytes cut: print less, or only what you need ...]\n\n", len(s)-len(head)-len(tail)) + tail
}

func joinNonEmpty(a, b string) string {
	if a == "" {
		return b
	}
	return a + "\n\n" + b
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
