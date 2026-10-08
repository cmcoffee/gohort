package customapps

// POST ask: an app's page asks the app's agent one question.
//
// {"prompt": "...", "json": true?} -> {"text": "..."}. Answered by the
// owner's app agent with no tools (orchestrate.AppAgentAsk), so the owner
// pays, and every ask is bounded before it is made: a daily spend cap for the
// whole app and one per user, plus a daily count per user, because a local
// worker costs nothing and its GPU is still finite.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/appscript"
)

const (
	askDefaultDailyUSD     = 2.00
	askDefaultUserDailyUSD = 0.50
	askUserDailyCalls      = 200
	askMaxPrompt           = 8000
)

const askSpendTable = "custom_ask_spend"

type askSpend struct {
	USD   float64 `json:"usd"`
	Calls int     `json:"calls"`
}

var askMu sync.Mutex

// appAgentAsk asks the app's agent; a test replaces it.
var appAgentAsk = func(ctx context.Context, owner, agentID, prompt string, jsonMode bool) (string, float64, error) {
	orch := findOrchestrate()
	if orch == nil {
		return "", 0, fmt.Errorf("agents are not available on this deployment")
	}
	return orch.AppAgentAsk(ctx, owner, agentID, prompt, jsonMode)
}

func askKeys(slug, user string, now time.Time) (app, mine string) {
	day := now.UTC().Format("2006-01-02")
	return slug + "\x00" + day, slug + "\x00" + day + "\x00" + user
}

func askCaps(spec AppSpec) (app, user float64) {
	app, user = spec.AskDailyUSD, spec.AskUserDailyUSD
	if app <= 0 {
		app = askDefaultDailyUSD
	}
	if user <= 0 {
		user = askDefaultUserDailyUSD
	}
	return app, user
}

// handleAsk is POST ask, for any user who can open the app.
func (T *CustomApps) handleAsk(w http.ResponseWriter, r *http.Request, ownerDB Database, owner, user string, spec AppSpec) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed: POST {\"prompt\": ...}", http.StatusMethodNotAllowed)
		return
	}
	if strings.TrimSpace(spec.AgentID) == "" {
		http.Error(w, "this app has no agent to ask: its owner sets agent_id", http.StatusBadRequest)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
		JSON   bool   `json:"json"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil || strings.TrimSpace(body.Prompt) == "" {
		http.Error(w, "send {\"prompt\": \"...\"}", http.StatusBadRequest)
		return
	}
	if len([]rune(body.Prompt)) > askMaxPrompt {
		http.Error(w, fmt.Sprintf("the prompt is over %d characters", askMaxPrompt), http.StatusRequestEntityTooLarge)
		return
	}
	text, status, err := askAppAgent(r.Context(), ownerDB, owner, user, spec, body.Prompt, body.JSON)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, map[string]any{"text": text})
}

// askAppAgent asks spec's agent prompt on user's behalf, under the app's and
// user's daily caps, and returns the text, or why not with the HTTP status
// that says so. The page's ask and a script's gohort.ask both come here, so
// a backend call is held to exactly the caps a page call is.
func askAppAgent(ctx context.Context, ownerDB Database, owner, user string, spec AppSpec, prompt string, jsonMode bool) (string, int, error) {
	if strings.TrimSpace(spec.AgentID) == "" {
		return "", http.StatusBadRequest, fmt.Errorf("this app has no agent to ask: its owner sets agent_id")
	}
	if strings.TrimSpace(prompt) == "" {
		return "", http.StatusBadRequest, fmt.Errorf("ask needs a prompt")
	}
	if len([]rune(prompt)) > askMaxPrompt {
		return "", http.StatusRequestEntityTooLarge, fmt.Errorf("the prompt is over %d characters", askMaxPrompt)
	}
	if ownerDB == nil {
		return "", http.StatusInternalServerError, fmt.Errorf("the app's store is not available")
	}
	appCap, userCap := askCaps(spec)
	ak, uk := askKeys(spec.Slug, user, time.Now())
	askMu.Lock()
	var appSpent, mine askSpend
	ownerDB.Get(askSpendTable, ak, &appSpent)
	ownerDB.Get(askSpendTable, uk, &mine)
	why := ""
	switch {
	case appSpent.USD >= appCap:
		why = fmt.Sprintf("this app has spent its $%.2f for today on its agent", appCap)
	case mine.USD >= userCap:
		why = fmt.Sprintf("you have spent your $%.2f for today on this app's agent", userCap)
	case mine.Calls >= askUserDailyCalls:
		why = fmt.Sprintf("you have asked this app's agent %d times today", askUserDailyCalls)
	}
	if why == "" {
		// Counted before the call, so a burst of parallel asks cannot all slip
		// under the call cap; the spend is added once the cost is known.
		mine.Calls++
		appSpent.Calls++
		ownerDB.Set(askSpendTable, uk, mine)
		ownerDB.Set(askSpendTable, ak, appSpent)
	}
	askMu.Unlock()
	if why != "" {
		return "", http.StatusTooManyRequests, fmt.Errorf("%s: it resets at midnight UTC", why)
	}
	text, cost, err := appAgentAsk(ctx, owner, spec.AgentID, prompt, jsonMode)
	if cost > 0 {
		askMu.Lock()
		ownerDB.Get(askSpendTable, ak, &appSpent)
		ownerDB.Get(askSpendTable, uk, &mine)
		appSpent.USD += cost
		mine.USD += cost
		ownerDB.Set(askSpendTable, ak, appSpent)
		ownerDB.Set(askSpendTable, uk, mine)
		askMu.Unlock()
	}
	if err != nil {
		Log("[customapps] ask %q for %s failed: %v", spec.Slug, user, err)
		return "", http.StatusBadGateway, fmt.Errorf("the app's agent could not answer: %w", err)
	}
	return text, http.StatusOK, nil
}

// A script's gohort.ask reaches the same caps, against the owner's store.
func init() {
	appscript.AppAsk = func(ctx context.Context, spec AppSpec, caller, prompt string, jsonMode bool) (string, error) {
		text, _, err := askAppAgent(ctx, appscript.RecordBase(spec, spec.Owner), spec.Owner, caller, spec, prompt, jsonMode)
		return text, err
	}
}
