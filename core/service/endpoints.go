package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Endpoint is one endpoint of a service as a person reads it: where it is,
// what it is sent and where each value comes from, what is read from its
// reply, and what uses it. Everything is read from the spec: it is the part
// of the endpoint the mapping uses, not the resource's whole schema.
type Endpoint struct {
	Name string `json:"name"`
	// Address is "POST /v1/images", or a program's command lines.
	Address string `json:"address"`
	UsedBy  string `json:"used_by"`
	Sends   string `json:"sends"`
	Reads   string `json:"reads"`
}

// endpointUse is one place a binding uses an operation.
type endpointUse struct {
	b    Binding
	step int
}

// Endpoints are the spec's endpoints: those its capabilities use, in the
// order their steps run, then the health check, then any nothing uses.
func (s Spec) Endpoints() []Endpoint {
	uses := map[string][]endpointUse{}
	var order []string
	for _, b := range s.Provides {
		for i, st := range b.Steps {
			op := st.Call + st.Wait
			if _, seen := uses[op]; !seen {
				order = append(order, op)
			}
			uses[op] = append(uses[op], endpointUse{b, i})
		}
	}
	health := ""
	if h := s.Provider.Health; h != nil && h.Call != "" {
		health = h.Call
		if _, seen := uses[health]; !seen {
			order = append(order, health)
			uses[health] = nil
		}
	}
	var unused []string
	for name := range s.Provider.Operations {
		if _, seen := uses[name]; !seen {
			unused = append(unused, name)
		}
	}
	sort.Strings(unused)
	order = append(order, unused...)

	var out []Endpoint
	for _, name := range order {
		op := s.Provider.Operations[name]
		e := Endpoint{Name: name, Address: s.address(op)}
		var used, reads []string
		var defaults map[string]any
		for _, u := range uses[name] {
			gives := u.b.Capability
			if c, ok := Lookup(u.b.Capability); ok && c.Gives != "" {
				gives = c.Gives
			}
			if len(u.b.Steps) > 1 {
				gives += fmt.Sprintf(", step %d", u.step+1)
			}
			used = append(used, gives)
			if defaults == nil {
				defaults = u.b.Defaults
			}
			reads = append(reads, stepReads(u.b, u.step, op)...)
		}
		if name == health {
			used = append(used, "health check")
			if x := s.Provider.Health.Expect; x != "" {
				reads = append(reads, "expects "+x)
			}
		}
		if len(used) == 0 {
			used = []string{"not used"}
		}
		e.UsedBy = strings.Join(used, "; ")
		e.Sends = sends(op, defaults)
		e.Reads = strings.Join(dedupe(reads), "; ")
		out = append(out, e)
	}
	return out
}

// address is where an operation is: its method and path, or for a program,
// its command lines in order.
func (s Spec) address(op Operation) string {
	if s.Provider.Transport == "command" {
		return strings.Join(op.Run, "  ⇒  ")
	}
	method := op.Method
	if method == "" {
		method = "GET"
	}
	where := op.Path
	if op.URL != "" {
		where = op.URL
	}
	return strings.ToUpper(method) + " " + where
}

// sends is what an operation's request carries and where each value comes
// from: a {token} is a contract input or a value an earlier step saved, and
// anything else is written as it is. A token with a default says what it is.
func sends(op Operation, defaults map[string]any) string {
	if op.Body == nil || op.Send == "none" {
		return ""
	}
	mode := op.Send
	if mode == "" {
		mode = "json"
	}
	if mode == "file" {
		return "the whole body ← " + valueText(op.Body, defaults)
	}
	fields, ok := op.Body.(map[string]any)
	if !ok {
		return mode + ": " + valueText(op.Body, defaults)
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+" ← "+valueText(fields[k], defaults))
	}
	return mode + ": " + strings.Join(parts, "; ")
}

// valueText is one body value as written: a template as it is, a literal
// quoted, and a whole-value token with its default.
func valueText(v any, defaults map[string]any) string {
	str, ok := v.(string)
	if !ok {
		b, _ := json.Marshal(v)
		return string(b)
	}
	if !tokenRE.MatchString(str) {
		return fmt.Sprintf("%q", str)
	}
	if m := tokenRE.FindStringSubmatch(str); m[0] == str && defaults[m[1]] != nil {
		return fmt.Sprintf("%s (default %v)", str, defaults[m[1]])
	}
	if tokenRE.FindString(str) == str {
		return str
	}
	return fmt.Sprintf("%q", str)
}

// stepReads is what one step reads from its operation's reply: what it
// saves, what it waits for, and, on the last step, the contract's outputs.
func stepReads(b Binding, i int, op Operation) []string {
	st := b.Steps[i]
	var out []string
	if st.Until != "" {
		w := "until " + st.Until
		if st.Fail != "" {
			w += ", failing on " + st.Fail
		}
		out = append(out, w)
	}
	saves := make([]string, 0, len(st.Save))
	for v, path := range st.Save {
		saves = append(saves, path+" → "+v)
	}
	sort.Strings(saves)
	out = append(out, saves...)
	if i != len(b.Steps)-1 {
		return out
	}
	names := make([]string, 0, len(b.Output))
	for n := range b.Output {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		o := b.Output[n]
		switch {
		case o.From == "body":
			out = append(out, "the reply → "+n+" (a file)")
		case o.Each != "":
			out = append(out, o.Path+"[each]."+o.Each+" → "+n)
		case o.As == "base64":
			out = append(out, o.Path+" → "+n+" (a file, from base64)")
		case o.As == "url":
			out = append(out, o.Path+" → "+n+" (a file, fetched)")
		case o.Path == "" && op.Read == "text":
			out = append(out, "the reply → "+n)
		default:
			out = append(out, o.Path+" → "+n)
		}
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
