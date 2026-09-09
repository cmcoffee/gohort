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

// TestSeedDocumentsUnchanged proves the move out of Go was a no-op. Each
// digest is of the file's body EXACTLY as written, before snippet expansion,
// because two of these prompts resolve differently per host (the sandbox
// Python probe) and per deployment (the memory-surface flag) and a digest of
// the expanded text would fail on a machine that merely has a newer Python.
//
// The values were read off the AgentRecord literals that used to live in
// agents_seed.go, and each prompt was compared byte for byte against its
// literal before the literal was deleted. A failure here means an edit
// changed a seed; if that was the intent, update the digest in the same
// commit so the change is visible in the diff.
func TestSeedDocumentsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		file string
		sum  string
	}{
		{"builder.md", "017cd19feec78829453f3e1b016c3e9829103701564b1964d052d74b8d9ed5f3"},
		{"conversational.md", "02180e1ef22e3f234b5883aa2b9ca9e246f75327981abd2f134821df0ecf4702"},
		{"knowledge_base.md", "a8254110d50093eaa81ad73ad41a1c46f002f8066cb4d09af3466139b7bd590c"},
		{"research.md", "2a570183462b68e77a0837242ee79f83120c4bfc67f5d7757e3ac38afa636a30"},
	} {
		data, err := seedFilesFS.ReadFile("seeds/" + tc.file)
		if err != nil {
			t.Errorf("%s: %v", tc.file, err)
			continue
		}
		_, body, err := splitFrontmatter(data)
		if err != nil {
			t.Errorf("%s: %v", tc.file, err)
			continue
		}
		sum := sha256.Sum256([]byte(body))
		if got := hex.EncodeToString(sum[:]); got != tc.sum {
			t.Errorf("%s: prompt digest = %s (%d bytes), want %s", tc.file, got, len(body), tc.sum)
		}
	}
}

// TestSeedSettingsUnchanged pins the settings that decide what each seed can
// reach and how far it can go. These are the fields where a silent change is
// worst: an allowlist that grew, a budget that shrank, a hidden seed that
// started answering dispatches.
func TestSeedSettingsUnchanged(t *testing.T) {
	get := func(id string) AgentRecord {
		t.Helper()
		rec, ok := seedAgentByID(id)
		if !ok {
			t.Fatalf("%s no longer resolves through seedAgentByID", id)
		}
		return rec
	}

	chat := get("seed-chat")
	if chat.Name != "Chat" || !chat.Cortex || !chat.Fleet || !chat.PreMortem {
		t.Errorf("chat: name=%q cortex=%v fleet=%v premortem=%v", chat.Name, chat.Cortex, chat.Fleet, chat.PreMortem)
	}
	// Empty is not "no tools": the runner reads it as the default pool, which
	// is what makes Chat-in-orchestrate equivalent to Chat-the-app.
	if len(chat.AllowedTools) != 0 {
		t.Errorf("chat allowed_tools = %v, want empty (the default pool)", chat.AllowedTools)
	}
	if chat.MaxPlanSteps != 6 || chat.MaxWorkerRounds != 18 {
		t.Errorf("chat budgets = %d/%d, want 6/18", chat.MaxPlanSteps, chat.MaxWorkerRounds)
	}
	if chat.MemoryMode != "chatbot" || !chat.AllowPrivateMode || chat.AllowExplorer {
		t.Errorf("chat memory=%q private=%v explorer=%v", chat.MemoryMode, chat.AllowPrivateMode, chat.AllowExplorer)
	}

	builder := get("seed-builder")
	wantTools := []string{
		"ask_user", "ask_user_form", "plan_set",
		"web_search", "fetch_url", "browse_page", "workspace",
		"store_fact", "forget_fact", "list_facts",
		"stay_silent", "keep_going",
	}
	if !reflect.DeepEqual(builder.AllowedTools, wantTools) {
		t.Errorf("builder allowed_tools = %v, want %v", builder.AllowedTools, wantTools)
	}
	if builder.MaxPlanSteps != 8 || builder.MaxWorkerRounds != 30 || builder.ExplorerHardCap != 80 || !builder.AllowExplorer {
		t.Errorf("builder budgets = %d/%d explorer=%v cap=%d", builder.MaxPlanSteps, builder.MaxWorkerRounds, builder.AllowExplorer, builder.ExplorerHardCap)
	}
	if len(builder.IntakeForm) != 1 || len(builder.IntakeForm[0].Options) != 6 {
		t.Fatalf("builder intake form = %+v, want one field with six starting points", builder.IntakeForm)
	}
	// Machine sits beside Pipeline on purpose: offering one without the other
	// told everybody Builder does not do machines.
	if !reflect.DeepEqual(builder.IntakeForm[0].Options, []string{"Agent", "App", "Tool", "Pipeline", "Machine", "Fix something"}) {
		t.Errorf("builder starting points = %v", builder.IntakeForm[0].Options)
	}

	research := get("seed-research")
	if want := []string{"web_search", "fetch_url", "browse_page", "screenshot_page"}; !reflect.DeepEqual(research.AllowedTools, want) {
		t.Errorf("research allowed_tools = %v, want %v", research.AllowedTools, want)
	}
	if research.MaxPlanSteps != 6 || research.MaxWorkerRounds != 16 || !research.GapCheck {
		t.Errorf("research budgets = %d/%d gap_check=%v", research.MaxPlanSteps, research.MaxWorkerRounds, research.GapCheck)
	}
	// The citation contract lives in rules, which win over the persona on the
	// turn a plausible answer is already in the model's head.
	if !strings.Contains(research.Rules, "inline citation") {
		t.Errorf("research lost the citation rule: %q", research.Rules)
	}
	if strings.TrimSpace(research.PlanGuidance) == "" {
		t.Error("research lost its plan guidance")
	}

	kb := get("seed-kb")
	if kb.Name != "Knowledge Base" {
		t.Errorf("kb name = %q", kb.Name)
	}
	if want := []string{"ask_user"}; !reflect.DeepEqual(kb.AllowedTools, want) {
		t.Errorf("kb allowed_tools = %v, want %v", kb.AllowedTools, want)
	}
	if kb.MaxPlanSteps != 3 || kb.MaxWorkerRounds != 6 {
		t.Errorf("kb budgets = %d/%d, want 3/6", kb.MaxPlanSteps, kb.MaxWorkerRounds)
	}
	// The anti-contamination stack. Any one of these off would let something
	// that is not the corpus into an answer.
	for name, on := range map[string]bool{
		"force_private":      kb.ForcePrivate,
		"disable_explicit":   kb.DisableExplicit,
		"disable_inferred":   kb.DisableInferred,
		"disable_skills":     kb.DisableSkills,
		"ingest_attachments": kb.IngestAttachments,
	} {
		if !on {
			t.Errorf("kb %s is off", name)
		}
	}
	if !strings.Contains(kb.Rules, "Answer only from the attached corpus") {
		t.Errorf("kb lost the corpus-only contract: %q", kb.Rules)
	}

	// No seed publishes itself, and none of them answer another agent's
	// dispatch by default.
	for _, rec := range fileSeedAgents() {
		if rec.Exposed {
			t.Errorf("%s is exposed", rec.ID)
		}
		if !rec.Hidden {
			t.Errorf("%s is not hidden", rec.ID)
		}
	}
}

