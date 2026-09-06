package core

import (
	"fmt"
)

// MailConfig holds SMTP mail settings.
type MailConfig struct {
	Server    string `json:"server"`    // SMTP server host:port (e.g. "smtp.gmail.com:587")
	From      string `json:"from"`      // Sender email address
	Username  string `json:"username"`  // SMTP auth username
	Password  string `json:"password"`  // SMTP auth password
	Recipient string `json:"recipient"` // Default report recipient email address
}

// WebSearchConfig holds web search provider settings.
type WebSearchConfig struct {
	Provider string `json:"provider"` // Search provider: "duckduckgo", "brave", "google", "searxng"
	APIKey   string `json:"api_key"`  // API key (not required for duckduckgo/searxng)
	Endpoint string `json:"endpoint"` // Custom endpoint (for searxng instances)
	// Source records WHERE this config came from: "local" (a provider was
	// picked below) or "peer:<name>" (the fields were filled from a peer — see
	// ResolveSearchProvider). Separate from Provider because a resolved peer
	// config IS a searxng config, and without this the admin form would show
	// "SearXNG" back to an operator who chose a peer, with nothing on screen
	// admitting where the searches actually go.
	Source string `json:"source,omitempty"`
	// CostPerCall prices one search into the cost ledger. 0 = untracked.
	//
	// The operator is the only one who can supply it — what a provider charges
	// is on their invoice, not in any response. Without it a metered key could
	// be spent by a peer with nothing recorded anywhere: the peer surface
	// already capped search separately BECAUSE it spends money
	// (peerSearchRatePerMin), and then counted none of it.
	CostPerCall float64 `json:"cost_per_call,omitempty"`
}

// OllamaBackendFunc returns the Ollama backend base URL, configured model name,
// and context window size (num_ctx). Set by the main application at startup.
// Returns ("", "", 0) when Ollama is not the active provider or is not configured.
var OllamaBackendFunc func() (backend, model string, numCtx int)

// LlamaCppBackendFunc returns the llama.cpp server base URL (including /v1 path)
// and configured model name. Returns ("", "") when llama.cpp is not the active provider.
var LlamaCppBackendFunc func() (endpoint, model string)

// OllamaProxyEnabledFunc reports whether the Ollama proxy is enabled.
// Set by the main application. Returns false when unset.
var OllamaProxyEnabledFunc func() bool

// OllamaProxyPortFunc returns the port the standalone Ollama proxy server
// should listen on. Returns 0 when unset or not configured.
var OllamaProxyPortFunc func() int

// OllamaProxyBindFunc returns the interface the Ollama proxy binds to.
// Empty means the safe default, 127.0.0.1.
//
// It exists because the proxy is a SEPARATE http.Server on its own port, so
// nothing the dashboard does — AuthMiddleware, the admin IP allowlist, TLS —
// reaches it. It used to bind ":port", which is every interface, which on a
// host with a public address is an open inference endpoint. The bind is now a
// deliberate choice, and exposing it beyond loopback additionally requires a
// key (see the proxy's own gate).
var OllamaProxyBindFunc func() string

// LoadWebSearchConfigFunc is set by the application to load search settings from the database.
var LoadWebSearchConfigFunc func() WebSearchConfig

// LoadWebSearchConfig returns the stored web search configuration.
// LoadWebSearchConfig returns the stored search configuration with the current
// peer record overlaid, so a config whose Source names a peer uses that peer's
// endpoint and key as they are NOW rather than as they were when saved. Every
// consumer reads through here (tools/websearch, tools/imagefetch, and the peer
// surface itself), which is what makes the overlay complete.
func LoadWebSearchConfig() WebSearchConfig {
	if LoadWebSearchConfigFunc != nil {
		return resolveSearchPeer(LoadWebSearchConfigFunc())
	}
	return WebSearchConfig{}
}

// LoadMailConfigFunc is set by the application to load mail settings from the database.
var LoadMailConfigFunc func() MailConfig

// LoadMailConfig returns the stored mail configuration.
func LoadMailConfig() MailConfig {
	if LoadMailConfigFunc != nil {
		return LoadMailConfigFunc()
	}
	return MailConfig{}
}

// GhostConfig holds CMS connection details.
type GhostConfig struct {
	URL    string
	APIKey string
}

// LoadGhostConfigFunc is set by the application to load CMS settings from the database.
var LoadGhostConfigFunc func() GhostConfig

// LoadGhostConfig returns the stored CMS configuration.
func LoadGhostConfig() GhostConfig {
	if LoadGhostConfigFunc != nil {
		return LoadGhostConfigFunc()
	}
	return GhostConfig{}
}

// SaveGhostConfigFunc is set by the application to persist CMS settings to
// exactly where LoadGhostConfigFunc reads them.
//
// It exists because there was no writer seam and an app therefore had nowhere
// correct to save to: an app's T.DB is its own bucket, while setup and the
// publisher both use the root store, so a UI that wrote the obvious way wrote
// somewhere nothing reads. A loader without a matching saver is an invitation
// to guess, and the guess is wrong.
var SaveGhostConfigFunc func(GhostConfig)

// SaveGhostConfig stores the CMS configuration where LoadGhostConfig will find
// it. Reports false when the application wired no store, so a caller can say so
// rather than reporting a save that went nowhere.
func SaveGhostConfig(cfg GhostConfig) bool {
	if SaveGhostConfigFunc == nil {
		return false
	}
	SaveGhostConfigFunc(cfg)
	return true
}

// SaveSnippetFunc is set by the CodeWriter app so other apps can save code snippets
// to the user's CodeWriter library without importing the codewriter package.
var SaveSnippetFunc func(userID, name, lang, code string) (id string, err error)

// SaveArticleFunc is set by the TechWriter app so other apps can save documents
// to the user's TechWriter library without importing the techwriter package.
var SaveArticleFunc func(userID, subject, body string) (id string, err error)

// RunAgentFunc is set by the application to enable agent-to-agent delegation.
var RunAgentFunc func(name string, args []string) (string, error)

// DelegateAgent runs another agent by name and returns its captured output.
func (T *AppCore) DelegateAgent(name string, args ...string) (string, error) {
	if RunAgentFunc == nil {
		return "", fmt.Errorf("agent delegation is not available")
	}
	return RunAgentFunc(name, args)
}
