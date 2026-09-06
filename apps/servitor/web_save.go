package servitor

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handleSaveDestinations returns which save targets are available.
func (T *Servitor) handleSaveDestinations(w http.ResponseWriter, r *http.Request) {
	_, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{
		"techwriter": SaveArticleFunc != nil,
		"codewriter": SaveSnippetFunc != nil,
		"guide":      HasDocumentTarget("guide"),
	})
}

// handleSaveArticle saves the given assistant response to TechWriter as-is.
// Subject is derived from the first heading/line; body is the verbatim text.
func (T *Servitor) handleSaveArticle(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if SaveArticleFunc == nil {
		http.Error(w, "TechWriter not available", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Text    string `json:"text"`
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	subject := strings.TrimSpace(req.Subject)
	body := req.Text
	if subject == "" {
		lines := strings.SplitN(strings.TrimSpace(body), "\n", 2)
		subject = strings.TrimPrefix(strings.TrimSpace(lines[0]), "# ")
		subject = strings.TrimPrefix(subject, "## ")
		if subject == "" {
			subject = "Untitled"
		}
	}
	id, err := SaveArticleFunc(userID, subject, body)
	if err != nil {
		http.Error(w, "save error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id, "subject": subject})
}

// handleSaveSnippet reformats the given assistant response for CodeWriter and saves it.
func (T *Servitor) handleSaveSnippet(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if SaveSnippetFunc == nil {
		http.Error(w, "CodeWriter not available", http.StatusServiceUnavailable)
		return
	}
	if T.LLM == nil {
		http.Error(w, "LLM not configured", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		http.Error(w, "text required", http.StatusBadRequest)
		return
	}
	sysPrompt := "You are a code assistant. Extract the primary code snippet from the provided content. " +
		"Respond with a JSON object only — no other text — with three fields: " +
		`"name" (a short descriptive name for the snippet), "lang" (language, e.g. sql/bash/python/go), and "code" (the code only, no markdown fences).`
	userMsg := "Extract the code snippet:\n\n" + req.Text
	resp, err := T.WorkerChat(r.Context(),
		[]Message{{Role: "user", Content: userMsg}},
		WithSystemPrompt(sysPrompt),
		WithJSONMode(),
		WithThink(false),
	)
	if err != nil {
		http.Error(w, "LLM error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var out struct {
		Name string `json:"name"`
		Lang string `json:"lang"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &out); err != nil || out.Code == "" {
		out.Name = "Snippet"
		out.Lang = "text"
		out.Code = req.Text
	}
	id, err := SaveSnippetFunc(userID, out.Name, out.Lang, out.Code)
	if err != nil {
		http.Error(w, "save error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id, "name": out.Name})
}
