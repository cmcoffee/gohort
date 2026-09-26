// Builder triage — the diagnose-first gate on a session handed to Builder.
//
// "Send to Builder" used to name the agent as the thing to improve, whatever
// had actually gone wrong, and Builder went where it was pointed: a session
// whose fault was a broken script was answered with edits to the agent's
// prompt. The brief now lists what could be at fault, gathered from the
// session's own tool calls with the evidence beside each, and Builder must
// name a target (choose_target) before any authoring write goes through.
// When the evidence does not settle it, Builder asks the user with those
// candidates as the options. The rule is enforced at dispatch, not stated in
// the prompt: "diagnose before you edit" as a sentence is advice a model can
// skip.
//
// The handed-off session and Builder's session are linked by the brief's text:
// the page that consumes the brief stages its candidates under a hash of the
// text, and the Builder session that receives exactly that text as its first
// message claims them.

package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// BuilderTriage is a handed-off session's triage state: what could be at
// fault, and what Builder has named as the thing to fix. Nil on every session
// that did not start from a handoff.
type BuilderTriage struct {
	Candidates []TriageCandidate `json:"candidates,omitempty"`
	Targets    []TriageCandidate `json:"targets,omitempty"`
}

// TriageCandidate is one thing a handoff could be about.
type TriageCandidate struct {
	Kind     string `json:"kind"` // agent, tool, pipeline or machine
	Name     string `json:"name"`
	ID       string `json:"id,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Failures int    `json:"failures,omitempty"`
}

// pending reports whether editing is still closed: a handoff with no target.
func (tr *BuilderTriage) pending() bool { return tr != nil && len(tr.Targets) == 0 }

// triageKinds are the kinds of thing Builder can be pointed at, the same kinds
// an eval suite can grade.
var triageKinds = map[string]bool{"agent": true, "tool": true, "pipeline": true, "machine": true}

// triageCandidates gathers what a session's trouble could be in: the agent
// itself, and every custom tool, pipeline and machine that ran, each with how
// often it ran and failed. Built-in tools are left out: they are not Builder's
// to change, and the agent's use of them is the agent's candidacy. Failures
// sort first, since they are the likeliest cause.
func triageCandidates(agent AgentRecord, sess ChatSession, udb Database, user string) []TriageCandidate {
	type tally struct {
		c         TriageCandidate
		calls     int
		lastError string
	}
	seen := map[string]*tally{}
	var order []string
	note := func(kind, name, id string, failed bool, errText string) {
		key := kind + "\x00" + name
		t, ok := seen[key]
		if !ok {
			t = &tally{c: TriageCandidate{Kind: kind, Name: name, ID: id}}
			seen[key] = t
			order = append(order, key)
		}
		t.calls++
		if failed {
			t.c.Failures++
			t.lastError = errText
		}
	}
	turns := 0
	for _, m := range sess.Messages {
		if m.Role == "user" {
			turns++
		}
		for _, tc := range m.ToolCalls {
			failed, errText := triageCallFailed(tc)
			if tc.Framework && tc.Name == "machine_step" {
				machine := tc.Label
				if i := strings.Index(machine, ": "); i > 0 {
					machine = machine[:i]
				}
				note("machine", machine, "", failed, errText)
				continue
			}
			name := triageToolName(tc.Name)
			if p, ok := UserToolByName(udb, user, name); ok {
				kind := "tool"
				if p.Tool.Mode == TempToolModePipeline {
					kind = "pipeline"
				}
				note(kind, name, "", failed, errText)
				continue
			}
			if base := strings.TrimPrefix(name, "run_"); base != name {
				if def, ok := findPipelineByNameOrID(udb, user, base); ok {
					note("pipeline", def.Name, def.ID, failed, errText)
					continue
				}
				note("pipeline", base, "", failed, errText)
			}
		}
	}
	var out []TriageCandidate
	for _, key := range order {
		t := seen[key]
		ev := fmt.Sprintf("ran %d time(s)", t.calls)
		if t.c.Failures > 0 {
			ev += fmt.Sprintf(", failed %d; last failure: %s", t.c.Failures, t.lastError)
		}
		t.c.Evidence = ev
		out = append(out, t.c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Failures > out[j].Failures })
	self := TriageCandidate{Kind: "agent", Name: agent.Name, ID: agent.ID,
		Evidence: fmt.Sprintf("the agent itself: its prompt, rules and choice of tools, over %d turn(s)", turns)}
	// The agent leads unless something failed: then the failures lead, since
	// a broken tool looks like a misbehaving agent from the chat.
	if len(out) > 0 && out[0].Failures > 0 {
		i := sort.Search(len(out), func(i int) bool { return out[i].Failures == 0 })
		return append(append(append([]TriageCandidate{}, out[:i]...), self), out[i:]...)
	}
	return append([]TriageCandidate{self}, out...)
}

// triageToolName strips the display prefix a delegated call carries.
func triageToolName(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "↳ ") {
		if i := strings.Index(name, "] "); i > 0 {
			name = name[i+2:]
		}
	}
	return strings.TrimSpace(strings.TrimSuffix(name, " ♻"))
}

// triageCallFailed reads a saved call for failure: an error, a script that
// exited non-zero or timed out, a traceback, or an HTTP error status.
func triageCallFailed(tc PersistedToolCall) (bool, string) {
	if e := strings.TrimSpace(tc.Err); e != "" {
		return true, triageSnippet(e)
	}
	res := strings.TrimSpace(strings.TrimPrefix(tc.Result, UntrustedToolResultFence))
	switch {
	case strings.HasPrefix(res, "HTTP 4"), strings.HasPrefix(res, "HTTP 5"),
		strings.Contains(res, "[exit: "), strings.Contains(res, "[TIMED OUT"),
		strings.Contains(res, "Traceback (most recent call last)"):
		return true, triageSnippet(res)
	}
	return false, ""
}

func triageSnippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// triageBriefSection is the candidate list the brief carries, and what Builder
// is told to do with it.
func triageBriefSection(cands []TriageCandidate) string {
	var b strings.Builder
	b.WriteString("**What could be at fault** (from this session's own tool calls):\n")
	for _, c := range cands {
		label := c.Name
		if c.ID != "" && c.ID != c.Name {
			label += " (id `" + c.ID + "`)"
		}
		fmt.Fprintf(&b, "- %s `%s`: %s\n", c.Kind, label, c.Evidence)
	}
	b.WriteString("\nBefore anything else, decide which of these to fix and call choose_target(kind, name, why) with the evidence that settles it. " +
		"If the transcript does not settle it, ask me with ask_user, offering these as the options (plus \"something else\"), and name the target from my answer. " +
		"Editing stays closed until a target is named: reads (get, list, test) work, changes are refused. A fix can span more than one: call choose_target once for each.\n\n")
	return b.String()
}

// builderTriagePendingTable stages a consumed brief's candidates until the
// Builder session that receives the brief claims them.
const builderTriagePendingTable = "orchestrate_builder_triage_pending"

// builderTriageStaleAfter drops a staged triage nobody claimed: the brief page
// was opened and closed without sending.
const builderTriageStaleAfter = 24 * time.Hour

type pendingTriage struct {
	Candidates []TriageCandidate `json:"candidates"`
	Created    time.Time         `json:"created"`
}

func triageTextKey(text string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(h[:])
}

// stageBuilderTriage holds a brief's candidates for the session its text
// arrives in.
func stageBuilderTriage(udb Database, briefText string, cands []TriageCandidate) {
	if udb == nil || len(cands) == 0 {
		return
	}
	udb.Set(builderTriagePendingTable, triageTextKey(briefText), pendingTriage{Candidates: cands, Created: time.Now()})
}

// claimBuilderTriage returns the triage for a new Builder session whose first
// message is a staged brief, or nil. One-shot.
func claimBuilderTriage(udb Database, firstMessage string) *BuilderTriage {
	if udb == nil || strings.TrimSpace(firstMessage) == "" {
		return nil
	}
	key := triageTextKey(firstMessage)
	var p pendingTriage
	if !udb.Get(builderTriagePendingTable, key, &p) {
		return nil
	}
	udb.Unset(builderTriagePendingTable, key)
	if time.Since(p.Created) > builderTriageStaleAfter || len(p.Candidates) == 0 {
		return nil
	}
	return &BuilderTriage{Candidates: p.Candidates}
}

// triageGatedTools are the authoring tools the gate holds shut; a read action
// on one of them passes (triageReadActions).
var triageGatedTools = map[string]bool{
	"create_agent": true, "update_agent": true, "clone_agent": true, "delete_agent": true,
	"add_tool": true, "tool_def": true, "app_def": true, "skill_def": true,
	"pipeline_def": true, "pipeline": true, "machine": true, "eval": true,
	"connector": true, "bridge": true, "collections": true, "tool_template": true,
	"draft_api_credential": true, "draft_oauth_credential": true,
	"update_api_credential": true, "store_credential_secret": true,
}

// triageReadActions pass the gate: they look, they do not change. Listed
// rather than the writes, so an action nobody thought of stays shut.
var triageReadActions = map[string]bool{
	"get": true, "list": true, "help": true, "test": true, "verify": true,
	"run": true, "search": true, "show": true, "status": true, "check": true,
}

// triageBlocks reports whether a call would change something while the
// session's triage is still open.
func triageBlocks(tr *BuilderTriage, name string, args map[string]any) bool {
	if !tr.pending() || !triageGatedTools[name] {
		return false
	}
	action, _ := args["action"].(string)
	return !triageReadActions[strings.ToLower(strings.TrimSpace(action))]
}

// triageRefusal is what a refused write says.
const triageRefusal = "nothing was changed: this session was handed to you to fix something, and you have not said what yet. " +
	"Name the target first with choose_target(kind, name, why), picking from the candidates in the handoff; " +
	"if the transcript does not settle which, ask the user with ask_user, offering those candidates as the options. " +
	"Reads (get, list, test) stay open meanwhile"

// chooseTargetToolDef names what a handed-off session is to fix, which opens
// editing. Offered only on a session that came from a handoff.
func (t *chatTurn) chooseTargetToolDef() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "choose_target",
			Description: "Name what this handed-off session needs fixed, before changing anything: the agent itself, or one of its tools, pipelines or machines. Pick from the candidates listed in the handoff with the evidence that settles it; when the transcript does not settle it, ask the user first (ask_user, offering the candidates as options). Editing is refused until a target is named. Call again to add a second target when a fix spans two.",
			Parameters: map[string]ToolParam{
				"kind": {Type: "string", Description: "What kind of thing it is.", Enum: []string{"agent", "tool", "pipeline", "machine"}},
				"name": {Type: "string", Description: "Its name, as listed in the handoff (or as the user named it)."},
				"why":  {Type: "string", Description: "The evidence that settles it, or \"the user chose it\"."},
			},
			Required: []string{"kind", "name", "why"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if t.session == nil || t.session.Triage == nil {
				return "", errors.New("choose_target: this session did not start from a handoff, so there is nothing to choose")
			}
			kind := strings.ToLower(strings.TrimSpace(StringArg(args, "kind")))
			name := strings.TrimSpace(StringArg(args, "name"))
			why := strings.TrimSpace(StringArg(args, "why"))
			if !triageKinds[kind] {
				return "", fmt.Errorf("choose_target: kind must be agent, tool, pipeline or machine, not %q", kind)
			}
			if name == "" || why == "" {
				return "", errors.New("choose_target: name and why are both required: why is the evidence, or \"the user chose it\"")
			}
			tr := t.session.Triage
			pick := TriageCandidate{Kind: kind, Name: name, Evidence: why}
			listed := false
			for _, c := range tr.Candidates {
				if c.Kind == kind && (strings.EqualFold(c.Name, name) || (c.ID != "" && c.ID == name)) {
					pick.Name, pick.ID, listed = c.Name, c.ID, true
					break
				}
			}
			for _, c := range tr.Targets {
				if c.Kind == pick.Kind && c.Name == pick.Name {
					return fmt.Sprintf("%s %q is already a target. Editing is open.", kind, pick.Name), nil
				}
			}
			tr.Targets = append(tr.Targets, pick)
			msg := fmt.Sprintf("Target set: %s %q. Editing is open. Read its current definition before changing it, and write the failing case as an eval of this %s (target_kind=%q) before the fix.", kind, pick.Name, kind, kind)
			if !listed {
				msg += " It was not among the candidates this session's tool calls produced, so say in your reply why it is the one."
			}
			return msg, nil
		},
	}
}
