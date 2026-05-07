package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"

	"github.com/cmcoffee/snugforge/nfo"
)

// browseModels queries the provider's API for available models and presents
// a selection menu. Sets *target to the chosen model name.
func browseModels(provider, apiKey, endpoint string, target *string) bool {
	var models []string
	var err error

	switch provider {
	case "ollama":
		ep := endpoint
		if ep == "" {
			ep = "http://localhost:11434"
		}
		models, err = OllamaModels(ep)
	case "llama.cpp":
		ep := endpoint
		if ep == "" {
			ep = "http://localhost:8080/v1"
		}
		models, err = LlamaCppModels(ep)
	case "openai":
		if apiKey == "" {
			Stderr("\n  Set API Key first.\n")
			return false
		}
		models, err = OpenAIModels(apiKey)
	case "gemini":
		if apiKey == "" {
			Stderr("\n  Set API Key first.\n")
			return false
		}
		models, err = GeminiModels(apiKey)
	default:
		Stderr("\n  Model browsing not available for %s.\n", provider)
		return false
	}

	if err != nil {
		Stderr("\n  Failed to query %s: %s\n", provider, err)
		return false
	}
	if len(models) == 0 {
		Stderr("\n  No models found.\n")
		return false
	}

	sel := NewOptions(" [Select Model] ", "(selection or 'q' to cancel)", 'q')
	for _, m := range models {
		name := m
		sel.Func(name, func() bool {
			*target = name
			return true
		})
	}
	sel.Select(false)
	return true
}

// dbCFG is a zero-value accessor for all persistent configuration stored in
// global.db. Callers use the package-level dbcfg variable; no initialization
// required. Adding a new config field means one new method here, not a new
// standalone function scattered across the file.
type dbCFG struct{}

var dbcfg dbCFG

func (d dbCFG) llm() LLMProviderConfig {
	var c LLMProviderConfig
	global.db.Get(LLMTable, "provider", &c.Provider)
	global.db.Get(LLMTable, "model", &c.Model)
	global.db.Get(LLMTable, "api_key", &c.APIKey)
	global.db.Get(LLMTable, "endpoint", &c.Endpoint)
	global.db.Get(LLMTable, "context_size", &c.ContextSize)
	global.db.Get(LLMTable, "disable_thinking", &c.DisableThinking)
	global.db.Get(LLMTable, "thinking_budget", &c.ThinkingBudget)
	global.db.Get(LLMTable, "native_tools", &c.NativeTools)
	global.db.Get(LLMTable, "ollama_max_parallel", &c.OllamaMaxParallel)
	global.db.Get(LLMTable, "llamacpp_max_parallel", &c.LlamacppMaxParallel)
	// No-think signals: load each toggle individually. If the operator
	// has never configured no-think (no_think_configured key absent),
	// fall back to defaults that match the empirically-proven combo —
	// kwarg + budget on, prepends off. Without this fallback, fresh
	// installs would send no signals at all on no-think calls.
	var noThinkConfigured bool
	global.db.Get(LLMTable, "no_think_configured", &noThinkConfigured)
	if noThinkConfigured {
		global.db.Get(LLMTable, "no_think_use_kwarg", &c.NoThinkUseKwarg)
		global.db.Get(LLMTable, "no_think_send_budget", &c.NoThinkSendBudget)
		global.db.Get(LLMTable, "no_think_prepend_system", &c.NoThinkPrependSystem)
		global.db.Get(LLMTable, "no_think_prepend_user", &c.NoThinkPrependUser)
	} else {
		c.NoThinkUseKwarg = true
		c.NoThinkSendBudget = true
	}
	global.db.Get(LLMTable, "no_think_budget", &c.NoThinkBudget)
	var timeout_seconds int
	global.db.Get(LLMTable, "request_timeout_seconds", &timeout_seconds)
	if timeout_seconds > 0 {
		c.RequestTimeout = time.Duration(timeout_seconds) * time.Second
	}
	return c
}

func (d dbCFG) leadLLM() LLMProviderConfig {
	var c LLMProviderConfig
	global.db.Get(LeadLLMTable, "provider", &c.Provider)
	global.db.Get(LeadLLMTable, "model", &c.Model)
	global.db.Get(LeadLLMTable, "api_key", &c.APIKey)
	global.db.Get(LeadLLMTable, "endpoint", &c.Endpoint)
	global.db.Get(LeadLLMTable, "disable_thinking", &c.DisableThinking)
	global.db.Get(LeadLLMTable, "thinking_budget", &c.ThinkingBudget)
	global.db.Get(LeadLLMTable, "native_tools", &c.NativeTools)
	global.db.Get(LLMTable, "ollama_max_parallel", &c.OllamaMaxParallel)
	global.db.Get(LLMTable, "llamacpp_max_parallel", &c.LlamacppMaxParallel)
	var leadNoThinkConfigured bool
	global.db.Get(LeadLLMTable, "no_think_configured", &leadNoThinkConfigured)
	if leadNoThinkConfigured {
		global.db.Get(LeadLLMTable, "no_think_use_kwarg", &c.NoThinkUseKwarg)
		global.db.Get(LeadLLMTable, "no_think_send_budget", &c.NoThinkSendBudget)
		global.db.Get(LeadLLMTable, "no_think_prepend_system", &c.NoThinkPrependSystem)
		global.db.Get(LeadLLMTable, "no_think_prepend_user", &c.NoThinkPrependUser)
	} else {
		c.NoThinkUseKwarg = true
		c.NoThinkSendBudget = true
	}
	global.db.Get(LeadLLMTable, "no_think_budget", &c.NoThinkBudget)
	return c
}

func (d dbCFG) mail() MailConfig {
	var c MailConfig
	global.db.Get(MailTable, "server", &c.Server)
	global.db.Get(MailTable, "from", &c.From)
	global.db.Get(MailTable, "username", &c.Username)
	global.db.Get(MailTable, "password", &c.Password)
	global.db.Get(MailTable, "recipient", &c.Recipient)
	return c
}

func (d dbCFG) search() WebSearchConfig {
	var c WebSearchConfig
	global.db.Get(SearchTable, "provider", &c.Provider)
	global.db.Get(SearchTable, "api_key", &c.APIKey)
	global.db.Get(SearchTable, "endpoint", &c.Endpoint)
	return c
}

func (d dbCFG) imageProvider() string {
	var provider string
	global.db.Get(ImageTable, "provider", &provider)
	if provider == "" {
		return "gemini"
	}
	return provider
}

func (d dbCFG) imageGenProfile(name string) ImageGenProfile {
	var p ImageGenProfile
	global.db.Get(ImageTable, name+"_provider", &p.Provider)
	global.db.Get(ImageTable, name+"_api_key", &p.APIKey)
	return p
}

func (d dbCFG) geminiAPIKey() string {
	var imageKey string
	global.db.Get(ImageTable, "api_key", &imageKey)
	if imageKey != "" {
		return imageKey
	}
	c := d.llm()
	if c.Provider == "gemini" && c.APIKey != "" {
		return c.APIKey
	}
	lead := d.leadLLM()
	if lead.Provider == "gemini" && lead.APIKey != "" {
		return lead.APIKey
	}
	return ""
}

func (d dbCFG) openAIAPIKey() string {
	var imageKey string
	global.db.Get(ImageTable, "api_key", &imageKey)
	if imageKey != "" {
		return imageKey
	}
	c := d.llm()
	if c.Provider == "openai" && c.APIKey != "" {
		return c.APIKey
	}
	lead := d.leadLLM()
	if lead.Provider == "openai" && lead.APIKey != "" {
		return lead.APIKey
	}
	return ""
}

