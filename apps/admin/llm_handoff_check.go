package admin

// The handoff check: a short scripted conversation played against one model,
// to find the ways a provider refuses a conversation it did not write alone.
//
// "Reply with ok" proves the key and the host. It said nothing about the
// failure that actually took the lead away: Gemini 3 answered the first round
// of every turn and refused every follow-up, because the client dropped the
// signature it must send back with a tool call. That only shows on the SECOND
// call of a tool round, and gohort's lead and worker take turns in one
// conversation, so each model also has to accept a call the other one made.
// Every provider has its own rules for that (Gemini's signatures, Anthropic's
// ID pattern, OpenAI's ID length), and a provider changing them breaks the
// handoff, not the hello.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	handoffPass = iota
	handoffFail
	handoffSkip
)

// handoffStep is one step's outcome.
type handoffStep struct {
	name  string
	state int
	note  string
}

// handoffCheck runs the steps against llm. partner is the other tier's live
// model (nil when there is none), whose calls this model must continue and
// which must continue this model's.
type handoffCheck struct {
	llm          LLM
	tools        bool // false: this model calls no tools natively, so the tool steps are skipped
	partner      LLM
	partnerName  string // "the worker", "the lead"
	partnerTools bool
	steps        []handoffStep
	// onStep, when set, hears each step as it starts, for a page watching
	// the check run.
	onStep func(name string)
}

// stepCount is how many steps run will start.
func (h *handoffCheck) stepCount() int {
	if !h.tools {
		return 0
	}
	if h.partner != nil {
		return 4
	}
	return 3
}

func (h *handoffCheck) begin(name string) {
	if h.onStep != nil {
		h.onStep(name)
	}
}

// handoffWords are what lookup_word answers.
var handoffWords = map[string]string{
	"kestrel": "a small falcon that hovers over open ground",
	"heron":   "a long-legged wading bird",
	"wren":    "a very small brown songbird",
}

var handoffTool = Tool{
	Name:        "lookup_word",
	Description: "Look up a word in the dictionary and return its definition.",
	Parameters:  map[string]ToolParam{"word": {Type: "string", Description: "The word to look up."}},
	Required:    []string{"word"},
}

func (h *handoffCheck) opts() []ChatOption {
	return []ChatOption{WithTools([]Tool{handoffTool}), WithMaxTokens(2048)}
}

func (h *handoffCheck) add(name string, state int, note string) {
	h.steps = append(h.steps, handoffStep{name: name, state: state, note: note})
}

// run plays every step in order. A step that cannot start (the model made no
// call to continue) skips the ones that need it rather than failing them.
func (h *handoffCheck) run(ctx context.Context) {
	if !h.tools {
		h.add("Tool steps", handoffSkip, "this model is not set to call tools natively")
		return
	}
	own := h.roundTrip(ctx)
	if ctx.Err() != nil {
		return
	}
	h.twoCalls(ctx)
	if ctx.Err() != nil {
		return
	}
	h.continuesForeign(ctx)
	if ctx.Err() != nil {
		return
	}
	h.partnerContinues(ctx, own)
}

// handoffAsk is the opening turn of a tool step.
func handoffAsk(text string) []Message {
	return []Message{{Role: "user", Content: text}}
}

// handoffReplay is history with the model's turn and the tool results added,
// the shape the agent loop sends on its next round.
func handoffReplay(hist []Message, resp *Response) []Message {
	var results []ToolResult
	for _, tc := range resp.ToolCalls {
		word, _ := tc.Args["word"].(string)
		def, ok := handoffWords[strings.ToLower(strings.TrimSpace(word))]
		if !ok {
			def = "no entry for that word"
		}
		results = append(results, ToolResult{ID: tc.ID, Content: def})
	}
	out := append([]Message(nil), hist...)
	return append(out,
		Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls},
		Message{Role: "user", ToolResults: results})
}

// answered reports a continuation that produced something: text, or another
// call. An empty one is what makes the agent loop fall back.
func answered(resp *Response) bool {
	return resp != nil && (strings.TrimSpace(resp.Content) != "" || len(resp.ToolCalls) > 0)
}

