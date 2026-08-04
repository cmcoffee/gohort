package servitor

import (
	"strings"
	"testing"
)

// TestWorkerProtocolSelection pins the map/chat split. mapExecutionProtocol was
// silently orphaned once already — declared, referenced by nothing, with a
// comment claiming it was wired in. These assertions fail loudly if that
// happens again, or if someone concatenates the two protocols whose failure
// rules directly contradict each other.
func TestWorkerProtocolSelection(t *testing.T) {
	ssh := Appliance{Name: "lab-box", Type: "ssh", Host: "10.0.0.5"}
	const scratch = "/tmp/servitor-abc123"

	mapPrompt := buildProbeWorkerPrompt(ssh, scratch, true)
	chatPrompt := buildProbeWorkerPrompt(ssh, scratch, false)

	if !strings.Contains(mapPrompt, mapExecutionProtocol) {
		t.Error("map worker must carry mapExecutionProtocol — it is orphaned again")
	}
	if strings.Contains(mapPrompt, probeWorkerProtocol) {
		t.Error("map worker must not also carry probeWorkerProtocol: the two give opposite failure instructions")
	}
	if !strings.Contains(chatPrompt, probeWorkerProtocol) {
		t.Error("chat probe must carry probeWorkerProtocol")
	}
	if strings.Contains(chatPrompt, mapExecutionProtocol) {
		t.Error("chat probe must not carry mapExecutionProtocol")
	}

	// The retry rule in the Rules section has to move with the protocol, or the
	// worker is told to stop after one attempt AND to try several.
	const stopAfterOne = "after one attempt, report it and stop"
	if strings.Contains(mapPrompt, stopAfterOne) {
		t.Error("map worker keeps the stop-after-one rule, contradicting its own protocol")
	}
	if !strings.Contains(chatPrompt, stopAfterOne) {
		t.Error("chat probe lost the stop-after-one rule")
	}

	// Both modes point temp files at the run's scratch directory; the protocols
	// each reference it, so a prompt without the path leaves that dangling.
	for name, p := range map[string]string{"map": mapPrompt, "chat": chatPrompt} {
		if !strings.Contains(p, scratch) {
			t.Errorf("%s worker prompt never names the scratch directory", name)
		}
	}
}

// TestProtocolsStayContradictory documents WHY the selection above exists: if a
// future edit reconciles the two protocols, this test fails and whoever did it
// can collapse them into one instead of maintaining a split that no longer
// buys anything.
func TestProtocolsStayContradictory(t *testing.T) {
	if !strings.Contains(probeWorkerProtocol, "stop immediately") {
		t.Error("probeWorkerProtocol no longer stops immediately on an empty result — is the map/chat split still earning its keep?")
	}
	if !strings.Contains(mapExecutionProtocol, "try one alternative approach") {
		t.Error("mapExecutionProtocol no longer retries — is the map/chat split still earning its keep?")
	}
}

// TestRepoWorkerIgnoresMapping guards the early return: a repo worker searches an
// ingested store, so it has no commands to retry and no filesystem to write to.
func TestRepoWorkerIgnoresMapping(t *testing.T) {
	repo := Appliance{Name: "orchestrator", Type: "repo", RepoURL: "https://example.com/o/r"}
	withMapping := buildProbeWorkerPrompt(repo, "/tmp/servitor-abc123", true)
	without := buildProbeWorkerPrompt(repo, "/tmp/servitor-abc123", false)
	if withMapping != without {
		t.Error("repo workers have no execution protocol to select between")
	}
	if strings.Contains(withMapping, mapExecutionProtocol) || strings.Contains(withMapping, probeWorkerProtocol) {
		t.Error("repo worker prompt should carry neither shell execution protocol")
	}
}
