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
			Detail:   "Every picture comes from a connector chosen here: Gemini or OpenAI added from their templates, a local ComfyUI or Automatic1111, a peer's renderer, or any backend declared as a spec.\n\nThe list below is every image backend: which is the default, which are offered to agents, what each does and how its size and steps are decided. Edit on a row opens that backend's own editor (for ComfyUI, its workflow and how it is mapped), the same one its row under Connectors opens.\n\nUse “Add image backend” to stand up a local ComfyUI or A1111 in one step.",
			Body: ui.Stack{
				Children: []ui.Component{
					ui.FormPanel{
						Assist:    true,
						Source:    "api/image-gen",
						TestURL:   "api/image-gen/test",
						TestLabel: "Test",
						Fields:    imageFormFields(),
						// Which backend is the default, and which are offered,
						// is the list's Role column.
						Invalidate: []string{imageBackendsSource},
					},
					imageBackendsTable(),
					ui.Toolbar{
						Actions: []ui.ToolbarAction{{Label: "Add image backend…", Method: "client", URL: "add_image_backend"}},
					},
				},
			},
		},
		{
			Title:    "Music",
			Subtitle: "Music connectors agents may make songs with.",
			Detail:   "Each one ticked is offered to agents as a tool of its own, generate_music_<connector>. Add a music connector under Admin > Services > Connectors, from its spec; it is tested and fixed there. A flow may use one as a step without it being offered here.",
			Body: ui.FormPanel{
				Source: "api/music",
				Fields: []ui.FormField{staleField, {Field: "agents", Label: "Offered to agents", Type: "checklist", Options: musicOptions(),
					Help: "None listed means no connector here makes music yet."}},
			},
		},
		{
			Title:    "Web Search",
			Subtitle: "Provider for the web_search tool.",
			Detail:   "DuckDuckGo is built in and needs no key. Any other engine is a connector that searches: add one from a template (Admin > Services > Templates has Brave, Serper, Tavily, Google Programmable Search and SearXNG) and pick it here, its key kept on it.",
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
			Detail:   "Signup approvals, scheduled deliveries, watcher alerts and the send_email tool. Leave Server blank for localhost:25. A mail API (Resend, Postmark and others) can send instead: add a connector that sends mail from Admin > Services > Templates and pick it under Send with.",
			Body: ui.FormPanel{
				Assist:    true,
				Source:    "api/mail",
				TestURL:   "api/mail/test",
				TestLabel: "Send test email",
				Fields:    mailFormFields(),
			},
		},
	}
}

