package admin

import (
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// capabilitiesSections is the capabilities part of the admin page: Embeddings, Audio Transcription (STT), System Dependencies, Image Generation, Web Search, Page Rendering (Browser), Mail (SMTP), Network Timeouts.
func (a *AdminApp) capabilitiesSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "Embeddings",
			Subtitle: "Vector store ingestion + semantic search. Endpoint is an Ollama-compatible /api/embed server — typically the same host as the worker LLM. Disabling makes ingestion and search no-ops.",
			Body: ui.FormPanel{
				Source:    "api/embeddings",
				TestURL:   "api/embeddings/test",
				TestLabel: "Test embed call",
				Fields:    embeddingFormFields(),
			},
		},
		{
			Title:    "Audio Transcription (STT)",
			Subtitle: "OpenAI-compatible /audio/transcriptions endpoint used for video / audio attachment transcription. Endpoint includes the API version prefix; gohort appends /audio/transcriptions.",
			Body: ui.FormPanel{
				Source:    "api/transcribe",
				TestURL:   "api/transcribe/test",
				TestLabel: "Test endpoint",
				Fields:    transcribeFormFields(),
			},
		},
		{
			Title:    "System Dependencies",
			Subtitle: "External tools gohort shells out to for media + document handling. A missing one disables the feature it gates (e.g. no ffmpeg → inbound voice memos can't be transcribed). Date-versioned tools like yt-dlp are flagged when they go stale (its extractors rot fast, so a stale yt-dlp silently breaks video downloads). Install or update on the gohort host and restart; this list refreshes on reload.",
			Body: ui.Table{
				Source: "api/dependencies",
				RowKey: "name",
				Columns: []ui.Col{
					{Field: "name", Label: "Tool", Flex: 1},
					{Field: "present", Label: "Status", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Installed", Color: "success"},
						{Value: false, Label: "Missing", Color: "danger"},
					}},
					{Field: "version", Label: "Version", Flex: 1, Mute: true},
					{Field: "stale", Label: "Freshness", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: true, Label: "Stale, update", Color: "warning"},
					}},
					{Field: "enables", Label: "Enables", Flex: 3, Mute: true},
					{Field: "install", Label: "Install", Flex: 2, Mute: true},
				},
				EmptyText: "No dependency information.",
			},
		},
		{
			Title:    "Image Generation",
			Subtitle: "Image generation provider used by tools that produce illustrations or thumbnails. Choose a built-in provider (leave API key blank to reuse the matching LLM provider's key), or an approved rest_image connector — a local ComfyUI / Automatic1111 or any spec-declared backend. Use “Add image backend” to stand up a local ComfyUI / A1111 in one step.",
			Body: ui.Stack{
				Children: []ui.Component{
					ui.FormPanel{
						Source:    "api/image-gen",
						TestURL:   "api/image-gen/test",
						TestLabel: "Test API key",
						Fields: []ui.FormField{
							{Field: "provider", Label: "Provider", Type: "select",
								Options: imageProviderOptions()},
							{Field: "api_key", Label: "API Key", Type: "password",
								Help:     "Provider API key. Leave blank to reuse the matching LLM provider's key. (Ignored for connector backends — they carry their own credential.)",
								ShowWhen: "provider"},
						},
					},
					ui.Toolbar{
						Actions: imageBackendToolbarActions(),
					},
				},
			},
		},
		{
			Title:    "Web Search",
			Subtitle: "Provider for the web_search tool. DuckDuckGo and a SearXNG instance require no key; Brave / Google / Serper need one.",
			Body: ui.FormPanel{
				Source:    "api/web-search",
				TestURL:   "api/web-search/test",
				TestLabel: "Test search call",
				Fields:    webSearchFormFields(),
			},
		},
		{
			Title:    "Page Rendering (Browser)",
			Subtitle: "Where browse_page and the page-render escalations run. A headless Chromium is a heavyweight dependency to install on every machine; borrowing a peer's lets a laptop skip it entirely. Public web only either way.",
			Body: ui.FormPanel{
				Source: "api/browse",
				Fields: browseFormFields(),
			},
		},
		{
			Title:    "Mail (SMTP)",
			Subtitle: "Outbound SMTP for notification emails — signup approvals, scheduled deliveries, watcher alerts. Leave Server blank for localhost:25.",
			Body: ui.FormPanel{
				Source:    "api/mail",
				TestURL:   "api/mail/test",
				TestLabel: "Send test email",
				Fields: []ui.FormField{
					{Field: "server", Label: "SMTP Server", Type: "text",
						Placeholder: "smtp.gmail.com:587",
						Presets: []ui.FieldPreset{
							{Label: "Gmail", Value: "smtp.gmail.com:587"},
							{Label: "Outlook", Value: "smtp-mail.outlook.com:587"},
							{Label: "iCloud", Value: "smtp.mail.me.com:587"},
							{Label: "Local", Value: "localhost:25"},
						}},
					{Field: "from", Label: "From Address", Type: "text",
						Placeholder: "noreply@example.com"},
					{Field: "recipient", Label: "Default Recipient", Type: "text",
						Help: "Test emails and pipeline reports go here when no per-call recipient is given."},
					{Field: "username", Label: "SMTP Username", Type: "text"},
					{Field: "password", Label: "SMTP Password", Type: "password"},
				},
			},
		},
		{
			Title:    "Network Timeouts",
			Subtitle: "Outbound HTTP timeouts for source hooks and search APIs. Raise when working against slow upstreams; lower to fail fast in a tight loop.",
			Body: ui.FormPanel{
				Source: "api/network",
				Fields: []ui.FormField{
					{Field: "connect_timeout_seconds", Label: "Connect timeout (seconds)",
						Type: "number", Min: 1, Max: 120, Placeholder: "10",
						Help: "TCP + TLS connection timeout. Default 10."},
					{Field: "request_timeout_seconds", Label: "Request timeout (seconds)",
						Type: "number", Min: 1, Max: 300, Placeholder: "15",
						Help: "Per-read I/O timeout for HTTP response bodies. Default 15."},
				},
			},
		},
	}
}

