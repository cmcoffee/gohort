package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/sourcehooks"
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
	sub.HandleFunc("/api/web-search", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleWebSearch, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleWebSearchSave, a.gate()),
	}.Serve)

	// The Source Hooks that can be the search engine: API hooks that are on,
	// for the Web Search form's picker.
	sub.HandleFunc("/api/web-search/hooks", jsonapi.Get(a.db, a.handleWebSearchHooks, a.gate()))

	// Page rendering — where browse_page and the render escalations happen.
	sub.HandleFunc("/api/browse", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleBrowse, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleBrowseSave, a.gate()),
	}.Serve)

	// Web search connectivity test — temporarily swap in the form's
	// working WebSearchConfig via LoadWebSearchConfigFunc, run a one-
	// shot WebSearch("gohort connectivity test") call, restore the
	// loader on exit. Empty result counts as failure (most providers
	// return SOMETHING for any term; an empty result implies a config
	// problem rather than a genuinely empty corpus).
	sub.HandleFunc("/api/web-search/test", jsonapi.Post(a.db, a.handleWebSearchTest, a.gate()))

	// Mail / SMTP — per-key rows under MailTable. The password reads as
	// secretUnchanged on GET and is kept when that comes back.
	sub.HandleFunc("/api/mail", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleMail, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleMailSave, a.gate()),
	}.Serve)

	// Mail connectivity test — sends a real email to the recipient
	// using the form's current (possibly unsaved) MailConfig. Mirrors
	// the "Send Test Email" flow in --setup.
	sub.HandleFunc("/api/mail/test", jsonapi.Post(a.db, a.handleMailTest, a.gate()))

	// Network timeouts — per-key rows under NetworkTable.
	sub.HandleFunc("/api/network", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleNetwork, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleNetworkSave, a.gate()),
	}.Serve)

	// Agent-loop tuning — history-budget cap (and future LLM-retry
	// knobs). Mirrors /api/network's GET-current/POST-new shape so the
	// admin FormPanel can save without app-specific JS.
	sub.HandleFunc("/api/agent-loop-tuning", jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleAgentLoopTuning, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleAgentLoopTuningSave, a.gate()),
	}.Serve)
}

// handleWebSearch is the web search config, its key masked.
func (a *AdminApp) handleWebSearch(c *jsonapi.Call) (any, error) {
	var provider, key, endpoint, source, hook string
	if a.db != nil {
		a.db.Get(SearchTable, "provider", &provider)
		a.db.Get(SearchTable, "api_key", &key)
		a.db.Get(SearchTable, "endpoint", &endpoint)
		a.db.Get(SearchTable, "source", &source)
		a.db.Get(SearchTable, "hook", &hook)
	}
	if source == "" {
		source = EmbeddingProviderLocal
	}
	return map[string]any{
		"provider": provider, "api_key": maskSecret(key), "endpoint": endpoint, "source": source, "hook": hook,
	}, nil
}