// mailFormFields are the mail settings: what sends (SMTP, or a connector that
// sends mail through an API, or + Add another… for one from a template),
// then SMTP's server and sign-in, shown while SMTP sends, and the From
// address and Default Recipient, which both use.
func mailFormFields() []ui.FormField {
	choices := append([]ui.SelectOption{{Value: "smtp", Label: "SMTP (the server below)"}}, serviceProviderOptions("mail.send", "Send")...)
	smtp := "provider:smtp"
	fields := []ui.FormField{staleField,
		{Field: "provider", Label: "Send with", Type: "select", Options: append(choices, addAnother("mail.send")),
			Help: "This instance's SMTP server, or a connector that sends mail through an API, set up below it. + Add another… adds one from a template (Resend, Postmark, Mailgun)."}}
	fields = append(fields, connectorSettingsFields("mail.send", "", "provider")...)
	return append(fields,
		ui.FormField{Field: "server", Label: "SMTP Server", Type: "text",
			Placeholder: "smtp.gmail.com:587", ShowWhen: smtp,
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

// imageBackendsTable is every image backend under Image Generation's picker,
// each with the editor its Connectors row opens, so a backend's workflow,
// size and steps are changed from the page that chooses it. The row actions
// are the Connectors table's own (connectorEditRowActions and the rest): one
// editor per connector, reached from two places.
func imageBackendsTable() ui.Table {
	return ui.Table{
		Source: imageBackendsSource,
		RowKey: "name",
		Columns: []ui.Col{
			{Field: "title", Label: "Backend", Flex: 2},
			{Field: "role", Type: "badge", Badges: []ui.BadgeMapping{
				{Value: "default", Label: "Default", Color: "success"},
				{Value: "offered", Label: "Offered to agents", Color: "info"},
				{Value: "unused", Label: "Not offered", Color: "mute"},
			}},
			{Field: "does", Label: "Does", Mute: true},
			{Field: "size", Label: "Size", Mute: true},
			{Field: "steps", Label: "Steps", Mute: true},
			{Field: "summary", Label: "", Mute: true, Flex: 4, Line: 2},
			{Field: "last_error", Label: "", Mute: true, Flex: 4, Line: 2},
		},
		RowActions: append(append([]ui.RowAction{}, connectorEditRowActions()...),
			connectorSettingsAction(), connectorTestAction()),
		EmptyText: "No image backend yet. Add image backend… stands up a local ComfyUI or A1111; + Add another… above adds one from a template.",
	}
}

// imageFormFields builds the Image Generation settings: the connector that
// makes pictures, chosen from every one that does (a service giving
// image.generate or image.edit, or a peer's renderer), or Disabled.
// A new connector appears here with no edit.
func imageFormFields() []ui.FormField {
	var opts []ui.SelectOption
	for _, c := range ListConnectors(RootDB) {
		// Every image connector (choosing it here references it), and any
		// other backend already registered.
		if imageBackendListed(c) {
			label := connectorLabel(c)
			if _, waiting := service.PendingChange(RootDB, c.Name); waiting {
				label += " (changed, waiting to be accepted)"
			}
			opts = append(opts, ui.SelectOption{Value: c.Name, Label: label, Help: ConnectorSummary(c)})
		}
	}
	f := ui.FormField{Field: "provider", Label: "Make pictures with", Type: "select",
		Options: append(append([]ui.SelectOption{disabledOption}, opts...), addAnother("image.generate"))}
	if len(opts) == 0 {
		f.Help = "Nothing here makes pictures yet: + Add another… adds one from a template (Gemini, OpenAI, ComfyUI, Automatic1111), or connect a peer that lends image generation (Admin > Integrations > Peers)."
		return []ui.FormField{staleField, f}
	}
	f.Help = "The connector that makes pictures; + Add another… adds one from a template. Its settings (an address, a model, a key) are set here below it."
	f.Detail = "A peer that lends image generation is connected under Admin > Integrations > Peers. A connector changed by someone who is not an administrator is held, so no call reaches it: read the change on its row, then save here with it chosen to accept it."
	agents := ui.FormField{Field: "agents", Label: "Also offered to agents", Type: "checklist", Options: opts,
		Help:   "Other image connectors agents may choose by name, and peers borrowing image generation. The one above is always offered.",
		Detail: "Agents' image tools, and a backend's own generate_image_<name> tool, reach only the connector above and the ones ticked here. They offer the connector above as the default and these by name; a peer that borrows image generation from this machine may name them too."}
	fields := append([]ui.FormField{staleField, f}, connectorSettingsFields("image.generate", "", "provider")...)
	return append(fields, agents)
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
// under Admin > Services > Connectors, so there is one place each is kept.
func embeddingFormFields() []ui.FormField {
	choices := append(serviceProviderOptions("embed.text", "Embed"), EmbeddingProviderOptions()...)
	fields := []ui.FormField{staleField,
		{Field: "provider", Label: "Embed with", Type: "select",
			Options: append(append([]ui.SelectOption{disabledOption}, choices...), addAnother("embed.text")),
			Help:    "The connector or peer that makes embeddings; + Add another… adds one from a template. A connector's settings (its address, its model, a key) are set here below it.",
			Detail:  "A peer that lends embeddings is connected under Admin > Integrations > Peers. Changing what embeds, or its model, changes the embedding space: saving starts re-embedding the stored documents (progress and Stop under Maintenance, Vector index), and through a paid service that is billed."}}
	fields = append(fields, connectorSettingsFields("embed.text", "", "provider")...)
	return append(fields,
		// The prefixes belong to the MODEL, not to where it runs, so they stay
		// beside whichever connector or peer does the embedding.
		ui.FormField{Field: "query_prefix", Label: "Query prefix", Type: "text",
			Placeholder: "search_query: ",
			Help:        "Prepended to every search query before embedding. Asymmetric retrieval models need it.",
			Detail:      "e5 uses \"query: \", nomic-embed uses \"search_query: \", bge uses \"Represent this sentence for searching relevant passages: \". Leave it blank for symmetric models. It is included verbatim, trailing space and all, and is safe to change at any time.",
			ShowWhen:    "provider:!none",
			Presets: []ui.FieldPreset{
				{Label: "e5", Value: "query: ", Hint: "intfloat/e5 family"},
				{Label: "nomic", Value: "search_query: ", Hint: "nomic-embed-text"},
				{Label: "bge", Value: "Represent this sentence for searching relevant passages: ", Hint: "BAAI/bge v1.5 family"},
			}},
		ui.FormField{Field: "doc_prefix", Label: "Document prefix", Type: "text",
			Placeholder: "search_document: ",
			Help:        "Prepended to every chunk, fact and tool description before embedding.",
			Detail:      "e5 uses \"passage: \" and nomic-embed uses \"search_document: \"; bge and Qwen3-Embedding use none. Changing it changes the embedding space: saving starts re-embedding the stored documents, with its progress under Maintenance, Vector index.",
			ShowWhen:    "provider:!none",
			Presets: []ui.FieldPreset{
				{Label: "e5", Value: "passage: ", Hint: "intfloat/e5 family"},
				{Label: "nomic", Value: "search_document: ", Hint: "nomic-embed-text"},
			}},
	)
}

// transcribeFormFields builds the STT settings: on or off, and the connector
// or peer that transcribes. Its address, model and key are its own, set up,
// tested and fixed under Admin > Services > Connectors, as embeddings'
// are.
func transcribeFormFields() []ui.FormField {
	choices := append(serviceProviderOptions("speech.transcribe", "Transcribe"), TranscribeProviderOptions()...)
	fields := []ui.FormField{staleField,
		{Field: "provider", Label: "Transcribe with", Type: "select",
			Options: append(append([]ui.SelectOption{disabledOption}, choices...), addAnother("speech.transcribe")),
			Help:    "The connector or peer that transcribes; + Add another… adds one from a template. A connector's settings (its address, its model, a key) are set here below it.",
			Detail:  "A peer that lends transcription is connected under Admin > Integrations > Peers."}}
	return append(fields, connectorSettingsFields("speech.transcribe", "", "provider")...)
}

// speechFormFields builds the speech (TTS) settings: on or off, the
// connector or peer that speaks, and the voice, chosen from its list. Its
// address, model and key are its own, set up, tested and fixed under Admin >
// Extensions > Connectors, as transcription's are.
func speechFormFields() []ui.FormField {
	choices := append(serviceProviderOptions("speech.synthesize", "Speak"), SpeechProviderOptions()...)
	fields := []ui.FormField{staleField,
		{Field: "provider", Label: "Speak with", Type: "select",
			Options: append(append([]ui.SelectOption{disabledOption}, choices...), addAnother("speech.synthesize")),
			Help:    "The connector or peer that speaks; + Add another… adds one from a template. A connector's settings (its address, its model, a key) are set here below it.",
			Detail:  "A peer that lends speech is connected under Admin > Integrations > Peers."}}
	fields = append(fields, connectorSettingsFields("speech.synthesize", "", "provider")...)
	return append(fields,
		ui.FormField{Field: "voice", Label: "Voice", Type: "text",
			Placeholder: "af_heart",
			Help:        "Optional. A voice it offers, such as Kokoro's 'af_heart' or OpenAI's 'alloy'. Blank uses the connector's own default.",
			Detail:      "Save the connector or peer first, then click a chip to pick from its own list, and Preview voice to hear it before you save. One without a voice list shows no chips; type the voice instead.",
			ShowWhen:    "provider:!none",
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
	fields := []ui.FormField{staleField}
	if hasPeers {
		fields = append(fields, ui.FormField{
			Field: "source", Label: "Search on", Type: "select",
			Options: peers,
			Help:    "Search from this instance, or through a peer you have connected.",
			Detail:  "Peers live under Resource Sharing, then Peers. Borrowing a peer means this machine never holds the search API key.",
		})
	}
	// The engines, and the research sources apart: a research source is
	// chosen by "Use a research source…" and the list under it, never
	// offered as if it were an engine.
	research := researchNamed()
	var engines, sources []ui.SelectOption
	for _, o := range serviceProviderOptions("web.search", "Search") {
		if c, ok := GetConnector(RootDB, o.Value); ok && researchConnector(c, research) {
			sources = append(sources, o)
			continue
		}
		engines = append(engines, o)
	}
	opts := append([]ui.SelectOption{disabledOption, {Value: "duckduckgo", Label: "DuckDuckGo (built in, no key)"}}, engines...)
	if len(sources) > 0 {
		opts = append(opts, ui.SelectOption{Value: useResearchSource, Label: "Use a research source…"})
	}
	fields = append(fields,
		ui.FormField{Field: "provider", Label: "Search with", Type: "select",
			ShowWhen: local,
			Options:  append(opts, addAnother("web.search")),
			Help:     "The engine every search goes to; + Add another… adds one from a template (Brave, Serper, Tavily, Google, SearXNG). A connector's settings (its key, an address) are set here below it.",
			Detail:   "Disabled withdraws web_search (and find_image) from every agent, and anything else that searches is told it is off. Serper's engine also finds pictures, for agents' find_image. Use a research source… searches with one of the sources set up under Research Sources instead; the source picked here is not also asked as a research source, since it already answers web_search."},
	)
	if len(sources) > 0 {
		when := "provider:" + useResearchSource
		if local != "" {
			when = local + ";" + when
		}
		fields = append(fields, ui.FormField{Field: "research_source", Label: "Research source", Type: "select",
			ShowWhen: when, Options: sources,
			Help: "The research source every search goes to. Its settings are on its row under Admin > Services > Connectors."})
	}
	return append(fields, connectorSettingsFields("web.search", local, "provider")...)
}

// browseFormFields is the page-rendering picker. Unlike search or STT there is
// nothing to configure locally — the browser is either linked into the build or
// it is not — so this is one choice, and the section says so rather than
// showing an empty form when no peer offers rendering.
func browseFormFields() []ui.FormField {
	opts := append(append(BrowseProviderOptions(), serviceProviderOptions("page.render", "Render")...), addAnother("page.render"))
	help := "Render locally, on a peer, or through a connector; + Add another… adds a connector from a template (Firecrawl, Jina Reader). Borrowing means this machine never downloads or runs Chromium. Public web only, whichever renders."
	return append([]ui.FormField{
		staleField,
		{Field: "source", Label: "Render pages on", Type: "select", Options: opts, Help: help,
			Detail: "A peer that lends page rendering is connected under Admin > Integrations > Peers."},
	}, connectorSettingsFields("page.render", "", "source")...)
}
