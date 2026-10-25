package servitor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// handleSaveDestinations returns which save targets are available.
func (T *Servitor) handleSaveDestinations(c *jsonapi.Call) (any, error) {
	return map[string]bool{
		"scribe":     SaveArticleFunc != nil,
		"codewriter": SaveSnippetFunc != nil,
		"guide":      HasDocumentTarget("guide"),
	}, nil
}

// handleSaveArticle saves the given assistant response to Scribe as a guide, as-is.
// Subject is derived from the first heading/line; body is the verbatim text.
func (T *Servitor) handleSaveArticle(c *jsonapi.Call, req saveArticleReq) (any, error) {
	userID := c.User
	if SaveArticleFunc == nil {
		return nil, jsonapi.Status(http.StatusServiceUnavailable, "Scribe not available")
	}
	if req.Text == "" {
		return nil, jsonapi.BadRequest("text required")
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
		return nil, jsonapi.Status(http.StatusInternalServerError, "save error: "+err.Error())
	}
	return map[string]string{"id": id, "subject": subject}, nil
}

// saveArticleReq is what handleSaveArticle is posted.
type saveArticleReq struct {
	Text    string `json:"text"`
	Subject string `json:"subject"`
}

// handleSaveSnippet reformats the given assistant response for CodeWriter and saves it.
func (T *Servitor) handleSaveSnippet(c *jsonapi.Call, req saveSnippetReq) (any, error) {
	userID := c.User
	if SaveSnippetFunc == nil {
		return nil, jsonapi.Status(http.StatusServiceUnavailable, "CodeWriter not available")
	}
	if T.LLM == nil {
		return nil, jsonapi.Status(http.StatusServiceUnavailable, "LLM not configured")
	}
	if req.Text == "" {
		return nil, jsonapi.BadRequest("text required")
	}
	sysPrompt := "You are a code assistant. Extract the primary code snippet from the provided content. " +
		"Respond with a JSON object only (no other text) with three fields: " +
		`"name" (a short descriptive name for the snippet), "lang" (language, e.g. sql/bash/python/go), and "code" (the code only, no markdown fences).`
	userMsg := "Extract the code snippet:\n\n" + req.Text
	resp, err := T.WorkerChat(c.R.Context(),
		[]Message{{Role: "user", Content: userMsg}},
		WithSystemPrompt(sysPrompt),
		WithJSONMode(),
		WithThink(false),
	)
	if err != nil {
		// The provider's words go to the log, not the browser.
		return nil, fmt.Errorf("LLM error: %w", err)
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
		return nil, jsonapi.Status(http.StatusInternalServerError, "save error: "+err.Error())
	}
	return map[string]string{"id": id, "name": out.Name}, nil
}

// saveSnippetReq is what handleSaveSnippet is posted.
type saveSnippetReq struct {
	Text string `json:"text"`
}
