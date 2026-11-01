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
			Detail:   "Made by a connector or a peer that embeds, often on the same host as the worker LLM. Disabling it makes ingestion and search no-ops.",
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
			Detail:   "Done by a connector or a peer that transcribes: voice notes, the sound of videos, and audio files you attach become text the agent reads. The Test sends a second of a tone.",
			Body: ui.FormPanel{
				Assist:    true,
				Source:    "api/transcribe",
				TestURL:   "api/transcribe/test",
				TestLabel: "Test transcription",
				Fields:    transcribeFormFields(),
			},
		},
		{
			Title:    "Speech (TTS)",
			Subtitle: "Spoken replies to voice notes.",
			Detail: "When someone sends a channel agent a voice note, the reply goes back as a voice note. On iMessage it goes as the voice note alone, " +
				"an audio attachment rather than a voice-memo bubble, which only Messages itself can send; services that cannot deliver audio get the text. " +
				"Needs Audio Transcription too, so the agent can hear the note. Code and web addresses are left out of what is read aloud, " +
				"a reply over about 2,500 characters goes as text only, and if speech fails the text still goes. " +
				"Done by a connector or a peer that speaks.",
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
			Detail:   "Every picture comes from a connector chosen here: Gemini or OpenAI added from their templates, a local ComfyUI or Automatic1111, a peer's renderer, or any backend declared as a spec. Its address and key are kept on it, under Admin > Extensions > Connectors.\n\nUse “Add image backend” to stand up a local ComfyUI or A1111 in one step.",
			Body: ui.Stack{
				Children: []ui.Component{
					ui.FormPanel{
						Assist:    true,
						Source:    "api/image-gen",
						TestURL:   "api/image-gen/test",
						TestLabel: "Test",
						Fields:    imageFormFields(),
					},
					ui.Toolbar{
						Actions: imageBackendToolbarActions(),
					},
				},
			},
		},
		{
			Title:    "Music",
			Subtitle: "Music connectors agents may make songs with.",
			Detail:   "Each one ticked is offered to agents as a tool of its own, generate_music_<connector>, and put to use. Add a music connector under Admin > Extensions > Connectors, from its spec; it is tested and fixed there. A flow may use one as a step without it being offered here.",
			Body: ui.FormPanel{
				Source: "api/music",
				Fields: []ui.FormField{{Field: "agents", Label: "Offered to agents", Type: "checklist", Options: musicOptions(),
					Help: "None listed means no connector here makes music yet."}},
			},
		},
		{
			Title:    "Web Search",
			Subtitle: "Provider for the web_search tool.",
			Detail:   "DuckDuckGo is built in and needs no key. Any other engine is a connector that searches: add one from a template (Admin > Extensions > Templates has Brave, Serper, Tavily, Google Programmable Search and SearXNG) and pick it here as Service: <name>, which puts it to use, its key kept on it.",
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
			Detail:   "Signup approvals, scheduled deliveries, watcher alerts and the send_email tool. Leave Server blank for localhost:25. A mail API (Resend, Postmark and others) can send instead: add a connector that sends mail from Admin > Extensions > Templates and pick it under Send with, which puts it to use.",
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
			Subtitle: "Outbound HTTP timeouts for research sources and search APIs.",
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
			Help: "This instance's SMTP server, or a connector that sends mail through an API."})
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

