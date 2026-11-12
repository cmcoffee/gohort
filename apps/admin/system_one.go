package admin

// Services > System One: what answers gohort's quick decisions (core/decide),
// a pick among fixed answers read from a model's first token. Off until
// someone chooses it here. The choices are gohort's own models (the Judge,
// Worker or Lead model) that answer a known-answer question right, found by
// asking each rather than set, and connections that give decisions
// (decide.choice), added from a template with + Add another....
//
// Private first. A decision sees what the checks see, messages and replies,
// so a model or connection that sends them to a third party is not offered,
// unless every model is declared private (Admin > Models); then it is,
// listed after the ones that are private in their own right, and marked.
//
// A model is asked by vLLM's own /v1/systemone where its server has one
// (vLLM 0.32+, started with --logprobs-mode raw_logprobs) and by gohort's own
// first-token reading where it does not; a connection is asked only its own
// way. Which, and whether it works at all, is kept against what was asked
// and asked again when that changes, or after a failure has stood a while.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/decide"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
)

const (
	systemOneTable  = decide.ConfigTable
	systemOneSource = "api/system-one"
	// systemOneJudge is the provider "Use the Judge model".
	systemOneJudge = decide.ProviderJudge
	// systemOneRecheck is how long a failed check stands before the page
	// asks again: a model that was down comes back with nothing changed.
	systemOneRecheck = 10 * time.Minute
	// systemOneAskTimeout bounds one known-answer question.
	systemOneAskTimeout = 20 * time.Second
)

// systemOneModels are gohort's own models System One can use, in the order
// offered.
var systemOneModels = []struct{ value, label, unset string }{
	{decide.ProviderJudge, "Use the Offload model", "no Offload model is set (Admin > Models > Decision Offloading)"},
	{decide.ProviderWorker, "Use the Worker model", "no Worker model is set (Admin > Models)"},
	{decide.ProviderLead, "Use the Lead model", "no separate Lead model is set (Admin > Models)"},
}

// systemOneStatus is what the last check of one choice found.
type systemOneStatus struct {
	Of     string    `json:"of"` // what was checked (systemOneIdentity)
	At     time.Time `json:"at"`
	OK     bool      `json:"ok"`
	Native bool      `json:"native,omitempty"`
	Conf   float64   `json:"confidence,omitempty"`
	TookMS int64     `json:"took_ms,omitempty"`
	Err    string    `json:"error,omitempty"`
}

// line is the status as the section says it.
func (s systemOneStatus) line() string {
	if !s.OK {
		return "Unavailable: " + s.Err + ". Checks use their full answers."
	}
	how := "gohort's own first-token reading (this vLLM has no /v1/systemone: it needs vLLM 0.32 or later, started with --logprobs-mode raw_logprobs)"
	if s.Native {
		how = "native, the server's own System One"
	}
	return fmt.Sprintf("Working: %s. The test question answered right at confidence %.2f in %d ms.", how, s.Conf, s.TookMS)
}

// systemOneProvider is what answers decisions: "" (off), a model, or a
// connection.
func systemOneProvider() string { return decide.Chosen() }

// systemOneModel is choice's entry in systemOneModels, if it is a model.
func systemOneModel(choice string) (label, unset string, ok bool) {
	for _, m := range systemOneModels {
		if m.value == choice {
			return m.label, m.unset, true
		}
	}
	return "", "", false
}

// systemOneSet reports whether choice names something set up: a model
// configured, or a connection that gives decisions.
func systemOneSet(choice string) bool {
	switch choice {
	case decide.ProviderJudge:
		return JudgeLLM() != nil
	case decide.ProviderWorker:
		return SharedWorkerLLM() != nil
	case decide.ProviderLead:
		return LeadIsDistinct()
	}
	c, ok := GetConnector(RootDB, choice)
	return ok && service.Provides(c, service.DecideChoice)
}

// systemOneIdentity is what a check of choice was made on, so its finding is
// kept until that changes.
func systemOneIdentity(choice string) string { return decide.Identity(choice) }

