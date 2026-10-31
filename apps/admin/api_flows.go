package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/service"
)

// registerFlowsRoutes wires saved flows (core/service): deterministic wiring
// from connector to connector, each one a tool agents can be given. GET lists
// (or with ?name= gives one as its form), POST checks and saves, DELETE
// removes, and test runs one on its sample inputs.
func (a *AdminApp) registerFlowsRoutes(sub *http.ServeMux) {
	service.LoadFlows()
	sub.HandleFunc("/api/flows", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleFlows, a.gate()),
		"POST":   jsonapi.Post(a.db, a.handleFlowSave, a.gate(), jsonapi.MaxBody(1<<20)),
		"DELETE": jsonapi.Delete(a.db, a.handleFlowDelete, a.gate()),
	}.Serve)
	sub.HandleFunc("/api/flows/test", jsonapi.Post(a.db, a.handleFlowTest, a.gate(), jsonapi.MaxBody(1<<20)))
}

// flowRow is one row of the Flows table.
type flowRow struct {
	Name        string `json:"name"`
	Tool        string `json:"tool"`
	Description string `json:"description"`
	Line        string `json:"line"`
	Status      string `json:"status"`
	TestConfirm string `json:"test_confirm"`
}

// wiringForm is a flow as its form edits it: inputs and steps as rows, and
// what feeds each step, and the sample inputs, as "name = value" lines.
type wiringForm struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Inputs      []wiringInput `json:"inputs"`
	Steps       []wiringStep  `json:"steps"`
	Test        string        `json:"test"`
	// Starts is the monitor whose fire runs the flow, as "owner:name", and
	// StartsInput the input its text goes into.
	Starts      string `json:"starts"`
	StartsInput string `json:"starts_input"`
	EventRuns   int    `json:"event_runs_per_day"`
}

type wiringInput struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    string `json:"required"`
	Description string `json:"description"`
}

type wiringStep struct {
	ID         string `json:"id"`
	Connector  string `json:"connector"`
	Capability string `json:"capability"`
	With       string `json:"with"`
}

func (f wiringForm) flow() service.Flow {
	out := service.Flow{
		Name:        strings.TrimSpace(f.Name),
		Description: strings.TrimSpace(f.Description),
		Test:        lines(f.Test),
		EventRuns:   f.EventRuns,
	}
	if owner, name, ok := strings.Cut(strings.TrimSpace(f.Starts), ":"); ok && name != "" {
		out.Starts = &service.FlowTrigger{Owner: owner, Monitor: name, Input: strings.TrimSpace(f.StartsInput)}
	}
	for _, in := range f.Inputs {
		out.Inputs = append(out.Inputs, service.FlowInput{
			Name:        strings.TrimSpace(in.Name),
			Type:        service.ValueType(strings.TrimSpace(in.Type)),
			Required:    in.Required == "yes",
			Description: strings.TrimSpace(in.Description),
		})
	}
	for _, st := range f.Steps {
		out.Steps = append(out.Steps, service.FlowStep{
			ID:         strings.TrimSpace(st.ID),
			Connector:  strings.TrimSpace(st.Connector),
			Capability: strings.TrimSpace(st.Capability),
			With:       lines(st.With),
		})
	}
	return out
}

func wiringFormOf(f service.Flow) wiringForm {
	out := wiringForm{Name: f.Name, Description: f.Description, Test: unlines(f.Test), EventRuns: f.EventRuns}
	if t := f.Starts; t != nil {
		out.Starts, out.StartsInput = t.Owner+":"+t.Monitor, t.Input
	}
	for _, in := range f.Inputs {
		req := ""
		if in.Required {
			req = "yes"
		}
		out.Inputs = append(out.Inputs, wiringInput{Name: in.Name, Type: string(in.Type), Required: req, Description: in.Description})
	}
	for _, st := range f.Steps {
		out.Steps = append(out.Steps, wiringStep{ID: st.ID, Connector: st.Connector, Capability: st.Capability, With: unlines(st.With)})
	}
	return out
}