// imageBackendToolbarActions builds the Image Generation toolbar: "Add image
// backend" is always available; "Configure backend…" appears only when an image
// backend actually exists (nothing to configure otherwise).
func imageBackendToolbarActions() []ui.ToolbarAction {
	acts := []ui.ToolbarAction{{Label: "Add image backend…", Method: "client", URL: "add_image_backend"}}
	for _, c := range ListConnectors(RootDB) {
		if c.Kind == RestImageConnectorKind {
			acts = append(acts, ui.ToolbarAction{Label: "Configure backend…", Method: "client", URL: "configure_backend_pick"})
			break
		}
	}
	return acts
}

// imageProviderOptions builds the image-generation provider dropdown: the two
// built-in providers, every APPROVED rest_image connector (ComfyUI / A1111 /
// custom backends materialize as native image providers named for the
// connector), then Disabled. A new connector appears here with no edit.
func imageProviderOptions() []ui.SelectOption {
	opts := []ui.SelectOption{
		{Value: "gemini", Label: "Gemini (Imagen)"},
		{Value: "openai", Label: "OpenAI (DALL-E)"},
	}
	for _, c := range ListConnectors(RootDB) {
		if c.Kind == RestImageConnectorKind && c.Approved {
			opts = append(opts, ui.SelectOption{Value: c.Name, Label: "Connector: " + c.Name})
		}
	}
	opts = append(opts, ui.SelectOption{Value: "none", Label: "Disabled"})
	return opts
}

