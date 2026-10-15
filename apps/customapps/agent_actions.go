// An app's actions, data and status, as tools for the app's agent in the
// app's chat.
//
// An app's buttons are what it can do: fetch a folder, extract it, push it on,
// delete it. Its agent could talk about them and not press one, so "fetch the
// logs for this case" in the chat came back as instructions for the person.
// Now each action is one of the agent's tools while it answers in the app's
// chat, and a call is a click: the same script, run as the person chatting,
// with the same records and settings, upserting into their records, shown on
// the button's own spinner (the job is started under the button's key) and
// stoppable from it.
//
// The agent gets the app's verbs and nothing else: it can do what the page
// does, with the inputs each action declares, and what those scripts may
// touch is what the app was granted (a file store's grant, a credential).
// An action with a confirm prompt asks the person before the call runs, and
// fails closed when nobody is there to ask. A long one moves to the
// background after the detach time and reports back in the chat
// (core/bgrun), the way a long command does.
//
// And it can see what the page shows: <app>_status gives the person's actions
// running now (the latest line each printed, which is what a progress bar on
// the page draws from), those that ended recently and how, and the newest
// records; each data source is a read_<app>_<name> tool returning the JSON
// the page renders. Read-only, and computed for the person chatting.
package customapps

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bgrun"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// agentToolsFor is everything the app's agent gets in the app's chat: what
// the page shows (its status, its data sources) and what its buttons do.
func (T *CustomApps) agentToolsFor(owner, uid string, udb Database, spec AppSpec, pipelineOut, pipelineRun string) []AgentToolDef {
	if len(spec.Actions)+len(spec.DataSources) == 0 {
		return nil
	}
	tools := T.actionTools(owner, uid, udb, spec, pipelineOut, pipelineRun)
	tools = append(tools, T.dataSourceTools(owner, uid, udb, spec)...)
	taken := map[string]bool{}
	for _, td := range tools {
		taken[td.Tool.Name] = true
	}
	return append(tools, T.statusTool(owner, uid, udb, spec, taken))
}

// actionToolName is the tool an action becomes: the app and the action, so
// two apps' "fetch" cannot meet in one catalog.
func actionToolName(spec AppSpec, act AppAction) string {
	return RefToolSlug(spec.Slug + "_" + act.Name)
}

// actionTools are the app's actions as tools for its agent, run for uid.
// pipelineOut and pipelineRun are the last finished run of the app's
// pipeline, handed to every action the way a click hands them.
func (T *CustomApps) actionTools(owner, uid string, udb Database, spec AppSpec, pipelineOut, pipelineRun string) []AgentToolDef {
	var out []AgentToolDef
	for _, act := range spec.Actions {
		if strings.TrimSpace(act.Script) == "" {
			continue
		}
		act := act
		name := actionToolName(spec, act)
		label := strings.TrimSpace(act.Label)
		if label == "" {
			label = act.Name
		}
		desc := fmt.Sprintf("The %q button on %s's page", label, spec.Name)
		if d := strings.TrimSpace(act.Desc); d != "" {
			desc += ": " + strings.TrimSuffix(d, ".")
		}
		desc += ". Calling it is the same as the person clicking it: it runs the app's own script and answers with what the script reported."
		params := map[string]ToolParam{}
		keys := make([]string, 0, len(act.Inputs))
		for k := range act.Inputs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			params[k] = ToolParam{Type: "string", Description: act.Inputs[k]}
		}
		if len(params) == 0 {
			desc += " It takes no inputs."
		}
		def := AgentToolDef{
			Tool: Tool{
				Name:        name,
				Description: desc,
				Parameters:  params,
				Caps:        []Capability{CapExecute},
			},
			SingleFirePerBatch: true,
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				in := map[string]string{}
				for k := range act.Inputs {
					if v, ok := args[k]; ok && v != nil {
						in[k] = strings.TrimSpace(fmt.Sprint(v))
					}
				}
				run := func(work context.Context) (string, error) {
					return T.runActionForAgent(work, owner, uid, udb, spec, act, in, pipelineOut, pipelineRun)
				}
				sess := ToolSessionFromContext(ctx)
				if sess == nil {
					return run(ctx)
				}
				return bgrun.BackgroundRun{Tool: name, Label: spec.Name + ": " + label, Run: func(work *ToolSession) (string, error) {
					return run(work.Context())
				}}.Start(sess)
			},
		}
		if c := strings.TrimSpace(act.Confirm); c != "" {
			def.Confirmation = &ToolConfirmation{Prompt: c, Scope: "app-action:" + owner + "/" + spec.Slug + "/" + act.Name}
		}
		out = append(out, def)
	}
	return out
}