// checkSystemOne asks choice a question with a known answer as a decision,
// and keeps what it found against what it asked. Nothing is asked of a
// choice that is not set up or not private.
func checkSystemOne(ctx context.Context, choice string) systemOneStatus {
	st := systemOneStatus{Of: systemOneIdentity(choice), At: time.Now()}
	_, unset, isModel := systemOneModel(choice)
	ok, _, why := decide.Privacy(choice)
	switch {
	case !systemOneSet(choice) && isModel:
		st.Err = unset
	case !systemOneSet(choice):
		st.Err = fmt.Sprintf("no connection named %q gives System One decisions", choice)
	case !ok:
		st.Err = "it is not private (" + why + "), and only a private model may see what the checks see"
	default:
		cctx, cancel := context.WithTimeout(ctx, systemOneAskTimeout)
		defer cancel()
		a, err := decide.Ask(cctx, decide.For(choice), decide.Question{
			State:   "The sky is clear and blue today.",
			Ask:     "What colour is the sky?",
			Options: []string{"blue", "green"},
		})
		switch {
		case err != nil:
			st.Err = err.Error()
		case !a.OK:
			st.Err = "the model put no weight on either answer's label"
		case a.Choice != 0:
			st.Err = fmt.Sprintf("it answered the test question wrong (P(blue)=%.2f)", a.Probs[0])
		default:
			st.OK, st.Native, st.Conf, st.TookMS = true, a.Native, a.Confidence, a.Took.Milliseconds()
		}
	}
	if RootDB != nil {
		RootDB.Set(systemOneTable, "status:"+choice, st)
	}
	return st
}

// currentSystemOne is the last check of choice, asked again when what it
// names has changed since, or when it failed long enough ago.
func currentSystemOne(ctx context.Context, choice string) systemOneStatus {
	var st systemOneStatus
	if RootDB != nil && RootDB.Get(systemOneTable, "status:"+choice, &st) && !st.At.IsZero() &&
		st.Of == systemOneIdentity(choice) && (st.OK || time.Since(st.At) < systemOneRecheck) {
		return st
	}
	return checkSystemOne(ctx, choice)
}

// systemOneChoices are the section's choices: Off; the models that answer
// the test right and the connections that give decisions, the private ones
// first and those allowed only because every model is declared private
// after, marked; then + Add another.... The saved choice is always listed, so
// a choice that stopped working says so rather than vanishing.
func systemOneChoices() []ui.SelectOption {
	saved := decide.Chosen()
	found := make([]systemOneStatus, len(systemOneModels))
	var wg sync.WaitGroup
	for i, m := range systemOneModels {
		if ok, _, _ := decide.Privacy(m.value); !ok || !systemOneSet(m.value) {
			continue
		}
		wg.Add(1)
		go func(i int, choice string) {
			defer wg.Done()
			found[i] = currentSystemOne(context.Background(), choice)
		}(i, m.value)
	}
	wg.Wait()
	var own, allowed []ui.SelectOption
	place := func(o ui.SelectOption) {
		ok, isOwn, why := decide.Privacy(o.Value)
		switch {
		case !ok:
			o.Label += " (not private: unavailable)"
			o.Help = "Not offered: " + why + "."
			own = append(own, o)
		case isOwn:
			own = append(own, o)
		default:
			o.Label += " (allowed: every model declared private)"
			o.Help = strings.TrimSpace(o.Help + " Not private in its own right: " + why + ".")
			allowed = append(allowed, o)
		}
	}
	for i, m := range systemOneModels {
		works := found[i].OK
		if !works && m.value != saved {
			continue
		}
		o := ui.SelectOption{Value: m.value, Label: m.label, Help: found[i].line()}
		if !works {
			if ok, _, _ := decide.Privacy(m.value); ok {
				o.Label += " (unavailable)"
			}
			if found[i].At.IsZero() {
				o.Help = ""
			}
		}
		place(o)
	}
	for _, o := range serviceProviderOptions(service.DecideChoice, "Decide") {
		if _, _, isModel := systemOneModel(o.Value); isModel {
			continue // a connection named like a model would be read as the model
		}
		if ok, _, _ := decide.Privacy(o.Value); !ok && o.Value != saved {
			continue
		}
		place(o)
	}
	out := []ui.SelectOption{{Value: "", Label: "Off"}}
	out = append(append(out, own...), allowed...)
	return append(out, addAnother(service.DecideChoice))
}

func (a *AdminApp) registerSystemOneRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/"+systemOneSource, jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleSystemOne, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleSystemOneSave, a.gate()),
	}.Serve)
	sub.HandleFunc("/"+systemOneSource+"/test", jsonapi.Post(a.db, a.handleSystemOneTest, a.gate()))
}

// systemOneForm is the section's own fields; a connection's settings ride
// beside them (withSettings).
type systemOneForm struct {
	Provider string `json:"provider"`
}

// systemOneConnection is the connection choice names, or "" for a model or
// off.
func systemOneConnection(choice string) string {
	if _, _, isModel := systemOneModel(choice); isModel {
		return ""
	}
	return chosenConnection(choice)
}