// databaseBrowserCard is the read-only kvlite table/key/record browser.
// A 3-pane drill-down is app-specific developer tooling, not a generic
// primitive, so it rides the sanctioned Card escape hatch (raw HTML +
// inline script) and talks to the existing /api/db/{tables,keys,record}
// endpoints. Self-contained: scoped CSS + DOM-built rows (textContent, no
// innerHTML injection). No backticks inside this raw string per repo rule.
// embeddingFormFields builds the Embeddings settings form.
//
// The "Embed on" dropdown appears only when a peer offering embeddings is
// actually registered. With none, it is a select with one option — a control
// that asks a question with a single possible answer, on a page that already
// has plenty to read.
//
// Its absence has to be matched by the gating on the fields below it. Those
// carry ShowWhen "enabled;provider:local" so they disappear while a peer is
// selected; leaving that clause in place with no dropdown to set `provider`
// would hide the endpoint, model and key on every deployment that has no peers
// — the whole form, blank, for the overwhelmingly common case. So the clause is
// added and removed together with the field that drives it.
func embeddingFormFields() []ui.FormField {
	peers := EmbeddingProviderOptions()
	// One option means local only: nothing to choose between.
	hasPeers := len(peers) > 1

	local := "enabled"
	if hasPeers {
		local = "enabled;provider:local"
	}

	fields := []ui.FormField{
		{Field: "enabled", Label: "Enable embeddings", Type: "toggle"},
	}
	if hasPeers {
		// Where the embedding happens. "This instance" keeps the manual
		// endpoint/model below; picking a peer fills those from its manifest at
		// save time, so everything downstream sees an ordinary config.
		fields = append(fields, ui.FormField{
			Field: "provider", Label: "Embed on", Type: "select",
			Options:  peers,
			ShowWhen: "enabled",
			Help:     "Use this instance's own embedder, or a peer instance you've connected under Resource Sharing › Peers.",
		})
	}
	return append(fields,
		ui.FormField{Field: "endpoint", Label: "Endpoint", Type: "text",
			Placeholder: "http://localhost:11434/api",
			Help:        "Base URL including the API version prefix — gohort appends /embeddings. Pick a preset below for the canonical path on common platforms.",
			ShowWhen:    local,
			Presets: []ui.FieldPreset{
				{Label: "Ollama", Value: "http://localhost:11434/api", Hint: "Ollama native API (→ /api/embeddings)"},
				{Label: "llama.cpp", Value: "http://localhost:8080/v1", Hint: "llama.cpp OpenAI-compatible (→ /v1/embeddings)"},
				{Label: "vLLM", Value: "http://localhost:8000/v1", Hint: "vLLM OpenAI-compatible (→ /v1/embeddings)"},
				{Label: "OpenAI", Value: "https://api.openai.com/v1", Hint: "OpenAI hosted (→ /v1/embeddings, requires API key support)"},
			}},
		ui.FormField{Field: "model", Label: "Model", Type: "text",
			Placeholder: "nomic-embed-text",
			Help:        "Leave blank for single-model backends (llama.cpp, vLLM, hf-tei — they ignore this field). Required for Ollama. Click a chip below to fill from the endpoint's model list.",
			ShowWhen:    local,
			ChipsSource: "api/embeddings/models"},
		ui.FormField{Field: "api_key", Label: "API Key", Type: "password",
			Help:     "Optional bearer token. Set for OpenAI hosted / authenticated proxies; leave blank for local Ollama, llama.cpp, or vLLM.",
			ShowWhen: local},
	)
}

