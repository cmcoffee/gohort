package core

// Two names for the same act. An agent granted both the grouped tool and a
// twin picks between them per call — which is how a set of four begun on
// `image` was continued on generate_image_comfyui and reported as finished
// after two.

import (
	"slices"
	"testing"
)

type fakeGroupTool struct{ name string }

func (f *fakeGroupTool) Name() string                       { return f.name }
func (f *fakeGroupTool) Desc() string                       { return "grouped" }
func (f *fakeGroupTool) Params() map[string]ToolParam       { return map[string]ToolParam{} }
func (f *fakeGroupTool) Run(map[string]any) (string, error) { return "", nil }
func (f *fakeGroupTool) Supersedes(name string) bool        { return name == "twin_a" || name == "twin_b" }

type fakePlainTool struct{ name string }

func (f *fakePlainTool) Name() string                       { return f.name }
func (f *fakePlainTool) Desc() string                       { return "plain" }
func (f *fakePlainTool) Params() map[string]ToolParam       { return map[string]ToolParam{} }
func (f *fakePlainTool) Run(map[string]any) (string, error) { return "", nil }

func withFakeTools(t *testing.T) {
	t.Helper()
	registryMu.Lock()
	saved := registeredChatTools
	registeredChatTools = append(slices.Clone(saved),
		&fakeGroupTool{name: "grouper"}, &fakePlainTool{name: "twin_a"}, &fakePlainTool{name: "twin_b"})
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		registeredChatTools = saved
		registryMu.Unlock()
	})
}

func TestAGrantedTwinIsHiddenWhenTheGroupedToolIsThere(t *testing.T) {
	withFakeTools(t)
	got := dropSupersededNames(&ToolSession{}, []string{"grouper", "twin_a", "twin_b"})
	if !slices.Equal(got, []string{"grouper"}) {
		t.Errorf("got %v, want just the grouped tool", got)
	}
}

// The promise that makes the collapse non-breaking: an agent allowlisted onto
// ONE specific backend keeps exactly that access.
func TestATwinAloneIsUntouched(t *testing.T) {
	withFakeTools(t)
	got := dropSupersededNames(&ToolSession{}, []string{"twin_a"})
	if !slices.Equal(got, []string{"twin_a"}) {
		t.Errorf("got %v — a caller holding only the specific tool must keep it", got)
	}
}

func TestUnrelatedToolsAndOrderSurvive(t *testing.T) {
	withFakeTools(t)
	got := dropSupersededNames(&ToolSession{}, []string{"twin_a", "web_search", "grouper", "calculate"})
	if !slices.Equal(got, []string{"web_search", "grouper", "calculate"}) {
		t.Errorf("got %v, want the twin dropped and the rest in order", got)
	}
	// A name nothing knows about is left for the resolver to handle as always.
	if got := dropSupersededNames(&ToolSession{}, []string{"no_such_tool"}); !slices.Equal(got, []string{"no_such_tool"}) {
		t.Errorf("got %v, want unknown names passed through", got)
	}
}

func TestAToolCannotSupersedeItself(t *testing.T) {
	registryMu.Lock()
	saved := registeredChatTools
	// A grouped tool that (wrongly) claims its own name.
	registeredChatTools = append(slices.Clone(saved), &fakeSelfEater{})
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		registeredChatTools = saved
		registryMu.Unlock()
	})
	if got := dropSupersededNames(&ToolSession{}, []string{"self_eater"}); !slices.Equal(got, []string{"self_eater"}) {
		t.Errorf("got %v — a tool must not supersede itself out of existence", got)
	}
}

type fakeSelfEater struct{}

func (f *fakeSelfEater) Name() string                       { return "self_eater" }
func (f *fakeSelfEater) Desc() string                       { return "d" }
func (f *fakeSelfEater) Params() map[string]ToolParam       { return map[string]ToolParam{} }
func (f *fakeSelfEater) Run(map[string]any) (string, error) { return "", nil }
func (f *fakeSelfEater) Supersedes(string) bool             { return true }
