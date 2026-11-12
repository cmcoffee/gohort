package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

// A refusal of a field as sent moves its subject one step down the field's
// ladder, says so once, and only for that subject; one that is not a refusal
// of the field moves nothing. The retry marker carries the field, and Forget
// puts every subject back on the top step.
func TestARefusalMovesItsSubjectOneStep(t *testing.T) {
	const field = "test.field"
	said := 0
	registerRequestField(requestField{
		key: field,
		top: "a",
		next: func(current string, err error) (string, bool) {
			if err.Error() != "refused" {
				return "", false
			}
			switch current {
			case "a":
				return "b", true
			case "b":
				return "c", true
			}
			return "", false
		},
		said: func(string, string, string, error) { said++ },
	})
	t.Cleanup(func() { forgetAdaptations(field) })

	if got := adaptedStep("m1", field); got != "a" {
		t.Fatalf("a subject starts on %q, want the top step", got)
	}
	if adaptFrom("m1", field, errors.New("something else")) || adaptedStep("m1", field) != "a" {
		t.Error("an error that is not a refusal of the field moved it")
	}
	if !adaptFrom("m1", field, errors.New("refused")) || adaptedStep("m1", field) != "b" || said != 1 {
		t.Errorf("a refusal: on %q, said %d times; want b, once", adaptedStep("m1", field), said)
	}
	if adaptedStep("m2", field) != "a" {
		t.Error("another subject moved with it")
	}
	adaptFrom("m1", field, errors.New("refused"))
	if adaptFrom("m1", field, errors.New("refused")) || adaptedStep("m1", field) != "c" || said != 2 {
		t.Errorf("the bottom of the ladder: on %q, said %d times; want c, twice, and no further move", adaptedStep("m1", field), said)
	}
	if setAdaptedStep("m1", field, "c", nil) || said != 2 {
		t.Error("setting the step it is already on counted as a move")
	}
	err := adaptedRetry{field: field, error: errors.New("refused")}
	if f, ok := adaptedField(err); !ok || f != field || !errors.Is(err, err.error) {
		t.Errorf("the retry marker: %q, %v", f, ok)
	}
	forgetAdaptations(field)
	if adaptedStep("m1", field) != "a" {
		t.Error("Forget left the subject where it was")
	}
}

// adaptTestDB gives a test its own store, and the next read loads from it.
func adaptTestDB(t *testing.T) {
	t.Helper()
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
}

// reloadAdaptations is a restart: the next read loads from the store again.
func reloadAdaptations() {
	adaptState.Lock()
	adaptState.loaded = false
	adaptState.Unlock()
}

// A move is stored with the refusal that made it and survives a restart; a
// step older than adaptRecheckAfter is dropped, so the field is sent as
// configured again; List says it in words; Forget puts it back at once.
func TestAMoveIsStoredListedAndExpires(t *testing.T) {
	adaptTestDB(t)
	t.Cleanup(func() { forgetAdaptations(fieldThinkingOff) })
	err := &APIError{Provider: "bedrock-runtime", StatusCode: 400, Message: `To turn thinking off on this model, send "thinking": {"type": "between_tools"} instead of {"type": "disabled"}.`}
	if !adaptFrom("claude-test-stored", fieldThinkingOff, err) {
		t.Fatal("the refusal did not move it")
	}
	reloadAdaptations()
	if got := thinkingOffFor("claude-test-stored"); got != anthOffBetweenTools {
		t.Fatalf("after a restart it is on %q, want between_tools", got)
	}
	list := ListRequestAdaptations()
	if len(list) != 1 || list[0].What != "Thinking off" || !strings.Contains(list[0].Sends, "between_tools") || !strings.Contains(list[0].Refusal, "instead of") {
		t.Errorf("listed %+v", list)
	}
	if !ForgetRequestAdaptation(fieldThinkingOff, "claude-test-stored") || thinkingOffFor("claude-test-stored") != anthOffDisabled {
		t.Error("Forget left it moved")
	}
	reloadAdaptations()
	if thinkingOffFor("claude-test-stored") != anthOffDisabled || len(ListRequestAdaptations()) != 0 {
		t.Error("Forget was not stored")
	}
	// Older than the re-check: sent as configured again, and dropped.
	RootDB.Set(adaptTable, adaptKey(fieldThinkingOff, "claude-test-old"), RequestAdaptation{
		Field: fieldThinkingOff, Subject: "claude-test-old", Step: anthOffBetweenTools,
		Since: time.Now().Add(-adaptRecheckAfter - time.Hour)})
	reloadAdaptations()
	if thinkingOffFor("claude-test-old") != anthOffDisabled {
		t.Error("a step past its re-check still stands")
	}
	var left RequestAdaptation
	if RootDB.Get(adaptTable, adaptKey(fieldThinkingOff, "claude-test-old"), &left) {
		t.Error("an expired step was not dropped from the store")
	}
}