// transcribeFormFields builds the STT settings, with the peer picker added only
// when a peer actually offers transcription.
//
// Same conditional shape as embeddingFormFields, and for the same reason: the
// endpoint/model/key carry ShowWhen "enabled;provider:local" so they disappear
// while a peer is selected, and leaving that clause in with no dropdown to set
// `provider` would hide the whole form on every deployment with no peers.
func transcribeFormFields() []ui.FormField {
	peers := TranscribeProviderOptions()
	hasPeers := len(peers) > 1

	local := "enabled"
	if hasPeers {
		local = "enabled;provider:local"
	}

	fields := []ui.FormField{
		{Field: "enabled", Label: "Enable transcription", Type: "toggle"},
	}
	if hasPeers {
		fields = append(fields, ui.FormField{
			Field: "provider", Label: "Transcribe on", Type: "select",
			Options:  peers,
			ShowWhen: "enabled",
			Help:     "Use this instance's own STT endpoint, or a peer instance you've connected under Resource Sharing › Peers.",
		})
	}
	return append(fields,
		ui.FormField{Field: "endpoint", Label: "Endpoint", Type: "text",
			Placeholder: "http://localhost:8089/v1",
			Help:        "Base URL with the version prefix — gohort appends /audio/transcriptions.",
			ShowWhen:    local,
			Presets: []ui.FieldPreset{
				{Label: "whisper.cpp", Value: "http://localhost:8089/v1", Hint: "Default whisper.cpp HTTP server port"},
				{Label: "OpenAI", Value: "https://api.openai.com/v1", Hint: "OpenAI hosted Whisper"},
			}},
		ui.FormField{Field: "model", Label: "Model", Type: "text",
			Placeholder: "whisper-1",
			Help:        "Optional. whisper.cpp ignores this; OpenAI expects 'whisper-1'.",
			ShowWhen:    local},
		ui.FormField{Field: "api_key", Label: "API Key", Type: "password",
			Help:     "Optional bearer token. Set for real OpenAI / authenticated proxies; leave blank for local whisper.cpp.",
			ShowWhen: local},
	)
}

// webSearchFormFields builds the Web Search settings, with the peer picker
// added only when a peer actually offers search.
//
// Same conditional shape as embeddings and transcription: the provider/key/
// endpoint fields carry ShowWhen "source:local" so they disappear while a peer
// is selected, and the clause is added and removed together with the dropdown
// that drives it — leaving it in with no dropdown would blank the whole form on
// every deployment with no peers.
func webSearchFormFields() []ui.FormField {
	peers := SearchProviderOptions()
	hasPeers := len(peers) > 1
	local := ""
	if hasPeers {
		local = "source:local"
	}

	var fields []ui.FormField
	if hasPeers {
		fields = append(fields, ui.FormField{
			Field: "source", Label: "Search on", Type: "select",
			Options: peers,
			Help:    "Search from this instance, or through a peer you've connected under Resource Sharing › Peers. Borrowing a peer means this machine never holds the search API key.",
		})
	}
	return append(fields,
		ui.FormField{Field: "provider", Label: "Provider", Type: "select",
			ShowWhen: local,
			Options: []ui.SelectOption{
				{Value: "duckduckgo", Label: "DuckDuckGo (no key)"},
				{Value: "brave", Label: "Brave"},
				{Value: "google", Label: "Google"},
				{Value: "serper", Label: "Serper"},
				{Value: "searxng", Label: "SearXNG (self-hosted)"},
			}},
		ui.FormField{Field: "api_key", Label: "API Key", Type: "password",
			ShowWhen: local,
			Help:     "Required for Brave / Google / Serper."},
		ui.FormField{Field: "endpoint", Label: "Endpoint", Type: "text",
			Placeholder: "https://searx.example.com",
			ShowWhen:    local,
			Help:        "Required for SearXNG. The base URL of your instance."},
	)
}

// browseFormFields is the page-rendering picker. Unlike search or STT there is
// nothing to configure locally — the browser is either linked into the build or
// it is not — so this is one choice, and the section says so rather than
// showing an empty form when no peer offers rendering.
func browseFormFields() []ui.FormField {
	opts := BrowseProviderOptions()
	help := "Only this instance offers page rendering. Connect a peer that grants it under Resource Sharing › Peers to render elsewhere."
	if len(opts) > 1 {
		help = "Render locally, or on a peer. Borrowing means this machine never downloads or runs Chromium."
	}
	return []ui.FormField{
		{Field: "source", Label: "Render pages on", Type: "select", Options: opts, Help: help},
	}
}
