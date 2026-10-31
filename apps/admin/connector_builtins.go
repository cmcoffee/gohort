package admin

// gohort's own providers, read as connectors (docs/design/connectors-and-
// flows.md, stage 3). Web search, the browser, mail and the LLM tiers are
// wrappers written in Go rather than specs: the same inside (a contract), an outside they reach, a test. Listing
// them with the other connectors means every capability's choice is among
// connectors, built in or added.
//
// Nothing here is stored as a connector. Each row is read from the settings
// the provider already uses, is changed there, and cannot be approved,
// exported or deleted: it is part of gohort.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/media"
)

// builtinPrefix names a built-in row. A connector's name cannot hold a
// colon, so one can never be taken for the other.
const builtinPrefix = "built-in:"

// builtinConnector is one built-in provider as a connector row.
type builtinConnector struct {
	name    string
	wrapper string // inside ⇐ mapping ⇐ outside, as a service's line reads
	status  string // in use or not, and where it is set up
	// test runs the provider's own settings test on what is stored; nil
	// when there is nothing of its own to test (another provider is set).
	test func(c *jsonapi.Call) (any, error)
}

// builtinConnectors are gohort's own providers, read from their settings.
func (a *AdminApp) builtinConnectors() []builtinConnector {
	return []builtinConnector{a.builtinSearch(), builtinBrowse(), a.builtinMail(),
		a.builtinLLM(LLMTable, "worker-llm", "Worker LLM"), a.builtinLLM(LeadLLMTable, "lead-llm", "Lead LLM")}
}

// llmHosts are where each hosted LLM provider is reached: a tier still naming
// one has no key (one with a key runs on a model connector), and the row says
// so.
var llmHosts = map[string]string{
	"anthropic": "api.anthropic.com", "openai": "api.openai.com", "gemini": "generativelanguage.googleapis.com",
}

// builtinLLM is one of gohort's model tiers, the client for it written in Go
// (core/llm_*.go): what it runs, where, and with what key. A tier on
// Anthropic, OpenAI or Gemini runs on a model connector, which has its own
// row; this one shows the tier with the others, and Test is the tier's own
// live check.
func (a *AdminApp) builtinLLM(table, id, title string) builtinConnector {
	provider, model, endpoint := a.storedString(table, "provider"), a.storedString(table, "model"), a.storedString(table, "endpoint")
	key := a.storedString(table, "api_key")
	b := builtinConnector{name: builtinPrefix + id}
	set := " Set up under Admin > LLMs > " + title + "."
	if provider == "" {
		b.wrapper = title + " ⇐ not set"
		if table == LeadLLMTable {
			b.status = "Not set: the worker LLM answers in its place." + set
		} else {
			b.status = "Off: no model is set, so agents cannot run." + set
		}
		return b
	}
	what := title + " ⇐ " + withModel(provider, model)
	switch {
	case strings.HasPrefix(provider, media.ServiceProviderPrefix):
		b.wrapper = title + " ⇐ " + providerName(provider) + " (its row shows its calls and cost)"
	case strings.HasPrefix(provider, "peer:"):
		b.wrapper = title + " ⇐ " + model + " on " + providerName(provider)
	case provider == "bedrock":
		region := a.storedString(table, "aws_region")
		if region == "" {
			region = "the default region"
		}
		b.wrapper = what + " ⇐ AWS Bedrock in " + region + " (the machine's AWS credentials)"
	case llmHosts[provider] != "" && strings.TrimSpace(endpoint) == "":
		b.wrapper = what + " ⇐ " + endpointHost("https://"+llmHosts[provider], key)
	default:
		b.wrapper = what + " ⇐ " + endpointHost(endpoint, key)
	}
	first, rest, _ := strings.Cut(title, " ")
	b.status = "In use as the " + strings.ToLower(first) + " " + rest + "." + set
	b.test = func(c *jsonapi.Call) (any, error) {
		body, _ := json.Marshal(map[string]any{"provider": provider, "model": model, "endpoint": endpoint,
			"aws_region": a.storedString(table, "aws_region"), "aws_profile": a.storedString(table, "aws_profile"),
			"bedrock_api": a.storedString(table, "bedrock_api")})
		r := httptest.NewRequest("POST", "/api/llm/test", bytes.NewReader(body)).WithContext(c.R.Context())
		r.Header = c.R.Header.Clone()
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.testLLM(w, r, table)
		// The tier's test reports its steps as lines as it goes; the last
		// line is how it ended.
		lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
		var res map[string]any
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &res); err != nil {
			return testResultBody(false, "", strings.TrimSpace(w.Body.String())), nil
		}
		return res, nil
	}
	return b
}

// builtinMail is gohort's own mail: an SMTP server (core/notify.go).
func (a *AdminApp) builtinMail() builtinConnector {
	cfg := MailConfig{Server: a.storedString(MailTable, "server"), From: a.storedString(MailTable, "from"),
		Username: a.storedString(MailTable, "username"), Password: a.storedString(MailTable, "password"),
		Recipient: a.storedString(MailTable, "recipient"), Provider: a.storedString(MailTable, "provider")}
	server := cfg.Server
	if server == "" {
		server = "localhost:25"
	}
	signin := "no sign-in"
	if cfg.Username != "" && cfg.Password != "" {
		signin = "sign-in set"
	}
	b := builtinConnector{name: builtinPrefix + "mail", wrapper: "Mail ⇐ SMTP ⇐ " + server + " (" + signin + ")"}
	set := " Set up under Admin > System > Mail (SMTP)."
	switch {
	case strings.HasPrefix(cfg.Provider, media.ServiceProviderPrefix):
		b.status = "Not in use: mail is sent by " + providerName(cfg.Provider) + "." + set
	case cfg.Server == "" && cfg.From == "":
		b.status = "Off: no server or From address is set, so mail is not sent." + set
	default:
		b.status = "In use for mail." + set
		b.test = func(c *jsonapi.Call) (any, error) { return a.handleMailTest(c, cfg) }
	}
	return b
}