// TestSeedSnippetsResolve covers the two prompts that are not fixed text: one
// names the memory-save tool by its live surface, the other appends a Python
// note only when the sandbox interpreter is old enough to need it. A
// placeholder that survives into a loaded prompt would reach the model as
// prose.
func TestSeedSnippetsResolve(t *testing.T) {
	for _, tc := range []struct{ file, token string }{
		{"builder.md", "{{sandbox_python_note}}"},
		{"research.md", "{{memory_save_call}}"},
	} {
		data, err := seedFilesFS.ReadFile("seeds/" + tc.file)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		_, body, err := splitFrontmatter(data)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if n := strings.Count(body, tc.token); n != 1 {
			t.Errorf("%s: %s appears %d times, want once", tc.file, tc.token, n)
		}
	}
	for _, rec := range fileSeedAgents() {
		if strings.Contains(rec.OrchestratorPrompt, "{{") {
			t.Errorf("%s: an unexpanded placeholder reached the prompt", rec.ID)
		}
	}
	// The sandbox note carries its own blank-line separator, so an empty one
	// appends nothing at all rather than leaving a gap.
	if got := expandSeedSnippets("tail{{sandbox_python_note}}"); got != "tail"+sandboxPythonNoteSection() {
		t.Errorf("sandbox note expansion = %q", got)
	}
	if got := expandSeedSnippets("call {{memory_save_call}} now"); got != "call "+memFindingSavePhrase()+" now" {
		t.Errorf("memory phrase expansion = %q", got)
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
		"unknown snippet": "---\n{\"id\":\"seed-x\",\"name\":\"X\"}\n---\nbody {{sandbox_pyton_note}}\n",
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

// TestSeedCopyCoversEverySliceField is the backstop for copySeedRecord, which
// copies slice fields by name. Every slice a seed document actually populates
// has to be named there; one that is not would be shared with the cached
// record, and a caller appending to it would change a different agent's
// settings. Reflection lives here rather than in the loader so the runtime
// path stays plain.
func TestSeedCopyCoversEverySliceField(t *testing.T) {
	covered := map[string]bool{
		"AllowedTools":       true,
		"Triggers":           true,
		"IntakeForm":         true,
		"IntakeForm.Options": true,
	}
	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		switch v.Kind() {
		case reflect.Slice, reflect.Map:
			if v.IsNil() || v.Len() == 0 {
				return
			}
			if !covered[path] {
				t.Errorf("seed field %s is populated but copySeedRecord does not copy it; "+
					"add it there and to this test's covered list", path)
				return
			}
			if v.Kind() == reflect.Slice {
				for i := 0; i < v.Len(); i++ {
					walk(v.Index(i), path)
				}
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				f := v.Type().Field(i)
				if f.PkgPath != "" { // unexported: json cannot populate it
					continue
				}
				name := f.Name
				if path != "" {
					name = path + "." + f.Name
				}
				walk(v.Field(i), name)
			}
		case reflect.Ptr, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), path)
			}
		}
	}
	for _, rec := range fileSeedAgents() {
		walk(reflect.ValueOf(rec), "")
	}

	// And the copy is real: mutating what a caller got must not reach the
	// next caller.
	first := fileSeedAgents()
	for i := range first {
		for j := range first[i].AllowedTools {
			first[i].AllowedTools[j] = "clobbered"
		}
	}
	for _, rec := range fileSeedAgents() {
		for _, tool := range rec.AllowedTools {
			if tool == "clobbered" {
				t.Fatalf("%s: allowed_tools is shared with the cache", rec.ID)
			}
		}
	}
}