// handleWebSearchSave saves the web search config.
func (a *AdminApp) handleWebSearchSave(c *jsonapi.Call, req WebSearchConfig) (any, error) {
	req.APIKey = keepSecret(req.APIKey, a.storedString(SearchTable, "api_key"))
	// A peer selection carries no provider/key/endpoint of its own —
	// those fields are hidden while one is picked. Resolve here, the
	// same way the embeddings and transcription saves do, so what gets
	// stored is an ordinary searxng config and tools/websearch never
	// learns that peers exist.
	resolved, serr := ResolveSearchProvider(req, req.Source)
	if serr != nil {
		return nil, jsonapi.BadRequest(serr.Error())
	}
	req = resolved
	// A connector as the engine must be one that searches, and approved.
	if err := serviceProviderOK(req.Provider, "web.search"); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	// A Source Hook as the engine must be one that can be: refused
	// here, where the admin sees it, rather than every search failing.
	if req.Provider == "hook" {
		if _, ok := sourcehooks.SearchHook(req.Hook); !ok {
			return nil, jsonapi.BadRequest("pick a Source Hook to search with: an API hook that is switched on")
		}
	} else {
		req.Hook = ""
	}
	if a.db != nil {
		a.db.Set(SearchTable, "provider", req.Provider)
		a.db.Set(SearchTable, "api_key", req.APIKey)
		a.db.Set(SearchTable, "endpoint", req.Endpoint)
		a.db.Set(SearchTable, "source", req.Source)
		a.db.Set(SearchTable, "hook", req.Hook)
	}
	adminChange(c.R, activity.Settings, "updated web-search config (provider=%q)",
		req.Provider)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleWebSearchHooks is the hooks the search engine may be.
func (a *AdminApp) handleWebSearchHooks(c *jsonapi.Call) (any, error) {
	opts := []map[string]string{}
	// Every hook that can be the engine, the engine itself among them:
	// it is out of the source hooks' own uses, not out of this list.
	for _, h := range sourcehooks.RegisteredSourceHooks() {
		if _, ok := sourcehooks.SearchHook(h.Name); !ok {
			continue
		}
		label := h.Name
		if u, err := url.Parse(h.Endpoint); err == nil && u.Host != "" {
			label += " (" + u.Host + ")"
		}
		opts = append(opts, map[string]string{"value": h.Name, "label": label})
	}
	return opts, nil
}

// handleBrowse is where pages are rendered.
func (a *AdminApp) handleBrowse(c *jsonapi.Call) (any, error) {
	cfg := LoadBrowseConfig()
	if cfg.Source == "" {
		cfg.Source = EmbeddingProviderLocal
	}
	return cfg, nil
}

// handleBrowseSave sets where pages are rendered.
func (a *AdminApp) handleBrowseSave(c *jsonapi.Call, req BrowseConfig) (any, error) {
	src := strings.TrimSpace(req.Source)
	// Validate the selection rather than storing a name that silently
	// renders nothing: a peer that was forgotten, or had its browse
	// grant pulled, must be refused here where the operator can see it.
	if strings.HasPrefix(src, media.ServiceProviderPrefix) {
		if err := serviceProviderOK(src, "page.render"); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
	} else if src != "" && src != EmbeddingProviderLocal {
		p, ok := PeerFromProvider(src)
		if !ok {
			return nil, jsonapi.BadRequest("no peer named " + strings.TrimPrefix(src, "peer:") + " is registered")
		}
		if !p.Offers(PeerCapBrowse) {
			return nil, jsonapi.BadRequest("peer " + p.Name + " does not offer page rendering (it offers: " + strings.Join(p.Caps, ", ") + ")")
		}
	}
	if a.db != nil {
		a.db.Set(BrowseTable, "source", src)
	}
	SetBrowseConfig(BrowseConfig{Source: src})
	adminChange(c.R, activity.Settings, "set page rendering to %q", src)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleWebSearchTest runs one search with the form's working config.
func (a *AdminApp) handleWebSearchTest(c *jsonapi.Call, req WebSearchConfig) (any, error) {
	req.APIKey = keepSecret(req.APIKey, a.storedString(SearchTable, "api_key"))
	// Resolve before inspecting the fields — with a peer picked they are
	// hidden and empty, and testing a valid peer would otherwise fail on
	// the provider being blank. Same lesson as the embeddings and
	// transcription test buttons.
	resolved, serr := ResolveSearchProvider(req, req.Source)
	if serr != nil {
		return testResultBody(false, "", serr.Error()), nil
	}
	req = resolved
	if req.Provider == "" {
		return testResultBody(false, "", "provider is required"), nil
	}
	orig := LoadWebSearchConfigFunc
	LoadWebSearchConfigFunc = func() WebSearchConfig { return req }
	defer func() { LoadWebSearchConfigFunc = orig }()
	// Under the request's context: the form's Cancel closes the request,
	// and the search tool's handler takes a context, so the provider call
	// is dropped with it rather than running out its own timeout.
	ctx, cancel := context.WithTimeout(c.R.Context(), 20*time.Second)
	defer cancel()
	out, serr := webSearchCtx(ctx, "gohort connectivity test")
	if serr != nil {
		return testResultBody(false, "", serr.Error()), nil
	}
	if strings.TrimSpace(out) == "" {
		return testResultBody(false, "", "no results returned: check provider/key/endpoint"), nil
	}
	return testResultBody(true, fmt.Sprintf("OK via %s: %d chars returned", req.Provider, len(out)), ""), nil
}

// handleMail is the mail config, its password masked.
func (a *AdminApp) handleMail(c *jsonapi.Call) (any, error) {
	var cfg MailConfig
	if a.db != nil {
		a.db.Get(MailTable, "server", &cfg.Server)
		a.db.Get(MailTable, "from", &cfg.From)
		a.db.Get(MailTable, "username", &cfg.Username)
		a.db.Get(MailTable, "password", &cfg.Password)
		a.db.Get(MailTable, "recipient", &cfg.Recipient)
		a.db.Get(MailTable, "provider", &cfg.Provider)
	}
	if cfg.Provider == "" {
		cfg.Provider = "smtp"
	}
	cfg.Password = maskSecret(cfg.Password)
	return cfg, nil
}

// handleMailSave saves the mail config.
func (a *AdminApp) handleMailSave(c *jsonapi.Call, req MailConfig) (any, error) {
	req.Password = keepSecret(req.Password, a.storedString(MailTable, "password"))
	if err := serviceProviderOK(req.Provider, "mail.send"); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if a.db != nil {
		a.db.Set(MailTable, "provider", req.Provider)
		a.db.Set(MailTable, "server", req.Server)
		a.db.Set(MailTable, "from", req.From)
		a.db.Set(MailTable, "recipient", req.Recipient)
		a.db.Set(MailTable, "username", req.Username)
		a.db.Set(MailTable, "password", req.Password)
	}
	adminChange(c.R, activity.Settings, "updated mail config (provider=%q server=%q from=%q)",
		req.Provider, req.Server, req.From)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleMailTest sends a test email with the form's working config.
func (a *AdminApp) handleMailTest(c *jsonapi.Call, req MailConfig) (any, error) {
	req.Password = keepSecret(req.Password, a.storedString(MailTable, "password"))
	to := req.Recipient
	if to == "" {
		return testResultBody(false, "", "set a Default Recipient first; test mail needs an address"), nil
	}
	// The posted (unsaved) config sends directly, under the request's
	// context, so Cancel ends the SMTP conversation wherever it is
	// blocked instead of leaving it to the server's timeout.
	ctx, cancel := context.WithTimeout(c.R.Context(), 30*time.Second)
	defer cancel()
	if err := req.SendNotification(ctx, to, "Gohort Admin Test Email",
		"This is a test from the gohort admin UI.\n\nIf you received this, mail is configured correctly.\n"); err != nil {
		if ctx.Err() != nil {
			return testResultBody(false, "", "cancelled before the mail server answered"), nil
		}
		return testResultBody(false, "", err.Error()), nil
	}
	return testResultBody(true, fmt.Sprintf("Test email sent to %s", to), ""), nil
}

// handleNetwork is the network timeouts.
func (a *AdminApp) handleNetwork(c *jsonapi.Call) (any, error) {
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
	return map[string]int{
		"connect_timeout_seconds": connectSec,
		"request_timeout_seconds": requestSec,
	}, nil
}

// handleNetworkSave sets the network timeouts; a value of zero or less leaves
// that one as it was.
func (a *AdminApp) handleNetworkSave(c *jsonapi.Call, req struct {
	ConnectTimeoutSeconds int `json:"connect_timeout_seconds"`
	RequestTimeoutSeconds int `json:"request_timeout_seconds"`
}) (any, error) {
	if a.db != nil {
		if req.ConnectTimeoutSeconds > 0 {
			a.db.Set(NetworkTable, "connect_timeout_seconds", req.ConnectTimeoutSeconds)
		}
		if req.RequestTimeoutSeconds > 0 {
			a.db.Set(NetworkTable, "request_timeout_seconds", req.RequestTimeoutSeconds)
		}
	}
	adminChange(c.R, activity.Settings, "updated network timeouts (connect=%ds request=%ds)",
		req.ConnectTimeoutSeconds, req.RequestTimeoutSeconds)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleAgentLoopTuning is the agent loop's history budget.
func (a *AdminApp) handleAgentLoopTuning(c *jsonapi.Call) (any, error) {
	return map[string]int{
		"history_budget_percent": GetAgentLoopTuning().HistoryBudgetPercent,
	}, nil
}

// handleAgentLoopTuningSave sets the agent loop's history budget.
func (a *AdminApp) handleAgentLoopTuningSave(c *jsonapi.Call, req struct {
	HistoryBudgetPercent int `json:"history_budget_percent"`
}) (any, error) {
	if err := SaveAgentLoopTuningToDB(a.db, AgentLoopTuning{
		HistoryBudgetPercent: req.HistoryBudgetPercent,
	}); err != nil {
		return nil, err
	}
	adminChange(c.R, activity.Settings, "updated agent-loop tuning (history_budget_percent=%d)",
		GetAgentLoopTuning().HistoryBudgetPercent)
	return jsonapi.Empty(http.StatusNoContent), nil
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