// handleSystemOne is the section: what answers decisions, how it is doing,
// and a chosen connection's settings.
func (a *AdminApp) handleSystemOne(c *jsonapi.Call) (any, error) {
	p := systemOneProvider()
	out := withValues(systemOneForm{Provider: p}, systemOneConnection(p), service.DecideChoice)
	if p != "" {
		out["_status"] = currentSystemOne(c.R.Context(), p).line()
	}
	var shadows []string
	for _, t := range decide.Shadows() {
		shadows = append(shadows, t.Line())
	}
	if len(shadows) > 0 {
		out["_shadows"] = strings.Join(shadows, "\n")
	}
	return out, nil
}

// handleSystemOneSave sets what answers decisions, and a connection's
// settings. A choice that is not set up, or not private, is refused.
func (a *AdminApp) handleSystemOneSave(c *jsonapi.Call, form withSettings[systemOneForm]) (any, error) {
	p := strings.TrimSpace(form.Form.Provider)
	if p != "" {
		if !systemOneSet(p) {
			return nil, jsonapi.BadRequest("System One is off, one of gohort's models, or a connection that gives decisions")
		}
		if ok, _, why := decide.Privacy(p); !ok {
			return nil, jsonapi.BadRequest("Not private (" + why + "): System One sees what the checks see, so it must be a private model or connection, unless every model is declared private (Admin > Models)")
		}
	}
	conn := systemOneConnection(p)
	if err := form.Settings.save(conn, service.DecideChoice); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	RootDB.Set(systemOneTable, "provider", p)
	acceptChanges(conn)
	what := p
	if p == "" {
		what = "off"
	}
	adminChange(c.R, activity.Settings, "System One: %s", what)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleSystemOneTest checks the form's choice now, whatever was found
// before, with a connection's settings as the form holds them.
func (a *AdminApp) handleSystemOneTest(c *jsonapi.Call, form withSettings[systemOneForm]) (any, error) {
	p := strings.TrimSpace(form.Form.Provider)
	if p == "" {
		p = systemOneProvider()
	}
	if p == "" {
		return testResultBody(false, "", "System One is off: choose what answers it first."), nil
	}
	ctx := c.R.Context()
	if conn := systemOneConnection(p); conn != "" {
		ctx = form.Settings.testContext(ctx, conn, service.DecideChoice)
	}
	st := checkSystemOne(ctx, p)
	if !st.OK {
		return testResultBody(false, "", st.line()), nil
	}
	return testResultBody(true, st.line(), ""), nil
}

// systemOneSection is Services > System One.
func systemOneSection() ui.Section {
	fields := []ui.FormField{
		{Field: "provider", Label: "Answered by", Type: "select", Options: systemOneChoices(),
			Help:   "Off, one of gohort's own models that answers System One, or a connection that does; + Add another... adds one from a template.",
			Detail: "A model is offered once it answers a test question right, and only while it is private: System One sees what the checks see, messages and replies. A model or connection that is not private is offered only when every model is declared private (Admin > Models), and then marked. A connection's settings (its address, its model, a key) are set here below it."},
	}
	fields = append(fields, connectorSettingsFields(service.DecideChoice, "", "provider")...)
	fields = append(fields, ui.FormField{Field: "_status", Label: "Status", Type: "readonly", ShowWhen: "provider"},
		ui.FormField{Field: "_shadows", Label: "Router shadows", Type: "readonly", ShowWhen: "_shadows",
			Help:   "How System One does on machine steps that only choose where to go, asked beside each step and never deciding.",
			Detail: fmt.Sprintf("Sure means both asks (the options shown in order, then reversed) chose the route, the weaker with probability %.2f or more, the mark a route would need to stand on System One. Each step's own choice still decides; a router that is sure and agrees nearly every time on your traffic is one System One could take.", decide.RouteSure)})
	return ui.Section{
		Title:    "System One",
		Subtitle: "Quick decisions for gohort's checks: a pick among fixed answers, read from a model's first token.",
		Detail: "A check asked as a System One decision gets its answer in one read of the prompt, with no reason written, and a confidence; it falls back to its full answer when that is not confident. " +
			"Off until chosen here. A model is asked by vLLM's own /v1/systemone where its server has one (vLLM 0.32 or later, started with --logprobs-mode raw_logprobs), " +
			"and by gohort's own reading of the first token where it does not, which also needs vLLM. A connection is a System One server of its own. Status says which, from a test question asked when this page opens after the choice changed; Test asks it again.\n\n" +
			"The channel gatekeeper and the Bridges loop check use it now, and machine steps that only route are shadowed (Router shadows, below). The check tests (Admin > Models > Decision Offloading > Check tests) show how it does beside each check's full answer.",
		Body: ui.FormPanel{
			Source:       systemOneSource,
			TestURL:      systemOneSource + "/test",
			TestLabel:    "Test",
			TestShowWhen: "provider",
			Fields:       fields,
		},
	}
}
