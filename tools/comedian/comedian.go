package comedian

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(new(ComedianTool)) }

// ComedianTool fetches random jokes from JokeAPI, pooling them in batches
// and tracking seen IDs so the same joke is never repeated until the full
// category has been exhausted, at which point the seen set resets.
type ComedianTool struct {
	mu   sync.Mutex
	pool map[string][]jokeEntry // category → queued jokes not yet told
	seen map[string]map[int]bool // category → set of told joke IDs
}

type jokeEntry struct {
	id   int
	text string
}

func (t *ComedianTool) Name() string { return "get_joke" }
func (t *ComedianTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} } // fetches jokes over HTTP
func (t *ComedianTool) Desc() string {
	return "Fetch a random joke from the internet. Jokes are pooled and deduplicated so the same joke is never repeated. ALWAYS use this tool when the user asks for a joke — never make up a joke from your own knowledge. Use when the user wants you to be funny."
}

func (t *ComedianTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"category": {
			Type:        "string",
			Description: `Joke category — one of: "Any", "Misc", "Programming", "Dark", "Pun", "Spooky", "Christmas". Defaults to "Any".`,
		},
	}
}

var validCategory = map[string]bool{
	"Any": true, "Misc": true, "Programming": true,
	"Dark": true, "Pun": true, "Spooky": true, "Christmas": true,
}

var jokeClient = &http.Client{Timeout: 10 * time.Second}

func (t *ComedianTool) IsInternetTool() bool { return true }

func (t *ComedianTool) Run(args map[string]any) (string, error) {
	category := StringArg(args, "category")
	if !validCategory[category] {
		category = "Dark"
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.pool == nil {
		t.pool = make(map[string][]jokeEntry)
		t.seen = make(map[string]map[int]bool)
	}

	// Refill the pool if empty.
	if len(t.pool[category]) == 0 {
		if err := t.refill(category); err != nil {
			return "", err
		}
		// If still empty after refill (all seen), rotate: clear seen and retry.
		if len(t.pool[category]) == 0 {
			t.seen[category] = nil
			if err := t.refill(category); err != nil {
				return "", err
			}
		}
		if len(t.pool[category]) == 0 {
			return "", fmt.Errorf("no jokes available for category %q", category)
		}
	}

	// Pop the first joke.
	entry := t.pool[category][0]
	t.pool[category] = t.pool[category][1:]

	// Mark as seen.
	if t.seen[category] == nil {
		t.seen[category] = make(map[int]bool)
	}
	t.seen[category][entry.id] = true

	return entry.text, nil
}

// refill fetches up to 10 jokes for the category and appends any unseen ones
// to the pool. Must be called with t.mu held.
func (t *ComedianTool) refill(category string) error {
	fetched, err := fetchJokes(category, 10)
	if err != nil {
		return err
	}
	seen := t.seen[category]
	for _, e := range fetched {
		if seen == nil || !seen[e.id] {
			t.pool[category] = append(t.pool[category], e)
		}
	}
	return nil
}

// API response types.

type jokeBatch struct {
	Error  bool           `json:"error"`
	Amount int            `json:"amount"`
	Jokes  []jokeResponse `json:"jokes"`
	// Single-joke fields (when amount=1 the API returns a flat object).
	jokeResponse
}

type jokeResponse struct {
	Error    bool   `json:"error"`
	ID       int    `json:"id"`
	Type     string `json:"type"`
	Joke     string `json:"joke"`
	Setup    string `json:"setup"`
	Delivery string `json:"delivery"`
	Message  string `json:"message"`
}

func fetchJokes(category string, amount int) ([]jokeEntry, error) {
	url := fmt.Sprintf("https://v2.jokeapi.dev/joke/%s?blacklistFlags=racist&amount=%d", category, amount)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := jokeClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("joke fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("joke API returned HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}

	// The API returns a batch object when amount > 1 and a flat joke when amount = 1.
	var batch jokeBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}
	if batch.Error {
		return nil, fmt.Errorf("joke API error: %s", batch.Message)
	}

	// Normalise: use the embedded flat joke when the API returned a single result.
	jokes := batch.Jokes
	if len(jokes) == 0 && batch.jokeResponse.Type != "" {
		jokes = []jokeResponse{batch.jokeResponse}
	}

	var out []jokeEntry
	for _, j := range jokes {
		text, err := formatJoke(j)
		if err != nil || text == "" {
			continue
		}
		out = append(out, jokeEntry{id: j.ID, text: text})
	}
	return out, nil
}

func formatJoke(j jokeResponse) (string, error) {
	switch j.Type {
	case "twopart":
		return fmt.Sprintf("%s\n\n%s", j.Setup, j.Delivery), nil
	case "single":
		return j.Joke, nil
	default:
		return "", fmt.Errorf("unexpected joke type %q", j.Type)
	}
}