// runActionForAgent runs one action as a click would, as a job under the
// button's key so the page shows it, and waits for it: until it ends, or
// until ctx is done, which stops it.
func (T *CustomApps) runActionForAgent(ctx context.Context, owner, uid string, udb Database, spec AppSpec, act AppAction, inputs map[string]string, pipelineOut, pipelineRun string) (string, error) {
	recJSON, _ := json.Marshal(appscript.ReadRecords(udb, spec.Slug))
	args := map[string]any{"records": string(recJSON), "pipeline_output": pipelineOut, "pipeline_run": pipelineRun}
	for k, v := range inputs {
		args[k] = v
	}
	T.applySettings(args, spec, uid) // last, as for a click: an input never overrides a setting
	what := strings.TrimSpace(act.Label)
	if what == "" {
		what = act.Name
	}
	ownerDB := T.recordBase(spec, owner)
	act2 := bgrun.RunActivityFrom(ctx)
	job := startActionJob(actionJobKey(owner, spec.Slug, uid, act.Name), appWork{UID: uid, Slug: spec.Slug, AppName: spec.Name, What: what + " (its agent)", Kind: "action"},
		func(jctx context.Context, progress func(string)) (actionOutcome, error) {
			// Each line the script prints reaches the button and the run
			// the call belongs to, so neither reads a working script as stuck.
			tee := func(line string) {
				progress(line)
				if act2 != nil {
					fmt.Fprintln(act2, line)
				}
			}
			return runActionAndPersist(owner, ownerDB, udb, spec, act, args, uid, scriptRun{Interactive: true, Long: true, Ctx: jctx, OnProgress: tee})
		})
	select {
	case <-job.done:
	case <-ctx.Done():
		job.cancel()
		select {
		case <-job.done:
		case <-time.After(10 * time.Second):
		}
		return "", fmt.Errorf("%s was stopped", what)
	}
	job.mu.Lock()
	res, err := job.res, job.err
	job.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("%s failed: %w", what, err)
	}
	reply := map[string]any{"message": res.Message, "saved": res.Saved}
	if res.Result != nil {
		reply["result"] = res.Result
	}
	b, _ := json.Marshal(reply)
	return string(b), nil
}

// agentReadMax is how much of a status or a data source's JSON goes back to
// the model.
const agentReadMax = 24 << 10

