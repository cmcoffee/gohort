package service

// What a Fix works with: the connector as the two halves (an older kind
// translated), a draft checked against it, one request of a draft with its
// raw reply, and a draft's contract test. A Fix proposes; the admin saves.
// It may change how the service is reached and how each capability's steps
// read the replies, never which capabilities the connector provides, what
// they cost, or its test budget: those are the admin's.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// ForFix is connector name as the two halves: a service's own spec, or an
// older kind's translated, with notes on what did not carry over.
func ForFix(name string) (Spec, []string, Connector, error) {
	c, ok := GetConnector(RootDB, name)
	if !ok {
		return Spec{}, nil, c, fmt.Errorf("no connector named %q", name)
	}
	switch c.Kind {
	case Kind:
		s, err := Parse(c.Spec)
		return s, nil, c, err
	case RestImageConnectorKind:
		s, notes, err := FromRestImage(c)
		return s, notes, c, err
	}
	return Spec{}, nil, c, fmt.Errorf("a %s connector cannot be fixed here", c.Kind)
}

// Fixable reports whether c opens in a Fix: a service, or an older image
// backend whose shape translates.
func Fixable(c Connector) bool {
	switch c.Kind {
	case Kind:
		return true
	case RestImageConnectorKind:
		_, _, err := FromRestImage(c)
		return err == nil
	}
	return false
}

// CheckDraft refuses a draft that is not valid, or that changes what the
// admin decides: its capabilities, their prices, its test budget.
func CheckDraft(name string, raw string) (Spec, error) {
	draft, err := Parse([]byte(raw))
	if err != nil {
		return draft, err
	}
	if err := draft.Validate(); err != nil {
		return draft, err
	}
	cur, _, _, err := ForFix(name)
	if err != nil {
		return draft, err
	}
	if a, b := capabilities(cur), capabilities(draft); a != b {
		return draft, fmt.Errorf("a fix keeps what the connector provides (%s); this draft provides %s", a, b)
	}
	for _, b := range draft.Provides {
		was, _, _ := cur.binding(b.Capability)
		if prices(was.Cost) != prices(b.Cost) {
			return draft, fmt.Errorf("a fix keeps %s's price as the admin set it (%s)", b.Capability, prices(was.Cost))
		}
	}
	if draft.TestBudget != cur.TestBudget {
		return draft, fmt.Errorf("a fix keeps the test budget (%d)", cur.TestBudget)
	}
	return draft, nil
}

