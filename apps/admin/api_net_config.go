package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/service"
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

// handleWebSearch is the web search config: the engine, and where it
// searches. A key and an address are the connector's or the peer's.
func (a *AdminApp) handleWebSearch(c *jsonapi.Call) (any, error) {
	var provider, source string
	if a.db != nil {
		a.db.Get(SearchTable, "provider", &provider)
		a.db.Get(SearchTable, "source", &source)
	}
	if source == "" {
		source = EmbeddingProviderLocal
	}
	conn := chosenConnection(provider)
	out := markStale(map[string]any{"provider": provider, "source": source}, "provider", "")
	for k, v := range settingsValues(conn, "web.search") {
		out[k] = v
	}
	// A research source as the engine shows as "Use a research source…"
	// and which one.
	if c, ok := GetConnector(RootDB, chosenConnection(fmt.Sprint(out["provider"]))); ok && researchConnector(c, researchNamed()) {
		out["provider"], out["research_source"] = useResearchSource, c.Name
	}
	return out, nil
}

// webSearchForm is what the Web Search page posts: its config, and the
// research source chosen when the engine is "Use a research source…".
type webSearchForm struct {
	WebSearchConfig
	ResearchSource string `json:"research_source,omitempty"`
}

// useResearchSource is the page's "Use a research source…": the engine is
// the research source's connector the second list names. A page value only,
// never stored, and no connector can take it (names have no @).
const useResearchSource = "@research"

// engine reads "Use a research source…" back as the research source chosen,
// the engine the setting names.
func (f *webSearchForm) engine() error {
	if f.Provider != useResearchSource {
		return nil
	}
	if f.Provider = strings.TrimSpace(f.ResearchSource); f.Provider == "" {
		return fmt.Errorf("choose which research source to search with")
	}
	return nil
}

// searchEngineOK refuses an engine a local search cannot use: one that is
// not DuckDuckGo or a connector that searches (the Brave, Google,
// Serper and SearXNG code is templates now, and Source Hooks connectors).
func searchEngineOK(cfg WebSearchConfig) error {
	if strings.HasPrefix(cfg.Source, "peer:") {
		return nil
	}
	p := strings.TrimSpace(cfg.Provider)
	_, isConnector := GetConnector(RootDB, chosenConnection(p))
	switch {
	case p == "", p == "duckduckgo", p == "none":
		return nil
	case isConnector:
		return serviceProviderOK(p, "web.search")
	default:
		return fmt.Errorf("%s is not an engine here: add a connector that searches from a template (Brave, Serper, Tavily, Google Programmable Search, SearXNG) and choose it", p)
	}
}

