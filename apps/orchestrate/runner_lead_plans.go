package orchestrate

// "Lead plans, worker does the work" (AgentRecord.LeadPlans).
//
// On "Lead for every turn" the lead runs the whole turn with every tool, and
// small jobs it does itself: each tool round goes to the lead with the full
// catalog attached and the raw result coming back, which is most of what a
// lead turn costs. Here the lead decides and writes, and the worker works:
//
//   - the lead's round carries no work tools. It answers directly when the
//     turn needs none, and otherwise commits a plan_set (one step is enough
//     here) whose steps run on the worker with the tools it names for each;
//   - the tools a step can be given are LISTED for it in the prompt, by name
//     and first sentence, not offered as schemas, and a work tool named
//     anyway does not resolve (no lazy fallback, no mid-turn tool feed);
//   - the reply is the synthesis round, which runs on the lead as it does for
//     every plan.
//
// Only on the chat's own turn, which is the path that runs plans. A turn that
// cannot reach the lead (Private, ForcePrivate), a machine phase, Builder (its
// lead round has no work tools already), an agent with a tracked work plan
// (no plan_set) and one held to exactly its own tools run as they would
// without it.

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// leadDropsInPlanMode are the conversation tools the planning lead does not
// keep. It keeps the rest of them (memory, knowledge, saying something to the
// owner, showing a page, the app's own tools), none of which is a step's work.
var leadDropsInPlanMode = map[string]bool{
	"load_tool":  true, // the gateway to the custom tools a step uses
	"find_tools": true, // searching the work catalog is a step's job
}

// decideLeadPlans settles whether this turn plans on the lead.
func (pr *planRun) decideLeadPlans() error {
	t := pr.t
	pr.leadPlansOn = t.agent.LeadPlans && t.agent.LeadModel && t.shouldUseLeadModel() &&
		!pr.mach.on && !isBuilderAgent(t.agent.ID) && !t.agent.WorkPlan && !appAgentExactTools(t.agent.ID)
	if pr.leadPlansOn {
		Log("[orchestrate.routing] agent=%s lead plans this turn; the work runs in worker steps", t.agent.ID)
	}
	return nil
}

// splitForLeadPlans takes the work tools out of the lead's catalog and lists
// them in its prompt as what a step can be given.
func (pr *planRun) splitForLeadPlans() {
	if !pr.leadPlansOn {
		return
	}
	keep := map[string]bool{}
	for _, td := range pr.cat.controlTools {
		keep[td.Tool.Name] = true
	}
	for _, td := range pr.cat.knowTools {
		if !leadDropsInPlanMode[td.Tool.Name] {
			keep[td.Tool.Name] = true
		}
	}
	var lead []AgentToolDef
	pr.delegable = nil
	for _, td := range pr.allTools {
		switch {
		case keep[td.Tool.Name]:
			lead = append(lead, td)
		case leadDropsInPlanMode[td.Tool.Name]:
		default:
			pr.delegable = append(pr.delegable, td)
		}
	}
	for i := range lead {
		if lead[i].Tool.Name == "plan_set" {
			lead[i].Tool = planSetForPlanningLead(lead[i].Tool)
		}
	}
	pr.allTools = lead
	pr.sys += leadPlansBlock(pr.delegable)
	Log("[orchestrate.routing] agent=%s lead keeps %d tool(s); %d listed for steps", pr.t.agent.ID, len(lead), len(pr.delegable))
}

// leadPlansBlock tells the lead how this turn works and what a step can use.
func leadPlansBlock(delegable []AgentToolDef) string {
	var b strings.Builder
	b.WriteString("\n\n## You plan; workers do the work\n")
	b.WriteString("You have no work tools of your own this turn. When the request needs none, answer it directly. When it needs work (a search, a fetch, a file, a command, anything below), commit it with plan_set: each step runs on a worker with the tools you name in its tools list, and one step is enough for a small job. Write each worker_brief so it stands alone: a worker sees the user's latest message, the earlier steps' results and your brief, not this conversation, so put in the brief anything from earlier turns it needs. When the steps finish you write the reply from what they found.\n")
	if len(delegable) == 0 {
		b.WriteString("\nThis agent has no work tools, so answer directly.\n")
		return b.String()
	}
	b.WriteString("\nTools a step can be given:\n")
	names := make([]string, 0, len(delegable))
	desc := map[string]string{}
	for _, td := range delegable {
		if _, dup := desc[td.Tool.Name]; dup {
			continue
		}
		names = append(names, td.Tool.Name)
		desc[td.Tool.Name] = stepToolLine(td.Tool.Description)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "- %s: %s\n", n, desc[n])
	}
	return b.String()
}

// stepToolLine is a tool's description cut to its first sentence, kept short.
func stepToolLine(s string) string {
	s = firstSentence(strings.Join(strings.Fields(s), " "))
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}

// planSetForPlanningLead rewords plan_set for a lead with no work tools: its
// usual description says to call a tool directly for a small job, wants two
// steps, and has a step's tools picked "from your catalog", none of which is
// true here.
func planSetForPlanningLead(tool Tool) Tool {
	tool.Description = "Hand this turn's work to workers. Each step is {title, intent, worker_brief, tools}; a fresh worker runs each step on the worker model, in order, and you write the reply from what they found. One step is enough for a small job. Use it whenever the request needs any tool; answer directly only when it needs none."
	steps, ok := tool.Parameters["steps"]
	if !ok || steps.Items == nil {
		return tool
	}
	items := *steps.Items
	props := map[string]ToolParam{}
	for k, v := range items.Properties {
		props[k] = v
	}
	if p, ok := props["tools"]; ok {
		p.Description = "The tools this step's worker gets, by name, from the list under \"Tools a step can be given\". Name every tool the brief needs: the worker has nothing else, and no plan_set or ask_user of its own."
		props["tools"] = p
	}
	items.Properties = props
	steps.Items = &items
	params := map[string]ToolParam{}
	for k, v := range tool.Parameters {
		params[k] = v
	}
	params["steps"] = steps
	tool.Parameters = params
	return tool
}