// imageFormFields builds the Image Generation settings: the connector that
// makes pictures, chosen from every approved one that does (a rest_image
// backend that generates, or a service giving image.generate), or Disabled.
// A new connector appears here with no edit.
func imageFormFields() []ui.FormField {
	var opts []ui.SelectOption
	for _, c := range ListConnectors(RootDB) {
		// Every image connector (choosing it here puts it to use), and any
		// other backend already registered, a peer's renderer among them.
		if imageConnector(c.Name) || (c.Approved && ImageBackendRegistered(c.Name)) {
			label := "Connector: " + c.Name
			if _, waiting := service.PendingChange(RootDB, c.Name); waiting {
				label += " (changed, waiting to be accepted)"
			}
			opts = append(opts, ui.SelectOption{Value: c.Name, Label: label, Help: ConnectorSummary(c)})
		}
	}
	add := "add a connector that makes pictures from a template (Admin > Extensions > Templates: Gemini (image generation); OpenAI (image generation)), stand up a local ComfyUI or A1111 with Add image backend below, or connect a peer that lends image generation (Admin > Capabilities > Peers)"
	f := ui.FormField{Field: "provider", Label: "Make pictures with", Type: "select",
		Options: append(append([]ui.SelectOption(nil), opts...), ui.SelectOption{Value: "none", Label: "Disabled"})}
	if len(opts) == 0 {
		f.Help = "Nothing here makes pictures yet: " + add + "."
		return []ui.FormField{f}
	}
	f.Help = "The connector that makes pictures. Choosing it puts it to use; its address and key are kept on it, under Admin > Extensions > Connectors, where it is tested and fixed."
	f.Detail = "To make pictures with something not listed, " + add + ". A connector changed by someone who is not an administrator since it was put to use waits out of use: read the change on its row, then save here with it chosen to accept it."
	agents := ui.FormField{Field: "agents", Label: "Also offered to agents", Type: "checklist", Options: opts,
		Help:   "Other image connectors agents may choose by name, and peers borrowing image generation. The one above is always offered.",
		Detail: "Each one ticked is put to use. Agents' image tools offer the connector above as the default and these by name; a peer that borrows image generation from this machine may name them too."}
	return []ui.FormField{f, agents}
}

// databaseBrowserCard is the read-only kvlite table/key/record browser.
// A 3-pane drill-down is app-specific developer tooling, not a generic
// primitive, so it rides the sanctioned Card escape hatch (raw HTML +
// inline script) and talks to the existing /api/db/{tables,keys,record}
// endpoints. Self-contained: scoped CSS + DOM-built rows (textContent, no
// innerHTML injection). No backticks inside this raw string per repo rule.

