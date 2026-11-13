// Package decide asks a model a multiple-choice question and reads its answer
// from the probabilities of its first token: one prompt read, no answer
// written. It is System One done on an ordinary instruct model (the method
// SemIf uses): the options are labelled (A), (B), ...; the reply is
// pre-started "Answer: (" so the next token is a label; and the labels'
// probabilities, renormalised among themselves, are the decision.
//
// The model is served by vLLM (core's DecidePrefix). A decision is only as
// good as the model's first-token judgement, which is why its confidence
// comes with it: a caller acts on a confident answer and asks the full
// question when it is not.
package decide

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// Question is one decision: what the model is told (System), what it
// decides about (State), the question (Ask), and the options, in order.
// Both asks the question twice, the second time with the options reversed,
// and averages: small models lean toward the first option, and this
// cancels it for a decision where that matters.
type Question struct {
	System  string
	State   string
	Ask     string
	Options []string
	Both    bool
}

// Answer is a decision: the option chosen (its index in Question.Options),
// each option's probability, and how confident the model was. OK false is no
// decision: no option's label was among the model's likely first tokens.
type Answer struct {
	Choice     int
	Probs      []float64
	Confidence float64
	OK         bool
	Took       time.Duration
	// Native is an answer from the server's own /v1/systemone (vLLM 0.32+);
	// false, from decide's own reading of the first token. LabelMass is how
	// much of the model's probability fell on the options' labels at all
	// (native only; 0 when not known).
	Native    bool
	LabelMass float64
	// Orders are the options' probabilities as each ask saw them, the
	// options in order first, then reversed (Both), each renormalised among
	// the labels: an answer that follows the position it was shown in is a
	// leaning toward a label, not a judgement.
	Orders [][]float64
}

// prefix is what the reply is pre-started with, so its next token is a label.
const prefix = "Answer: ("

// topTokens is how many first tokens the server reports: enough to hold every
// label with room for the tokens a model also considers.
const topTokens = 20

// Ask decides q on llm. opts go on each call (a route key, a caller), after
// decide's own.
func Ask(ctx context.Context, llm LLM, q Question, opts ...ChatOption) (Answer, error) {
	n := len(q.Options)
	if n < 2 || n > 26 {
		return Answer{}, fmt.Errorf("a decision needs 2 to 26 options, has %d", n)
	}
	began := time.Now()
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	rev := make([]int, n)
	for i := range rev {
		rev[i] = n - 1 - i
	}
	// The server's own System One when it has one, else decide's reading of
	// the first token: for every ask on a server without it, and for one ask
	// its System One failed (vLLM's does while another request is
	// generating, where a chat's logprobs do not).
	ask := func(ord []int) ([]float64, float64, bool, error) {
		if p, mass, ok, err := askNative(ctx, llm, q, ord); err != nil || ok {
			return p, mass, ok, err
		}
		p, err := askOnce(ctx, llm, q, ord, opts)
		return p, 0, false, err
	}
	probs, mass, native, err := ask(order)
	if err != nil {
		return Answer{}, err
	}
	orders := [][]float64{normalised(probs)}
	if q.Both {
		second, mass2, native2, err := ask(rev)
		if err != nil {
			return Answer{}, err
		}
		orders = append(orders, normalised(second))
		for i := range probs {
			probs[i] = (probs[i] + second[i]) / 2
		}
		// Native only when both asks were: a first-token reading has no
		// label mass, and averaged in as nothing it halved the confidence.
		if native && native2 {
			mass = (mass + mass2) / 2
		} else {
			native, mass = false, 0
		}
	}
	a := answerFrom(probs, time.Since(began))
	a.Native, a.LabelMass, a.Orders = native, mass, orders
	// Weighted by how much of the model's probability was on the labels at
	// all, as the server's own confidence is: an answer the model barely put
	// on the options reads as the weak one it is.
	if native && mass > 0 {
		a.Confidence *= mass
	}
	return a, nil
}

// Firm is how firmly the answer was chosen: the weaker of the probabilities
// each ask gave the chosen option, when every ask chose it, and 0 when the
// asks chose differently. It is what decides whether an answer stands.
//
// The averaged Confidence read an answer right both times but not by much
// (0.73 one way, 0.53 the other) as a coin flip, and could not tell it from
// one that followed whichever option came first (0.10 one way, 0.93 the
// other), which is the answer that should not stand. Weighted by label mass
// like Confidence, when the server reported it.
func (a Answer) Firm() float64 {
	if !a.OK || len(a.Orders) == 0 {
		return 0
	}
	weakest := 1.0
	for _, o := range a.Orders {
		if a.Choice >= len(o) {
			return 0
		}
		for i, p := range o {
			if i != a.Choice && p > o[a.Choice] {
				return 0 // this ask chose otherwise
			}
		}
		if o[a.Choice] < weakest {
			weakest = o[a.Choice]
		}
	}
	if a.Native && a.LabelMass > 0 {
		weakest *= a.LabelMass
	}
	return weakest
}

// systemOner is a model that can be asked a structured decision directly:
// core's vLLM client, whose server may have /v1/systemone.
type systemOner interface {
	SystemOne(ctx context.Context, req map[string]any) (int, []byte, error)
}