// What one client learns about a model's pictures holds for every client on
// that model, so two tiers on it, and a reload, do not each pay again.
func TestPicturesRefusedAreKeptForTheModel(t *testing.T) {
	adaptTestDB(t)
	t.Cleanup(func() { forgetAdaptations(fieldImages) })
	msgs := []Message{{Role: "user", Content: "look", Images: [][]byte{{1, 2, 3}}}}
	refusal := errors.New("HTTP 400: image inputs are not supported by this model")
	a := &retryLLM{model: "vllm/test-blind"}
	b := &retryLLM{model: "vllm/test-blind"}
	other := &retryLLM{model: "vllm/test-sees"}
	if !a.learnNoImages(refusal, msgs) {
		t.Fatal("the refusal was not learned")
	}
	if !b.blind() || other.blind() {
		t.Errorf("another client on the model: blind %v; a client on another model: blind %v", b.blind(), other.blind())
	}
}

// The output maximum a refusal names is read in each provider's words, and
// only from a 400 that is about max_tokens.
func TestTheOutputMaximumIsReadFromARefusal(t *testing.T) {
	for msg, want := range map[string]int{
		"max_tokens: 64000 > 32000, which is the maximum allowed number of output tokens for claude-test": 32000,
		"max_tokens is too large: 100000. This model supports at most 16384 completion tokens":            16384,
		"Malformed input request: #/max_tokens: 200000 is not less or equal to 128000":                    128000,
		"messages: text content blocks must be non-empty":                                                 0,
	} {
		if got := maxTokensLimitFrom(&APIError{StatusCode: 400, Message: msg}); got != want {
			t.Errorf("%q: %d, want %d", msg, got, want)
		}
	}
	if maxTokensLimitFrom(&APIError{StatusCode: 500, Message: "max_tokens: 64000 > 32000"}) != 0 {
		t.Error("a 500 named a maximum")
	}
}

// A sampling refusal is a sampling setting refused, not one out of range.
func TestASamplingRefusalIsRecognised(t *testing.T) {
	for msg, want := range map[string]bool{
		"Unsupported parameter: 'temperature' is not supported with this model.":                                          true,
		"Unsupported value: 'temperature' does not support 0.2 with this model. Only the default (1) value is supported.": true,
		"temperature: must be between 0 and 2":                                                                            false,
	} {
		if got := samplingRefusedRE.MatchString(msg); got != want {
			t.Errorf("%q: %v, want %v", msg, got, want)
		}
	}
}

