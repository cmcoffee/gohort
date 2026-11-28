package toolscript

// A tool that both reads and writes, as most of an author's are (a workspace,
// an app manager), cannot be handed to New whole: its declared capabilities
// say it writes, and they are right. A view of it that lets only its reading
// actions through can be: the same tool, under the same name, called the
// same way, refusing any other action before the tool sees it.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// ReadAction is one action a reading view lets through, and the arguments it
// takes. The tool's other arguments are left out of the view, so a script is
// not shown a "content" or a "replace" it has no use for.
type ReadAction struct {
	Name   string
	Params []string
}

// Reads is def cut down to the actions named, for a script to call. The
// caller vouches that each one only reads; GroupedReads takes that from a
// grouped tool's own declarations instead. A call naming any other action, or
// none, is refused with the actions it may name. When def's own action
// argument lists its actions, an action it leaves out (one the owner turned
// off for this agent) stays out of the view too.
func Reads(def AgentToolDef, actions ...ReadAction) AgentToolDef {
	offered := map[string]bool{}
	for _, a := range def.Tool.Parameters["action"].Enum {
		offered[a] = true
	}
	allowed := map[string]bool{}
	var names []string
	params := map[string]ToolParam{}
	for _, a := range actions {
		if a.Name == "" || allowed[a.Name] || len(offered) > 0 && !offered[a.Name] {
			continue
		}
		allowed[a.Name] = true
		names = append(names, a.Name)
		for _, p := range a.Params {
			if tp, ok := def.Tool.Parameters[p]; ok && p != "action" {
				params[p] = tp
			}
		}
	}
	sort.Strings(names)
	params["action"] = ToolParam{Type: "string", Enum: names, Description: "Which reading action to run."}

	view := def
	view.Tool = Tool{
		Name:        def.Tool.Name,
		Description: strings.TrimSpace(firstSentence(def.Tool.Description) + " Here, its reading actions only: " + strings.Join(names, ", ") + "."),
		Parameters:  params,
		Required:    []string{"action"},
		Caps:        []Capability{CapRead},
	}
	view.NeedsConfirm = false
	view.Confirmation = nil
	inner := def.Handler
	view.Handler = func(ctx context.Context, args map[string]any) (string, error) {
		action, _ := args["action"].(string)
		if !allowed[action] {
			return "", fmt.Errorf("a script may call %s's reading actions only (%s), not %q", def.Tool.Name, strings.Join(names, ", "), action)
		}
		if inner == nil {
			return "", fmt.Errorf("%s has no handler", def.Tool.Name)
		}
		return inner(ctx, args)
	}
	return view
}

// GroupedReads is Reads over each of g's actions that declares reading and
// nothing else and does not ask before it runs, with the arguments each one
// takes; def is g as the turn mounted it. False when g has no such action
// that def still offers.
func GroupedReads(def AgentToolDef, g *GroupedTool) (AgentToolDef, bool) {
	var actions []ReadAction
	for _, name := range g.ActionNames() {
		a, ok := g.Action(name)
		if !ok || a.NeedsConfirm || !onlyReads(a.Caps) {
			continue
		}
		var ps []string
		for p := range a.Params {
			ps = append(ps, p)
		}
		sort.Strings(ps)
		actions = append(actions, ReadAction{Name: name, Params: ps})
	}
	view := Reads(def, actions...)
	if len(view.Tool.Parameters["action"].Enum) == 0 {
		return AgentToolDef{}, false
	}
	return view, true
}

// onlyReads is whether caps declares reading and nothing else.
func onlyReads(caps []Capability) bool {
	if len(caps) == 0 {
		return false
	}
	for _, c := range caps {
		if c != CapRead {
			return false
		}
	}
	return true
}
