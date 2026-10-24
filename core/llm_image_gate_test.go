package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func gateFixture(t *testing.T, workerSees bool) (lead *retryLLM, leadInner, worker *FakeLLM) {
	t.Helper()
	worker = &FakeLLM{Turns: []FakeTurn{{Content: "A red bicycle against a brick wall; a sign reads OPEN 9-5.", Repeat: true}}}
	leadInner = &FakeLLM{Turns: []FakeTurn{{Content: "It is a bicycle.", Repeat: true}}}
	w := &retryLLM{inner: worker, maxRetries: 1, noImages: !workerSees}
	lead = &retryLLM{inner: leadInner, maxRetries: 1, noImages: true}
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	SetSharedLLMs(w, lead)
	t.Cleanup(func() { SetSharedLLMs(prevW, prevL) })
	describeMu.Lock()
	describeCache, describeOrder = map[[32]byte]string{}, nil
	describeMu.Unlock()
	return lead, leadInner, worker
}

// A model set not to read images never receives one, on either call path: the
// picture goes to the worker, which reads them, and the lead gets the
// worker's description in its place. The caller's messages keep the image,
// and the same picture is described once however often it is re-sent.
func TestAModelThatDoesNotReadImagesGetsADescription(t *testing.T) {
	lead, leadInner, worker := gateFixture(t, true)
	photo := []byte("\x89PNG fake bytes of a photo")
	msgs := []Message{{Role: "user", Content: "what is in this picture?", Images: [][]byte{photo}}}

	if _, err := lead.Chat(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	if _, err := lead.ChatStream(context.Background(), msgs, func(string) {}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < leadInner.Calls(); i++ {
		for _, m := range leadInner.Sent(i) {
			if len(m.Images) > 0 || len(m.Videos) > 0 {
				t.Fatalf("call %d: the lead was sent an image", i)
			}
		}
		got := leadInner.Sent(i)[0].Content
		if !strings.Contains(got, "what is in this picture?") || !strings.Contains(got, "sign reads OPEN 9-5") ||
			!strings.Contains(got, "described by another model") {
			t.Errorf("call %d: the lead got %q", i, got)
		}
	}
	if worker.Calls() != 1 {
		t.Errorf("the picture was described %d times; once is enough", worker.Calls())
	}
	if len(worker.Sent(0)[0].Images) != 1 {
		t.Error("the describing model was not shown the picture")
	}
	if len(msgs[0].Images) != 1 || msgs[0].Content != "what is in this picture?" {
		t.Error("the caller's messages were changed")
	}
}

// With nothing that reads images, the model is told an image was there and
// that its contents are unknown, rather than nothing at all; a model that
// reads images is sent them as before; and a video with no still to take is
// said to have been there.
func TestWithNoDescriberTheModelIsToldAnImageWasThere(t *testing.T) {
	lead, leadInner, worker := gateFixture(t, false)
	lead.Chat(context.Background(), []Message{{Role: "user", Content: "look", Images: [][]byte{[]byte("img")}, Videos: [][]byte{[]byte("not a video")}}})
	sent := leadInner.Sent(0)[0]
	if len(sent.Images) > 0 || len(sent.Videos) > 0 || !strings.Contains(sent.Content, "contents are unknown") {
		t.Errorf("the lead got %+v", sent)
	}
	if worker.Calls() != 0 {
		t.Error("a model that does not read images was asked to describe one")
	}

	sees := &FakeLLM{Turns: []FakeTurn{{Content: "ok", Repeat: true}}}
	open := &retryLLM{inner: sees, maxRetries: 1}
	open.Chat(context.Background(), []Message{{Role: "user", Content: "look", Images: [][]byte{[]byte("img")}}})
	if len(sees.Sent(0)[0].Images) != 1 {
		t.Error("a model that reads images was not sent the image")
	}
}

// The setting reaches the client built from a configuration.
func TestNoImagesReachesTheClient(t *testing.T) {
	llm, err := NewLLMFromConfig(LLMProviderConfig{Provider: "ollama", Model: "m", NoImages: true})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := llm.(*retryLLM); !ok || !r.noImages {
		t.Errorf("the client does not hold back images: %#v", llm)
	}
}

// capLLM reads pictures but refuses more than max in one request, the way
// vLLM does over its --limit-mm-per-prompt, and records how many each call
// carried.
type capLLM struct {
	max  int
	sent [][]Message
}

func (c *capLLM) answer(messages []Message) (*Response, error) {
	c.sent = append(c.sent, messages)
	if n := countImages(messages); n > c.max {
		return nil, &APIError{Provider: "vllm", StatusCode: 400, Message: fmt.Sprintf("At most %d image(s) may be provided in one prompt. (parameter=image)", c.max)}
	}
	return &Response{Content: "a dog on a sofa"}, nil
}

func (c *capLLM) Chat(_ context.Context, messages []Message, _ ...ChatOption) (*Response, error) {
	return c.answer(messages)
}

func (c *capLLM) ChatStream(_ context.Context, messages []Message, _ StreamHandler, _ ...ChatOption) (*Response, error) {
	return c.answer(messages)
}

// A model that takes two pictures a request, sent four: the refusal is not
// the end of the turn. The request goes again with the newest two, each older
// picture described by the same model on its own, and the next request is
// trimmed before it is sent. Observed 2026-10-23: four parallel finds queued
// four pictures, vLLM refused the round, and the turn failed outright.
func TestAModelsPictureLimitIsLearnedAndKept(t *testing.T) {
	describeMu.Lock()
	describeCache, describeOrder = map[[32]byte]string{}, nil
	describeMu.Unlock()
	stub := &capLLM{max: 2}
	r := &retryLLM{inner: stub, maxRetries: 1}
	pic := func(s string) []byte { return []byte("\x89PNG " + s) }
	msgs := []Message{
		{Role: "user", Content: "earlier", Images: [][]byte{pic("old")}},
		{Role: "user", Content: "the four you found", Images: [][]byte{pic("a"), pic("b"), pic("c")}},
	}
	if _, err := r.Chat(context.Background(), msgs); err != nil {
		t.Fatalf("the refusal should have been retried within the limit: %v", err)
	}
	counts := func(from int) (n []int) {
		for _, m := range stub.sent[from:] {
			n = append(n, countImages(m))
		}
		return n
	}
	// Refused with four; each of the two held back described alone; then two.
	if got := fmt.Sprint(counts(0)); got != "[4 1 1 2]" {
		t.Fatalf("pictures per call = %s, want [4 1 1 2]", got)
	}
	last := stub.sent[len(stub.sent)-1]
	if imgs := last[1].Images; len(imgs) != 2 || string(imgs[0]) != string(pic("b")) || string(imgs[1]) != string(pic("c")) {
		t.Errorf("the newest two should be kept: %q", imgs)
	}
	if len(last[0].Images) != 0 || !strings.Contains(last[0].Content, "earlier") ||
		!strings.Contains(last[0].Content, "reads at most 2 in one request. What it shows: a dog on a sofa") {
		t.Errorf("an older picture should go as its description: %q", last[0].Content)
	}
	if len(msgs[1].Images) != 3 || msgs[0].Content != "earlier" {
		t.Error("the caller's messages were changed")
	}

	// Kept: the next request, streamed this time, is trimmed before it goes,
	// and the descriptions come from the cache.
	from := len(stub.sent)
	if _, err := r.ChatStream(context.Background(), msgs, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(counts(from)); got != "[2]" {
		t.Errorf("after learning the limit, pictures per call = %s, want [2]", got)
	}
}

func TestAPictureLimitIsReadFromTheRefusal(t *testing.T) {
	for err, want := range map[error]int{
		&APIError{Provider: "vllm", StatusCode: 400, Message: "At most 2 image(s) may be provided in one prompt. (parameter=image)"}: 2,
		errors.New("at most 5 images may be provided in one prompt"):                                                                 5,
		errors.New("vllm api error (400): maximum context length exceeded"):                                                          0,
	} {
		if got := imageLimitFrom(err); got != want {
			t.Errorf("%v: limit %d, want %d", err, got, want)
		}
	}
	if imageLimitFrom(nil) != 0 {
		t.Error("no error, no limit")
	}
}

// A limit lowered while gohort runs is learned again from the next refusal.
func TestALoweredPictureLimitIsLearnedAgain(t *testing.T) {
	describeMu.Lock()
	describeCache, describeOrder = map[[32]byte]string{}, nil
	describeMu.Unlock()
	stub := &capLLM{max: 2}
	r := &retryLLM{inner: stub, maxRetries: 1}
	msgs := []Message{{Role: "user", Content: "four", Images: [][]byte{[]byte("1"), []byte("2"), []byte("3"), []byte("4")}}}
	if _, err := r.Chat(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	stub.max = 1
	if _, err := r.Chat(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	if got := r.imageLimit.Load(); got != 1 {
		t.Errorf("limit %d, want the lowered 1", got)
	}
}