// End to end, a Claude call refused for asking more output than the model
// takes is asked again at the maximum it named, and answers; the next call is
// clamped the first time.
func TestAnOutputMaximumRefusalIsRetriedClamped(t *testing.T) {
	adaptTestDB(t)
	t.Cleanup(func() { forgetAdaptations(fieldMaxTokens) })
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = append(sent, string(b))
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), `"max_tokens":64000`) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 64000 > 32000, which is the maximum allowed number of output tokens for claude-test-max"}}`))
			return
		}
		w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`))
	}))
	defer srv.Close()
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "anthropic", Model: "claude-test-max", APIKey: "k", Endpoint: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if resp, err := llm.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, WithMaxTokens(64000)); err != nil || strings.TrimSpace(resp.Content) != "ok" {
			t.Fatalf("call %d: %v %+v", i, err, resp)
		}
	}
	if len(sent) != 3 || !strings.Contains(sent[1], `"max_tokens":32000`) || !strings.Contains(sent[2], `"max_tokens":32000`) {
		t.Errorf("sent %d requests:\n%s", len(sent), strings.Join(sent, "\n"))
	}
}

// End to end, an OpenAI-compatible call refused for its temperature is asked
// again without sampling settings, and answers.
func TestASamplingRefusalIsRetriedWithout(t *testing.T) {
	adaptTestDB(t)
	t.Cleanup(func() { forgetAdaptations(fieldSampling) })
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = append(sent, string(b))
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), `"temperature"`) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"Unsupported parameter: 'temperature' is not supported with this model.","type":"invalid_request_error"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "openai", Model: "test-reasoner", APIKey: "k", Endpoint: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := llm.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, WithTemperature(0.2)); err != nil || strings.TrimSpace(resp.Content) != "ok" {
		t.Fatalf("the call: %v %+v", err, resp)
	}
	if len(sent) != 2 || strings.Contains(sent[1], `"temperature"`) {
		t.Errorf("sent %d requests:\n%s", len(sent), strings.Join(sent, "\n"))
	}
}

// refusingLLM refuses every call with err.
type refusingLLM struct{ err error }

func (f refusingLLM) Chat(context.Context, []Message, ...ChatOption) (*Response, error) {
	return nil, f.err
}

func (f refusingLLM) ChatStream(context.Context, []Message, StreamHandler, ...ChatOption) (*Response, error) {
	return nil, f.err
}

// A 400 refusing a setting no field reads is kept as a row the Models page
// shows, so a refusal gohort cannot adapt to is seen rather than found in a
// log. One a field reads, a context overflow, or a refusal of content is not.
func TestARefusalNoFieldReadsIsKept(t *testing.T) {
	adaptTestDB(t)
	t.Cleanup(func() { forgetAdaptations(fieldUnmapped) })
	refuse := func(model, msg string) {
		r := &retryLLM{inner: refusingLLM{&APIError{Provider: "openai", StatusCode: 400, Message: msg}}, model: model}
		r.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	}
	refuse("openai/test-odd", "Unsupported parameter: 'logit_bias' is not supported with this model.")
	refuse("openai/test-long", "This model's maximum context length is 8192 tokens. However, your messages resulted in 9000 tokens.")
	refuse("openai/test-read", "Unsupported parameter: 'temperature' is not supported with this model.")
	refuse("openai/test-content", "Your request was rejected as a result of our safety system.")
	var got []string
	for _, a := range ListRequestAdaptations() {
		if a.Unmapped {
			got = append(got, a.Subject)
		}
	}
	if len(got) != 1 || got[0] != "openai/test-odd" {
		t.Errorf("unmapped rows: %v, want only the refused setting no field reads", got)
	}
}

// The probes ask one request field each and report what the model was
// adapted to: here thinking off as between_tools, and an output maximum it
// names. Probed again, the same model reports nothing new: what it refuses
// is already learned.
func TestTheProbesLearnAModelsLimits(t *testing.T) {
	adaptTestDB(t)
	t.Cleanup(func() {
		forgetAdaptations(fieldThinkingOff)
		forgetAdaptations(fieldMaxTokens)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(body, `"type":"disabled"`):
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"To turn thinking off on this model, send \"thinking\": {\"type\": \"between_tools\"} instead of {\"type\": \"disabled\"}."}}`))
		case strings.Contains(body, `"max_tokens":200000`):
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 200000 > 64000, which is the maximum allowed number of output tokens for claude-test-probe"}}`))
		default:
			w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`))
		}
	}))
	defer srv.Close()
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "anthropic", Model: "claude-test-probe", APIKey: "k", Endpoint: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	steps := 0
	lines := ProbeRequestFields(context.Background(), llm, func(i, n int, name string) {
		steps++
		if i != steps || n != len(requestProbes) || name == "" {
			t.Errorf("step %d of %d %q, want %d of %d", i, n, name, steps, len(requestProbes))
		}
	})
	got := strings.Join(lines, " | ")
	if steps != len(requestProbes) || !strings.Contains(got, "Thinking off") || !strings.Contains(got, "at most 64000") {
		t.Errorf("after %d probes, adapted: %q", steps, got)
	}
	if again := ProbeRequestFields(context.Background(), llm, nil); len(again) != 0 {
		t.Errorf("probed again, it reports %v as new", again)
	}
}

// A model is probed once per process, however many times its tier's fields
// are saved.
func TestAModelIsProbedOnce(t *testing.T) {
	calls := make(chan struct{}, 32)
	llm := countingLLM{calls}
	ProbeOnce("test/probe-once", llm)
	ProbeOnce("test/probe-once", llm)
	deadline := time.After(5 * time.Second)
	for n := 0; n < len(requestProbes); n++ {
		select {
		case <-calls:
		case <-deadline:
			t.Fatalf("the probes did not run: %d calls", n)
		}
	}
	select {
	case <-calls:
		t.Error("the same model was probed twice")
	case <-time.After(200 * time.Millisecond):
	}
}

// countingLLM answers every call, counting them.
type countingLLM struct{ calls chan struct{} }

func (c countingLLM) Chat(context.Context, []Message, ...ChatOption) (*Response, error) {
	c.calls <- struct{}{}
	return &Response{Content: "ok"}, nil
}

func (c countingLLM) ChatStream(ctx context.Context, m []Message, _ StreamHandler, o ...ChatOption) (*Response, error) {
	return c.Chat(ctx, m, o...)
}