// statusTool tells the agent what the person would see on the page: each of
// their actions running now (its latest line and how long), each one that
// ended recently (how it went), and their newest records. A page's progress
// bars are drawn from these, or from a data source (dataSourceTools).
func (T *CustomApps) statusTool(owner, uid string, udb Database, spec AppSpec, taken map[string]bool) AgentToolDef {
	name := RefToolSlug(spec.Slug + "_status")
	if taken[name] {
		name = RefToolSlug(spec.Slug + "_app_status")
	}
	desc := fmt.Sprintf("What %s's page shows right now: each of its actions that is running (the latest line it printed and how long it has run), each that ended in the last half hour (how it went), and the newest of the app's records.", spec.Name)
	if len(spec.DataSources) > 0 {
		desc += " Its data sources are tools of their own (read_*)."
	}
	return AgentToolDef{
		Tool: Tool{Name: name, Description: desc, Caps: []Capability{CapRead}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			out := map[string]any{"running": []map[string]any{}, "ended": []map[string]any{}}
			for _, j := range jobsFor(actionJobKey(owner, spec.Slug, uid, "")) {
				j.mu.Lock()
				row := map[string]any{"action": strings.TrimPrefix(j.key, actionJobKey(owner, spec.Slug, uid, "")), "what": j.work.What}
				if j.ended.IsZero() {
					row["latest"] = strings.TrimSpace(j.progress)
					row["running_for"] = sinceShort(j.started)
					out["running"] = append(out["running"].([]map[string]any), row)
				} else {
					row["ended_ago"] = sinceShort(j.ended)
					if j.err != nil {
						row["failed"] = j.err.Error()
					} else {
						row["outcome"] = j.res.Message
					}
					out["ended"] = append(out["ended"].([]map[string]any), row)
				}
				j.mu.Unlock()
			}
			recs := appscript.ReadRecords(udb, spec.Slug)
			out["record_count"] = len(recs)
			if len(recs) > 50 {
				recs = recs[len(recs)-50:]
			}
			out["newest_records"] = recs
			b, _ := json.Marshal(out)
			return capJSON(b), nil
		},
	}
}

// jobsFor is every kept job whose key starts with prefix, oldest first.
func jobsFor(prefix string) []*actionJob {
	actionJobs.Lock()
	var out []*actionJob
	for _, j := range actionJobs.byID {
		if strings.HasPrefix(j.key, prefix) {
			out = append(out, j)
		}
	}
	actionJobs.Unlock()
	sort.Slice(out, func(a, b int) bool { return out[a].started.Before(out[b].started) })
	return out
}

// dataSourceTools are the app's data sources as read tools: the same JSON the
// page draws from, computed for the person chatting with the inputs each
// declares.
func (T *CustomApps) dataSourceTools(owner, uid string, udb Database, spec AppSpec) []AgentToolDef {
	var out []AgentToolDef
	for _, ds := range spec.DataSources {
		if strings.TrimSpace(ds.Script) == "" {
			continue
		}
		ds := ds
		params := map[string]ToolParam{}
		for k, d := range ds.Inputs {
			params[k] = ToolParam{Type: "string", Description: d}
		}
		out = append(out, AgentToolDef{
			Tool: Tool{
				Name:        RefToolSlug("read_" + spec.Slug + "_" + ds.Name),
				Description: fmt.Sprintf("The %q data on %s's page: the JSON its sections and status are drawn from, as the person would see it now.", ds.Name, spec.Name),
				Parameters:  params,
				Caps:        []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				recJSON, _ := json.Marshal(appscript.ReadRecords(udb, spec.Slug))
				in := map[string]any{"records": string(recJSON)}
				for k := range ds.Inputs {
					if v, ok := args[k]; ok && v != nil {
						in[k] = strings.TrimSpace(fmt.Sprint(v))
					}
				}
				if len(spec.SharedCollections) > 0 {
					in["shared"] = sharedInput(T.recordBase(spec, owner), spec)
				}
				T.applySettings(in, spec, uid)
				res, err := cachedRunDataSource(owner, T.recordBase(spec, owner), spec, ds, in, uid)
				if err != nil {
					return "", fmt.Errorf("%s failed: %w", ds.Name, err)
				}
				return capJSON([]byte(strings.TrimSpace(res))), nil
			},
		})
	}
	return out
}

// capJSON is b as text, its end cut past agentReadMax with a note saying so.
func capJSON(b []byte) string {
	if len(b) <= agentReadMax {
		return string(b)
	}
	return string(b[:agentReadMax]) + fmt.Sprintf("\n[... cut: %d more bytes]", len(b)-agentReadMax)
}