// continueFrom sends hist to llm over streaming, the way the agent loop does.
func (h *handoffCheck) continueFrom(ctx context.Context, llm LLM, hist []Message) error {
	resp, err := llm.ChatStream(ctx, hist, func(string) {}, h.opts()...)
	if err != nil {
		return err
	}
	if !answered(resp) {
		return fmt.Errorf("it answered nothing")
	}
	return nil
}

// roundTrip is one tool call and then its result: the step that broke. It
// returns the history ending in this model's call, for partnerContinues.
func (h *handoffCheck) roundTrip(ctx context.Context) []Message {
	const name = "Tool call, then its result"
	h.begin(name)
	ask := handoffAsk(`Use the lookup_word tool to look up "kestrel", then tell me in one sentence what it said.`)
	resp, err := h.llm.Chat(ctx, ask, h.opts()...)
	if err != nil {
		h.add(name, handoffFail, "the call itself: "+err.Error())
		return nil
	}
	if len(resp.ToolCalls) == 0 {
		h.add(name, handoffFail, "it answered without calling the tool it was told to use, and agents work through tool calls")
		return nil
	}
	hist := handoffReplay(ask, resp)
	if err := h.continueFrom(ctx, h.llm, hist); err != nil {
		h.add(name, handoffFail, "sending its own call back with the result: "+err.Error())
		return nil
	}
	h.add(name, handoffPass, "")
	return hist
}

// twoCalls is two calls in one turn, both results sent back together.
func (h *handoffCheck) twoCalls(ctx context.Context) {
	const name = "Two calls in one turn"
	h.begin(name)
	ask := handoffAsk(`In this one turn, call lookup_word twice, once for "heron" and once for "wren". Then say what both are.`)
	resp, err := h.llm.Chat(ctx, ask, h.opts()...)
	if err != nil {
		h.add(name, handoffFail, "the call itself: "+err.Error())
		return
	}
	if len(resp.ToolCalls) < 2 {
		h.add(name, handoffSkip, "it made one call at a time, which works too")
		return
	}
	if err := h.continueFrom(ctx, h.llm, handoffReplay(ask, resp)); err != nil {
		h.add(name, handoffFail, "sending both calls back with their results: "+err.Error())
		return
	}
	h.add(name, handoffPass, "")
}

// continuesForeign is this model continuing a call it did not make: the
// partner's own, when there is a partner that calls tools, else one written
// here with another provider's ID shape and none of this provider's
// metadata, which is what any other model's call looks like to it.
func (h *handoffCheck) continuesForeign(ctx context.Context) {
	ask := handoffAsk(`Use the lookup_word tool to look up "kestrel", then tell me in one sentence what it said.`)
	name := "Continues another model's tool call"
	h.begin(name)
	var hist []Message
	if h.partner != nil && h.partnerTools {
		if resp, err := h.partner.Chat(ctx, ask, h.opts()...); err == nil && len(resp.ToolCalls) > 0 {
			name = "Continues " + h.partnerName + "'s tool call"
			hist = handoffReplay(ask, resp)
		}
	}
	if hist == nil {
		hist = handoffReplay(ask, &Response{ToolCalls: []ToolCall{{
			ID: "call_7f3a9c2e", Name: handoffTool.Name, Args: map[string]any{"word": "kestrel"}}}})
	}
	if err := h.continueFrom(ctx, h.llm, hist); err != nil {
		h.add(name, handoffFail, err.Error())
		return
	}
	h.add(name, handoffPass, "")
}

// partnerContinues is the reverse: the partner continuing this model's call.
func (h *handoffCheck) partnerContinues(ctx context.Context, own []Message) {
	if h.partner == nil {
		return
	}
	name := handoffTitle(h.partnerName) + " continues this model's tool call"
	h.begin(name)
	if own == nil {
		h.add(name, handoffSkip, "this model made no call to continue")
		return
	}
	if !h.partnerTools {
		h.add(name, handoffSkip, h.partnerName+" is not set to call tools natively")
		return
	}
	if err := h.continueFrom(ctx, h.partner, own); err != nil {
		h.add(name, handoffFail, err.Error())
		return
	}
	h.add(name, handoffPass, "")
}

