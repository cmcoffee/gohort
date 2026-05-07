package techwriter

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const personaTable = "techwriter_personas"

// Persona stores a writing style that the LLM follows when generating content.
type Persona struct {
	Name        string `json:"name"`
	Description string `json:"description"` // brief summary
	Style       string `json:"style"`       // writing style instructions
}

// handleUploadPersona accepts a PDF, converts pages to images, sends them
// to Gemma vision to analyze the writing style, and returns the analysis
// for user review before saving.
func (T *TechWriterAgent) handleUploadPersona(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if T.DB == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "PDF file required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	tmpDir, err := os.MkdirTemp("", "gohort-persona-*")
	if err != nil {
		http.Error(w, "failed to create temp dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmpDir)

	pdfPath := filepath.Join(tmpDir, "input.pdf")
	pdfFile, err := os.Create(pdfPath)
	if err != nil {
		http.Error(w, "failed to create temp file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(pdfFile, file); err != nil {
		pdfFile.Close()
		http.Error(w, "failed to write PDF: "+err.Error(), http.StatusInternalServerError)
		return
	}
	pdfFile.Close()

	imgPattern := filepath.Join(tmpDir, "page-%03d.png")
	cmd := exec.Command("convert", "-density", "200", pdfPath, "-quality", "90", imgPattern)
	if output, err := cmd.CombinedOutput(); err != nil {
		Debug("[personas] ImageMagick convert failed: %s\n%s", err, string(output))
		http.Error(w, "PDF conversion failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	matches, _ := filepath.Glob(filepath.Join(tmpDir, "page-*.png"))
	if len(matches) == 0 {
		http.Error(w, "no pages extracted from PDF", http.StatusBadRequest)
		return
	}
	if len(matches) > 5 {
		matches = matches[:5]
	}

	var images [][]byte
	for _, imgPath := range matches {
		data, err := os.ReadFile(imgPath)
		if err != nil {
			continue
		}
		images = append(images, data)
	}
	if len(images) == 0 {
		http.Error(w, "failed to read converted images", http.StatusInternalServerError)
		return
	}

	Debug("[personas] extracted %d page images from PDF", len(images))

	agent := &AppCore{LLM: T.AppCore.LLM}
	session := agent.CreateSession(WORKER)
	resp, err := session.Chat(r.Context(), []Message{
		{
			Role: "user",
			Content: `Analyze the WRITING STYLE of this document. Your job is to produce a set of rules that a writer can follow to write NEW documents in the SAME style.

Focus on:

VOICE AND TONE:
- Is it formal, casual, technical, conversational?
- Does it use first person, second person, third person?
- Is it authoritative, instructional, friendly, neutral?
- Are there any distinctive patterns (dry humor, direct commands, cautious hedging)?

SENTENCE STRUCTURE:
- Short and punchy? Long and detailed? Mixed?
- Does it use active or passive voice?
- Are there sentence fragments for emphasis?
- How are transitions handled between ideas?

FORMATTING CONVENTIONS:
- How are headings structured? (numbered, hierarchical, question-based?)
- How is code or technical content presented?
- Are there callout boxes, warnings, tips?
- How are lists used? (bullets, numbered steps, checklists?)
- What goes in tables vs prose?

CONTENT PATTERNS:
- Does each section follow a pattern? (concept → example → warning?)
- Are there standard sections that always appear?
- How detailed are explanations? (brief overview or step-by-step?)
- How are prerequisites or assumptions stated?

Write these as RULES a writer should follow. Be specific — "use active voice" is too vague, "write commands as imperative sentences: 'Run the following command' not 'The following command should be run'" is actionable.

Start IMMEDIATELY with the rules. No preamble, no introduction.`,
			Images: images,
		},
	})

	if err != nil {
		http.Error(w, "LLM analysis failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	style := strings.TrimSpace(ResponseText(resp))
	if style == "" {
		http.Error(w, "LLM returned empty analysis", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"style": style,
		"pages": fmt.Sprintf("%d", len(images)),
	})
}

// handleSavePersona saves a persona to the database.
func (T *TechWriterAgent) handleSavePersona(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if udb == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}
	var req struct {
		Name  string `json:"name"`
		Style string `json:"style"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Style == "" {
		http.Error(w, "name and style required", http.StatusBadRequest)
		return
	}

	agent := &AppCore{LLM: T.AppCore.LLM}
	session := agent.CreateSession(WORKER)
	desc_resp, _ := session.Chat(r.Context(), []Message{
		{Role: "user", Content: fmt.Sprintf("Summarize this writing style in one sentence (under 15 words):\n\n%s", req.Style)},
	}, WithMaxTokens(64), WithThink(false))

	desc := "Custom writing style"
	if desc_resp != nil {
		desc = strings.TrimSpace(ResponseText(desc_resp))
	}

	udb.Set(personaTable, req.Name, Persona{
		Name:        req.Name,
		Description: desc,
		Style:       req.Style,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "saved", "name": req.Name})
}

// handleListPersonas returns all saved personas.
func (T *TechWriterAgent) handleListPersonas(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if udb == nil {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "[]")
		return
	}
	type summary struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	var items []summary
	for _, key := range udb.Keys(personaTable) {
		var p Persona
		if udb.Get(personaTable, key, &p) {
			items = append(items, summary{Name: p.Name, Description: p.Description})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// handlePersona handles GET (load) and DELETE (remove) for a single persona.
func (T *TechWriterAgent) handlePersona(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/persona/")
	if name == "" || udb == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		var p Persona
		if !udb.Get(personaTable, name, &p) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p)
	case http.MethodDelete:
		udb.Unset(personaTable, name)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// loadPersonaStyle returns the style text for a named persona from the
// given user's sub-store, or "" if not found.
func (T *TechWriterAgent) loadPersonaStyle(udb Database, name string) string {
	if udb == nil || name == "" {
		return ""
	}
	var p Persona
	if udb.Get(personaTable, name, &p) {
		return p.Style
	}
	return ""
}

// personaPromptSection returns the system prompt addition when a persona
// is selected, or "" if none is active.
func personaPromptSection(style string) string {
	if style == "" {
		return ""
	}
	return fmt.Sprintf(`

WRITING STYLE — FOLLOW THESE RULES:
The user has selected a writing persona. Your output MUST follow the writing style rules below. Apply these rules to all content you produce — tone, sentence structure, formatting, and content patterns.

Style rules:
%s
`, style)
}