func capabilities(s Spec) string {
	var out []string
	for _, b := range s.Provides {
		out = append(out, b.Capability)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func prices(c *Cost) string {
	if c == nil {
		return "none declared"
	}
	b, _ := json.Marshal(c)
	return string(b)
}

// budget is how many contract tests connector c may run a day.
func budget(s Spec) int {
	if s.TestBudget > 0 {
		return s.TestBudget
	}
	return defaultTestBudget
}

// takeTest counts one paid call against today's budget, or refuses it with
// what it would cost.
func takeTest(name string, s Spec) error {
	allowed := false
	limit := budget(s)
	updateRecord(name, func(r *Record) {
		if r.Tests < limit {
			r.Tests++
			allowed = true
		}
	})
	if allowed {
		return nil
	}
	est, known := 0.0, true
	for _, b := range s.Provides {
		v, k := b.estimate()
		est += v
		known = known && k
	}
	what := "an unknown amount, as no price is declared"
	if known {
		what = fmt.Sprintf("about $%.4f", est)
	}
	return fmt.Errorf("%s has used its %d contract tests for today; another would cost %s. Raise test_budget on the connector to allow more", name, limit, what)
}

// TestsLeft is how many paid calls connector name has left today.
func TestsLeft(name string, s Spec) int {
	if n := budget(s) - RecordOf(name).Tests; n > 0 {
		return n
	}
	return 0
}

// TryDraft runs a draft's contract tests against the service, on the
// connector's budget, without saving it.
func TryDraft(ctx context.Context, name string, draft Spec) ([]TestReport, error) {
	if err := takeTest(name, draft); err != nil {
		return nil, err
	}
	return runTests(ctx, name, draft), nil
}

// RequestDraft makes one operation of a draft, with values for its tokens,
// and gives the raw reply: what a fix needs to see how the service answers
// now. The free health operation costs nothing; any other draws on the
// budget, as it may cost.
func RequestDraft(ctx context.Context, name string, draft Spec, operation string, values map[string]any) (string, error) {
	op, ok := draft.Provider.Operations[operation]
	if !ok {
		return "", fmt.Errorf("the draft has no operation %q", operation)
	}
	free := draft.Provider.Health != nil && draft.Provider.Health.Call == operation
	if !free {
		if err := takeTest(name, draft); err != nil {
			return "", err
		}
	}
	for _, tok := range opTokens(op) {
		if _, ok := values[tok]; !ok {
			return "", fmt.Errorf("%s uses {%s}: give it in values", operation, tok)
		}
	}
	vars := map[string]any{}
	for k, v := range values {
		vars[k] = v
	}
	for _, b := range draft.Provides {
		c, _ := Lookup(b.Capability)
		for _, name := range c.TestImages {
			if _, ok := vars[name]; !ok {
				if f, err := samplePicture(); err == nil {
					vars[name] = f
				}
			}
		}
	}
	r, err := transport{p: draft.Provider}.do(ctx, operation, vars)
	var b strings.Builder
	fmt.Fprintf(&b, "HTTP %d", r.status)
	switch {
	case r.file != nil:
		fmt.Fprintf(&b, ", a file: %s, %d bytes", r.file.Type, r.file.Size)
	case r.node != nil:
		pretty, _ := json.MarshalIndent(r.node, "", "  ")
		b.WriteString("\n" + clip(string(pretty), 6000))
	default:
		b.WriteString("\n" + clip(r.text, 6000))
	}
	if err != nil {
		b.WriteString("\nError: " + err.Error())
	}
	if !free {
		fmt.Fprintf(&b, "\n(%d paid calls left today)", TestsLeft(name, draft))
	}
	return b.String(), nil
}

// runTests runs each capability's contract test of s, recorded as tests.
func runTests(ctx context.Context, name string, s Spec) []TestReport {
	var reports []TestReport
	for _, b := range s.Provides {
		ct, _ := Lookup(b.Capability)
		rep := TestReport{Capability: b.Capability}
		if b.Dialect != "" {
			reports = append(reports, testModel(ctx, name))
			continue
		}
		in, err := ct.testInputs()
		if err != nil {
			rep.Detail = err.Error()
			reports = append(reports, rep)
			continue
		}
		out, err := s.run(ctx, nil, b.Capability, in)
		if err == nil && ct.testCheck != nil {
			if err = ct.testCheck(out.result); err != nil {
				err = fmt.Errorf("%s came back, but %w", ct.Name, err)
			}
		}
		noteTest(ctx, name, b.Capability, out)
		rep.OK, rep.Cost, rep.Priced = err == nil, out.cost, out.priced
		if err != nil {
			rep.Detail = err.Error()
		} else {
			rep.Detail = "passed: " + describe(out.result)
			for _, v := range out.result {
				if v.File != nil {
					rep.File = v.File.Path
				}
			}
		}
		reports = append(reports, rep)
	}
	return reports
}

// testModel is a model connector's test: a short real chat through the
// client its dialect names, as a tier on it would make, on the connector as
// saved (a model's key is read from its credential, never from a draft).
func testModel(ctx context.Context, name string) TestReport {
	rep := TestReport{Capability: "llm.chat"}
	if c, ok := GetConnector(RootDB, name); !ok || !c.Approved {
		rep.Detail = "a model is tested by a real chat once its connector is saved and approved (its key is read from its credential then)"
		return rep
	}
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: media.ServiceProviderPrefix + name})
	if err != nil {
		rep.Detail = err.Error()
		return rep
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := llm.Chat(ctx, []Message{{Role: "user", Content: "Reply with the single word OK."}})
	switch {
	case err != nil:
		rep.Detail = err.Error()
	case strings.TrimSpace(resp.Content) == "":
		rep.Detail = "the model answered with nothing"
	default:
		rep.OK, rep.Detail = true, fmt.Sprintf("passed: the model answered %q (%d tokens in, %d out)", clip(strings.TrimSpace(resp.Content), 40), resp.InputTokens, resp.OutputTokens)
	}
	return rep
}

// Grammar is the spec's shape as an assistant is told it.
const Grammar = `A service spec is JSON in two halves.
provider: {transport: "http", base_url, auth: {credential: "<name>"} or {none: true}, headers?: {name: value} (never auth: the credential adds it), operations: {<name>: {method, path (under base_url) or url, send?: json|form|multipart|file|none, body?, read?: json|text|file, timeout_secs?}}, limits?: {timeout_secs, max_bytes}, health?: {call: <a free operation>, expect?: <condition>, every_mins?}}.
provides: [{capability, steps: [{call: <operation>, save?: {var: path}} or {wait: <operation>, until: <condition>, fail?: <condition>, reason?: path, every_secs?, max_secs?, save?}], output: {<name>: {path, as: base64|url|text|json, each?: path} or {from: "body"}}, defaults?: {input: value}, cost?: {per_call?, per_unit?, unit?, from_reply?: path}}].
A local program instead: provider: {transport: "command", network?: true, operations: {<name>: {run: ["<command line>", ...], read?: json|text|file, timeout_secs?}}, health?} with no base_url, auth or headers; in a run line {token} is an input or saved value (a file input is a file), {in} the line before's result, {out} or {out.png} the file it writes (with no {out}, what it prints), every word quoted so no shell reads it, in a sandbox with no network unless network is true.
Bodies are templates: a string that is exactly "{token}" becomes the value with its type; a token inside a longer string is written in. Tokens are the capability's inputs and what earlier steps saved. send multipart: body is its parts, one may be a file input ("{image}"); send file: body is one file input's token, sent as the whole body.
Paths: a.b.0.c, list[field=value] for the first element whose field equals value, list[field] for the first that has the field, and {var} for a saved variable. A json output with each reads the list at path piece by piece: each is the path within every element to the piece kept (path "data", each "embedding" makes a list of the embeddings). A json output with fields reads the list at path into objects of the contract's field names, each the path within every element it comes from (path "organic", fields {"title": "title", "url": "link", "snippet": "snippet"}); a reply with no list there reads as an empty list. Conditions: path == a|b, path != a, or a bare path that holds something.`

// ContractText says what a capability takes and gives, for an assistant.
func ContractText(capability string) string {
	c, ok := Lookup(capability)
	if !ok {
		return capability + ": unknown"
	}
	var in, out []string
	for _, p := range c.Inputs {
		s := p.Name + " (" + string(p.Type)
		if p.Required {
			s += ", required"
		}
		in = append(in, s+")")
	}
	for _, p := range c.Outputs {
		out = append(out, p.Name+" ("+string(p.Type)+")")
	}
	return fmt.Sprintf("%s takes %s and gives %s, which must pass its check (an image must be a real PNG, JPEG, GIF or WebP picture).",
		c.Name, strings.Join(in, ", "), strings.Join(out, ", "))
}
