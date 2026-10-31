package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/apijob"
)

// binding returns the spec's binding for capability, with its contract.
func (s Spec) binding(capability string) (Binding, Contract, error) {
	for _, b := range s.Provides {
		if b.Capability == capability {
			c, ok := Lookup(capability)
			if !ok {
				return b, c, fmt.Errorf("unknown capability %q", capability)
			}
			return b, c, nil
		}
	}
	return Binding{}, Contract{}, fmt.Errorf("this service does not provide %s", capability)
}

// outcome is one capability call: what came back, what it cost, and whether
// the cost is known.
type outcome struct {
	result Result
	cost   float64
	priced bool
}

// run calls capability with inputs: the binding's defaults first, the
// caller's inputs over them. The output passes the contract's check before
// it is returned.
func (s Spec) run(ctx context.Context, sess *ToolSession, capability string, inputs map[string]any) (outcome, error) {
	var out outcome
	b, c, err := s.binding(capability)
	if err != nil {
		return out, err
	}
	vars := map[string]any{}
	for k, v := range b.Defaults {
		vars[k] = v
	}
	for k, v := range inputs {
		if v != nil && v != "" {
			vars[k] = v
		}
	}
	for _, in := range c.Inputs {
		if in.Required && (vars[in.Name] == nil || vars[in.Name] == "") {
			return out, fmt.Errorf("%s needs %s", c.Name, in.Name)
		}
	}
	t := transport{p: s.Provider, sess: sess}
	var last reply
	for i, st := range b.Steps {
		var r reply
		var err error
		if st.Call != "" {
			r, err = t.do(ctx, st.Call, vars)
		} else if r, err = s.wait(ctx, t, st, vars); err != nil {
			err = fmt.Errorf("step %d (waiting on %s): %w", i+1, st.Wait, err)
		}
		if err != nil {
			return out, err
		}
		for v, path := range st.Save {
			val := apijob.Lookup(r.node, fillPath(path, vars), "")
			if !apijob.Present(r.node, fillPath(path, vars), "") {
				return out, fmt.Errorf("step %d (%s): the reply has nothing at %q to save as %s: %s", i+1, st.Call+st.Wait, path, v, clip(r.text, 300))
			}
			vars[v] = val
		}
		last = r
	}
	res := Result{}
	for name, o := range b.Output {
		v, err := t.output(ctx, last, o, vars)
		if err != nil {
			return out, fmt.Errorf("output %s: %w", name, err)
		}
		res[name] = v
	}
	// Priced before the check: the provider did the work and billed for it
	// even when what came back fails the check.
	out.cost, out.priced = price(b.Cost, c, vars, res, last)
	if c.Check != nil {
		if err := c.Check(res); err != nil {
			return out, fmt.Errorf("%s came back, but %w", c.Name, err)
		}
	}
	out.result = res
	return out, nil
}

// wait polls the step's operation through the one wait engine (core/apijob)
// until its until condition holds, stopping on its fail condition.
func (s Spec) wait(ctx context.Context, t transport, st Step, vars map[string]any) (reply, error) {
	until, _ := parseCond(st.Until)
	fail, _ := parseCond(st.Fail)
	reason, _ := parseCond(st.Reason)
	var last reply
	id := fmt.Sprint(vars["id"])
	if vars["id"] == nil {
		id = st.Wait
	}
	spec := apijob.Spec{
		IDPath:       "id",
		PollURL:      st.Wait,
		IntervalSecs: st.EverySecs,
		MaxSecs:      st.MaxSecs,
		Check: func(node any, _ string) (bool, error) {
			if fail.holds(node, vars) {
				why := ""
				if reason != nil {
					why = apijob.Lookup(node, fillPath(reason.path, vars), "")
				}
				if why == "" {
					why = "no reason given"
				}
				return false, fmt.Errorf("the provider says it failed: %s", why)
			}
			return until.holds(node, vars), nil
		},
	}
	call := func(ctx context.Context, _, _ string) (apijob.Response, error) {
		r, err := t.do(ctx, st.Wait, vars)
		last = r
		if err != nil {
			if r.status >= 400 {
				return apijob.Response{Status: r.status, Body: []byte(r.text)}, nil
			}
			return apijob.Response{}, err
		}
		b, _ := json.Marshal(r.node)
		return apijob.Response{Status: 200, Body: b}, nil
	}
	submit, _ := json.Marshal(map[string]string{"id": id})
	_, err := apijob.Run(ctx, spec, t.p.BaseURL, submit, call, nil)
	return last, err
}