// handoffTitle capitalizes "the worker" for the start of a line.
func handoffTitle(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// failed reports whether any step failed.
func (h *handoffCheck) failed() bool {
	for _, s := range h.steps {
		if s.state == handoffFail {
			return true
		}
	}
	return false
}

// firstFailure is the first failing step as one line, or "".
func (h *handoffCheck) firstFailure() string {
	for _, s := range h.steps {
		if s.state == handoffFail {
			return s.name + ": " + s.note
		}
	}
	return ""
}

// lines is one line per step: a mark, the step, and what happened.
func (h *handoffCheck) lines() string {
	var b strings.Builder
	for _, s := range h.steps {
		mark := "✓"
		switch s.state {
		case handoffFail:
			mark = "✗"
		case handoffSkip:
			mark = "–"
		}
		b.WriteString("\n" + mark + " " + s.name)
		if s.note != "" {
			b.WriteString(": " + s.note)
		}
	}
	return b.String()
}

// handoffPartner is the other tier's live model for a check of table's: the
// worker for the lead, the lead (when one is configured) for the worker.
func handoffPartner(table string) (LLM, string, bool) {
	if table == LeadLLMTable {
		if SharedWorkerLLM() == nil {
			return nil, "", false
		}
		promptTools, _ := PromptToolsMode()
		return ReloadableWorkerLLM(), "the worker", !promptTools
	}
	if !LeadIsDistinct() {
		return nil, "", false
	}
	return ReloadableLeadLLM(), "the lead", true
}

// The check that runs after a save, against what is now live, so a model
// that cannot hand off is found when it is set rather than by its answers
// getting worse. Only the finished result is kept: it runs in the
// background, and nothing claims it is still going.
type handoffResult struct {
	at    time.Time
	model string
	ok    bool
	text  string // the step lines
	first string // the first failure, for the dashboard
}

var (
	handoffMu   sync.Mutex
	handoffLast = map[string]handoffResult{}
)

// checkAfterSave runs the check against table's live model and keeps the
// result. Off the request's context: the save has answered by the time this
// is half done.
func checkAfterSave(table string) {
	var llm LLM
	tools := true
	if table == LeadLLMTable {
		if !LeadIsDistinct() {
			handoffMu.Lock()
			delete(handoffLast, table)
			handoffMu.Unlock()
			return
		}
		llm = ReloadableLeadLLM()
	} else {
		llm = ReloadableWorkerLLM()
		promptTools, _ := PromptToolsMode()
		tools = !promptTools
	}
	partner, partnerName, partnerTools := handoffPartner(table)
	h := &handoffCheck{llm: llm, tools: tools, partner: partner, partnerName: partnerName, partnerTools: partnerTools}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	h.run(ctx)
	worker, lead := LiveLLMs()
	model := worker
	if table == LeadLLMTable {
		model = lead
	}
	res := handoffResult{at: time.Now(), model: model, ok: !h.failed(), text: h.lines(), first: h.firstFailure()}
	if ctx.Err() != nil {
		res.ok = false
		res.first = "the check ran out of time"
		res.text += "\n✗ the check ran out of time before it finished"
	}
	handoffMu.Lock()
	handoffLast[table] = res
	handoffMu.Unlock()
	if !res.ok {
		Warn("[admin] %s failed its handoff check after the save: %s", model, res.first)
	}
}

// handoffDescription is the last after-save result for the form, or "".
func handoffDescription(table string) string {
	handoffMu.Lock()
	res, ok := handoffLast[table]
	handoffMu.Unlock()
	if !ok {
		return ""
	}
	head := "Passed"
	if !res.ok {
		head = "FAILED"
	}
	return fmt.Sprintf("%s, checked after the save at %s (%s):%s", head, res.at.Format("Jan 2 15:04"), res.model, res.text)
}

// handoffFailure is the last after-save failure for table, or "".
func handoffFailure(table string) (model, first string) {
	handoffMu.Lock()
	defer handoffMu.Unlock()
	if res, ok := handoffLast[table]; ok && !res.ok {
		return res.model, res.first
	}
	return "", ""
}
