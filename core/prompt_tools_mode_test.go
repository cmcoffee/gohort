package core

import "testing"

// The mode used to be a per-agent snapshot taken at startup and never
// recomputed, so an admin flipping "Native tool calling" got a rebuilt LLM and
// an unchanged mode — the setting read correct everywhere it could be
// inspected while every tool call still went out prompt-parsed.
func TestPromptToolsModeIsLiveAndOverridesTheAgentField(t *testing.T) {
	restore := func(mode, set bool) {
		promptToolsMu.Lock()
		promptToolsMode, promptToolsSet = mode, set
		promptToolsMu.Unlock()
	}
	promptToolsMu.RLock()
	oldMode, oldSet := promptToolsMode, promptToolsSet
	promptToolsMu.RUnlock()
	t.Cleanup(func() { restore(oldMode, oldSet) })

	// Nothing published (an SDK embedder): the agent's own field decides.
	restore(false, false)
	agent := &AppCore{PromptTools: true}
	if !agent.promptToolsMode() {
		t.Error("with nothing published, the agent's own field must decide")
	}

	// Published: it WINS, in both directions. Turning native tools on has to
	// be able to switch the mode OFF, which is the direction that was broken.
	SetPromptToolsMode(false)
	if agent.promptToolsMode() {
		t.Error("a published mode must override the agent's stale field")
	}
	SetPromptToolsMode(true)
	if !agent.promptToolsMode() {
		t.Error("a published mode must apply in both directions")
	}

	mode, ok := PromptToolsMode()
	if !ok || !mode {
		t.Errorf("PromptToolsMode() = %v,%v", mode, ok)
	}
}