// askNative asks the server's /v1/systemone with the options in order. ok
// false (and no error) is a server without it (not vLLM, too old, or a model
// it cannot read: status 0, 404, 405 or 501), or one whose System One failed
// on this ask (5xx), for the caller to ask the first token itself.
func askNative(ctx context.Context, llm LLM, q Question, order []int) ([]float64, float64, bool, error) {
	// A connection answers only this way: there is no first token to fall
	// back to.
	if c, ok := llm.(connection); ok {
		p, mass, err := c.choose(ctx, q, order)
		return p, mass, err == nil, err
	}
	so, can := llm.(systemOner)
	if !can {
		return nil, 0, false, nil
	}
	// The options in the order shown: the server labels them A, B, ... in
	// the order the request lists them, so they are written in that order
	// rather than marshalled from a map, which would sort them.
	var crit strings.Builder
	crit.WriteString("{")
	for k, i := range order {
		if k > 0 {
			crit.WriteString(",")
		}
		key, _ := json.Marshal(fmt.Sprintf("o%02d", i))
		desc, _ := json.Marshal(q.Options[i])
		crit.Write(key)
		crit.WriteString(":")
		crit.Write(desc)
	}
	crit.WriteString("}")
	question, _ := json.Marshal(strings.TrimSpace(q.Ask))
	questions := `{"d":{"type":"choice","instructions":` + string(question) + `,"criteria":` + crit.String() + `}}`
	req := map[string]any{
		"state":                strings.TrimSpace(q.State),
		"instructions":         strings.TrimSpace(q.System),
		"questions":            json.RawMessage(questions),
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	}
	status, body, err := so.SystemOne(ctx, req)
	if err != nil {
		return nil, 0, false, err
	}
	switch {
	case status == 0, status == http.StatusNotFound, status == http.StatusMethodNotAllowed, status == http.StatusNotImplemented:
		return nil, 0, false, nil
	case status >= 500:
		// The server's System One failed on this ask (vLLM's has no
		// logprobs while it batches it with a request that is generating):
		// the first token is read instead, through a chat, which has them.
		Debug("[decide] the server's System One answered HTTP %d (%s): reading the first token instead", status, strings.TrimSpace(string(body)))
		return nil, 0, false, nil
	case status == http.StatusOK:
	default:
		return nil, 0, false, fmt.Errorf("the server's System One answered HTTP %d: %s", status, strings.TrimSpace(string(body)))
	}
	var reply struct {
		Answers map[string]struct {
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"answers"`
		Diagnostics map[string]struct {
			LabelMass float64 `json:"label_mass"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return nil, 0, false, fmt.Errorf("the server's System One reply was not readable: %v", err)
	}
	probs := make([]float64, len(q.Options))
	for i := range q.Options {
		probs[i] = reply.Answers["d"].Probabilities[fmt.Sprintf("o%02d", i)]
	}
	// The probabilities are the share among the labels; label_mass is how
	// much of the model's whole probability was on them (Ask weighs the
	// confidence by it).
	mass := reply.Diagnostics["d"].LabelMass
	return probs, mass, true, nil
}

// askOnce asks with the options in order (order[k] is the option shown at
// label k) and returns each option's probability, in Question.Options order,
// or all zero when no label was a likely first token.
func askOnce(ctx context.Context, llm LLM, q Question, order []int, opts []ChatOption) ([]float64, error) {
	var b strings.Builder
	if s := strings.TrimSpace(q.State); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	b.WriteString(strings.TrimSpace(q.Ask))
	b.WriteString("\n")
	for k, i := range order {
		fmt.Fprintf(&b, "(%c) %s\n", 'A'+k, q.Options[i])
	}
	b.WriteString("\nAnswer with the letter of one option.")
	call := append([]ChatOption{
		WithSystemPrompt(q.System),
		WithThink(false),
		WithTemperature(0),
		func(c *ChatConfig) { c.DecidePrefix, c.TopLogprobs = prefix, topTokens },
	}, opts...)
	resp, err := llm.Chat(ctx, []Message{{Role: "user", Content: b.String()}}, call...)
	if err != nil {
		return nil, err
	}
	probs := make([]float64, len(q.Options))
	for _, t := range resp.TopLogprobs {
		k, ok := labelIndex(t.Token, len(order))
		if !ok {
			continue
		}
		// The first spelling of a label is its most likely one; another
		// spelling of the same letter (" A" beside "A") adds to it.
		probs[order[k]] += math.Exp(t.Logprob)
	}
	return probs, nil
}

// labelIndex reads a token as an option label: a single letter A, B, ...
// within the options given, with any space or bracket around it.
func labelIndex(token string, n int) (int, bool) {
	t := strings.Trim(strings.TrimSpace(token), "()[].:")
	if len(t) != 1 {
		return 0, false
	}
	c := t[0]
	if c >= 'a' && c <= 'z' {
		c -= 'a' - 'A'
	}
	k := int(c) - 'A'
	return k, k >= 0 && k < n
}

// normalised is p renormalised to sum to one, or p when it sums to nothing.
func normalised(p []float64) []float64 {
	total := 0.0
	for _, v := range p {
		total += v
	}
	out := append([]float64(nil), p...)
	if total <= 0 {
		return out
	}
	for i := range out {
		out[i] /= total
	}
	return out
}

// answerFrom renormalises the options' probabilities among themselves, picks
// the likeliest, and says how confident that is: Kev's measure, how far the
// top probability stands above an even split, 0 at even and 1 when certain.
func answerFrom(raw []float64, took time.Duration) Answer {
	total := 0.0
	for _, p := range raw {
		total += p
	}
	if total <= 0 {
		return Answer{Probs: raw, Took: took}
	}
	a := Answer{Probs: make([]float64, len(raw)), OK: true, Took: took}
	for i, p := range raw {
		a.Probs[i] = p / total
		if a.Probs[i] > a.Probs[a.Choice] {
			a.Choice = i
		}
	}
	even := 1 / float64(len(raw))
	a.Confidence = (a.Probs[a.Choice] - even) / (1 - even)
	return a
}