// builtinBrowse is gohort's own page rendering: a headless Chromium on this
// machine (tools/browser), public web only.
func builtinBrowse() builtinConnector {
	src := strings.TrimSpace(LoadBrowseConfig().Source)
	b := builtinConnector{name: builtinPrefix + "page-rendering",
		wrapper: "Page rendering ⇐ a headless Chromium on this machine (public web only)"}
	set := " Set up under Admin > Capabilities > Page Rendering (Browser)."
	if !isLocal(src) {
		b.status = "Not in use: pages are rendered by " + providerName(src) + "." + set
		return b
	}
	b.status = "In use for page rendering." + set
	b.test = func(c *jsonapi.Call) (any, error) {
		if BrowserFetchFunc == nil {
			return testResultBody(false, "", "no browser is linked into this build"), nil
		}
		text, err := BrowserFetchFunc("https://example.com", 2000)
		if err != nil {
			return testResultBody(false, "", err.Error()), nil
		}
		return testResultBody(true, fmt.Sprintf("rendered example.com: %d characters of text", len([]rune(text))), ""), nil
	}
	return b
}

// builtinSearch is gohort's own web search engine, DuckDuckGo, read from the
// page tools/websearch scrapes. Every other engine is a connector, a Source
// Hook or a peer's.
func (a *AdminApp) builtinSearch() builtinConnector {
	provider := a.storedString(SearchTable, "provider")
	if provider == "" {
		provider = "duckduckgo"
	}
	source := a.storedString(SearchTable, "source")
	b := builtinConnector{name: builtinPrefix + "web-search", wrapper: "Web search ⇐ duckduckgo ⇐ lite.duckduckgo.com"}
	set := " Set up under Admin > Capabilities > Web Search."
	switch {
	case !isLocal(source):
		b.status = "Not in use: web search is done by " + providerName(source) + "." + set
	case provider == "hook":
		b.status = "Not in use: web search is done by the Source Hook " + a.storedString(SearchTable, "hook") + "." + set
	case strings.HasPrefix(provider, media.ServiceProviderPrefix):
		b.status = "Not in use: web search is done by " + providerName(provider) + "." + set
	case provider != "duckduckgo":
		b.status = "Not in use: web search is set to " + provider + ", which is not an engine here: choose one." + set
	default:
		b.status = "In use for web search." + set
		b.test = func(c *jsonapi.Call) (any, error) {
			return a.handleWebSearchTest(c, WebSearchConfig{Provider: provider, Source: source})
		}
	}
	return b
}

// builtinTest runs the ?name= built-in's test and says how it went in the
// Connectors table's terms.
func (a *AdminApp) builtinTest(c *jsonapi.Call, name string) (any, error) {
	for _, b := range a.builtinConnectors() {
		if b.name != name {
			continue
		}
		if b.test == nil {
			return map[string]any{"message": b.status, "warnings": []string{b.status}}, nil
		}
		res, err := b.test(c)
		if err != nil {
			return nil, err
		}
		body, _ := res.(map[string]any)
		if ok, _ := body["ok"].(bool); ok {
			msg, _ := body["message"].(string)
			return map[string]any{"message": msg}, nil
		}
		why, _ := body["error"].(string)
		return map[string]any{"message": why, "warnings": []string{why}}, nil
	}
	return nil, jsonapi.NotFound("no built-in connector named " + name)
}

// isLocal reports whether a capability's provider is this instance.
func isLocal(provider string) bool {
	p := strings.TrimSpace(provider)
	return p == "" || p == EmbeddingProviderLocal
}

// providerName says which peer or service a capability uses, for a status.
func providerName(provider string) string {
	if name, ok := strings.CutPrefix(provider, media.ServiceProviderPrefix); ok {
		return "the connector " + name
	}
	if p, ok := PeerFromProvider(provider); ok {
		return "the peer " + p.Name
	}
	return provider
}

// imageChoiceName says what Image Generation is set to.
func imageChoiceName(chosen string) string {
	if chosen == "" || chosen == "none" {
		return "Disabled"
	}
	return "the connector " + chosen
}

// withModel is an endpoint path with the model it asks for, when one is set.
func withModel(path, model string) string {
	if m := strings.TrimSpace(model); m != "" {
		return fmt.Sprintf("%s (model %s)", path, m)
	}
	return path
}

// endpointHost is where a built-in reaches and with what, as a service's line
// says it: the host, and whether a key is set (never the key).
func endpointHost(endpoint, key string) string {
	host := strings.TrimSpace(endpoint)
	if host == "" {
		return "no endpoint set"
	}
	if u, err := url.Parse(host); err == nil && u.Host != "" {
		host = u.Host
	}
	if strings.TrimSpace(key) != "" {
		return host + " (key set)"
	}
	return host + " (no key)"
}
