package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"

	"github.com/cmcoffee/snugforge/nfo"
)

const (
	llm_table      = "llm_config"
	lead_llm_table = "lead_llm_config"
	mail_table     = "mail_config"
	search_table   = "search_config"
	image_table    = "image_config"
	routing_table  = "llm_routing"
	web_table      = "web_config"
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

// load_mail_config reads the stored mail configuration from the database.
func load_mail_config() MailConfig {
	var cfg MailConfig
	global.db.Get(mail_table, "server", &cfg.Server)
	global.db.Get(mail_table, "from", &cfg.From)
	global.db.Get(mail_table, "username", &cfg.Username)
	global.db.Get(mail_table, "password", &cfg.Password)
	global.db.Get(mail_table, "recipient", &cfg.Recipient)
	return cfg
}

// load_search_config reads the stored web search configuration from the database.
func load_search_config() WebSearchConfig {
	var cfg WebSearchConfig
	global.db.Get(search_table, "provider", &cfg.Provider)
	global.db.Get(search_table, "api_key", &cfg.APIKey)
	global.db.Get(search_table, "endpoint", &cfg.Endpoint)
	return cfg
}

// setup_fuzz runs the interactive configuration.
func setup_fuzz() {
	init_database()

	// Load current LLM values.
	var provider, model, apiKey, endpoint string
	var contextSize int
	var requestTimeoutSec int
	var disableThinking bool
	var nativeTools bool
	var ollamaMaxParallel int
	global.db.Get(llm_table, "provider", &provider)
	global.db.Get(llm_table, "model", &model)
	global.db.Get(llm_table, "api_key", &apiKey)
	global.db.Get(llm_table, "endpoint", &endpoint)
	global.db.Get(llm_table, "context_size", &contextSize)
	global.db.Get(llm_table, "request_timeout_seconds", &requestTimeoutSec)
	global.db.Get(llm_table, "disable_thinking", &disableThinking)
	global.db.Get(llm_table, "native_tools", &nativeTools)
	global.db.Get(llm_table, "ollama_max_parallel", &ollamaMaxParallel)
	if ollamaMaxParallel < 1 {
		ollamaMaxParallel = 1 // default: strict serial execution through Ollama
	}

	// Load current Lead LLM values.
	var leadProvider, leadModel, leadAPIKey, leadEndpoint string
	var leadDisableThinking bool
	var leadNativeTools bool
	global.db.Get(lead_llm_table, "provider", &leadProvider)
	global.db.Get(lead_llm_table, "model", &leadModel)
	global.db.Get(lead_llm_table, "api_key", &leadAPIKey)
	global.db.Get(lead_llm_table, "endpoint", &leadEndpoint)
	global.db.Get(lead_llm_table, "disable_thinking", &leadDisableThinking)
	global.db.Get(lead_llm_table, "native_tools", &leadNativeTools)
	if leadProvider == "" {
		leadProvider = "(use primary)"
	}

	// Load current mail values.
	var mailServer, mailFrom, mailUser, mailPass, mailRecipient string
	global.db.Get(mail_table, "server", &mailServer)
	global.db.Get(mail_table, "from", &mailFrom)
	global.db.Get(mail_table, "username", &mailUser)
	global.db.Get(mail_table, "password", &mailPass)
	global.db.Get(mail_table, "recipient", &mailRecipient)

	setup := NewOptions("--- Gohort Configuration ---", "(selection or 'q' to save & exit)", 'q')

	// LLM settings.
	llm := NewOptions(" [LLM Provider Settings] ", "(selection or 'q' to return to previous)", 'q')
	llm.StringSelectVar(&provider, "LLM Provider", provider, "anthropic", "openai", "gemini", "ollama")
	llm.SecretVar(&apiKey, "API Key", apiKey, NONE)
	llm.ShowWhen(func() bool { return provider != "ollama" })
	llm.StringVar(&model, "Model", model, "LLM model name (leave blank for provider default).")
	llm.Func("Browse Available Models", func() bool {
		return browseModels(provider, apiKey, endpoint, &model)
	})
	llm.StringVar(&endpoint, "Endpoint", endpoint, "Custom API endpoint (leave blank for default).")
	llm.ShowWhen(func() bool { return provider == "ollama" })
	llm.IntVar(&contextSize, "Context Size", contextSize, "Context window size in tokens (0=default 65K).", 0, 262144)
	llm.ShowWhen(func() bool { return provider == "ollama" })
	llm.IntVar(&requestTimeoutSec, "Request Timeout (seconds)", requestTimeoutSec, "Max wait for first response header. 0=default 300s. Bump for slow local models or huge prompts.", 0, 3600)
	llm.ToggleVar(&disableThinking, "Disable Thinking (force think=false on every call)", disableThinking)
	llm.ShowWhen(func() bool { return provider == "ollama" })
	llm.ToggleVar(&nativeTools, "Native Tool Calling (disable for models without tool support)", nativeTools)
	llm.ShowWhen(func() bool { return provider == "ollama" })
	llm.IntVar(&ollamaMaxParallel, "Max Parallel Ollama Requests", ollamaMaxParallel, "How many concurrent requests Ollama will process. Default 1 (strict serial). Raise only if the host GPU can truly run more in parallel. Requests are fair-queued across caller sessions.", 1, 16)
	llm.ShowWhen(func() bool { return provider == "ollama" })
	// Precision LLM settings (judge/fact-checker — falls back to primary if not set).
	lead := NewOptions(" [Precision LLM (Secondary)] ", "(selection or 'q' to return to previous)", 'q')
	lead.StringSelectVar(&leadProvider, "LLM Provider", leadProvider, "(use primary)", "anthropic", "openai", "gemini", "ollama")
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
	lead.ShowWhen(func() bool { return leadProvider == "ollama" })
	lead.ToggleVar(&leadDisableThinking, "Disable Thinking (force think=false on every call)", leadDisableThinking)
	lead.ShowWhen(func() bool { return leadProvider == "ollama" })
	lead.ToggleVar(&leadNativeTools, "Native Tool Calling (disable for models without tool support)", leadNativeTools)
	lead.ShowWhen(func() bool { return leadProvider == "ollama" })

	// LLM Routing settings — built dynamically from registered route stages.
	stages := ListRouteStages()
	routeVals := make([]string, len(stages))
	for i, s := range stages {
		global.db.Get(routing_table, s.Key, &routeVals[i])
		if routeVals[i] == "" {
			routeVals[i] = "lead"
		}
	}

	// Image Generation settings.
	var imageProvider, imageAPIKey string
	global.db.Get(image_table, "provider", &imageProvider)
	global.db.Get(image_table, "api_key", &imageAPIKey)
	if imageProvider == "" {
		imageProvider = "gemini"
	}

	imagegen := NewOptions(" [Image Generation] ", "(selection or 'q' to return to previous)", 'q')
	imagegen.StringSelectVar(&imageProvider, "Image Provider", imageProvider, "gemini", "openai", "none")
	imagegen.SecretVar(&imageAPIKey, "API Key", imageAPIKey, "Dedicated image generation API key (only needed if no LLM provider already has this key).")
	imagegen.ShowWhen(func() bool {
		if imageProvider == "none" {
			return false
		}
		for _, p := range []string{provider, leadProvider} {
			if p == imageProvider {
				return false
			}
		}
		return true
	})

	// Group all LLM settings under one menu.
	llmSettings := NewOptions(" [LLM Settings] ", "(selection or 'q' to return to previous)", 'q')
	llmSettings.Options("Primary Provider", llm, false)
	llmSettings.Options("Precision LLM (Secondary)", lead, false)
	if len(stages) > 0 {
		routing := NewOptions(" [LLM Routing] ", "(selection or 'q' to return to previous)", 'q')
		for i, s := range stages {
			routing.StringSelectVar(&routeVals[i], s.Label, routeVals[i], "lead", "worker")
		}
		llmSettings.Options("Routing (worker = local, lead = remote)", routing, false)
	}
	llmSettings.Options("Image Generation", imagegen, false)
	setup.Options("LLM Settings", llmSettings, false)

	// --- External Sources ---
	var searchProvider, searchAPIKey, searchEndpoint string
	global.db.Get(search_table, "provider", &searchProvider)
	global.db.Get(search_table, "api_key", &searchAPIKey)
	global.db.Get(search_table, "endpoint", &searchEndpoint)

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
	global.db.Get(web_table, "addr", &webAddr)
	global.db.Get(web_table, "tls_cert", &webTLSCert)
	global.db.Get(web_table, "tls_key", &webTLSKey)
	global.db.Get(web_table, "tls_self_signed", &webTLSSelfSigned)
	global.db.Get(web_table, "max_concurrent", &webMaxConcurrent)
	global.db.Get(web_table, "admin_allowed_ips", &webAdminIPs)
	global.db.Get(web_table, "allow_signup", &webAllowSignup)
	global.db.Get(web_table, "session_days", &webSessionDays)
	global.db.Get(web_table, "api_key", &webAPIKey)
	global.db.Get(web_table, "external_url", &webExternalURL)
	global.db.Get(web_table, "service_name", &webServiceName)
	global.db.Get(web_table, "max_login_attempts", &webMaxLoginAttempts)
	global.db.Get(web_table, "lockout_minutes", &webLockoutMinutes)
	if webMaxLoginAttempts == 0 {
		webMaxLoginAttempts = 5
	}
	if webLockoutMinutes == 0 {
		webLockoutMinutes = 15
	}
	var webNotifyFrom string
	global.db.Get(web_table, "notify_from", &webNotifyFrom)
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
	global.db.Set(llm_table, "provider", provider)
	global.db.Set(llm_table, "model", model)
	global.db.Set(llm_table, "endpoint", endpoint)
	global.db.Set(llm_table, "context_size", contextSize)
	global.db.Set(llm_table, "request_timeout_seconds", requestTimeoutSec)
	global.db.Set(llm_table, "disable_thinking", disableThinking)
	global.db.Set(llm_table, "native_tools", nativeTools)
	global.db.Set(llm_table, "ollama_max_parallel", ollamaMaxParallel)
	if apiKey != "" {
		global.db.CryptSet(llm_table, "api_key", apiKey)
	}

	// Save Lead LLM configuration.
	if leadProvider == "(use primary)" {
		leadProvider = ""
	}
	global.db.Set(lead_llm_table, "provider", leadProvider)
	global.db.Set(lead_llm_table, "model", leadModel)
	global.db.Set(lead_llm_table, "endpoint", leadEndpoint)
	global.db.Set(lead_llm_table, "disable_thinking", leadDisableThinking)
	global.db.Set(lead_llm_table, "native_tools", leadNativeTools)
	if leadAPIKey != "" {
		global.db.CryptSet(lead_llm_table, "api_key", leadAPIKey)
	}

	// Save mail configuration.
	global.db.Set(mail_table, "server", mailServer)
	global.db.Set(mail_table, "from", mailFrom)
	global.db.Set(mail_table, "recipient", mailRecipient)
	global.db.Set(mail_table, "username", mailUser)
	if mailPass != "" {
		global.db.CryptSet(mail_table, "password", mailPass)
	}

	// Save search configuration.
	global.db.Set(search_table, "provider", searchProvider)
	global.db.Set(search_table, "endpoint", searchEndpoint)
	if searchAPIKey != "" {
		global.db.CryptSet(search_table, "api_key", searchAPIKey)
	}

	// Save Image Generation configuration.
	global.db.Set(image_table, "provider", imageProvider)
	if imageAPIKey != "" {
		global.db.CryptSet(image_table, "api_key", imageAPIKey)
	} else {
		global.db.Unset(image_table, "api_key")
	}

	// Save Web Server configuration.
	global.db.Set(web_table, "addr", webAddr)
	global.db.Set(web_table, "tls_cert", webTLSCert)
	global.db.Set(web_table, "tls_key", webTLSKey)
	global.db.Set(web_table, "tls_self_signed", webTLSSelfSigned)
	global.db.Set(web_table, "max_concurrent", webMaxConcurrent)
	global.db.Set(web_table, "admin_allowed_ips", strings.TrimSpace(webAdminIPs))
	global.db.Set(web_table, "allow_signup", webAllowSignup)
	global.db.Set(web_table, "session_days", webSessionDays)
	if webAPIKey != "" {
		global.db.CryptSet(web_table, "api_key", webAPIKey)
	}
	global.db.Set(web_table, "external_url", webExternalURL)
	global.db.Set(web_table, "service_name", webServiceName)
	global.db.Set(web_table, "max_login_attempts", webMaxLoginAttempts)
	global.db.Set(web_table, "lockout_minutes", webLockoutMinutes)
	global.db.Set(web_table, "notify_from", webNotifyFrom)

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
		global.db.Set(routing_table, s.Key, val)
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

// load_llm_config reads the stored LLM configuration from the database.
func load_llm_config() LLMProviderConfig {
	var cfg LLMProviderConfig
	global.db.Get(llm_table, "provider", &cfg.Provider)
	global.db.Get(llm_table, "model", &cfg.Model)
	global.db.Get(llm_table, "api_key", &cfg.APIKey)
	global.db.Get(llm_table, "endpoint", &cfg.Endpoint)
	global.db.Get(llm_table, "context_size", &cfg.ContextSize)
	global.db.Get(llm_table, "disable_thinking", &cfg.DisableThinking)
	global.db.Get(llm_table, "native_tools", &cfg.NativeTools)
	global.db.Get(llm_table, "ollama_max_parallel", &cfg.OllamaMaxParallel)
	var timeout_seconds int
	global.db.Get(llm_table, "request_timeout_seconds", &timeout_seconds)
	if timeout_seconds > 0 {
		cfg.RequestTimeout = time.Duration(timeout_seconds) * time.Second
	}
	return cfg
}

// load_lead_llm_config reads the stored Lead LLM configuration from the database.
func load_lead_llm_config() LLMProviderConfig {
	var cfg LLMProviderConfig
	global.db.Get(lead_llm_table, "provider", &cfg.Provider)
	global.db.Get(lead_llm_table, "model", &cfg.Model)
	global.db.Get(lead_llm_table, "api_key", &cfg.APIKey)
	global.db.Get(lead_llm_table, "endpoint", &cfg.Endpoint)
	global.db.Get(lead_llm_table, "disable_thinking", &cfg.DisableThinking)
	global.db.Get(lead_llm_table, "native_tools", &cfg.NativeTools)
	// Lead uses the same scheduler cap as primary when both point at
	// Ollama (global process-level limiter).
	global.db.Get(llm_table, "ollama_max_parallel", &cfg.OllamaMaxParallel)
	return cfg
}

// ImageProvider returns the configured image generation provider.
func ImageProvider() string {
	var provider string
	global.db.Get(image_table, "provider", &provider)
	if provider == "" {
		return "gemini"
	}
	return provider
}

// GeminiAPIKey returns the Gemini API key for image generation.
// Checks the dedicated image key first, then LLM configs.
func GeminiAPIKey() string {
	var imageKey string
	global.db.Get(image_table, "api_key", &imageKey)
	if imageKey != "" {
		return imageKey
	}
	cfg := load_llm_config()
	if cfg.Provider == "gemini" && cfg.APIKey != "" {
		return cfg.APIKey
	}
	lead := load_lead_llm_config()
	if lead.Provider == "gemini" && lead.APIKey != "" {
		return lead.APIKey
	}
	return ""
}

// OpenAIAPIKey returns the OpenAI API key for image generation.
// Checks the dedicated image config first, then falls back to
// whichever LLM config is set to OpenAI (primary or lead).
func OpenAIAPIKey() string {
	// Dedicated image generation key (set in --setup → Image Generation).
	var imageKey string
	global.db.Get(image_table, "api_key", &imageKey)
	if imageKey != "" {
		return imageKey
	}
	// Fall back to LLM provider keys.
	cfg := load_llm_config()
	if cfg.Provider == "openai" && cfg.APIKey != "" {
		return cfg.APIKey
	}
	lead := load_lead_llm_config()
	if lead.Provider == "openai" && lead.APIKey != "" {
		return lead.APIKey
	}
	return ""
}

// init_database initializes the application database.
func init_database() {
	var err error

	MkDir(fmt.Sprintf("%s/data/", global.root))
	SetImageDir(fmt.Sprintf("%s/data/images", global.root))

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
func _unlock_db() (padlock []byte) {
	if dbs := global.cfg.Get("do_not_modify", "db_locker"); len(dbs) > 0 {
		code := []byte(dbs[0:40])
		db_lock_code := []byte(dbs[40:])
		padlock = decrypt(db_lock_code, code)
	} else {
		padlock = get_mac_addr()
	}
	return
}

// enable_debug enables debug output to stdout.
func enable_debug() {
	nfo.SetOutput(nfo.DEBUG, os.Stdout)
	nfo.SetFile(nfo.DEBUG, nfo.GetFile(nfo.ERROR))
}
