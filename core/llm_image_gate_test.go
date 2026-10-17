package core

import (
	"context"
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