// embeddingFormFields builds the Embeddings settings: on or off, the
// connector or peer that embeds, and the prefixes its model wants. The
// endpoint, model and key are the connector's own, set up, tested and fixed
// under Admin > Extensions > Connectors, so there is one place each is kept.
func embeddingFormFields() []ui.FormField {
	choices := append(serviceProviderOptions("embed.text", "Embed"), EmbeddingProviderOptions()...)
	add := "add a connector that embeds from a template (Admin > Extensions > Templates: Ollama (embeddings); llama.cpp / vLLM / OpenAI-compatible (embeddings); OpenAI (embeddings)) or from its spec (Add, under Admin > Extensions > Connectors), or connect a peer that lends embeddings (Admin > Capabilities > Peers)"
	fields := []ui.FormField{{Field: "enabled", Label: "Enable embeddings", Type: "toggle"}}
	if len(choices) == 0 {
		// Nothing to choose: say how to get something, rather than a choice
		// with no answers.
		fields[0].Help = "Nothing here embeds yet: " + add + "."
	} else {
		fields = append(fields, ui.FormField{Field: "provider", Label: "Embed with", Type: "select",
			Options:  choices,
			ShowWhen: "enabled",
			Help:     "The connector or peer that makes embeddings. Its address, model and key are kept on it: open its row under Admin > Extensions > Connectors to see, test or fix them.",
			Detail:   "To embed with something not listed, " + add + ". Changing what embeds changes the embedding space: search skips stored documents from the old space until Repair in the Vector Index panel makes them again, and through a paid service that is billed."})
	}
	return append(fields,
		// The prefixes belong to the MODEL, not to where it runs, so they stay
		// beside whichever connector or peer does the embedding.
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

// transcribeFormFields builds the STT settings: on or off, and the connector
// or peer that transcribes. Its address, model and key are its own, set up,
// tested and fixed under Admin > Extensions > Connectors, as embeddings'
// are.
func transcribeFormFields() []ui.FormField {
	choices := append(serviceProviderOptions("speech.transcribe", "Transcribe"), TranscribeProviderOptions()...)
	add := "add a connector that transcribes from a template (Admin > Extensions > Templates: whisper.cpp / OpenAI-compatible (transcription); OpenAI (transcription)) or from its spec (Add, under Admin > Extensions > Connectors), or connect a peer that lends transcription (Admin > Capabilities > Peers)"
	fields := []ui.FormField{{Field: "enabled", Label: "Enable transcription", Type: "toggle"}}
	if len(choices) == 0 {
		fields[0].Help = "Nothing here transcribes yet: " + add + "."
		return fields
	}
	return append(fields, ui.FormField{Field: "provider", Label: "Transcribe with", Type: "select",
		Options:  choices,
		ShowWhen: "enabled",
		Help:     "The connector or peer that transcribes. Its address, model and key are kept on it: open its row under Admin > Extensions > Connectors to see, test or fix them.",
		Detail:   "To transcribe with something not listed, " + add + "."})
}

// speechFormFields builds the speech (TTS) settings: on or off, the
// connector or peer that speaks, and the voice, chosen from its list. Its
// address, model and key are its own, set up, tested and fixed under Admin >
// Extensions > Connectors, as transcription's are.
func speechFormFields() []ui.FormField {
	choices := append(serviceProviderOptions("speech.synthesize", "Speak"), SpeechProviderOptions()...)
	add := "add a connector that speaks from a template (Admin > Extensions > Templates: Kokoro / OpenAI-compatible (speech); ElevenLabs (speech)) or from its spec (Add, under Admin > Extensions > Connectors), or connect a peer that lends speech (Admin > Capabilities > Peers)"
	fields := []ui.FormField{{Field: "enabled", Label: "Enable speech", Type: "toggle"}}
	if len(choices) == 0 {
		fields[0].Help = "Nothing here speaks yet: " + add + "."
		return fields
	}
	return append(fields,
		ui.FormField{Field: "provider", Label: "Speak with", Type: "select",
			Options:  choices,
			ShowWhen: "enabled",
			Help:     "The connector or peer that speaks. Its address, model and key are kept on it: open its row under Admin > Extensions > Connectors to see, test or fix them.",
			Detail:   "To speak with something not listed, " + add + "."},
		ui.FormField{Field: "voice", Label: "Voice", Type: "text",
			Placeholder: "af_heart",
			Help:        "Optional. A voice it offers, such as Kokoro's 'af_heart' or OpenAI's 'alloy'. Blank uses the connector's own default.",
			Detail:      "Save the connector or peer first, then click a chip to pick from its own list, and Preview voice to hear it before you save. One without a voice list shows no chips; type the voice instead.",
			ShowWhen:    "enabled",
			ChipsSource: "api/speech/voices"},
	)
}

// webSearchFormFields builds the Web Search settings, with the peer picker
// added only when a peer actually offers search: the engine (DuckDuckGo, or
// a connector that searches), and where to search from. A
// connector's key and address are kept on it.
//
// The engine's fields carry ShowWhen "source:local" so they disappear while
// a peer is selected, and the clause is added and removed together with the
// dropdown that drives it: leaving it in with no dropdown would blank the
// whole form on every deployment with no peers.
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
			Help:    "Search from this instance, or through a peer you have connected.",
			Detail:  "Peers live under Resource Sharing, then Peers. Borrowing a peer means this machine never holds the search API key.",
		})
	}
	add := "add a connector that searches from a template (Admin > Extensions > Templates: Brave, Serper, Tavily, Google Programmable Search, SearXNG) or from its spec (Add, under Admin > Extensions > Connectors)"
	return append(fields,
		ui.FormField{Field: "provider", Label: "Search with", Type: "select",
			ShowWhen: local,
			Options: append([]ui.SelectOption{
				{Value: "duckduckgo", Label: "DuckDuckGo (built in, no key)"},
			}, serviceProviderOptions("web.search", "Search")...),
			Help:   "The engine every search goes to. A connector's address and key are kept on it: open its row under Admin > Extensions > Connectors to see, test or fix them.",
			Detail: "To search with an engine not listed, " + add + ". Once added it is offered here, and choosing it puts it to use. Serper's also finds pictures, for agents' find_image. The connector picked here is not also asked as a research source: it already answers web_search."},
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
