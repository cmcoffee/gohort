// Per-user persona library for phantom. Built-in personas (AI
// Assistant, Mirror) ship in code and can't be deleted; user-created
// personas are stored under the user's name in a kvlite table and
// surface alongside built-ins in the picker.
//
// AI Assist takes a seed (e.g. "Hank Hill") and asks the worker
// LLM to expand it into a full personality block, which the user
// reviews and saves as a new persona.

package phantom

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// personaTable holds user-created personas keyed by username.
const personaTable = "phantom_personas"

// Persona is one personality preset the operator can apply to a
// conversation. Personality is the only writable field for now —
// gatekeeper rules are handled separately. Built-ins have IDs
// prefixed "builtin:" and a fixed ordering.
type Persona struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Personality string `json:"personality"`
	BuiltIn     bool   `json:"builtin,omitempty"`
	Created     string `json:"created,omitempty"`
}

// builtInPersonas — fixed catalog. Order is the display order in
// the picker. AI Assistant first as the safe default, then Mirror
// for tone-matching use cases (covers both 1:1 and group chats).
func builtInPersonas() []Persona {
	return []Persona{
		{
			ID:   "builtin:ai_assistant",
			Name: "AI Assistant",
			Personality: "You are a helpful, neutral AI assistant. " +
				"Speak in a clear, professional tone. " +
				"Be concise — don't pad responses with apologies, hedges, " +
				"or restatements of the question. " +
				"When you're unsure, say so plainly rather than guessing. " +
				"Default to brief replies and expand only when asked.",
			BuiltIn: true,
		},
		{
			ID:   "builtin:mirror",
			Name: "Mirror",
			Personality: "Match the tone, style, formality, and energy of " +
				"whoever you're talking with. If they write short and casual, " +
				"reply short and casual. If they write longer and formal, " +
				"reply in kind. Pick up their slang, their punctuation habits, " +
				"their emoji usage. Don't impose a persona — reflect theirs back. " +
				"In a 1:1 conversation, mirror the single other party. " +
				"In a group conversation, mirror the group's collective tone " +
				"and lean toward the most recent few messages so your reply " +
				"slots into the current rhythm. " +
				"Stay genuinely helpful while you mirror; mirroring is the voice, " +
				"not a substitute for being useful.",
			BuiltIn: true,
		},
	}
}

// loadUserPersonas returns the user's saved personas. Empty when the
// user has none yet.
func loadUserPersonas(db Database, username string) []Persona {
	if db == nil || username == "" {
		return nil
	}
	var out []Persona
	db.Get(personaTable, username, &out)
	return out
}

func saveUserPersonas(db Database, username string, personas []Persona) {
	if db == nil || username == "" {
		return
	}
	db.Set(personaTable, username, personas)
}

// allPersonas merges built-ins (always first, fixed order) with the
// user's saved personas (sorted by name).
func allPersonas(db Database, username string) []Persona {
	out := builtInPersonas()
	users := loadUserPersonas(db, username)
	sort.Slice(users, func(i, j int) bool {
		return strings.ToLower(users[i].Name) < strings.ToLower(users[j].Name)
	})
	out = append(out, users...)
	return out
}

func (T *Phantom) handlePersonas(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		jsonOK(w, allPersonas(T.DB, user))
	case http.MethodPost:
		var req struct {
			Name        string `json:"name"`
			Personality string `json:"personality"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		req.Personality = strings.TrimSpace(req.Personality)
		if req.Name == "" || req.Personality == "" {
			http.Error(w, "name and personality required", http.StatusBadRequest)
			return
		}
		// Built-in name collision check — case-insensitive match
		// rejected so users can't shadow a built-in.
		for _, b := range builtInPersonas() {
			if strings.EqualFold(b.Name, req.Name) {
				http.Error(w, "name conflicts with a built-in persona", http.StatusConflict)
				return
			}
		}
		users := loadUserPersonas(T.DB, user)
		// Replace if same name exists, else append.
		replaced := false
		for i := range users {
			if strings.EqualFold(users[i].Name, req.Name) {
				users[i].Personality = req.Personality
				replaced = true
				break
			}
		}
		if !replaced {
			users = append(users, Persona{
				ID:          UUIDv4(),
				Name:        req.Name,
				Personality: req.Personality,
				Created:     time.Now().Format(time.RFC3339),
			})
		}
		saveUserPersonas(T.DB, user, users)
		jsonOK(w, allPersonas(T.DB, user))
	case http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/api/personas/")
		if id == "" || strings.HasPrefix(id, "builtin:") {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		users := loadUserPersonas(T.DB, user)
		out := users[:0]
		removed := false
		for _, p := range users {
			if p.ID == id {
				removed = true
				continue
			}
			out = append(out, p)
		}
		if !removed {
			http.NotFound(w, r)
			return
		}
		saveUserPersonas(T.DB, user, out)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePersonaAssist takes a short seed (e.g. "Hank Hill",
// "deadpan British butler", "Walter Cronkite") and asks the worker
// LLM to expand it into a full personality block. Returns plain
// text; the client drops it into the personality textarea so the
// user can review before saving.
func (T *Phantom) handlePersonaAssist(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Seed string `json:"seed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	seed := strings.TrimSpace(req.Seed)
	if seed == "" {
		http.Error(w, "seed required", http.StatusBadRequest)
		return
	}
	if T.LLM == nil {
		http.Error(w, "LLM not configured", http.StatusInternalServerError)
		return
	}
	sys := "You write personality prompts for an AI assistant that responds " +
		"as a specific character or in a specific style. The prompt will be " +
		"prepended to the assistant's system prompt for a text-message " +
		"conversation. " +
		"\n\nWrite ONE focused paragraph (3-6 sentences) that captures: voice, " +
		"tone, vocabulary patterns, mannerisms, and what topics they tend toward. " +
		"Use second-person imperative (\"You speak...\", \"You favor...\") so the " +
		"prompt instructs the assistant rather than describing a third party. " +
		"Don't include greetings, sign-offs, or examples — just the persona. " +
		"Don't start with \"As [name]\" or \"You are [name]\" — describe HOW they " +
		"speak, not just who they are. Keep it text-message friendly: don't " +
		"prescribe long-form essay style." +
		"\n\nReply with ONLY the prompt text — no preamble, no quotes."

	resp, err := T.AppCore.WorkerChat(r.Context(),
		[]Message{{Role: "user", Content: "Write the personality prompt for: " + seed}},
		WithSystemPrompt(sys),
		WithMaxTokens(400),
		WithThink(false),
	)
	if err != nil {
		http.Error(w, "LLM error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	text := strings.TrimSpace(resp.Content)
	// Strip surrounding quotes the LLM sometimes adds despite the rule.
	text = strings.Trim(text, "\"'")
	if text == "" {
		http.Error(w, "LLM returned empty", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, text)
}
