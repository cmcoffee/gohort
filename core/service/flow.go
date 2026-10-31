package service

// A flow is deterministic wiring from connector to connector: each step asks
// one connector for one contract, fed from the flow's own inputs or an
// earlier step's outputs. Nothing in a flow decides; an agent decides whether
// to run one. Because every step is typed by its contract, a flow that cannot
// fit together is refused when it is saved, not when it runs.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// FlowTable holds the saved flows, by name.
const FlowTable = "service_flows"

// Flow is a saved flow.
type Flow struct {
	Name string `json:"name"`
	// Description is what the flow does, as the agent given it reads it.
	Description string      `json:"description"`
	Inputs      []FlowInput `json:"inputs"`
	Steps       []FlowStep  `json:"steps"`
	// Test are sample inputs, as text, for a run on a click. A file input
	// left out takes the sample its first step's contract tests with.
	Test map[string]string `json:"test,omitempty"`
	// Starts is the event that runs the flow with no agent in between: a
	// monitor's fire, its text fed into one of the flow's inputs. An admin
	// sets it.
	Starts *FlowTrigger `json:"starts,omitempty"`
	// EventRuns caps the runs its event starts in a day (0 means
	// defaultEventRuns): an unattended run spends on its connectors with
	// nobody watching.
	EventRuns int       `json:"event_runs_per_day,omitempty"`
	Updated   time.Time `json:"updated"`
}

// FlowTrigger is the monitor whose fire starts a flow, and the flow input its
// text goes into.
type FlowTrigger struct {
	Owner   string `json:"owner"`
	Monitor string `json:"monitor"`
	Input   string `json:"input"`
}

// FlowInput is one input of a flow: what the agent gives it.
type FlowInput struct {
	Name        string    `json:"name"`
	Type        ValueType `json:"type"`
	Required    bool      `json:"required"`
	Description string    `json:"description"`
}

// FlowStep asks one connector for one contract. With feeds the contract's
// inputs: "{input.<name>}" is a flow input, "{<step>.<output>}" an earlier
// step's output, and anything else is the value itself. In a text input a
// reference may sit inside longer text, which it is written into.
type FlowStep struct {
	ID         string            `json:"id"`
	Connector  string            `json:"connector"`
	Capability string            `json:"capability"`
	With       map[string]string `json:"with"`
}