// setup_fuzz runs the interactive configuration.
func setup_fuzz() {
	init_database()

	// Load current LLM values.
	var provider, model, apiKey, endpoint string
	var contextSize int
	var requestTimeoutSec int
	var disableThinking bool
	var thinkingBudget int
	var nativeTools bool
	var ollamaMaxParallel int
	var llamacppMaxParallel int
	global.db.Get(LLMTable, "provider", &provider)
	global.db.Get(LLMTable, "model", &model)
	global.db.Get(LLMTable, "api_key", &apiKey)
	global.db.Get(LLMTable, "endpoint", &endpoint)
	global.db.Get(LLMTable, "context_size", &contextSize)
	global.db.Get(LLMTable, "request_timeout_seconds", &requestTimeoutSec)
	global.db.Get(LLMTable, "disable_thinking", &disableThinking)
	global.db.Get(LLMTable, "thinking_budget", &thinkingBudget)
	global.db.Get(LLMTable, "native_tools", &nativeTools)
	global.db.Get(LLMTable, "ollama_max_parallel", &ollamaMaxParallel)
	if ollamaMaxParallel < 1 {
		ollamaMaxParallel = 1 // default: strict serial execution through Ollama
	}
	global.db.Get(LLMTable, "llamacpp_max_parallel", &llamacppMaxParallel)
	if llamacppMaxParallel < 1 {
		llamacppMaxParallel = 1 // default: strict serial execution through llama.cpp
	}
	// No-think individual signal toggles + budget. Defaults are the
	// proven-working combo (kwarg + budget on, prepends off).
	var noThinkConfigured bool
	noThinkUseKwarg := true
	noThinkSendBudget := true
	var noThinkPrependSystem bool
	var noThinkPrependUser bool
	var noThinkBudget int
	global.db.Get(LLMTable, "no_think_configured", &noThinkConfigured)
	if noThinkConfigured {
		// Once explicitly configured, read all four toggles literally
		// (so an operator can disable kwarg or budget if they need to).
		noThinkUseKwarg, noThinkSendBudget = false, false
		global.db.Get(LLMTable, "no_think_use_kwarg", &noThinkUseKwarg)
		global.db.Get(LLMTable, "no_think_send_budget", &noThinkSendBudget)
		global.db.Get(LLMTable, "no_think_prepend_system", &noThinkPrependSystem)
		global.db.Get(LLMTable, "no_think_prepend_user", &noThinkPrependUser)
	}
	global.db.Get(LLMTable, "no_think_budget", &noThinkBudget)

	// Load current Lead LLM values.
	var leadProvider, leadModel, leadAPIKey, leadEndpoint string
	var leadDisableThinking bool
	var leadThinkingBudget int
	var leadNativeTools bool
	global.db.Get(LeadLLMTable, "provider", &leadProvider)
	global.db.Get(LeadLLMTable, "model", &leadModel)
	global.db.Get(LeadLLMTable, "api_key", &leadAPIKey)
	global.db.Get(LeadLLMTable, "endpoint", &leadEndpoint)
	global.db.Get(LeadLLMTable, "disable_thinking", &leadDisableThinking)
	global.db.Get(LeadLLMTable, "thinking_budget", &leadThinkingBudget)
	global.db.Get(LeadLLMTable, "native_tools", &leadNativeTools)
	var leadNoThinkConfigured bool
	leadNoThinkUseKwarg := true
	leadNoThinkSendBudget := true
	var leadNoThinkPrependSystem bool
	var leadNoThinkPrependUser bool
	var leadNoThinkBudget int
	global.db.Get(LeadLLMTable, "no_think_configured", &leadNoThinkConfigured)
	if leadNoThinkConfigured {
		leadNoThinkUseKwarg, leadNoThinkSendBudget = false, false
		global.db.Get(LeadLLMTable, "no_think_use_kwarg", &leadNoThinkUseKwarg)
		global.db.Get(LeadLLMTable, "no_think_send_budget", &leadNoThinkSendBudget)
		global.db.Get(LeadLLMTable, "no_think_prepend_system", &leadNoThinkPrependSystem)
		global.db.Get(LeadLLMTable, "no_think_prepend_user", &leadNoThinkPrependUser)
	}
	global.db.Get(LeadLLMTable, "no_think_budget", &leadNoThinkBudget)
	if leadProvider == "" {
		leadProvider = "(use primary)"
	}

	// Load current mail values.
	var mailServer, mailFrom, mailUser, mailPass, mailRecipient string
	global.db.Get(MailTable, "server", &mailServer)
	global.db.Get(MailTable, "from", &mailFrom)
	global.db.Get(MailTable, "username", &mailUser)
	global.db.Get(MailTable, "password", &mailPass)
	global.db.Get(MailTable, "recipient", &mailRecipient)

	setup := NewOptions("--- Gohort Configuration ---", "(selection or 'q' to save & exit)", 'q')

	// LLM settings.
	llm := NewOptions(" [LLM Provider Settings] ", "(selection or 'q' to return to previous)", 'q')
	llm.StringSelectVar(&provider, "LLM Provider", provider, "anthropic", "openai", "gemini", "ollama", "llama.cpp")
	llm.SecretVar(&apiKey, "API Key", apiKey, NONE)
	llm.ShowWhen(func() bool { return provider != "ollama" && provider != "llama.cpp" })
	llm.StringVar(&model, "Model", model, "LLM model name (leave blank for provider default).")
	llm.Func("Browse Available Models", func() bool {
		return browseModels(provider, apiKey, endpoint, &model)
	})
	llm.StringVar(&endpoint, "Endpoint", endpoint, "Custom API endpoint (leave blank for default).")
	llm.ShowWhen(func() bool { return provider == "ollama" || provider == "llama.cpp" })
	llm.IntVar(&contextSize, "Context Size", contextSize, "Context window size in tokens (0=default 65K). For llama.cpp: must match the --ctx-size the server was started with.", 0, 262144)
	llm.ShowWhen(func() bool { return provider == "ollama" || provider == "llama.cpp" })
	llm.IntVar(&requestTimeoutSec, "Request Timeout (seconds)", requestTimeoutSec, "Max wait for first response header. 0=default 300s. Bump for slow local models or huge prompts.", 0, 3600)
	llm.ToggleVar(&disableThinking, "Disable Thinking (force think=false on every call)", disableThinking)
	llm.ShowWhen(func() bool { return provider == "ollama" || provider == "gemini" || provider == "llama.cpp" })
	llm.IntVar(&thinkingBudget, "Thinking Budget (tokens, 0=unlimited)", thinkingBudget, "Max tokens the model may spend thinking per call. 0 = unlimited. Lower values reduce latency on simple queries. Only supported by llama.cpp and Gemini.", 0, 131072)
	llm.ShowWhen(func() bool { return (provider == "gemini" || provider == "llama.cpp") && !disableThinking })
	// No-Think Settings: dedicated sub-menu so the parent LLM section
	// stays focused on connection/model basics. Each signal toggles
	// independently — operators tune for whichever combo their model
	// honors reliably.
	noThinkSettings := NewOptions(" [No-Think Settings] ", "(selection or 'q' to return)", 'q')
	noThinkSettings.ToggleVar(&noThinkUseKwarg, "Send chat_template_kwargs.enable_thinking=false", noThinkUseKwarg)
	noThinkSettings.ToggleVar(&noThinkSendBudget, "Send thinking_budget_tokens cap", noThinkSendBudget)
	noThinkSettings.IntVar(&noThinkBudget, "Budget value (tokens)", noThinkBudget, "thinking_budget_tokens when 'send budget' is on. 0 = built-in default (512).", 0, 8192)
	noThinkSettings.ShowWhen(func() bool { return noThinkSendBudget })
	noThinkSettings.ToggleVar(&noThinkPrependSystem, "Prepend /no_think to system prompt", noThinkPrependSystem)
	noThinkSettings.ToggleVar(&noThinkPrependUser, "Prepend /no_think to last user message", noThinkPrependUser)
	llm.Options("No-Think Settings", noThinkSettings, false)
	llm.ShowWhen(func() bool { return provider == "llama.cpp" && !disableThinking })
	llm.ToggleVar(&nativeTools, "Native Tool Calling (disable for models without tool support)", nativeTools)
	llm.ShowWhen(func() bool { return provider == "ollama" })
	llm.IntVar(&ollamaMaxParallel, "Max Parallel Ollama Requests", ollamaMaxParallel, "How many concurrent requests Ollama will process. Default 1 (strict serial). Raise only if the host GPU can truly run more in parallel. Requests are fair-queued across caller sessions.", 1, 16)
	llm.ShowWhen(func() bool { return provider == "ollama" })
	llm.IntVar(&llamacppMaxParallel, "Max Parallel llama.cpp Requests", llamacppMaxParallel, "How many concurrent requests llama.cpp will process. Default 1 (single-threaded). Raise only if your server supports concurrent requests. Requests are fair-queued across caller sessions.", 1, 16)
	llm.ShowWhen(func() bool { return provider == "llama.cpp" })
	// Precision LLM settings (judge/fact-checker — falls back to primary if not set).
	lead := NewOptions(" [Precision LLM (Secondary)] ", "(selection or 'q' to return to previous)", 'q')
	lead.StringSelectVar(&leadProvider, "LLM Provider", leadProvider, "(use primary)", "anthropic", "openai", "gemini", "ollama", "llama.cpp")
	lead.SecretVar(&leadAPIKey, "API Key", leadAPIKey, "API key (leave blank to use primary).")
	lead.ShowWhen(func() bool { return leadProvider != "ollama" && leadProvider != "(use primary)" })
	lead.StringVar(&leadModel, "Model", leadModel, "Model name (leave blank for provider default).")
	lead.ShowWhen(func() bool { return leadProvider != "(use primary)" })
	lead.Func("Browse Available Models", func() bool {
		key := leadAPIKey
		if key == "" {
			key = apiKey
		}
		return browseModels(leadProvider, key, leadEndpoint, &leadModel)
	})
	lead.ShowWhen(func() bool { return leadProvider != "(use primary)" })
	lead.StringVar(&leadEndpoint, "Endpoint", leadEndpoint, "Custom API endpoint (leave blank for default).")
	lead.ShowWhen(func() bool { return leadProvider == "ollama" || leadProvider == "llama.cpp" })
	lead.ToggleVar(&leadDisableThinking, "Disable Thinking (force think=false on every call)", leadDisableThinking)
	lead.ShowWhen(func() bool { return leadProvider == "ollama" || leadProvider == "gemini" || leadProvider == "llama.cpp" })
	lead.IntVar(&leadThinkingBudget, "Thinking Budget (tokens, 0=unlimited)", leadThinkingBudget, "Max tokens the model may spend thinking per call. 0 = unlimited. Only supported by llama.cpp and Gemini.", 0, 131072)
	lead.ShowWhen(func() bool { return (leadProvider == "gemini" || leadProvider == "llama.cpp") && !leadDisableThinking })
	leadNoThinkSettings := NewOptions(" [No-Think Settings (Lead)] ", "(selection or 'q' to return)", 'q')
	leadNoThinkSettings.ToggleVar(&leadNoThinkUseKwarg, "Send chat_template_kwargs.enable_thinking=false", leadNoThinkUseKwarg)
	leadNoThinkSettings.ToggleVar(&leadNoThinkSendBudget, "Send thinking_budget_tokens cap", leadNoThinkSendBudget)
	leadNoThinkSettings.IntVar(&leadNoThinkBudget, "Budget value (tokens)", leadNoThinkBudget, "thinking_budget_tokens when 'send budget' is on. 0 = built-in default (512).", 0, 8192)
	leadNoThinkSettings.ShowWhen(func() bool { return leadNoThinkSendBudget })
	leadNoThinkSettings.ToggleVar(&leadNoThinkPrependSystem, "Prepend /no_think to system prompt", leadNoThinkPrependSystem)
	leadNoThinkSettings.ToggleVar(&leadNoThinkPrependUser, "Prepend /no_think to last user message", leadNoThinkPrependUser)
	lead.Options("No-Think Settings", leadNoThinkSettings, false)
	lead.ShowWhen(func() bool { return leadProvider == "llama.cpp" && !leadDisableThinking })
	lead.ToggleVar(&leadNativeTools, "Native Tool Calling (disable for models without tool support)", leadNativeTools)
	lead.ShowWhen(func() bool { return leadProvider == "ollama" })

	// LLM Routing settings — built dynamically from registered route stages.
	stages := ListRouteStages()
	routeVals := make([]string, len(stages))
	for i, s := range stages {
		global.db.Get(RoutingTable, s.Key, &routeVals[i])
		if routeVals[i] == "" {
			routeVals[i] = "lead"
		}
	}

	// Image Generation settings.
	var imageProvider, imageAPIKey string
	global.db.Get(ImageTable, "provider", &imageProvider)
	global.db.Get(ImageTable, "api_key", &imageAPIKey)
	if imageProvider == "" {
		imageProvider = "gemini"
	}

	imagegen := NewOptions(" [Image Generation] ", "(selection or 'q' to return to previous)", 'q')
	imagegen.StringSelectVar(&imageProvider, "Provider", imageProvider, "gemini", "openai", "none")
	imagegen.SecretVar(&imageAPIKey, "API Key", imageAPIKey, "API key for image generation. Leave blank to reuse the matching LLM provider key.")
	imagegen.ShowWhen(func() bool { return imageProvider != "none" })

	// Group all LLM settings under one menu.
	llmSettings := NewOptions(" [LLM Settings] ", "(selection or 'q' to return to previous)", 'q')
	llmSettings.Options("Primary Provider", llm, false)
	llmSettings.Options("Precision LLM (Secondary)", lead, false)
	if len(stages) > 0 {
		routing := NewOptions(" [LLM Routing] ", "(selection or 'q' to return to previous)", 'q')
		for i, s := range stages {
			routing.StringSelectVar(&routeVals[i], s.Label, routeVals[i], "lead", "worker", "worker (thinking)")
		}
		llmSettings.Options("Routing (worker = local, lead = remote)", routing, false)
	}
	llmSettings.Options("Image Generation", imagegen, false)
	setup.Options("LLM Settings", llmSettings, false)

	// --- External Sources ---
	var searchProvider, searchAPIKey, searchEndpoint string
	global.db.Get(SearchTable, "provider", &searchProvider)
	global.db.Get(SearchTable, "api_key", &searchAPIKey)
	global.db.Get(SearchTable, "endpoint", &searchEndpoint)

	search := NewOptions(" [Web Search] ", "(selection or 'q' to return to previous)", 'q')
	search.StringSelectVar(&searchProvider, "Search Provider", searchProvider, "duckduckgo", "brave", "google", "serper", "searxng")
	search.SecretVar(&searchAPIKey, "API Key", searchAPIKey, "API key for search provider.")
	search.ShowWhen(func() bool { return searchProvider != "duckduckgo" && searchProvider != "searxng" })
	search.StringVar(&searchEndpoint, "Endpoint", searchEndpoint, "Custom endpoint (e.g. https://search.example.com).")
	search.ShowWhen(func() bool { return searchProvider == "searxng" })

	LoadSourceHooks(global.db)
	hooks := NewOptions(" [Source Hooks] ", "(selection or 'q' to return to previous)", 'q')
	hooks.Func("List Configured Hooks", func() bool {
		list_source_hooks()
		return true
	})
	hooks.Func("Quick Add (Templates)", func() bool {
		add_template_hook()
		return true
	})
	hooks.Func("Add Custom Hook", func() bool {
		add_source_hook()
		return true
	})
	hooks.Func("Update Hook Triggers", func() bool {
		update_hook_triggers()
		return true
	})
	hooks.Func("Remove Source Hook", func() bool {
		remove_source_hook()
		return true
	})

	// Mail settings.
	mail := NewOptions(" [Mail Settings] ", "(selection or 'q' to return to previous)", 'q')
	mail.StringVar(&mailServer, "SMTP Server", mailServer, "SMTP server address, e.g. smtp.gmail.com:587 (leave blank for localhost:25).")
	mail.StringVar(&mailFrom, "From Address", mailFrom, "Sender email address (leave blank for fuzz@hostname).")
	mail.StringVar(&mailRecipient, "Default Recipient", mailRecipient, "Default report recipient email address (used when no --to is specified).")
	mail.StringVar(&mailUser, "SMTP Username", mailUser, "SMTP auth username (leave blank if not required).")
	mail.SecretVar(&mailPass, "SMTP Password", mailPass, NONE)
	mail.Func("Send Test Email", func() bool {
		to := mailRecipient
		if to == "" {
			to = GetInput("Recipient email: ")
			if to == "" {
				return true
			}
		}
		// Build a temporary config from the current (possibly unsaved) values.
		cfg := MailConfig{
			Server:   mailServer,
			From:     mailFrom,
			Username: mailUser,
			Password: mailPass,
		}
		// Temporarily override the config loader so SendNotification uses
		// the in-progress values rather than what's saved in the DB.
		orig := LoadMailConfigFunc
		LoadMailConfigFunc = func() MailConfig { return cfg }
		err := SendNotification(to, "Gohort Test Email", "This is a test email from Gohort.\n\nIf you received this, mail is configured correctly.\n")
		LoadMailConfigFunc = orig
		if err != nil {
			Stderr("\n  Failed: %s\n\n", err)
		} else {
			Stdout("\n  Test email sent to %s.\n\n", to)
		}
		return true
	})

	services := NewOptions(" [External Services] ", "(selection or 'q' to return to previous)", 'q')
	services.Options("Web Search", search, false)
	services.Options("Source Hooks (API, RAG, Paywall)", hooks, false)
	services.Options("Mail (SMTP)", mail, false)
	setup.Options("External Services", services, false)

	// Web Server Settings.
	var webAddr, webTLSCert, webTLSKey string
	var webTLSSelfSigned bool
	var webMaxConcurrent int
	var webAdminIPs string
	var webAdminUser, webAdminPass string
	var webAllowSignup bool
	var webSessionDays int
	var webAPIKey string
	var webExternalURL string
	var webServiceName string
	var webMaxLoginAttempts int
	var webLockoutMinutes int
	global.db.Get(WebTable, "addr", &webAddr)
	global.db.Get(WebTable, "tls_cert", &webTLSCert)
	global.db.Get(WebTable, "tls_key", &webTLSKey)
	global.db.Get(WebTable, "tls_self_signed", &webTLSSelfSigned)
	global.db.Get(WebTable, "max_concurrent", &webMaxConcurrent)
	global.db.Get(WebTable, "admin_allowed_ips", &webAdminIPs)
	global.db.Get(WebTable, "allow_signup", &webAllowSignup)
	global.db.Get(WebTable, "session_days", &webSessionDays)
	global.db.Get(WebTable, "api_key", &webAPIKey)
	global.db.Get(WebTable, "external_url", &webExternalURL)
	global.db.Get(WebTable, "service_name", &webServiceName)
	global.db.Get(WebTable, "max_login_attempts", &webMaxLoginAttempts)
	global.db.Get(WebTable, "lockout_minutes", &webLockoutMinutes)
	if webMaxLoginAttempts == 0 {
		webMaxLoginAttempts = 5
	}
	if webLockoutMinutes == 0 {
		webLockoutMinutes = 15
	}
	var webNotifyFrom string
	global.db.Get(WebTable, "notify_from", &webNotifyFrom)
	if webSessionDays == 0 {
		webSessionDays = 7
	}
	if webMaxConcurrent == 0 {
		webMaxConcurrent = 1
	}

	// Pre-fill admin email and password indicator from existing admin user.
	for _, u := range AuthListUsers(global.db) {
		if u.Admin {
			webAdminUser = u.Username
			if u.PassHash != "" {
				webAdminPass = "(configured)"
			}
			break
		}
	}

	webmenu := NewOptions(" [Web Server Settings] ", "(selection or 'q' to return to previous)", 'q')
	webmenu.StringVar(&webAddr, "Listen Address", webAddr, "Address for web dashboard (e.g. ':8080', '0.0.0.0:443').")
	webmenu.IntVar(&webMaxConcurrent, "Max Concurrent Tasks", webMaxConcurrent, "Max simultaneous apps. Others are queued.", 1, 32)
	webmenu.ToggleVar(&webTLSSelfSigned, "TLS (self-signed auto-certificate)", webTLSSelfSigned)
	webmenu.StringVar(&webTLSCert, "TLS Certificate", webTLSCert, "Path to PEM certificate file (overrides self-signed).")
	webmenu.ShowWhen(func() bool { return !webTLSSelfSigned })
	webmenu.StringVar(&webTLSKey, "TLS Key", webTLSKey, "Path to PEM private key file.")
	webmenu.ShowWhen(func() bool { return !webTLSSelfSigned && webTLSCert != "" })
	webmenu.StringVar(&webAdminUser, "Administrator Email", webAdminUser, "Login email for the web admin account.")
	webmenu.SecretVar(&webAdminPass, "Administrator Password", webAdminPass, "Password for the web admin account (leave blank to keep current).")
	webmenu.ShowWhen(func() bool { return webAdminUser != "" })
	webmenu.IntVar(&webSessionDays, "Session Length (days)", webSessionDays, "How long login sessions last before requiring re-authentication.", 1, 90)
	webmenu.IntVar(&webMaxLoginAttempts, "Max Login Attempts", webMaxLoginAttempts, "Failed login attempts before IP lockout.", 1, 100)
	webmenu.IntVar(&webLockoutMinutes, "Lockout Duration (minutes)", webLockoutMinutes, "How long an IP is locked out after max failed attempts.", 1, 1440)
	webmenu.ToggleVar(&webAllowSignup, "Allow New User Signup", webAllowSignup)
	webmenu.StringVar(&webAdminIPs, "Admin Allowed IPs", webAdminIPs, "Comma-separated CIDR/IP allowlist for /admin (empty = no IP restriction).")
	webmenu.SecretVar(&webAPIKey, "API Key", webAPIKey, "Shared key for machine-to-machine access (e.g. third-party API calls). Pass as ?key= query param to bypass login.")
	webmenu.StringVar(&webExternalURL, "External URL", webExternalURL, "Public-facing URL for email notification links (e.g. https://gohort.example.com). Leave blank to use listen address.")
	webmenu.StringVar(&webServiceName, "Service Name", webServiceName, "Name used in notification email subjects (default: Gohort).")
	webmenu.StringVar(&webNotifyFrom, "Notification From Address", webNotifyFrom, "From address for notification emails (default: uses mail config).")

	authmenu, _ := BuildAuthSetup(global.db)
	webmenu.Options("Manage Additional Users", authmenu, false)

	setup.Options("Web Server Settings", webmenu, false)

	// --- Cost Rates (per-run usage telemetry) ---
	// Cost rates are entered as decimal dollar amounts. Stored via
	// SaveCostRatesToDB below. A record's presence in the DB means the
	// operator has been through setup at least once; absence means
	// "never configured." $0.00 is a legitimate rate (free local worker)
	// distinct from "unconfigured."
	var stored_rates CostRates
	rates_exist := global.db.Get("cost_rates", "current", &stored_rates)
	render_rate := func(v float64) string {
		if !rates_exist {
			return ""
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	cost_worker_in := render_rate(stored_rates.WorkerInputPer1K)
	cost_worker_out := render_rate(stored_rates.WorkerOutputPer1K)
	cost_lead_in := render_rate(stored_rates.LeadInputPer1K)
	cost_lead_out := render_rate(stored_rates.LeadOutputPer1K)
	cost_search := render_rate(stored_rates.SearchPerCall)
	cost_image := render_rate(stored_rates.ImagePerCall)

	costs := NewOptions(" [Cost Rates] ", "(selection or 'q' to return to previous)", 'q')
	costs.StringVar(&cost_worker_in, "Worker Input $/1K tokens", cost_worker_in, "Dollar cost per 1,000 input tokens for the worker (primary) LLM. Example Gemini Flash 2.5: 0.075")
	costs.StringVar(&cost_worker_out, "Worker Output $/1K tokens", cost_worker_out, "Dollar cost per 1,000 output tokens for the worker LLM. Example Gemini Flash 2.5: 0.30")
	costs.StringVar(&cost_lead_in, "Lead Input $/1K tokens", cost_lead_in, "Dollar cost per 1,000 input tokens for the lead (precision) LLM. Example Claude Sonnet 4.6: 3.00")
	costs.StringVar(&cost_lead_out, "Lead Output $/1K tokens", cost_lead_out, "Dollar cost per 1,000 output tokens for the lead LLM. Example Claude Sonnet 4.6: 15.00")
	costs.StringVar(&cost_search, "Search $/call", cost_search, "Dollar cost per external search-API call. Example Serper: 0.0003")
	costs.StringVar(&cost_image, "Image $/call", cost_image, "Dollar cost per image generation call. Example DALL-E 3 1792x1024 standard: 0.08; Gemini Imagen 16:9: 0.03")
	setup.Options("Cost Rates (per-run telemetry)", costs, false)

	// --- Embeddings (vector-DB ingestion + semantic chat search) ---
	var storedEmbed EmbeddingConfig
	global.db.Get(EmbeddingTable, "current", &storedEmbed)
	embedEnabled := "no"
	if storedEmbed.Enabled {
		embedEnabled = "yes"
	}
	embedEndpoint := storedEmbed.Endpoint
	if embedEndpoint == "" {
		embedEndpoint = endpoint // default to worker LLM's endpoint
	}
	embedModel := storedEmbed.Model
	if embedModel == "" {
		embedModel = "nomic-embed-text"
	}

	embedOpts := NewOptions(" [Embeddings] ", "(selection or 'q' to return to previous)", 'q')
	embedOpts.StringSelectVar(&embedEnabled, "Enable embeddings", embedEnabled, "yes", "no")
	embedOpts.StringVar(&embedEndpoint, "Embedding endpoint", embedEndpoint, "Base URL of the ollama-compatible /api/embed server. Typically the same host as the worker LLM (e.g. http://localhost:11434).")
	embedOpts.StringVar(&embedModel, "Embedding model", embedModel, "Model name the endpoint should load. Default nomic-embed-text (run `ollama pull nomic-embed-text` on the server first). Alternatives: mxbai-embed-large, all-minilm.")
	setup.Options("Embeddings (vector-DB semantic search)", embedOpts, false)

	// --- Network Settings ---
	var netConnectSec, netRequestSec int
	global.db.Get(NetworkTable, "connect_timeout_seconds", &netConnectSec)
	global.db.Get(NetworkTable, "request_timeout_seconds", &netRequestSec)
	if netConnectSec <= 0 {
		netConnectSec = 10
	}
	if netRequestSec <= 0 {
		netRequestSec = 15
	}
	netmenu := NewOptions(" [Network Settings] ", "(selection or 'q' to return to previous)", 'q')
	netmenu.IntVar(&netConnectSec, "Connect Timeout (seconds)", netConnectSec, "TCP+TLS connection timeout for outbound HTTP calls (source hooks, search APIs). Default: 10.", 1, 120)
	netmenu.IntVar(&netRequestSec, "Request Timeout (seconds)", netRequestSec, "Per-read I/O timeout for HTTP response bodies. Default: 15.", 1, 300)
	setup.Options("Network Settings", netmenu, false)

	// App-contributed setup sections.
	app_sections := RegisteredSetupSections()
	if len(app_sections) > 0 {
		setup.Separator()
		for _, section := range app_sections {
			opts := section.Build(global.db)
			setup.Options(section.Name, opts, false)
		}
	}

	setup.Select(false)

	// Save app-contributed sections.
	for _, section := range app_sections {
		section.Save(global.db)
	}

	// Save LLM configuration.
	global.db.Set(LLMTable, "provider", provider)
	global.db.Set(LLMTable, "model", model)
	global.db.Set(LLMTable, "endpoint", endpoint)
	global.db.Set(LLMTable, "context_size", contextSize)
	global.db.Set(LLMTable, "request_timeout_seconds", requestTimeoutSec)
	global.db.Set(LLMTable, "disable_thinking", disableThinking)
	global.db.Set(LLMTable, "thinking_budget", thinkingBudget)
	global.db.Set(LLMTable, "native_tools", nativeTools)
	global.db.Set(LLMTable, "ollama_max_parallel", ollamaMaxParallel)
	global.db.Set(LLMTable, "llamacpp_max_parallel", llamacppMaxParallel)
	global.db.Set(LLMTable, "no_think_configured", true)
	global.db.Set(LLMTable, "no_think_use_kwarg", noThinkUseKwarg)
	global.db.Set(LLMTable, "no_think_send_budget", noThinkSendBudget)
	global.db.Set(LLMTable, "no_think_prepend_system", noThinkPrependSystem)
	global.db.Set(LLMTable, "no_think_prepend_user", noThinkPrependUser)
	global.db.Set(LLMTable, "no_think_budget", noThinkBudget)
	if apiKey != "" {
		global.db.CryptSet(LLMTable, "api_key", apiKey)
	}

	// Save Lead LLM configuration.
	if leadProvider == "(use primary)" {
		leadProvider = ""
	}
	global.db.Set(LeadLLMTable, "provider", leadProvider)
	global.db.Set(LeadLLMTable, "model", leadModel)
	global.db.Set(LeadLLMTable, "endpoint", leadEndpoint)
	global.db.Set(LeadLLMTable, "disable_thinking", leadDisableThinking)
	global.db.Set(LeadLLMTable, "thinking_budget", leadThinkingBudget)
	global.db.Set(LeadLLMTable, "native_tools", leadNativeTools)
	global.db.Set(LeadLLMTable, "no_think_configured", true)
	global.db.Set(LeadLLMTable, "no_think_use_kwarg", leadNoThinkUseKwarg)
	global.db.Set(LeadLLMTable, "no_think_send_budget", leadNoThinkSendBudget)
	global.db.Set(LeadLLMTable, "no_think_prepend_system", leadNoThinkPrependSystem)
	global.db.Set(LeadLLMTable, "no_think_prepend_user", leadNoThinkPrependUser)
	global.db.Set(LeadLLMTable, "no_think_budget", leadNoThinkBudget)
	if leadAPIKey != "" {
		global.db.CryptSet(LeadLLMTable, "api_key", leadAPIKey)
	}

	// Save mail configuration.
	global.db.Set(MailTable, "server", mailServer)
	global.db.Set(MailTable, "from", mailFrom)
	global.db.Set(MailTable, "recipient", mailRecipient)
	global.db.Set(MailTable, "username", mailUser)
	if mailPass != "" {
		global.db.CryptSet(MailTable, "password", mailPass)
	}

	// Save search configuration.
	global.db.Set(SearchTable, "provider", searchProvider)
	global.db.Set(SearchTable, "endpoint", searchEndpoint)
	if searchAPIKey != "" {
		global.db.CryptSet(SearchTable, "api_key", searchAPIKey)
	}

	// Save Image Generation configuration.
	global.db.Set(ImageTable, "provider", imageProvider)
	if imageAPIKey != "" {
		global.db.CryptSet(ImageTable, "api_key", imageAPIKey)
	} else {
		global.db.Unset(ImageTable, "api_key")
	}

	// Save cost rates. Parsed from the string inputs; invalid or blank
	// values default to zero. SetCostRates also updates the process's
	// in-memory rates so the change takes effect on any further runs
	// started from this setup session (no restart required).
	new_rates := CostRates{
		WorkerInputPer1K:  parseDollarRate(cost_worker_in),
		WorkerOutputPer1K: parseDollarRate(cost_worker_out),
		LeadInputPer1K:    parseDollarRate(cost_lead_in),
		LeadOutputPer1K:   parseDollarRate(cost_lead_out),
		SearchPerCall:     parseDollarRate(cost_search),
		ImagePerCall:      parseDollarRate(cost_image),
	}
	if err := SaveCostRatesToDB(global.db, new_rates); err != nil {
		Err("Failed to save cost rates: %s", err)
	} else {
		SetCostRates(new_rates)
	}

	// Save embedding configuration. SaveEmbeddingConfigToDB also
	// updates the in-memory config so the change takes effect without
	// a restart.
	newEmbedCfg := EmbeddingConfig{
		Endpoint: strings.TrimSpace(embedEndpoint),
		Model:    strings.TrimSpace(embedModel),
		Enabled:  embedEnabled == "yes",
	}
	if err := SaveEmbeddingConfigToDB(global.db, newEmbedCfg); err != nil {
		Err("Failed to save embedding config: %s", err)
	}

	// Save network timeouts and apply immediately so the running process
	// picks them up without a restart.
	global.db.Set(NetworkTable, "connect_timeout_seconds", netConnectSec)
	global.db.Set(NetworkTable, "request_timeout_seconds", netRequestSec)
	ApplyHTTPTimeouts(global.db)

	// Save Web Server configuration.
	global.db.Set(WebTable, "addr", webAddr)
	global.db.Set(WebTable, "tls_cert", webTLSCert)
	global.db.Set(WebTable, "tls_key", webTLSKey)
	global.db.Set(WebTable, "tls_self_signed", webTLSSelfSigned)
	global.db.Set(WebTable, "max_concurrent", webMaxConcurrent)
	global.db.Set(WebTable, "admin_allowed_ips", strings.TrimSpace(webAdminIPs))
	global.db.Set(WebTable, "allow_signup", webAllowSignup)
	global.db.Set(WebTable, "session_days", webSessionDays)
	if webAPIKey != "" {
		global.db.CryptSet(WebTable, "api_key", webAPIKey)
	}
	global.db.Set(WebTable, "external_url", webExternalURL)
	global.db.Set(WebTable, "service_name", webServiceName)
	global.db.Set(WebTable, "max_login_attempts", webMaxLoginAttempts)
	global.db.Set(WebTable, "lockout_minutes", webLockoutMinutes)
	global.db.Set(WebTable, "notify_from", webNotifyFrom)

	// Create or update the administrator account.
	// Skip password update if unchanged from the placeholder.
	if webAdminUser != "" {
		pass := webAdminPass
		if pass == "(configured)" {
			pass = ""
		}
		AuthSetUser(global.db, webAdminUser, pass, true)
	}

	// Save LLM routing configuration. "lead" stored as empty string so the
	// loader returns "" and RouteToWorker returns false (the default).
	for i, s := range stages {
		val := routeVals[i]
		if val == "lead" {
			val = ""
		}
		global.db.Set(RoutingTable, s.Key, val)
	}

	Stdout(NONE)
}

// add_source_hook interactively adds a new source hook.
func add_source_hook() {
	var hook SourceHook

	hook.Name = GetInput("Hook name (e.g., PubMed API, Internal Legal DB): ")
	if hook.Name == "" {
		return
	}

	hook_type := GetInput("Type (api, rag, paywall): ")
	switch strings.ToLower(hook_type) {
	case "api":
		hook.Type = HookTypeAPI
	case "rag":
		hook.Type = HookTypeRAG
	case "paywall":
		hook.Type = HookTypePaywall
	default:
		Stderr("Invalid type. Use: api, rag, or paywall")
		return
	}

	if hook.Type == HookTypePaywall {
		domains := GetInput("Domains (comma-separated, e.g., wsj.com,ft.com): ")
		hook.Domains = strings.Split(domains, ",")
		for i := range hook.Domains {
			hook.Domains[i] = strings.TrimSpace(hook.Domains[i])
		}
	} else {
		hook.Endpoint = GetInput("API endpoint URL: ")
		hook.QueryParam = GetInput("Query parameter name (default: q): ")
		hook.ResultsPath = GetInput("JSON results path (e.g., results, data.items — blank for root): ")
		hook.TitleField = GetInput("Title field name (default: title): ")
		hook.URLField = GetInput("URL field name (default: url): ")
		hook.SnippetField = GetInput("Snippet field name (default: snippet): ")
		if hook.Type == HookTypeRAG {
			hook.ContentField = GetInput("Content field name (default: content): ")
		}
	}

	auth_type := GetInput("Auth type (none, api_key, bearer): ")
	switch strings.ToLower(auth_type) {
	case "api_key":
		hook.AuthType = HookAuthAPIKey
		hook.AuthKey = GetInput("API key: ")
	case "bearer":
		hook.AuthType = HookAuthBearer
		hook.AuthKey = GetInput("Bearer token: ")
	default:
		hook.AuthType = HookAuthNone
	}

	triggers := GetInput("Trigger domains (comma-separated, e.g., legal,medical — or blank for always active): ")
	if triggers == "" {
		hook.AlwaysActive = true
	} else {
		hook.TriggerDomains = strings.Split(triggers, ",")
		for i := range hook.TriggerDomains {
			hook.TriggerDomains[i] = strings.TrimSpace(hook.TriggerDomains[i])
		}
	}

	SaveSourceHook(global.db, hook)
	Stdout("Source hook '%s' added.\n", hook.Name)
}

// list_source_hooks displays configured hooks.
func list_source_hooks() {
	hooks := RegisteredSourceHooks()
	if len(hooks) == 0 {
		Stdout("No source hooks configured.\n")
		return
	}
	for _, h := range hooks {
		trigger := "always active"
		if !h.AlwaysActive && len(h.TriggerDomains) > 0 {
			trigger = strings.Join(h.TriggerDomains, ", ")
		}
		auth := string(h.AuthType)
		if auth == "" {
			auth = "none"
		}
		Stdout("  %s [%s] — %s (auth: %s, trigger: %s)\n", h.Name, h.Type, h.Endpoint, auth, trigger)
	}
}

// remove_source_hook interactively removes a hook.
func remove_source_hook() {
	hooks := RegisteredSourceHooks()
	if len(hooks) == 0 {
		Stdout("No source hooks configured.\n")
		return
	}
	for i, h := range hooks {
		Stdout("  %d. %s [%s]\n", i+1, h.Name, h.Type)
	}
	choice := GetInput("Enter number to remove (or blank to cancel): ")
	if choice == "" {
		return
	}
	var idx int
	if _, err := fmt.Sscanf(choice, "%d", &idx); err != nil || idx < 1 || idx > len(hooks) {
		Stderr("Invalid selection.")
		return
	}
	DeleteSourceHook(global.db, hooks[idx-1].Name)
	Stdout("Removed '%s'.\n", hooks[idx-1].Name)
}

// update_hook_triggers loads an existing hook, lets the user edit its trigger
// domains, and resaves it. Useful for fixing over-broad triggers without
// having to delete and re-add the hook (which would lose API keys).
func update_hook_triggers() {
	hooks := RegisteredSourceHooks()
	if len(hooks) == 0 {
		Stdout("No source hooks configured.\n")
		return
	}
	Stdout("\n Configured hooks:\n")
	for i, h := range hooks {
		trigger := "always active"
		if !h.AlwaysActive && len(h.TriggerDomains) > 0 {
			trigger = strings.Join(h.TriggerDomains, ", ")
		}
		Stdout("  %d. %s — trigger: %s\n", i+1, h.Name, trigger)
	}

	choice := GetInput("\nEnter number to update (or blank to cancel): ")
	if choice == "" {
		return
	}
	var idx int
	if _, err := fmt.Sscanf(choice, "%d", &idx); err != nil || idx < 1 || idx > len(hooks) {
		Stderr("Invalid selection.")
		return
	}

	hook := hooks[idx-1]

	// Load any encrypted auth key so we don't lose it on resave.
	if hook.AuthType != HookAuthNone && hook.AuthKey == "" {
		key := strings.ToLower(strings.ReplaceAll(hook.Name, " ", "_"))
		var authKey string
		if global.db.Get("source_hooks", key+"_auth", &authKey) {
			hook.AuthKey = authKey
		}
	}

	// Look up the template defaults if this hook matches a known template.
	var template_defaults []string
	var template_always bool
	for _, t := range SourceHookTemplates() {
		if strings.EqualFold(t.Hook.Name, hook.Name) {
			template_defaults = t.Hook.TriggerDomains
			template_always = t.Hook.AlwaysActive
			break
		}
	}

	current := strings.Join(hook.TriggerDomains, ", ")
	if hook.AlwaysActive {
		current = "(always active)"
	}
	Stdout("\nCurrent trigger domains: %s\n", current)
	if template_defaults != nil || template_always {
		def_str := strings.Join(template_defaults, ", ")
		if template_always {
			def_str = "(always active)"
		}
		Stdout("Template defaults:       %s\n", def_str)
	}
	Stdout("\nAvailable categories: legal, medical, biomedical, science, technology, economics, political, criminal_justice, environmental, education, social_science, military\n")
	Stdout("Options:\n")
	Stdout("  - comma-separated categories (e.g. financial, economic, corporate)\n")
	Stdout("  - 'always' to make always active\n")
	if template_defaults != nil || template_always {
		Stdout("  - 'defaults' to restore template defaults\n")
	}
	Stdout("  - blank to cancel\n")

	input := strings.TrimSpace(GetInput("New trigger domains: "))
	if input == "" {
		Stdout("Cancelled.\n")
		return
	}

	switch strings.ToLower(input) {
	case "always":
		hook.AlwaysActive = true
		hook.TriggerDomains = nil
	case "defaults":
		if template_defaults == nil && !template_always {
			Stderr("No template defaults available for '%s' (custom hook).", hook.Name)
			return
		}
		hook.AlwaysActive = template_always
		hook.TriggerDomains = template_defaults
	default:
		var domains []string
		for _, d := range strings.Split(input, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				domains = append(domains, d)
			}
		}
		if len(domains) == 0 {
			Stderr("No valid domains provided.")
			return
		}
		hook.AlwaysActive = false
		hook.TriggerDomains = domains
	}

	SaveSourceHook(global.db, hook)

	updated := "always active"
	if !hook.AlwaysActive {
		updated = strings.Join(hook.TriggerDomains, ", ")
	}
	Stdout("Updated '%s' triggers: %s\n", hook.Name, updated)
}

// add_template_hook lets the user pick from pre-configured source templates.
func add_template_hook() {
	templates := SourceHookTemplates()
	existing := RegisteredSourceHooks()
	existingNames := make(map[string]bool)
	for _, h := range existing {
		existingNames[strings.ToLower(h.Name)] = true
	}

	Stdout("\n Available templates:\n")
	var available []SourceHookTemplate
	for _, t := range templates {
		status := ""
		if existingNames[strings.ToLower(t.Hook.Name)] {
			status = " (already configured)"
		}
		available = append(available, t)
		key_note := "no API key needed"
		if t.NeedsAPIKey {
			key_note = "API key required"
		}
		Stdout("  %d. %s — %s (%s)%s\n", len(available), t.Hook.Name, t.Description, key_note, status)
	}

	choice := GetInput("\nEnter number to add (or blank to cancel): ")
	if choice == "" {
		return
	}
	var idx int
	if _, err := fmt.Sscanf(choice, "%d", &idx); err != nil || idx < 1 || idx > len(available) {
		Stderr("Invalid selection.")
		return
	}

	tmpl := available[idx-1]
	hook := tmpl.Hook

	if existingNames[strings.ToLower(hook.Name)] {
		Stderr("'%s' is already configured.", hook.Name)
		return
	}

	if tmpl.NeedsAPIKey {
		hook.AuthKey = GetInput(fmt.Sprintf("Enter API key for %s: ", hook.Name))
		if hook.AuthKey == "" {
			Stderr("API key is required for %s.", hook.Name)
			return
		}
	}

	SaveSourceHook(global.db, hook)
	Stdout("Source hook '%s' added.\n", hook.Name)
}

// init_database initializes the application database.
func init_database() {
	var err error

	MkDir(fmt.Sprintf("%s/data/", global.root))
	SetImageDir(fmt.Sprintf("%s/data/images", global.root))
	SetBrowserDir(fmt.Sprintf("%s/data/browser", global.root))
	SetGeocodeDir(fmt.Sprintf("%s/data/geocode", global.root))
	SetWorkspacesDir(fmt.Sprintf("%s/data/workspaces", global.root))

	db_filename := FormatPath(fmt.Sprintf("%s/data/%s.db", global.root, APPNAME))
	global.db, err = SecureDatabase(db_filename)
	Critical(err)
	SetErrTable(global.db.Table("fuzz_errors"))
	global.cache = global.db.Sub("cache")

	// Wire the persistent source hook cache to the global cache sub-database.
	// This persists results from source hooks so repeated runs on the same
	// topic don't re-hit rate-limited upstream APIs. TTL default is 30 days.
	SetHookCacheDB(global.cache)

	// Load global source hooks — available to all agents regardless of entry point.
	LoadSourceHooks(global.db)

	// Apply configurable HTTP timeouts for all source-hook API clients.
	ApplyHTTPTimeouts(global.db)

	// Load persisted cost rates so per-run usage telemetry shows dollar
	// estimates instead of "rates not configured." Must happen after the
	// DB handle is live; called from here so every entry point that opens
	// the DB (web, chat, CLI, setup) picks up saved rates automatically.
	InitCostRates(global.db)

	// Load persisted embedding config so the vector-DB ingestion and
	// semantic_search tool have their endpoint + model. Silent no-op
	// when the record doesn't exist (embeddings default to disabled).
	LoadEmbeddingConfigFromDB(global.db)
}

// init_logging initializes the logging system.
func init_logging() {
	file, err := nfo.LogFile(FormatPath(fmt.Sprintf("%s/logs/%s.log", global.root, APPNAME)), 10, 10)
	Critical(err)
	nfo.SetFile(nfo.STD|nfo.AUX, file)
	if global.sysmode {
		nfo.SetOutput(nfo.STD, os.Stderr)
		nfo.SetOutput(nfo.INFO, os.Stdout)
		nfo.SetOutput(nfo.AUX|nfo.WARN|nfo.NOTICE, nfo.None)
	}
	if global.debug {
		nfo.SetOutput(nfo.DEBUG, os.Stderr)
		nfo.SetFile(nfo.DEBUG, nfo.GetFile(nfo.ERROR))
	}
	if global.snoop {
		nfo.SetOutput(nfo.TRACE, os.Stderr)
		nfo.SetFile(nfo.TRACE, nfo.GetFile(nfo.ERROR))
	}
}

// enable_trace enables trace output to stdout.
func enable_trace() {
	nfo.SetOutput(nfo.TRACE, os.Stdout)
	nfo.SetFile(nfo.TRACE, nfo.GetFile(nfo.ERROR))
}

// get_mac_addr retrieves the MAC address of the network interface.
func get_mac_addr() []byte {
	ifaces, err := net.Interfaces()
	Critical(err)

	for _, v := range ifaces {
		if len(v.HardwareAddr) == 0 {
			continue
		}
		return v.HardwareAddr
	}
	return nil
}

// SecureDatabase opens a database file, handling potential decryption
// or reset if hardware changes are detected.
func SecureDatabase(file string) (Database, error) {
	Debug("Opening database: %s (mac_lock=%v).", file, _db_lock_status())
	db, err := OpenDB(file, _unlock_db()[0:]...)
	if err != nil {
		if err == ErrBadPadlock {
			Debug("Database padlock mismatch, hardware change detected; resetting DB.")
			Notice("Hardware changes detected, you will need to reauthenticate.")
			if err := ResetDB(file); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
		db, err = OpenDB(file, _unlock_db()[0:]...)
		if err != nil {
			return nil, err
		}
	}
	Debug("Database opened successfully.")
	return db, nil
}

// hashBytes computes the SHA256 hash of the input values.
func hashBytes(input ...interface{}) []byte {
	var combine []string
	for _, v := range input {
		if x, ok := v.([]byte); ok {
			v = string(x)
		}
		combine = append(combine, fmt.Sprintf("%v", v))
	}
	sum := sha256.Sum256([]byte(strings.Join(combine[0:], NONE)))
	var output []byte
	output = append(output[0:], sum[0:]...)
	return output
}

// encrypt encrypts the input data using AES encryption with CFB mode.
func encrypt(input []byte, key []byte) []byte {
	var block cipher.Block

	key = hashBytes(key)
	block, _ = aes.NewCipher(key)

	buff := make([]byte, len(input))
	copy(buff, input)

	cipher.NewCFBEncrypter(block, key[0:block.BlockSize()]).XORKeyStream(buff, buff)

	return []byte(base64.RawStdEncoding.EncodeToString(buff))
}

// decrypt decrypts a base64 encoded ciphertext using AES-CFB.
func decrypt(input []byte, key []byte) (decoded []byte) {
	var block cipher.Block

	key = hashBytes(key)

	decoded, _ = base64.RawStdEncoding.DecodeString(string(input))
	block, _ = aes.NewCipher(key)
	cipher.NewCFBDecrypter(block, key[0:block.BlockSize()]).XORKeyStream(decoded, decoded)

	return
}

// _db_lock_status checks if database locking is enabled.
func _db_lock_status() bool {
	if v := global.cfg.Get("do_not_modify", "db_locker"); len(v) > 0 {
		return false
	}
	return true
}

// _set_db_locker sets a database locker to prevent multiple instances.
func _set_db_locker() {
	if v := global.cfg.Get("do_not_modify", "db_locker"); len(v) > 0 {
		global.cfg.Unset("do_not_modify", "db_locker")
		return
	} else {
		mac := get_mac_addr()
		random := RandBytes(40)
		db_lock_code := string(encrypt(mac, random))
		Critical(global.cfg.Set("do_not_modify", "db_locker", fmt.Sprintf("%s%s", string(random), db_lock_code)))
	}
	return
}

// _unlock_db decrypts or generates a database padlock.
//
// On first run (db_locker not yet stored), the current MAC is captured
// AND immediately wrapped + persisted via the random-key trick so
// subsequent restarts recover the same MAC bytes regardless of
// net.Interfaces() enumeration order. Without this, get_mac_addr()
// can return a different MAC each restart (Docker, libvirt, USB nics
// shifting positions), and any value written via CryptSet under the
// previous MAC fails to decrypt — the user-visible symptom is "my
// secret survived this session but not the next restart."
//
// The wrapping is the same shape _set_db_locker uses (random || encrypt(mac, random)),
// so existing tooling that toggles via that function still interoperates.
func _unlock_db() (padlock []byte) {
	if dbs := global.cfg.Get("do_not_modify", "db_locker"); len(dbs) > 0 {
		code := []byte(dbs[0:40])
		db_lock_code := []byte(dbs[40:])
		padlock = decrypt(db_lock_code, code)
		return
	}
	// First call ever — capture current MAC, wrap it, persist. From
	// now on, the padlock is recovered from the wrapping regardless of
	// what net.Interfaces() returns.
	padlock = get_mac_addr()
	if len(padlock) > 0 {
		random := RandBytes(40)
		db_lock_code := string(encrypt(padlock, random))
		Critical(global.cfg.Set("do_not_modify", "db_locker", fmt.Sprintf("%s%s", string(random), db_lock_code)))
	}
	return
}

// enable_debug enables debug output to stdout.
func enable_debug() {
	nfo.SetOutput(nfo.DEBUG, os.Stdout)
	nfo.SetFile(nfo.DEBUG, nfo.GetFile(nfo.ERROR))
}

// parseDollarRate converts a setup-menu string back into a float. Blank,
// whitespace-only, or unparseable input all return 0 — the same value as
// "not configured" so the operator can clear a rate by emptying the field.
func parseDollarRate(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Accept leading "$" in case the operator types it instinctively.
	s = strings.TrimPrefix(s, "$")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
