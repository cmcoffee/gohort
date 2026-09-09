package orchestrate

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

// TestFileSeedsLoad pins the shared contract of every seed document: it
// parses, it carries an id and a prompt, the framework owns it, and no two
// files claim the same id.
func TestFileSeedsLoad(t *testing.T) {
	got := fileSeedAgents()
	if len(got) == 0 {
		t.Fatal("no seeds loaded from seeds/*.md")
	}
	seen := map[string]bool{}
	for _, rec := range got {
		if rec.ID == "" {
			t.Errorf("seed with no id: %q", rec.Name)
		}
		if seen[rec.ID] {
			t.Errorf("duplicate seed id %q", rec.ID)
		}
		seen[rec.ID] = true
		if rec.Owner != seedOwner {
			t.Errorf("seed %q owner is %q, want %q", rec.ID, rec.Owner, seedOwner)
		}
		if strings.TrimSpace(rec.OrchestratorPrompt) == "" {
			t.Errorf("seed %q has no prompt body", rec.ID)
		}
		// isSeedID gates dozens of comparison sites; a file-declared seed
		// that does not answer to it would look like a user's agent.
		if !isSeedID(rec.ID) {
			t.Errorf("seed %q is not recognized by isSeedID", rec.ID)
		}
	}
}

// TestKnowledgeBaseSeedUnchanged proves the move out of Go was a no-op. The
// values below were read off the AgentRecord literal that used to live in
// agents_seed.go, and the prompt digest is of that literal's exact bytes. A
// failure here means the move changed the agent, which was never the point.
func TestKnowledgeBaseSeedUnchanged(t *testing.T) {
	rec, ok := seedAgentByID("seed-kb")
	if !ok {
		t.Fatal("seed-kb no longer resolves through seedAgentByID")
	}

	if rec.Name != "Knowledge Base" {
		t.Errorf("name = %q", rec.Name)
	}
	if want := []string{"ask_user"}; !reflect.DeepEqual(rec.AllowedTools, want) {
		t.Errorf("allowed_tools = %v, want %v", rec.AllowedTools, want)
	}
	if rec.MaxPlanSteps != 3 || rec.MaxWorkerRounds != 6 {
		t.Errorf("budgets = %d/%d, want 3/6", rec.MaxPlanSteps, rec.MaxWorkerRounds)
	}
	// The anti-contamination stack. Each of these off would let something
	// that is not the corpus into an answer.
	for name, on := range map[string]bool{
		"force_private":      rec.ForcePrivate,
		"disable_explicit":   rec.DisableExplicit,
		"disable_inferred":   rec.DisableInferred,
		"disable_skills":     rec.DisableSkills,
		"ingest_attachments": rec.IngestAttachments,
		"hidden":             rec.Hidden,
	} {
		if !on {
			t.Errorf("%s is off", name)
		}
	}
	if rec.Exposed {
		t.Error("exposed is on; no seed publishes itself")
	}
	if !strings.Contains(rec.Rules, "Answer only from the attached corpus") {
		t.Errorf("rules lost the corpus-only contract: %q", rec.Rules)
	}

	sum := sha256.Sum256([]byte(rec.OrchestratorPrompt))
	const wantSum = "a8254110d50093eaa81ad73ad41a1c46f002f8066cb4d09af3466139b7bd590c"
	if got := hex.EncodeToString(sum[:]); got != wantSum {
		t.Errorf("prompt digest = %s (%d bytes), want %s.\n"+
			"If you edited the prompt on purpose, update wantSum; if you did not, "+
			"the frontmatter split or a trailing-newline change is eating bytes.",
			got, len(rec.OrchestratorPrompt), wantSum)
	}
}

// TestParseSeedFileRejectsBadFiles: a seed that does not parse has to be an
// error, because the alternative is an agent that silently is not there.
func TestParseSeedFileRejectsBadFiles(t *testing.T) {
	good := "---\n{\"id\":\"seed-x\",\"name\":\"X\"}\n---\nbody\n"
	if _, err := parseSeedFile("t.md", []byte(good)); err != nil {
		t.Fatalf("valid seed rejected: %v", err)
	}

	for name, doc := range map[string]string{
		"no fence":        "{\"id\":\"seed-x\",\"name\":\"X\"}\nbody\n",
		"unclosed fence":  "---\n{\"id\":\"seed-x\",\"name\":\"X\"}\nbody\n",
		"bad json":        "---\n{\"id\": \"seed-x\",,}\n---\nbody\n",
		"unknown key":     "---\n{\"id\":\"seed-x\",\"name\":\"X\",\"alowed_tools\":[\"a\"]}\n---\nbody\n",
		"no id":           "---\n{\"name\":\"X\"}\n---\nbody\n",
		"no name":         "---\n{\"id\":\"seed-x\"}\n---\nbody\n",
		"no body":         "---\n{\"id\":\"seed-x\",\"name\":\"X\"}\n---\n\n",
		"prompt in front": "---\n{\"id\":\"seed-x\",\"name\":\"X\",\"orchestrator_prompt\":\"hi\"}\n---\nbody\n",
	} {
		if _, err := parseSeedFile("t.md", []byte(doc)); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}

	// Owner is stamped by the loader, so a file cannot hand itself to a user.
	rec, err := parseSeedFile("t.md", []byte("---\n{\"id\":\"seed-x\",\"name\":\"X\",\"owner\":\"someone\"}\n---\nbody\n"))
	if err != nil {
		t.Fatalf("owner-bearing seed rejected: %v", err)
	}
	if rec.Owner != seedOwner {
		t.Errorf("owner = %q, want %q", rec.Owner, seedOwner)
	}

	// notes exist for the humans reading the file and never reach the record.
	if _, err := parseSeedFile("t.md", []byte("---\n{\"id\":\"seed-x\",\"name\":\"X\",\"notes\":{\"why\":\"because\"}}\n---\nbody\n")); err != nil {
		t.Errorf("notes rejected: %v", err)
	}
}
