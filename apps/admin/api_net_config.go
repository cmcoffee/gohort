package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// secretUnchanged is what a stored secret reads as on a GET. The admin forms
// used to receive search, mail, image, embedding and transcription keys in
// the clear: a password field hides them on screen, not from the page's
// script, its network log or a cached response. The form posts this value
// back when nobody touched the field, and keepSecret turns it into the
// stored value again, so saving an unrelated field keeps the key and
// clearing the field still clears it.
const secretUnchanged = "(unchanged)"

// maskSecret is the GET side: a set secret reads as the placeholder, an
// unset one as empty, so the form can still tell the two apart.
func maskSecret(v string) string {
	if v == "" {
		return ""
	}
	return secretUnchanged
}

// keepSecret is the POST (and Test) side: the placeholder means "the value
// already stored".
func keepSecret(posted, stored string) string {
	if posted == secretUnchanged {
		return stored
	}
	return posted
}

// registerNetConfigRoutes wires the net config API under the admin sub-mux.
func (a *AdminApp) registerNetConfigRoutes(sub *http.ServeMux) {
	// Web search — per-key rows under SearchTable (provider, api_key,
	// endpoint). Same shape as --setup.
	sub.HandleFunc("/api/web-search", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req WebSearchConfig
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			req.APIKey = keepSecret(req.APIKey, a.storedString(SearchTable, "api_key"))
			// A peer selection carries no provider/key/endpoint of its own —
			// those fields are hidden while one is picked. Resolve here, the
			// same way the embeddings and transcription saves do, so what gets
			// stored is an ordinary searxng config and tools/websearch never
			// learns that peers exist.
			resolved, serr := ResolveSearchProvider(req, req.Source)
			if serr != nil {
				http.Error(w, serr.Error(), http.StatusBadRequest)
				return
			}
			req = resolved
			if a.db != nil {
				a.db.Set(SearchTable, "provider", req.Provider)
				a.db.Set(SearchTable, "api_key", req.APIKey)
				a.db.Set(SearchTable, "endpoint", req.Endpoint)
				a.db.Set(SearchTable, "source", req.Source)
			}
			Log("[admin] user %q updated web-search config (provider=%q)",
				AuthCurrentUser(r), req.Provider)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var provider, key, endpoint, source string
		if a.db != nil {
			a.db.Get(SearchTable, "provider", &provider)
			a.db.Get(SearchTable, "api_key", &key)
			a.db.Get(SearchTable, "endpoint", &endpoint)
			a.db.Get(SearchTable, "source", &source)
		}
		if source == "" {
			source = EmbeddingProviderLocal
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"provider": provider, "api_key": maskSecret(key), "endpoint": endpoint, "source": source,
		})
	})

	// Page rendering — where browse_page and the render escalations happen.
	sub.HandleFunc("/api/browse", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req BrowseConfig
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			src := strings.TrimSpace(req.Source)
			// Validate the selection rather than storing a name that silently
			// renders nothing: a peer that was forgotten, or had its browse
			// grant pulled, must be refused here where the operator can see it.
			if src != "" && src != EmbeddingProviderLocal {
				p, ok := PeerFromProvider(src)
				if !ok {
					http.Error(w, "no peer named "+strings.TrimPrefix(src, "peer:")+" is registered", http.StatusBadRequest)
					return
				}
				if !p.Offers(PeerCapBrowse) {
					http.Error(w, "peer "+p.Name+" does not offer page rendering (it offers: "+strings.Join(p.Caps, ", ")+")", http.StatusBadRequest)
					return
				}
			}
			if a.db != nil {
				a.db.Set(BrowseTable, "source", src)
			}
			SetBrowseConfig(BrowseConfig{Source: src})
			Log("[admin] user %q set page rendering to %q", AuthCurrentUser(r), src)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		cfg := LoadBrowseConfig()
		if cfg.Source == "" {
			cfg.Source = EmbeddingProviderLocal
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	})

	// Web search connectivity test — temporarily swap in the form's
	// working WebSearchConfig via LoadWebSearchConfigFunc, run a one-
	// shot WebSearch("gohort connectivity test") call, restore the
	// loader on exit. Empty result counts as failure (most providers
	// return SOMETHING for any term; an empty result implies a config
	// problem rather than a genuinely empty corpus).
	sub.HandleFunc("/api/web-search/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req WebSearchConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeTestResult(w, false, "", "invalid request body")
			return
		}
		req.APIKey = keepSecret(req.APIKey, a.storedString(SearchTable, "api_key"))
		// Resolve before inspecting the fields — with a peer picked they are
		// hidden and empty, and testing a valid peer would otherwise fail on
		// the provider being blank. Same lesson as the embeddings and
		// transcription test buttons.
		if resolved, serr := ResolveSearchProvider(req, req.Source); serr != nil {
			writeTestResult(w, false, "", serr.Error())
			return
		} else {
			req = resolved
		}
		if req.Provider == "" {
			writeTestResult(w, false, "", "provider is required")
			return
		}
		orig := LoadWebSearchConfigFunc
		LoadWebSearchConfigFunc = func() WebSearchConfig { return req }
		defer func() { LoadWebSearchConfigFunc = orig }()
		// Under the request's context: the form's Cancel closes the request,
		// and the search tool's handler takes a context, so the provider call
		// is dropped with it rather than running out its own timeout.
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		out, serr := webSearchCtx(ctx, "gohort connectivity test")
		if serr != nil {
			writeTestResult(w, false, "", serr.Error())
			return
		}
		if strings.TrimSpace(out) == "" {
			writeTestResult(w, false, "", "no results returned: check provider/key/endpoint")
			return
		}
		// Trim the result to a short preview so the inline UI doesn't
		// overflow with a wall of links.
		preview := strings.TrimSpace(out)
		if len(preview) > 80 {
			preview = preview[:80] + "…"
		}
		writeTestResult(w, true, fmt.Sprintf("OK via %s: %d chars returned", req.Provider, len(out)), "")
	})

	// Mail / SMTP — per-key rows under MailTable. The password reads as
	// secretUnchanged on GET and is kept when that comes back.
	sub.HandleFunc("/api/mail", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req MailConfig
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			req.Password = keepSecret(req.Password, a.storedString(MailTable, "password"))
			if a.db != nil {
				a.db.Set(MailTable, "server", req.Server)
				a.db.Set(MailTable, "from", req.From)
				a.db.Set(MailTable, "recipient", req.Recipient)
				a.db.Set(MailTable, "username", req.Username)
				a.db.Set(MailTable, "password", req.Password)
			}
			Log("[admin] user %q updated mail config (server=%q from=%q)",
				AuthCurrentUser(r), req.Server, req.From)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var cfg MailConfig
		if a.db != nil {
			a.db.Get(MailTable, "server", &cfg.Server)
			a.db.Get(MailTable, "from", &cfg.From)
			a.db.Get(MailTable, "username", &cfg.Username)
			a.db.Get(MailTable, "password", &cfg.Password)
			a.db.Get(MailTable, "recipient", &cfg.Recipient)
		}
		cfg.Password = maskSecret(cfg.Password)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	})

	// Mail connectivity test — sends a real email to the recipient
	// using the form's current (possibly unsaved) MailConfig. Mirrors
	// the "Send Test Email" flow in --setup.
	sub.HandleFunc("/api/mail/test", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req MailConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeTestResult(w, false, "", "invalid request body")
			return
		}
		req.Password = keepSecret(req.Password, a.storedString(MailTable, "password"))
		to := req.Recipient
		if to == "" {
			writeTestResult(w, false, "", "set a Default Recipient first; test mail needs an address")
			return
		}
		// The posted (unsaved) config sends directly, under the request's
		// context, so Cancel ends the SMTP conversation wherever it is
		// blocked instead of leaving it to the server's timeout.
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := req.SendNotification(ctx, to, "Gohort Admin Test Email",
			"This is a test from the gohort admin UI.\n\nIf you received this, mail is configured correctly.\n"); err != nil {
			if ctx.Err() != nil {
				writeTestResult(w, false, "", "cancelled before the mail server answered")
				return
			}
			writeTestResult(w, false, "", err.Error())
			return
		}
		writeTestResult(w, true, fmt.Sprintf("Test email sent to %s", to), "")
	})

	// Network timeouts — per-key rows under NetworkTable.
	sub.HandleFunc("/api/network", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				ConnectTimeoutSeconds int `json:"connect_timeout_seconds"`
				RequestTimeoutSeconds int `json:"request_timeout_seconds"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if a.db != nil {
				if req.ConnectTimeoutSeconds > 0 {
					a.db.Set(NetworkTable, "connect_timeout_seconds", req.ConnectTimeoutSeconds)
				}
				if req.RequestTimeoutSeconds > 0 {
					a.db.Set(NetworkTable, "request_timeout_seconds", req.RequestTimeoutSeconds)
				}
			}
			Log("[admin] user %q updated network timeouts (connect=%ds request=%ds)",
				AuthCurrentUser(r), req.ConnectTimeoutSeconds, req.RequestTimeoutSeconds)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var connectSec, requestSec int
		if a.db != nil {
			a.db.Get(NetworkTable, "connect_timeout_seconds", &connectSec)
			a.db.Get(NetworkTable, "request_timeout_seconds", &requestSec)
		}
		if connectSec <= 0 {
			connectSec = 10
		}
		if requestSec <= 0 {
			requestSec = 15
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{
			"connect_timeout_seconds": connectSec,
			"request_timeout_seconds": requestSec,
		})
	})

	// Agent-loop tuning — history-budget cap (and future LLM-retry
	// knobs). Mirrors /api/network's GET-current/POST-new shape so the
	// admin FormPanel can save without app-specific JS.
	sub.HandleFunc("/api/agent-loop-tuning", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				HistoryBudgetPercent int `json:"history_budget_percent"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if err := SaveAgentLoopTuningToDB(a.db, AgentLoopTuning{
				HistoryBudgetPercent: req.HistoryBudgetPercent,
			}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			Log("[admin] user %q updated agent-loop tuning (history_budget_percent=%d)",
				AuthCurrentUser(r), GetAgentLoopTuning().HistoryBudgetPercent)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		cur := GetAgentLoopTuning()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{
			"history_budget_percent": cur.HistoryBudgetPercent,
		})
	})

}

// webSearchCtx is core.WebSearch under a context: the registered web_search
// tool's handler, which takes one, called directly so a cancelled admin test
// cancels the provider call.
func webSearchCtx(ctx context.Context, query string) (string, error) {
	tools, err := GetAgentTools("web_search")
	if err != nil || len(tools) == 0 {
		return "", errors.New("no web_search tool is registered: enable a search provider first")
	}
	out, err := tools[0].Handler(ctx, map[string]any{"query": query})
	if err != nil {
		if ctx.Err() != nil {
			return "", errors.New("cancelled before the search provider answered")
		}
		return "", err
	}
	if out == "No results found." {
		return "", nil
	}
	return out, nil
}

// storedString reads one string row, "" when unset or no store.
func (a *AdminApp) storedString(table, key string) string {
	var v string
	if a.db != nil {
		a.db.Get(table, key, &v)
	}
	return v
}