var (
	flowNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	flowRefRE  = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\.([a-z][a-z0-9_]*)\}`)
)

// ref is a value a step input is fed from.
type ref struct{ from, name string }

func (r ref) String() string { return "{" + r.from + "." + r.name + "}" }

// refsIn are the references in a With value, and whether the value is
// exactly one reference (and so keeps its type).
func refsIn(v string) ([]ref, bool) {
	var out []ref
	for _, m := range flowRefRE.FindAllStringSubmatch(v, -1) {
		out = append(out, ref{m[1], m[2]})
	}
	whole := len(out) == 1 && strings.TrimSpace(v) == out[0].String()
	return out, whole
}

// Check reports everything that keeps f from fitting together, or nil.
func (f Flow) Check() error {
	var problems []string
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if !flowNameRE.MatchString(f.Name) {
		bad("the name must be lower case letters, digits and _, starting with a letter (it names the agent tool flow_<name>)")
	}
	if strings.TrimSpace(f.Description) == "" {
		bad("say what the flow does: an agent reads it to decide when to run it")
	}
	// types are what each reference names: a flow input, or an earlier
	// step's output.
	types := map[string]ValueType{}
	for _, in := range f.Inputs {
		switch {
		case !flowNameRE.MatchString(in.Name):
			bad("input %q: a name is lower case letters, digits and _", in.Name)
		case types["input."+in.Name] != "":
			bad("input %s is listed twice", in.Name)
		case in.Type != TypeText && in.Type != TypeNumber && in.Type != TypeJSON && in.Type != TypeFile:
			bad("input %s: the type is text, number, json or file", in.Name)
		default:
			types["input."+in.Name] = in.Type
		}
	}
	if len(f.Steps) == 0 {
		bad("a flow has at least one step")
	}
	ids := map[string]bool{"input": true}
	for i, st := range f.Steps {
		at := fmt.Sprintf("step %d", i+1)
		if st.ID != "" {
			at += " (" + st.ID + ")"
		}
		switch {
		case !flowNameRE.MatchString(st.ID):
			bad("%s: its id is lower case letters, digits and _, so later steps can name its outputs", at)
		case ids[st.ID]:
			bad("%s: the id %s is taken", at, st.ID)
		}
		ids[st.ID] = true
		ct, ok := Lookup(st.Capability)
		if !ok {
			bad("%s: %q is not a contract (%s)", at, st.Capability, strings.Join(Names(), ", "))
			continue
		}
		c, found := GetConnector(RootDB, st.Connector)
		switch {
		case !found:
			bad("%s: there is no connector named %q", at, st.Connector)
		case !Provides(c, st.Capability):
			bad("%s: the connector %s does not provide %s", at, st.Connector, st.Capability)
		}
		for k, v := range st.With {
			p, ok := ct.input(k)
			if !ok {
				bad("%s: %s takes no input %q", at, st.Capability, k)
				continue
			}
			if err := fits(p, v, types); err != nil {
				bad("%s: %s %v", at, k, err)
			}
		}
		for _, p := range ct.Inputs {
			if p.Required && strings.TrimSpace(st.With[p.Name]) == "" && (!found || Default(c, st.Capability, p.Name) == "") {
				bad("%s: %s needs %s, and nothing feeds it", at, st.Capability, p.Name)
			}
		}
		for _, o := range ct.Outputs {
			types[st.ID+"."+o.Name] = o.Type
		}
	}
	for k, v := range f.Test {
		if t := types["input."+k]; t == "" {
			bad("the test gives %s, which is not an input", k)
		} else if err := literal(t, v); err != nil {
			bad("the test's %s %v", k, err)
		}
	}
	if t := f.Starts; t != nil {
		if _, ok := GetEventMonitor(RootDB, t.Owner, t.Monitor); !ok {
			bad("it starts when %s fires, and %s has no monitor by that name", t.Monitor, t.Owner)
		}
		if types["input."+t.Input] != TypeText {
			bad("the event's text goes into a text input of the flow, and %q is not one", t.Input)
		}
		for _, in := range f.Inputs {
			if in.Required && in.Name != t.Input {
				bad("started by an event, the flow is given only %s, so the required input %s would never be fed", t.Input, in.Name)
			}
		}
		sends := false
		for _, st := range f.Steps {
			sends = sends || st.Capability == "message.send"
		}
		if !sends {
			bad("started by an event, nothing sees what the flow makes unless a step sends it: add a message.send step")
		}
	}
	if f.EventRuns < 0 {
		bad("the event runs a day is 0 (the default, %d) or more", defaultEventRuns)
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return nil
}

// fits checks that v can feed port p, given the types of what may be
// referenced so far.
func fits(p Port, v string, types map[string]ValueType) error {
	refs, whole := refsIn(v)
	for _, r := range refs {
		t := types[r.from+"."+r.name]
		switch {
		case t == "" && r.from == "input":
			return fmt.Errorf("names %s, and the flow has no input %s", r, r.name)
		case t == "":
			return fmt.Errorf("names %s, which no earlier step gives", r)
		case whole && t != p.Type && !(p.Type == TypeText && t == TypeNumber):
			return fmt.Errorf("is %s, but %s is %s", p.Type, r, t)
		case !whole && p.Type != TypeText:
			return fmt.Errorf("is %s, so it takes one reference on its own, not text around one", p.Type)
		case !whole && t != TypeText && t != TypeNumber:
			return fmt.Errorf("writes %s into text, but it is %s", r, t)
		}
	}
	if len(refs) > 0 {
		return nil
	}
	return literal(p.Type, v)
}

// literal checks a value written into a flow as itself.
func literal(t ValueType, v string) error {
	switch t {
	case TypeNumber:
		if _, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err != nil {
			return fmt.Errorf("is a number, and %q is not one", v)
		}
	case TypeJSON:
		if !json.Valid([]byte(v)) {
			return fmt.Errorf("is json, and %q is not", v)
		}
	case TypeFile:
		return fmt.Errorf("is a file, which comes from an input or a step")
	}
	return nil
}

// Line is the flow read left to right: what it takes, each step, and what
// it gives.
func (f Flow) Line() string {
	var ins []string
	for _, in := range f.Inputs {
		ins = append(ins, in.Name)
	}
	parts := []string{"(" + strings.Join(ins, ", ") + ")"}
	for _, st := range f.Steps {
		parts = append(parts, st.Capability+" on "+st.Connector)
	}
	if n := len(f.Steps); n > 0 {
		if ct, ok := Lookup(f.Steps[n-1].Capability); ok {
			var outs []string
			for _, o := range ct.Outputs {
				outs = append(outs, o.Name)
			}
			parts = append(parts, "("+strings.Join(outs, ", ")+")")
		}
	}
	return strings.Join(parts, " ⇒ ")
}

// Estimate is what one run is expected to cost, the sum of its steps'
// declared prices, and whether every step declares one.
func (f Flow) Estimate() (float64, bool) {
	total, known := 0.0, true
	for _, st := range f.Steps {
		c, ok := GetConnector(RootDB, st.Connector)
		if !ok {
			return 0, false
		}
		s, err := Parse(c.Spec)
		if err != nil {
			return 0, false
		}
		b, _, err := s.binding(st.Capability)
		if err != nil {
			return 0, false
		}
		v, k := b.estimate()
		total += v
		known = known && k
	}
	return total, known
}

// Waiting names the steps' connectors not approved yet: the flow is saved,
// and runs once they are.
func (f Flow) Waiting() []string {
	var out []string
	seen := map[string]bool{}
	for _, st := range f.Steps {
		if c, ok := GetConnector(RootDB, st.Connector); (!ok || !c.Approved) && !seen[st.Connector] {
			seen[st.Connector] = true
			out = append(out, st.Connector)
		}
	}
	return out
}

// FlowStepReport is what one step of a run gave.
type FlowStepReport struct {
	Step   string `json:"step"`
	Detail string `json:"detail"`
}

// Run runs f on inputs: a flow input's value as text, number, decoded json,
// or a *File (or a file's path) for a file input. It returns the last step's
// result and what each step gave. Files an earlier step made are removed;
// the last step's are the caller's.
func (f Flow) Run(ctx context.Context, sess *ToolSession, inputs map[string]any) (Result, []FlowStepReport, error) {
	vals := map[string]any{}
	for _, in := range f.Inputs {
		v, given := inputs[in.Name]
		if !given || v == nil || v == "" {
			if in.Required {
				return nil, nil, fmt.Errorf("%s needs %s", f.Name, in.Name)
			}
			continue
		}
		cv, err := convert(in.Type, v)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", in.Name, err)
		}
		vals["input."+in.Name] = cv
	}
	var made []string
	var reports []FlowStepReport
	var last Result
	defer func() {
		for _, p := range made {
			os.Remove(p)
		}
	}()
	for i, st := range f.Steps {
		ct, ok := Lookup(st.Capability)
		if !ok {
			return nil, reports, fmt.Errorf("step %s: %q is not a contract", st.ID, st.Capability)
		}
		in := map[string]any{}
		for k, v := range st.With {
			p, _ := ct.input(k)
			fed, err := feed(p, v, vals)
			if err != nil {
				return nil, reports, fmt.Errorf("step %s: %s %w", st.ID, k, err)
			}
			if fed != nil {
				in[k] = fed
			}
		}
		res, err := Call(ctx, sess, st.Connector, st.Capability, in)
		if err != nil {
			return nil, reports, fmt.Errorf("step %s (%s on %s): %w", st.ID, st.Capability, st.Connector, err)
		}
		reports = append(reports, FlowStepReport{Step: st.ID, Detail: describe(res)})
		for name, v := range res {
			if v.File != nil {
				vals[st.ID+"."+name] = v.File
				if i < len(f.Steps)-1 {
					made = append(made, v.File.Path)
				}
			} else {
				vals[st.ID+"."+name] = v.Text
			}
		}
		last = res
	}
	return last, reports, nil
}

// convert reads a flow input given as v as type t.
func convert(t ValueType, v any) (any, error) {
	switch t {
	case TypeNumber:
		switch n := v.(type) {
		case float64:
			return n, nil
		case int:
			return float64(n), nil
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(fmt.Sprint(v)), 64)
		if err != nil {
			return nil, fmt.Errorf("%v is not a number", v)
		}
		return n, nil
	case TypeJSON:
		s, ok := v.(string)
		if !ok {
			return v, nil
		}
		var out any
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return nil, fmt.Errorf("not json: %v", err)
		}
		return out, nil
	case TypeFile:
		return fileVar(map[string]any{"f": v}, "f")
	}
	return fmt.Sprint(v), nil
}

// feed is the value v gives port p, from the values so far: a reference on
// its own keeps its type, references in text are written in, and anything
// else is the value itself. Nil when it names an optional input not given.
func feed(p Port, v string, vals map[string]any) (any, error) {
	refs, whole := refsIn(v)
	if whole {
		x, ok := vals[refs[0].from+"."+refs[0].name]
		if !ok {
			return nil, nil
		}
		if p.Type == TypeText {
			return text(x), nil
		}
		if p.Type == TypeJSON {
			if s, ok := x.(string); ok {
				var out any
				if err := json.Unmarshal([]byte(s), &out); err != nil {
					return nil, fmt.Errorf("is json, and %s is not: %v", refs[0], err)
				}
				return out, nil
			}
		}
		return x, nil
	}
	if len(refs) > 0 {
		return flowRefRE.ReplaceAllStringFunc(v, func(m string) string {
			sub := flowRefRE.FindStringSubmatch(m)
			return text(vals[sub[1]+"."+sub[2]])
		}), nil
	}
	if p.Type == TypeText {
		return v, nil
	}
	return convert(p.Type, v)
}

// text is a value written into text.
func text(x any) string {
	switch t := x.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case string:
		return t
	}
	b, _ := json.Marshal(x)
	return string(b)
}

// testInputs are f's sample inputs: its Test values, and for a file input
// left out, the sample its first consuming step's contract tests with.
func (f Flow) testInputs() (map[string]any, error) {
	in := map[string]any{}
	for k, v := range f.Test {
		in[k] = v
	}
	for _, fi := range f.Inputs {
		if fi.Type != TypeFile || in[fi.Name] != nil {
			continue
		}
		sample, err := f.sampleFor(fi.Name)
		if err != nil {
			return nil, err
		}
		if sample != nil {
			in[fi.Name] = sample
		}
	}
	return in, nil
}

// sampleFor is the sample file for the flow input name: a picture or a
// tone, as the first step it feeds tests with. Nil when that step's contract
// has no sample for it.
func (f Flow) sampleFor(name string) (*File, error) {
	want := "{input." + name + "}"
	for _, st := range f.Steps {
		ct, _ := Lookup(st.Capability)
		for k, v := range st.With {
			if strings.TrimSpace(v) != want {
				continue
			}
			for _, n := range ct.TestImages {
				if n == k {
					return samplePicture()
				}
			}
			for _, n := range ct.TestAudio {
				if n == k {
					return sampleTone()
				}
			}
			return nil, nil
		}
	}
	return nil, nil
}

// TestFlow runs f once on its sample inputs. Each step's connector gives one
// of its contract tests for the day, as a test of the connector itself would.
func TestFlow(ctx context.Context, f Flow) (Result, []FlowStepReport, error) {
	if err := f.Check(); err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, st := range f.Steps {
		if seen[st.Connector] {
			continue
		}
		seen[st.Connector] = true
		c, _ := GetConnector(RootDB, st.Connector)
		s, err := specOf(c)
		if err != nil {
			return nil, nil, err
		}
		if err := takeTest(st.Connector, s); err != nil {
			return nil, nil, err
		}
	}
	in, err := f.testInputs()
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		for _, v := range in {
			if fl, ok := v.(*File); ok {
				os.Remove(fl.Path)
			}
		}
	}()
	return f.Run(ctx, nil, in)
}

var (
	flowsMu sync.Mutex
	// flowsLive are the saved flows, as their tools read them: a tool's
	// schema is built on every catalog build, so it reads this and not the
	// store.
	flowsLive  = map[string]Flow{}
	flowTools  = map[string]bool{}
	flowsReady bool
)

// LoadFlows reads the saved flows and makes each a tool agents can be given.
// Called once the store is open; later calls do nothing.
func LoadFlows() {
	flowsMu.Lock()
	if flowsReady || RootDB == nil {
		flowsMu.Unlock()
		return
	}
	flowsReady = true
	flowsMu.Unlock()
	for _, f := range Flows() {
		live(f)
	}
}

// Flows are the saved flows, by name.
func Flows() []Flow {
	if RootDB == nil {
		return nil
	}
	var out []Flow
	for _, k := range RootDB.Keys(FlowTable) {
		var f Flow
		if RootDB.Get(FlowTable, k, &f) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetFlow is the saved flow named name.
func GetFlow(name string) (Flow, bool) {
	var f Flow
	if RootDB == nil || name == "" {
		return f, false
	}
	ok := RootDB.Get(FlowTable, name, &f)
	return f, ok
}

// SaveFlow checks f and saves it, and its tool reads it from then on. A
// flow's event starts only one flow; replacing names a flow f replaces (a
// rename), whose event f may take over.
func SaveFlow(f Flow, replacing ...string) error {
	if RootDB == nil {
		return fmt.Errorf("the store is not open")
	}
	if err := f.Check(); err != nil {
		return err
	}
	if t := f.Starts; t != nil {
		for _, o := range Flows() {
			if o.Name != f.Name && !slices.Contains(replacing, o.Name) && o.Starts != nil && o.Starts.Owner == t.Owner && o.Starts.Monitor == t.Monitor {
				return fmt.Errorf("the flow %s already starts when %s fires: one flow per event", o.Name, t.Monitor)
			}
		}
	}
	f.Updated = time.Now()
	RootDB.Set(FlowTable, f.Name, f)
	live(f)
	return nil
}

// DeleteFlow removes the flow named name; its tool leaves every catalog.
func DeleteFlow(name string) {
	if RootDB != nil {
		RootDB.Unset(FlowTable, name)
	}
	flowsMu.Lock()
	delete(flowsLive, name)
	flowsMu.Unlock()
}

func live(f Flow) {
	flowsMu.Lock()
	flowsLive[f.Name] = f
	registered := flowTools[f.Name]
	flowTools[f.Name] = true
	flowsMu.Unlock()
	if !registered {
		RegisterChatTool(&flowTool{flow: f.Name})
	}
	InvalidateToolIndex()
}

func liveFlow(name string) (Flow, bool) {
	flowsMu.Lock()
	defer flowsMu.Unlock()
	f, ok := flowsLive[name]
	return f, ok
}

// defaultEventRuns is how many runs a day a flow's event may start when the
// flow does not say.
const defaultEventRuns = 24

// flowEventRunsTable counts each flow's event-started runs today.
const flowEventRunsTable = "service_flow_event_runs"

type eventRunCount struct {
	Day  string `json:"day"`
	Runs int    `json:"runs"`
}

var eventRunsMu sync.Mutex

// EventLimit is how many runs f's event may start in a day.
func (f Flow) EventLimit() int {
	if f.EventRuns > 0 {
		return f.EventRuns
	}
	return defaultEventRuns
}

// EventRunsToday is how many runs flow name's event has started today.
func EventRunsToday(name string) int {
	var c eventRunCount
	if RootDB == nil || !RootDB.Get(flowEventRunsTable, name, &c) || c.Day != time.Now().Format("2006-01-02") {
		return 0
	}
	return c.Runs
}

// takeEventRun counts one event-started run of f, or refuses it past the
// flow's limit for the day.
func takeEventRun(f Flow) error {
	eventRunsMu.Lock()
	defer eventRunsMu.Unlock()
	day := time.Now().Format("2006-01-02")
	var c eventRunCount
	RootDB.Get(flowEventRunsTable, f.Name, &c)
	if c.Day != day {
		c = eventRunCount{Day: day}
	}
	if c.Runs >= f.EventLimit() {
		return fmt.Errorf("it has used its %d event runs for today", f.EventLimit())
	}
	c.Runs++
	RootDB.Set(flowEventRunsTable, f.Name, c)
	return nil
}

// FlowForMonitor is the flow owner's monitor name starts, if one does.
func FlowForMonitor(owner, name string) (Flow, bool) {
	for _, f := range Flows() {
		if t := f.Starts; t != nil && t.Owner == owner && t.Monitor == name {
			return f, true
		}
	}
	return Flow{}, false
}

// RunOnEvent runs f for its event, the event's text fed into its event input,
// within its runs for the day, and says what the flow gave, for the run's
// record. Files the last step made are removed: its message.send step is
// what hands anything on.
func RunOnEvent(ctx context.Context, f Flow, text string) (string, error) {
	if f.Starts == nil {
		return "", fmt.Errorf("%s is not started by an event", f.Name)
	}
	if err := f.Check(); err != nil {
		return "", fmt.Errorf("it no longer fits together: %s", strings.ReplaceAll(err.Error(), "\n", "; "))
	}
	if err := takeEventRun(f); err != nil {
		return "", err
	}
	res, steps, err := f.Run(ctx, nil, map[string]any{f.Starts.Input: text})
	if err != nil {
		return "", err
	}
	for _, v := range res {
		if v.File != nil {
			os.Remove(v.File.Path)
		}
	}
	var done []string
	for _, st := range steps {
		done = append(done, st.Step)
	}
	return "ran " + strings.Join(done, ", then "), nil
}
