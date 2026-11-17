// An app's actions, data and pipeline as tools beyond its page.
//
// Inside the app's own chat its agent gets every action and data source
// (agent_actions.go). Outside it, an app could only be used by opening its
// page. Now an app marks what it offers (AppAction.Expose,
// AppDataSource.Expose, AppSpec.ExposePipeline), and those become tools of
// the owner's: any of their agents gets one when it is ticked in the agent's
// tools (never by default: an action writes), and their MCP clients can call
// them (apps/mcpserver). A call is the owner using the app: the same script,
// records and settings as a click, through the same builders the app's chat
// uses, so the two cannot drift.
//
// Only the owner's own apps, and only while they may use apps at all.
package customapps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// exposedTools is core.AppToolsFor: the tools user's apps expose, runnable.
// sess is unused beyond what a handler finds on its call's context: an
// action reached from an agent's run goes to the background as its button's
// job does when it runs long (agent_actions.go), and one from outside a run
// is waited for.
func (T *CustomApps) exposedTools(sess *ToolSession, user string) []AgentToolDef {
	if user == "" || !T.mayUseApps(user) {
		return nil
	}
	var out []AgentToolDef
	for _, spec := range ListAppSpecs(user) {
		if spec.Disabled || spec.Owner != user {
			continue
		}
		out = append(out, T.appExposedTools(spec, user)...)
	}
	return out
}

// appExposedTools are one app's exposed tools for uid, its owner, each in a
// Category named for the app so a picker lists them under it.
func (T *CustomApps) appExposedTools(spec AppSpec, uid string) []AgentToolDef {
	shown := spec
	shown.Actions, shown.DataSources = nil, nil
	for _, a := range spec.Actions {
		if a.Expose {
			shown.Actions = append(shown.Actions, a)
		}
	}
	for _, d := range spec.DataSources {
		if d.Expose {
			shown.DataSources = append(shown.DataSources, d)
		}
	}
	pipe := spec.ExposePipeline && strings.TrimSpace(spec.PipelineID) != ""
	if len(shown.Actions)+len(shown.DataSources) == 0 && !pipe {
		return nil
	}
	udb := T.recordBase(spec, uid)
	var out []AgentToolDef
	if len(shown.Actions) > 0 {
		pout, prun := latestPipelineRun(spec, uid)
		out = append(out, T.actionTools(spec.Owner, uid, udb, shown, pout, prun)...)
	}
	out = append(out, T.dataSourceTools(spec.Owner, uid, udb, shown)...)
	if pipe {
		out = append(out, pipelineTool(spec, uid))
	}
	group := "App: " + firstNonEmpty(strings.TrimSpace(spec.Name), spec.Slug)
	for i := range out {
		out[i].Tool.Category = group
	}
	return out
}

// pipelineTool runs the app's pipeline to its end on an input and answers
// with the final output, as a script's run_pipeline does, under the same
// daily allowance as the app's ask.
func pipelineTool(spec AppSpec, uid string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: RefToolSlug(spec.Slug + "_run_pipeline"),
			Description: fmt.Sprintf("Run %s's pipeline on an input, as its page's Start does, and answer with its final output. It runs every stage to the end before answering, which can take minutes.",
				firstNonEmpty(spec.Name, spec.Slug)),
			Parameters: map[string]ToolParam{
				"input": {Type: "string", Description: "What the pipeline works on: the text its page's form would take."},
			},
			Required: []string{"input"},
			Caps:     []Capability{CapExecute},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			input := strings.TrimSpace(fmt.Sprint(args["input"]))
			if input == "" || input == "<nil>" {
				return "", fmt.Errorf("input is required")
			}
			if appscript.AppRunPipeline == nil {
				return "", fmt.Errorf("pipelines are not available on this deployment")
			}
			return appscript.AppRunPipeline(ctx, spec, uid, "", input)
		},
	}
}

// latestPipelineRun is the last finished run of spec's pipeline for user,
// handed to an action as a click hands it (latestRunForApp, without a
// request).
func latestPipelineRun(spec AppSpec, user string) (string, string) {
	if strings.TrimSpace(spec.PipelineID) == "" {
		return "", ""
	}
	orch := agenthost.Get()
	if orch == nil {
		return "", ""
	}
	def, ok := orch.LookupAppPipeline(spec.Owner, spec.PipelineID)
	if !ok {
		return "", ""
	}
	run, ok := orch.PublicLatestPipelineRun(user, def.ID)
	if !ok {
		return "", ""
	}
	b, err := json.Marshal(run)
	if err != nil {
		return run.Output, ""
	}
	return run.Output, string(b)
}

// mayUseApps reports whether user may use apps here: the apps host switched
// on, and an administrator, someone granted it, or a deployment with no
// accounts. An app's tools stop with the person's access to apps.
func (T *CustomApps) mayUseApps(user string) bool {
	path := T.WebPath()
	if !AppEnabledHere(path) {
		return false
	}
	if AuthDB == nil {
		return true
	}
	db := AuthDB()
	if db == nil || !AuthHasUsers(db) {
		return true
	}
	u, ok := AuthGetUser(db, user)
	if !ok {
		return false
	}
	if u.Admin {
		return true
	}
	for _, p := range AuthResolveUserApps(db, u) {
		if p == path || strings.HasPrefix(p, path+"/") {
			return true
		}
	}
	return false
}
