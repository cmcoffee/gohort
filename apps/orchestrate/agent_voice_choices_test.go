package orchestrate

import (
	"strings"
	"testing"
)

// An agent with a voice of its own is a use of the speech voice: listed with
// whose agent it is, and changed by a mapping as an ordinary save of the
// agent. An agent with no voice of its own is not listed (it speaks in the
// deployment's, which is listed once, by itself).
func TestAnAgentsOwnVoiceIsAUseOfTheSpeechVoice(t *testing.T) {
	_, udb, user := newTestOrchestrate(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "v1", Owner: user, Name: "Narrator", Voice: "af_heart", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{ID: "v2", Owner: user, Name: "Quiet", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	var found int
	for _, u := range agentVoiceUses() {
		if strings.Contains(u.Where, "Quiet") {
			t.Error("an agent with no voice of its own was listed")
		}
		if !strings.Contains(u.Where, "Narrator") {
			continue
		}
		found++
		if u.Value != "af_heart" || !strings.Contains(u.Where, user+"'s agent") {
			t.Errorf("use: %+v", u)
		}
		if err := u.Set("narrator"); err != nil {
			t.Fatal(err)
		}
	}
	if found != 1 {
		t.Fatalf("the agent's voice listed %d time(s)", found)
	}
	if got, _ := loadAgent(udb, "v1"); got.Voice != "narrator" {
		t.Errorf("the agent's voice after the mapping: %q", got.Voice)
	}
}