// output fills one contract output from the last reply.
func (t transport) output(ctx context.Context, last reply, o Output, vars map[string]any) (Value, error) {
	o.Path = fillPath(o.Path, vars)
	if o.From == "body" {
		if last.file == nil {
			return Value{}, fmt.Errorf("the last reply is not a file")
		}
		return Value{File: last.file}, nil
	}
	switch o.As {
	case "base64":
		s := apijob.Lookup(last.node, o.Path, "")
		if s == "" {
			return Value{}, fmt.Errorf("nothing at %q in the reply", o.Path)
		}
		f, err := writeBase64(s, t.maxBytes())
		return Value{File: f}, err
	case "url":
		u := apijob.Lookup(last.node, o.Path, "")
		if u == "" {
			return Value{}, fmt.Errorf("no URL at %q in the reply", o.Path)
		}
		addr, err := resolveURL(t.p.BaseURL, u)
		if err != nil {
			return Value{}, err
		}
		f, _, err := t.fetch(ctx, addr, "GET")
		return Value{File: f}, err
	case "json":
		return jsonOutput(last.node, o)
	}
	if o.Each != "" {
		return jsonOutput(last.node, o)
	}
	if last.node == nil {
		return Value{Text: last.text}, nil
	}
	return Value{Text: apijob.Lookup(last.node, o.Path, "")}, nil
}

// jsonOutput reads a json output: the value at its path, or with each, the
// piece at each in every element of the list there, as one list.
func jsonOutput(node any, o Output) (Value, error) {
	raw := apijob.LookupJSON(node, o.Path, "")
	if o.Each == "" {
		return Value{Text: raw}, nil
	}
	var list []any
	if err := json.Unmarshal([]byte(raw), &list); err != nil || raw == "" {
		return Value{}, fmt.Errorf("no list at %q in the reply", o.Path)
	}
	pieces := make([]json.RawMessage, len(list))
	for i, el := range list {
		p := apijob.LookupJSON(el, o.Each, "")
		if p == "" {
			return Value{}, fmt.Errorf("element %d of %q has nothing at %q", i, o.Path, o.Each)
		}
		piece := json.RawMessage(p)
		if !json.Valid(piece) {
			piece, _ = json.Marshal(p) // a string piece: LookupJSON gives it bare
		}
		pieces[i] = piece
	}
	b, err := json.Marshal(pieces)
	return Value{Text: string(b)}, err
}

// price is what a call cost: the provider's own figure when its reply has
// one, else the declared price. Unpriced when nothing is declared.
func price(c *Cost, contract Contract, vars map[string]any, res Result, last reply) (float64, bool) {
	if c == nil {
		return 0, false
	}
	if c.FromReply != "" {
		if v, err := strconv.ParseFloat(strings.TrimSpace(apijob.Lookup(last.node, fillPath(c.FromReply, vars), "")), 64); err == nil && v >= 0 {
			return v, true
		}
	}
	cost := c.PerCall
	if c.PerUnit > 0 && contract.units != nil {
		cost += c.PerUnit * contract.units(vars, res)
	}
	return cost, c.PerCall > 0 || c.PerUnit > 0
}

// estimate is what one call is expected to cost before it is made.
func (b Binding) estimate() (float64, bool) {
	if b.Cost == nil || (b.Cost.PerCall == 0 && b.Cost.PerUnit == 0) {
		return 0, false
	}
	return b.Cost.PerCall + b.Cost.PerUnit, true
}