// lines reads "name = value" lines; a blank line is skipped.
func lines(s string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(l, "=")
		if k = strings.TrimSpace(k); ok && k != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func unlines(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %s\n", k, m[k])
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (a *AdminApp) handleFlows(c *jsonapi.Call) (any, error) {
	if name := strings.TrimSpace(c.R.URL.Query().Get("name")); name != "" {
		f, ok := service.GetFlow(name)
		if !ok {
			return nil, jsonapi.Status(http.StatusNotFound, "no flow named "+name)
		}
		return wiringFormOf(f), nil
	}
	rows := []flowRow{}
	for _, f := range service.Flows() {
		rows = append(rows, flowRow{
			Name:        f.Name,
			Tool:        "flow_" + f.Name,
			Description: f.Description,
			Line:        f.Line(),
			Status:      flowStatus(f),
			TestConfirm: flowTestConfirm(f),
		})
	}
	return rows, nil
}

// flowStatus is what stands between a saved flow and a run, or its cost.
func flowStatus(f service.Flow) string {
	if err := f.Check(); err != nil {
		return "Will not run: " + strings.ReplaceAll(err.Error(), "\n", "; ")
	}
	status := "Ready. A step declares no price, so a run's cost is not known ahead."
	if est, known := f.Estimate(); known {
		status = fmt.Sprintf("Ready. A run costs about $%.4f.", est)
	}
	if w := f.Waiting(); len(w) > 0 {
		status = "Waiting on approval of " + strings.Join(w, ", ") + " under Connectors."
	}
	if t := f.Starts; t != nil {
		status += fmt.Sprintf(" Starts when %s (%s) fires, its text into %s: %d of %d event runs today.",
			t.Monitor, t.Owner, t.Input, service.EventRunsToday(f.Name), f.EventLimit())
	}
	return status
}

func flowTestConfirm(f service.Flow) string {
	cost := "It may cost money: a step declares no price."
	if est, known := f.Estimate(); known {
		cost = fmt.Sprintf("It should cost about $%.4f.", est)
	}
	return "Run " + f.Name + " once on its sample inputs? Each step makes a real call. " + cost + " Each step's connector gives one of its daily tests."
}

// handleFlowSave checks and saves a flow. Saved from a row's form
// (?name=), a new name replaces the old flow.
func (a *AdminApp) handleFlowSave(c *jsonapi.Call, form wiringForm) (any, error) {
	f := form.flow()
	old := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if err := service.SaveFlow(f, old); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if old != "" && old != f.Name {
		service.DeleteFlow(old)
	}
	return map[string]any{"ok": true, "name": f.Name}, nil
}

func (a *AdminApp) handleFlowDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("missing name")
	}
	service.DeleteFlow(name)
	return map[string]any{"ok": true}, nil
}

// handleFlowTest runs a flow once on its sample inputs: the saved one named
// ?name= (the row's Test), or the form's unsaved values (the form's Test).
// The answer carries both shapes: ok/message/error for the form, and
// message/warnings for the row.
func (a *AdminApp) handleFlowTest(c *jsonapi.Call, body json.RawMessage) (any, error) {
	var f service.Flow
	if name := strings.TrimSpace(c.R.URL.Query().Get("name")); name != "" {
		saved, ok := service.GetFlow(name)
		if !ok {
			return nil, jsonapi.Status(http.StatusNotFound, "no flow named "+name)
		}
		f = saved
	} else {
		var form wiringForm
		if err := json.Unmarshal(body, &form); err != nil {
			return nil, jsonapi.BadRequest("the form did not come through: " + err.Error())
		}
		f = form.flow()
	}
	res, steps, err := service.TestFlow(c.R.Context(), f)
	for _, v := range res {
		if v.File != nil {
			os.Remove(v.File.Path)
		}
	}
	var out []string
	for _, s := range steps {
		out = append(out, s.Step+": "+s.Detail)
	}
	if err != nil {
		out = append(out, err.Error())
		msg := strings.Join(out, "\n")
		return map[string]any{"ok": false, "error": msg, "message": msg, "warnings": []string{err.Error()}}, nil
	}
	msg := "passed\n" + strings.Join(out, "\n")
	return map[string]any{"ok": true, "message": msg}, nil
}
