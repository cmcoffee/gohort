package servitor

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// runWorkspaceSession answers one question against a workspace. It is the
// workspace analogue of runSession's chat branch: scout the members, run the
// coordinator loop, emit the reply, persist the turn.
//
// It deliberately does NOT go through runSession. runSession is built around a
// single target — it acquires a connection, owns a scratch directory, and hands
// a worker to an investigator. A workspace has no target of its own; each drill
// re-enters runSession for the member it touches, which is where connections,
// scratch directories and workers actually belong.
func (T *Servitor) runWorkspaceSession(ctx context.Context, id, userID string, ws Appliance, messages []Message, udb Database) {
	members, missing := T.workspaceMembers(userID, udb, ws)
	if len(members) == 0 {
		text := fmt.Sprintf("Workspace %q has no usable members.", ws.Name)
		if len(missing) > 0 {
			text += " Configured but unavailable: " + strings.Join(missing, ", ") + ". They may have been deleted or un-shared."
		} else {
			text += " Edit it and select the systems and repositories it should investigate."
		}
		probeSessions.AppendEvent(id, probeEvent{Kind: "error", Text: text}, true)
		probeSessions.ScheduleCleanup(id)
		return
	}

	question := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			question = strings.TrimSpace(messages[i].Content)
			break
		}
	}

	emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Scouting %d member(s)…", len(members))})
	scouts := T.scoutWorkspace(ws, members, question)
	var relevant []string
	for _, s := range scouts {
		if s.Relevant() {
			relevant = append(relevant, s.Member.Name())
		}
	}
	if len(relevant) > 0 {
		emit(id, probeEvent{Kind: "status", Text: "Scout matched: " + strings.Join(relevant, ", ")})
	} else {
		emit(id, probeEvent{Kind: "status", Text: "Scout found no direct matches — the lead will choose where to look."})
	}
	if len(missing) > 0 {
		emit(id, probeEvent{Kind: "status", Text: "Unavailable member(s): " + strings.Join(missing, ", ")})
	}

	a := &Servitor{}
	a.AppCore = T.AppCore

	// The plan group is optional here for the same reason it is in single-appliance
	// chat: most questions are one or two dispatches, and taxing every follow-up
	// with a checklist would cost more than it explains.
	plan := buildPlanTools(id, false)
	tools := append(T.workspaceLeadTools(ctx, id, userID, ws, members), plan.All()...)
	assertOnlyAllowedTools("servitor.workspace", tools, servitorWorkspaceToolAllowList)

	leadPrompt := buildWorkspaceLeadPrompt(ws, scouts, missing, len(ws.Collections) > 0)

	var resp *Response
	var err error
	withHeartbeat(ctx, id, "Coordinator: working", func() {
		resp, _, err = a.RunAgentLoop(ctx,
			[]Message{{Role: "user", Content: buildScopedLeadMessage(messages)}},
			AgentLoopConfig{
				SystemPrompt: leadPrompt,
				Tools:        tools,
				MaxRounds:    40,
				RouteKey:     "app.servitor",
				// The coordinator REASONS across members rather than running
				// commands, so it follows the orchestrator setting — of the
				// workspace record, which is the appliance the operator
				// configured; each member's own run picks up its own.
				TierOverride:    applianceTierOverride(ws.OrchestratorTier),
				MaskDebugOutput: true,
				ChatOptions:     []ChatOption{WithTemperature(0.2), WithThink(true)},
				SerialTools:     true,
			},
		)
	})
	if ctx.Err() != nil {
		probeSessions.ScheduleCleanup(id)
		return
	}
	if err != nil {
		emit(id, probeEvent{Kind: "error", Text: "Coordinator error: " + err.Error()})
	}

	reply := ""
	if resp != nil {
		reply = strings.TrimSpace(resp.Content)
	}
	if reply == "" {
		reply = "The coordinator finished without producing an answer. Try narrowing the question to a specific member, or check that the members are reachable."
	}
	emit(id, probeEvent{Kind: "reply", Text: reply})

	if udb != nil {
		var lastUser string
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				lastUser = messages[i].Content
				break
			}
		}
		appendTurn(udb, ws.ID, id, lastUser, reply)
	}
}