// handleWebSearchSave saves the web search config.
func (a *AdminApp) handleWebSearchSave(c *jsonapi.Call, form withSettings[webSearchForm]) (any, error) {
	if err := form.Form.engine(); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	req, conn := form.Form.WebSearchConfig, chosenConnection(form.Form.Provider)
	// A local engine keeps no key or address here: they are the
	// connector's.
	req.APIKey, req.Endpoint = "", ""
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
	if err := searchEngineOK(req); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if err := form.Settings.save(conn, "web.search"); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if a.db != nil {
		a.db.Set(SearchTable, "provider", req.Provider)
		a.db.Set(SearchTable, "api_key", req.APIKey)
		a.db.Set(SearchTable, "endpoint", req.Endpoint)
		a.db.Set(SearchTable, "source", req.Source)
	}
	acceptChanges(req.Provider)
	adminChange(c.R, activity.Settings, "updated web-search config (provider=%q)",
		req.Provider)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleBrowse is where pages are rendered.
func (a *AdminApp) handleBrowse(c *jsonapi.Call) (any, error) {
	cfg := LoadBrowseConfig()
	if cfg.Source == "" {
		cfg.Source = EmbeddingProviderLocal
	}
	return markStale(withValues(cfg, chosenConnection(cfg.Source), "page.render"), "source", ""), nil
}

// handleBrowseSave sets where pages are rendered.
func (a *AdminApp) handleBrowseSave(c *jsonapi.Call, form withSettings[BrowseConfig]) (any, error) {
	req := form.Form
	src := strings.TrimSpace(req.Source)
	// Validate the selection rather than storing a name that silently
	// renders nothing: a peer that was forgotten, or had its browse
	// grant pulled, must be refused here where the operator can see it.
	if media.IsConnection(src) {
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
	if err := form.Settings.save(chosenConnection(src), "page.render"); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if a.db != nil {
		a.db.Set(BrowseTable, "source", src)
	}
	SetBrowseConfig(BrowseConfig{Source: src})
	acceptChanges(src)
	adminChange(c.R, activity.Settings, "set page rendering to %q", src)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleWebSearchTest runs one search with the form's working config.
func (a *AdminApp) handleWebSearchTest(c *jsonapi.Call, form withSettings[webSearchForm]) (any, error) {
	if err := form.Form.engine(); err != nil {
		return testResultBody(false, "", err.Error()), nil
	}
	if form.Form.Provider == "none" {
		return testResultBody(false, "", "web search is disabled: choose an engine first"), nil
	}
	req, conn := form.Form.WebSearchConfig, chosenConnection(form.Form.Provider)
	req.APIKey, req.Endpoint = "", ""
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
	if err := searchEngineOK(req); err != nil {
		return testResultBody(false, "", err.Error()), nil
	}
	orig := LoadWebSearchConfigFunc
	LoadWebSearchConfigFunc = func() WebSearchConfig { return req }
	defer func() { LoadWebSearchConfigFunc = orig }()
	// Under the request's context: the form's Cancel closes the request,
	// and the search tool's handler takes a context, so the provider call
	// is dropped with it rather than running out its own timeout.
	ctx, end := previewTieIn(form.Settings.testContext(c.R.Context(), conn, "web.search"), req.Provider)
	defer end()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	search := webSearchCtx
	if name, ok := media.ChosenConnection(req.Provider); ok {
		// The connector's own search, which the tool dispatches to, under
		// this request's context: the preview reaches it, and Cancel stops it.
		search = func(ctx context.Context, q string) (string, error) {
			out, err := service.Search(ctx, name, q)
			if out == "No results found." {
				out = ""
			}
			return out, err
		}
	}
	out, serr := search(ctx, "gohort connectivity test")
	if serr != nil {
		return testResultBody(false, "", serr.Error()), nil
	}
	if strings.TrimSpace(out) == "" {
		return testResultBody(false, "", "no results returned: check the engine (a connector's row under Admin > Extensions > Connectors shows its request and key)"), nil
	}
	return testResultBody(true, fmt.Sprintf("OK via %s: %d chars returned", ProviderTitle(req.Provider), len(out)), ""), nil
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
	return markStale(withValues(cfg, chosenConnection(cfg.Provider), "mail.send"), "provider", ""), nil
}

// handleMailSave saves the mail config.
func (a *AdminApp) handleMailSave(c *jsonapi.Call, form withSettings[MailConfig]) (any, error) {
	req, conn := form.Form, chosenConnection(form.Form.Provider)
	req.Password = keepSecret(req.Password, a.storedString(MailTable, "password"))
	if err := serviceProviderOK(req.Provider, "mail.send"); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	if err := form.Settings.save(conn, "mail.send"); err != nil {
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
	acceptChanges(req.Provider)
	adminChange(c.R, activity.Settings, "updated mail config (provider=%q server=%q from=%q)",
		req.Provider, req.Server, req.From)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleMailTest sends a test email with the form's working config.
func (a *AdminApp) handleMailTest(c *jsonapi.Call, form withSettings[MailConfig]) (any, error) {
	req, conn := form.Form, chosenConnection(form.Form.Provider)
	req.Password = keepSecret(req.Password, a.storedString(MailTable, "password"))
	to := req.Recipient
	if to == "" {
		return testResultBody(false, "", "set a Default Recipient first; test mail needs an address"), nil
	}
	// The posted (unsaved) config sends directly, under the request's
	// context, so Cancel ends the SMTP conversation wherever it is
	// blocked instead of leaving it to the server's timeout.
	ctx, end := previewTieIn(form.Settings.testContext(c.R.Context(), conn, "mail.send"), req.Provider)
	defer end()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
