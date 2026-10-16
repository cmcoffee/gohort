package orchestrate

// The app, pipeline and machine managers on every path an author runs.
//
// They are built on a turn, so builderAuthoringTools, which has only a
// session, cannot carry them, and the dispatch paths added that alone. A
// Builder another agent sent therefore had no app_def, and told the person
// "app_def isn't actually available in this deployment" while it is on
// Builder's own chat surface.

import (
	"context"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// authoringPrimitiveToolDefs are the managers for what an author makes
// besides agents and tools.
//
// pipeline (create / update / run / list / get / delete): declarative
// multi-stage workflows. Other agents RUN pipelines through their attached
// run_<pipeline> tools, not this management surface.
//
// machine (create / update / list / get / delete): session-resident phase
// machines. Other agents run the one they are pointed at, and leave it with
// change_phase.
//
// app_def (create / update / list / get / delete): data-driven gohort apps
// served at /apps/<slug>/, so "build me an app" gets a real app instead of a
// standalone HTML file.
func (t *chatTurn) authoringPrimitiveToolDefs() []AgentToolDef {
	return []AgentToolDef{t.pipelineGroupedToolDef(), t.machineGroupedToolDef(), t.appDefToolDef()}
}

// authoringPrimitivesFor builds the managers for a dispatched author, on a
// stand-in turn carrying the run's identity and the sending agent's limits.
func (T *OrchestrateApp) authoringPrimitivesFor(ctx context.Context, target AgentRecord, user string, udb Database, sess *ToolSession) []AgentToolDef {
	t := &chatTurn{app: T, agent: target, user: user, udb: udb, ctx: ctx, network: sess.Network, editRefusal: sess.ToolEditRefusal}
	return t.authoringPrimitiveToolDefs()
}

// appDelegationRefusal is what a Builder run another agent sent may not
// save: an app whose scripts use an API or a tool that agent may not use.
// App scripts run with the owner's credentials, so an app is the same side
// door tool_def is closed against. The whole app is checked, not the change:
// rewriting a script that already declares a credential uses it just the
// same. "" on an owner's own run.
func (t *chatTurn) appDelegationRefusal(spec AppSpec) string {
	if t == nil || t.editRefusal == nil {
		return ""
	}
	check := func(caps []string) string {
		for _, c := range caps {
			c = strings.TrimSpace(c)
			if name, ok := strings.CutPrefix(c, "tool:"); ok {
				if msg := t.editRefusal(strings.TrimSpace(name), ""); msg != "" {
					return msg
				}
				continue
			}
			for _, p := range []string{"secret:", "fetch_via:"} {
				if cred, ok := strings.CutPrefix(c, p); ok {
					if msg := t.editRefusal("", strings.TrimSpace(cred)); msg != "" {
						return msg
					}
				}
			}
		}
		return ""
	}
	for _, d := range spec.DataSources {
		if msg := check(d.Capabilities); msg != "" {
			return msg
		}
	}
	for _, a := range spec.Actions {
		if msg := check(a.Capabilities); msg != "" {
			return msg
		}
	}
	return ""
}
