package admin

import (
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
)

// capabilitiesSections is the capabilities part of the admin page: System Hooks, Embeddings, Audio Transcription (STT), Speech (TTS), Image Generation, Web Search, Page Rendering (Browser), Mail (SMTP), Network Timeouts.
func (a *AdminApp) capabilitiesSections() []ui.Section {
	return []ui.Section{
		// First: what this machine can and cannot do, which the settings
		// below it are referred to from (transcription, for one).
		systemDepsSection(),
		{
			Title:    "Embeddings",
			Subtitle: "Vector store ingestion and semantic search.",
			Detail:   "The endpoint is an Ollama-compatible /api/embed server, typically the same host as the worker LLM. Disabling it makes ingestion and search no-ops.",
			Body: ui.FormPanel{
				Assist:    true,
				Source:    "api/embeddings",
				TestURL:   "api/embeddings/test",
				TestLabel: "Test embed call",
				Fields:    embeddingFormFields(),
			},
		},
		{
			Title:    "Audio Transcription (STT)",
			Subtitle: "Transcription for video and audio attachments.",
			Detail:   "An OpenAI-compatible /audio/transcriptions endpoint. The endpoint you give includes the API version prefix; gohort appends /audio/transcriptions.",
			Body: ui.FormPanel{
				Assist:    true,
				Source:    "api/transcribe",
				TestURL:   "api/transcribe/test",
				TestLabel: "Test endpoint",
				Fields:    transcribeFormFields(),
			},
		},
		{
			Title:    "Speech (TTS)",
			Subtitle: "Spoken replies to voice notes.",
			Detail: "When someone sends a channel agent a voice note, the reply goes back as a voice note. On iMessage it goes as the voice note alone, " +
				"an audio attachment rather than a voice-memo bubble, which only Messages itself can send; services that cannot deliver audio get the text. " +
				"Needs Audio Transcription too, so the agent can hear the note. Code and web addresses are left out of what is read aloud, " +
				"a reply over about 2,500 characters goes as text only, and if the speech server fails the text still goes. " +
				"The endpoint is an OpenAI-compatible /audio/speech server, such as Kokoro-FastAPI; gohort appends /audio/speech.",
			Body: ui.FormPanel{
				Assist:    true,
				Source:    "api/speech",
				TestURL:   "api/speech/test",
				TestLabel: "Preview voice",
				Fields:    speechFormFields(),
			},
		},
		{
			Title:    "Image Generation",
			Subtitle: "Image generation for tools that produce illustrations or thumbnails.",
			Detail:   "Choose a built-in provider, leaving the API key blank to reuse the matching LLM provider's key, or an approved rest_image connector: a local ComfyUI or Automatic1111, or any spec-declared backend.\n\nUse “Add image backend” to stand up a local ComfyUI or A1111 in one step.",
			Body: ui.Stack{
				Children: []ui.Component{
					ui.FormPanel{
						Assist:    true,
						Source:    "api/image-gen",
						TestURL:   "api/image-gen/test",
						TestLabel: "Test API key",
						Fields: []ui.FormField{
							{Field: "provider", Label: "Provider", Type: "select",
								Options: imageProviderOptions()},
							{Field: "api_key", Label: "API Key", Type: "password",
								Help:     "Provider API key. Leave it blank to reuse the matching LLM provider's key.",
								Detail:   "Only Gemini and OpenAI read it. Connector backends carry their own credential.",
								ShowWhen: "provider:gemini|openai"},
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
			Subtitle: "Provider for the web_search tool.",
			Detail:   "DuckDuckGo and a SearXNG instance require no key; Brave, Google and Serper need one. Any other search API can be the engine too: add a connector that searches (Admin > Extensions > Templates has Brave, Serper and Tavily), approve it, and pick it here as Service: <name>; or set the API up as a Source Hook and pick that.",
			Body: ui.FormPanel{
				Assist:    true,
				Source:    "api/web-search",
				TestURL:   "api/web-search/test",
				TestLabel: "Test search call",
				Fields:    webSearchFormFields(),
			},
		},
		{
			Title:    "Page Rendering (Browser)",
			Subtitle: "Where browse_page and the page-render escalations run.",
			Detail:   "A headless Chromium is a heavyweight dependency to install on every machine; borrowing a peer's lets a laptop skip it entirely. Public web only either way.",
			Body: ui.FormPanel{
				Source: "api/browse",
				Fields: browseFormFields(),
			},
		},
		{
			Title:    "Mail (SMTP)",
			Subtitle: "Outbound SMTP for notification emails.",
			Detail:   "Signup approvals, scheduled deliveries, watcher alerts and the send_email tool. Leave Server blank for localhost:25. A mail API (Resend, Postmark and others) can send instead: add a connector that sends mail from Admin > Extensions > Templates, approve it, and pick it under Send with.",
			Body: ui.FormPanel{
				Assist:    true,
				Source:    "api/mail",
				TestURL:   "api/mail/test",
				TestLabel: "Send test email",
				Fields:    mailFormFields(),
			},
		},
		{
			Title:    "Network Timeouts",
			Subtitle: "Outbound HTTP timeouts for source hooks and search APIs.",
			Detail:   "Raise them when working against slow upstreams; lower them to fail fast in a tight loop.",
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

// mailPointer is the mail form's pointer while SMTP is all there is.
func mailPointer(hasChoice bool) string {
	if hasChoice {
		return ""
	}
	return "Mail is sent by the SMTP server set here. To send through a mail API instead, add a connector that sends mail (Admin > Extensions > Templates): once approved, it is offered here to choose."
}

// mailFormFields are the mail settings: what sends (SMTP, or a connector that
// sends mail, offered once one is approved), then SMTP's server and sign-in,
// shown while SMTP sends, and the From address and Default Recipient, which
// both use.
func mailFormFields() []ui.FormField {
	choices := append([]ui.SelectOption{{Value: "smtp", Label: "SMTP (the server below)"}}, serviceProviderOptions("mail.send", "Send")...)
	hasChoice := len(choices) > 1
	smtp := ""
	if hasChoice {
		smtp = "provider:smtp"
	}
	var fields []ui.FormField
	if hasChoice {
		fields = append(fields, ui.FormField{Field: "provider", Label: "Send with", Type: "select", Options: choices,
			Help: "This instance's SMTP server, or an approved connector that sends mail through an API."})
	}
	return append(fields,
		ui.FormField{Field: "server", Label: "SMTP Server", Type: "text",
			Placeholder: "smtp.gmail.com:587", ShowWhen: smtp,
			Help: mailPointer(hasChoice),
			Presets: []ui.FieldPreset{
				{Label: "Gmail", Value: "smtp.gmail.com:587"},
				{Label: "Outlook", Value: "smtp-mail.outlook.com:587"},
				{Label: "iCloud", Value: "smtp.mail.me.com:587"},
				{Label: "Local", Value: "localhost:25"},
			}},
		ui.FormField{Field: "from", Label: "From Address", Type: "text",
			Placeholder: "noreply@example.com",
			Help:        "Mail is sent from this address, whatever sends it; blank, a connector uses its own."},
		ui.FormField{Field: "recipient", Label: "Default Recipient", Type: "text",
			Help: "Test emails and pipeline reports go here when no per-call recipient is given."},
		ui.FormField{Field: "username", Label: "SMTP Username", Type: "text", ShowWhen: smtp},
		ui.FormField{Field: "password", Label: "SMTP Password", Type: "password", ShowWhen: smtp},
	)
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
		if c.Approved && (c.Kind == RestImageConnectorKind || service.Provides(c, "image.generate")) {
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
// elsewhere is the pointer a capability's page shows while this instance is
// the only thing that can do it: where to get something else to choose. The
// choice itself (Embed on, Transcribe on, Speak with) appears once there is
// one, rather than as a dropdown with a single answer.
func elsewhere(hasChoice bool, verb, does, what string) string {
	if hasChoice {
		return ""
	}
	return "Done by this instance's endpoint below. To " + verb + " somewhere else, add a connector that " + does +
		" (Admin > Extensions > Templates) or a peer that lends " + what + " (Admin > Capabilities > Peers): once approved, it is offered here to choose."
}

func embeddingFormFields() []ui.FormField {
	peers := append(EmbeddingProviderOptions(), serviceProviderOptions("embed.text", "Embed")...)
	// One option means local only: nothing to choose between.
	hasPeers := len(peers) > 1

	local := "enabled"
	if hasPeers {
		local = "enabled;provider:local"
	}

	fields := []ui.FormField{
		{Field: "enabled", Label: "Enable embeddings", Type: "toggle", Help: elsewhere(hasPeers, "embed", "embeds", "embeddings")},
	}
	if hasPeers {
		// Where the embedding happens. "This instance" keeps the manual
		// endpoint/model below; picking a peer fills those from its manifest at
		// save time, so everything downstream sees an ordinary config.
		fields = append(fields, ui.FormField{
			Field: "provider", Label: "Embed on", Type: "select",
			Options:  peers,
			ShowWhen: "enabled",
			Help:     "Use this instance's own embedder, a peer instance you've connected under Resource Sharing › Peers, or an approved service connector.",
			Detail:   "Changing where embeddings are made changes the embedding space: search skips stored documents from the old space until Repair in the Vector Index panel makes them again, and through a paid service that is billed.",
		})
	}
	return append(fields,
		ui.FormField{Field: "endpoint", Label: "Endpoint", Type: "text",
			Placeholder: "http://localhost:11434/api",
			Help:        "Base URL including the API version prefix. gohort appends /embeddings.",
			Detail:      "Pick a preset below for the canonical path on common platforms.",
			ShowWhen:    local,
			Presets: []ui.FieldPreset{
				{Label: "Ollama", Value: "http://localhost:11434/api", Hint: "Ollama native API (→ /api/embeddings)"},
				{Label: "llama.cpp", Value: "http://localhost:8080/v1", Hint: "llama.cpp OpenAI-compatible (→ /v1/embeddings)"},
				{Label: "vLLM", Value: "http://localhost:8000/v1", Hint: "vLLM OpenAI-compatible (→ /v1/embeddings)"},
				{Label: "OpenAI", Value: "https://api.openai.com/v1", Hint: "OpenAI hosted (→ /v1/embeddings, requires API key support)"},
			}},
		ui.FormField{Field: "model", Label: "Model", Type: "text",
			Placeholder: "nomic-embed-text",
			Help:        "Leave it blank for single-model backends. Required for Ollama.",
			Detail:      "llama.cpp, vLLM and hf-tei ignore this field. Click a chip below to fill it from the endpoint's model list.",
			ShowWhen:    local,
			ChipsSource: "api/embeddings/models"},
		ui.FormField{Field: "api_key", Label: "API Key", Type: "password",
			Help:     "Optional bearer token.",
			Detail:   "Set it for OpenAI hosted or authenticated proxies. Leave it blank for local Ollama, llama.cpp, or vLLM.",
			ShowWhen: local},
		// The prefixes belong to the MODEL, not to where it runs, so they stay
		// visible when a peer is doing the embedding.
		ui.FormField{Field: "query_prefix", Label: "Query prefix", Type: "text",
			Placeholder: "search_query: ",
			Help:        "Prepended to every search query before embedding. Asymmetric retrieval models need it.",
			Detail:      "e5 uses \"query: \", nomic-embed uses \"search_query: \", bge uses \"Represent this sentence for searching relevant passages: \". Leave it blank for symmetric models. It is included verbatim, trailing space and all, and is safe to change at any time.",
			ShowWhen:    "enabled",
			Presets: []ui.FieldPreset{
				{Label: "e5", Value: "query: ", Hint: "intfloat/e5 family"},
				{Label: "nomic", Value: "search_query: ", Hint: "nomic-embed-text"},
				{Label: "bge", Value: "Represent this sentence for searching relevant passages: ", Hint: "BAAI/bge v1.5 family"},
			}},
		ui.FormField{Field: "doc_prefix", Label: "Document prefix", Type: "text",
			Placeholder: "search_document: ",
			Help:        "Prepended to every chunk, fact and tool description before embedding.",
			Detail:      "e5 uses \"passage: \" and nomic-embed uses \"search_document: \"; bge and Qwen3-Embedding use none. Changing it changes the embedding space: stored vectors stop matching until their documents are re-ingested.",
			ShowWhen:    "enabled",
			Presets: []ui.FieldPreset{
				{Label: "e5", Value: "passage: ", Hint: "intfloat/e5 family"},
				{Label: "nomic", Value: "search_document: ", Hint: "nomic-embed-text"},
			}},
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
	peers := append(TranscribeProviderOptions(), serviceProviderOptions("speech.transcribe", "Transcribe")...)
	hasPeers := len(peers) > 1

	local := "enabled"
	if hasPeers {
		local = "enabled;provider:local"
	}

	fields := []ui.FormField{
		{Field: "enabled", Label: "Enable transcription", Type: "toggle", Help: elsewhere(hasPeers, "transcribe", "transcribes", "transcription")},
	}
	if hasPeers {
		fields = append(fields, ui.FormField{
			Field: "provider", Label: "Transcribe on", Type: "select",
			Options:  peers,
			ShowWhen: "enabled",
			Help:     "Use this instance's own STT endpoint, a peer instance you've connected under Resource Sharing › Peers, or an approved service connector that transcribes.",
		})
	}
	return append(fields,
		ui.FormField{Field: "endpoint", Label: "Endpoint", Type: "text",
			Placeholder: "http://localhost:8089/v1",
			Help:        "Base URL with the version prefix: gohort appends /audio/transcriptions.",
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
			Help:     "Optional bearer token.",
			Detail:   "Required for OpenAI hosted Whisper, or a whisper.cpp server behind an authenticating proxy. Leave it blank for a plain local whisper.cpp server.",
			ShowWhen: local},
	)
}

// speechFormFields builds the speech (TTS) settings, with the peer picker
// added only when a peer offers speech: the same conditional shape as
// transcribeFormFields, for the same reason. The voice stays while a peer is
// picked: it is chosen here, from the peer's own list.
func speechFormFields() []ui.FormField {
	peers := append(SpeechProviderOptions(), serviceProviderOptions("speech.synthesize", "Speak")...)
	hasPeers := len(peers) > 1

	local := "enabled"
	if hasPeers {
		local = "enabled;provider:local"
	}

	fields := []ui.FormField{
		{Field: "enabled", Label: "Enable speech", Type: "toggle", Help: elsewhere(hasPeers, "speak", "speaks", "speech")},
	}
	if hasPeers {
		fields = append(fields, ui.FormField{
			Field: "provider", Label: "Speak with", Type: "select",
			Options:  peers,
			ShowWhen: "enabled",
			Help:     "Use this instance's own speech server, a peer instance you've connected under Resource Sharing › Peers, or an approved service connector that speaks.",
		})
	}
	return append(fields,
		ui.FormField{Field: "endpoint", Label: "Endpoint", Type: "text",
			Placeholder: "http://localhost:8880/v1",
			Help:        "Base URL with the version prefix: gohort appends /audio/speech.",
			ShowWhen:    local,
			Presets: []ui.FieldPreset{
				{Label: "Kokoro-FastAPI", Value: "http://localhost:8880/v1", Hint: "Kokoro-FastAPI's default port"},
				{Label: "OpenAI", Value: "https://api.openai.com/v1", Hint: "OpenAI hosted speech"},
			}},
		ui.FormField{Field: "model", Label: "Model", Type: "text",
			Placeholder: "kokoro",
			Help:        "Optional. Kokoro-FastAPI takes 'kokoro'; OpenAI 'tts-1'.",
			ShowWhen:    local},
		ui.FormField{Field: "voice", Label: "Voice", Type: "text",
			Placeholder: "af_heart",
			Help:        "Optional. A voice the server offers, such as Kokoro's 'af_heart' or OpenAI's 'alloy'. A service connector uses its own default when this is blank.",
			Detail:      "Save the endpoint or peer first, then click a chip to pick from the server's own list, and Preview voice to hear it before you save. Servers without a voice list show no chips; type the name instead.",
			ShowWhen:    "enabled",
			ChipsSource: "api/speech/voices"},
		ui.FormField{Field: "api_key", Label: "API Key", Type: "password",
			Help:     "Optional bearer token.",
			Detail:   "Required for OpenAI hosted speech, or a server behind an authenticating proxy. Leave it blank for a local speech server.",
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
	// The key and endpoint also depend on the provider, so their rule adds a
	// provider clause to the source one when there is a source to check.
	withLocal := func(clause string) string {
		if local == "" {
			return clause
		}
		return local + ";" + clause
	}

	var fields []ui.FormField
	if hasPeers {
		fields = append(fields, ui.FormField{
			Field: "source", Label: "Search on", Type: "select",
			Options: peers,
			Help:    "Search from this instance, or through a peer you have connected.",
			Detail:  "Peers live under Resource Sharing, then Peers. Borrowing a peer means this machine never holds the search API key.",
		})
	}
	return append(fields,
		ui.FormField{Field: "provider", Label: "Provider", Type: "select",
			ShowWhen: local,
			Options: append([]ui.SelectOption{
				{Value: "duckduckgo", Label: "DuckDuckGo (no key)"},
				{Value: "brave", Label: "Brave"},
				{Value: "google", Label: "Google"},
				{Value: "serper", Label: "Serper"},
				{Value: "searxng", Label: "SearXNG (self-hosted)"},
				{Value: "hook", Label: "A Source Hook (your own search API)"},
			}, serviceProviderOptions("web.search", "Search")...)},
		ui.FormField{Field: "hook", Label: "Source Hook", Type: "select",
			OptionsSource: "api/web-search/hooks",
			ShowWhen:      withLocal("provider:hook"),
			Help:          "The API hook that answers searches: any search API, set up under Source Hooks with its endpoint, key and where its results are.",
			Detail:        "Picking one makes it the search engine: it stops being a source hook of its own, so research no longer routes to it by topic and it is no longer a tool of its own; web_search is how it is reached. Its results are read as the hook reads them (the results path, and the title, link and snippet fields), and every search asks its API live, never the hook's cache. Only API hooks that are switched on are listed; add one under Source Hooks first. Its key is the hook's own, so none is set here."},
		ui.FormField{Field: "api_key", Label: "API Key", Type: "password",
			ShowWhen: withLocal("provider:brave|google|serper|searxng"),
			Help:     "Required for Brave, Google and Serper. Google takes it as key:cx (the API key, a colon, then the search engine ID).",
			Detail:   "SearXNG needs no key; one set here is sent only as an optional bearer token, for an instance behind an authenticating proxy."},
		ui.FormField{Field: "endpoint", Label: "Endpoint", Type: "text",
			Placeholder: "https://searx.example.com",
			ShowWhen:    withLocal("provider:searxng"),
			Help:        "Required for SearXNG. The base URL of your instance."},
	)
}

// browseFormFields is the page-rendering picker. Unlike search or STT there is
// nothing to configure locally — the browser is either linked into the build or
// it is not — so this is one choice, and the section says so rather than
// showing an empty form when no peer offers rendering.
func browseFormFields() []ui.FormField {
	opts := append(BrowseProviderOptions(), serviceProviderOptions("page.render", "Render")...)
	help := "Only this instance renders pages. To render elsewhere, add a connector that renders pages (Admin > Extensions > Templates) or a peer that lends page rendering (Admin > Capabilities > Peers): once approved, it is offered here to choose."
	if len(opts) > 1 {
		help = "Render locally, on a peer, or through a connector. Borrowing means this machine never downloads or runs Chromium. Public web only, whichever renders."
	}
	return []ui.FormField{
		{Field: "source", Label: "Render pages on", Type: "select", Options: opts, Help: help},
	}
}
