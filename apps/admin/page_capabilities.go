package admin

import (
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// capabilitiesSections is the capabilities part of the admin page: System Hooks, Embeddings, Audio Transcription (STT), Speech (TTS), Media Generation, Web Search, Page Rendering (Browser), Mail (SMTP), Network Timeouts.
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
			Title:    "Media Generation",
			Subtitle: "What makes pictures, music and other media for agents.",
			Detail: "Every connector that makes media is listed here, banded by what it makes: image backends (Gemini or OpenAI added from their templates, a local ComfyUI or Automatic1111, a peer's renderer), music, and any other kind a connector declares it makes, such as video or 3D models.\n\n" +
				"The switch under each row's name says how it is used. Images: Primary is the default, what agents' image tools use unless they ask for another, and its row is badged Default; Secondary can be asked for by name; Disabled is not used. Each image row's Used for says the image tool's jobs it is used for. A backend that both makes and edits pictures (Gemini, OpenAI) has a choice there: generating, editing and blending (the default), editing and blending, or generating. One that does one job (a ComfyUI workflow, an A1111 mode) says that job: its Edit setup, under Advanced, changes it.\n\n" +
				"Music and the other kinds: Secondary gives each its own agent tool, generate_<kind>_<connector> (generate_music_<connector> for music). A flow may use one as a step without it being offered here.\n\n" +
				"A choice that no longer reads (it names a connection that is not here, or one named the old way) is listed first, under Needs setting again, with why; Remove takes it off, and choosing a backend below sets it again.\n\n" +
				"A connector's own settings (its key, its model) are on its row's Settings, and its key can come from Services > Keystore. Advanced opens its editor, the same one its row under Connectors opens. Test makes a sample.\n\n" +
				"Add AI generator lists the templates by what they make (images: Gemini, OpenAI, a local ComfyUI or A1111; and music or another kind once a template for it is added under Services > Templates). A kind with no template yet is added as a connector from its spec under Connectors.",
			Body: ui.Stack{
				Children: []ui.Component{
					mediaBackendsTable(),
					ui.Toolbar{
						Actions: []ui.ToolbarAction{
							{Label: "Add AI generator…", Method: "client", URL: "connector_add_another", Data: "media", Variant: "primary"},
						},
					},
				},
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

// mediaBackendsTable is every media connector, banded by what it makes, each
// with the editor its Connectors row opens, so a backend's workflow, size and
// steps are changed from the page that chooses it. The row actions after the
// role buttons are the Connectors table's own (connectorEditRowActions and
// the rest): one editor per connector, reached from two places.
func mediaBackendsTable() ui.Table {
	return ui.Table{
		Source:  mediaBackendsSource,
		RowKey:  "name",
		GroupBy: "group",
		Columns: []ui.Col{
			{Field: "title", Label: "Connector", Flex: 2},
			{Field: "is_default", Type: "badge", Badges: []ui.BadgeMapping{{Value: true, Label: "Default", Color: "success"}}},
			{Field: "does", Label: "Does", Mute: true},
			{Field: "stale", Label: "", Flex: 4, Line: 2},
			{Field: "used_for", Label: "", Mute: true, Line: 2},
			{Field: "size", Label: "Size", Mute: true},
			{Field: "steps", Label: "Steps", Mute: true},
			{Field: "summary", Label: "", Mute: true, Flex: 4, Line: 2},
			{Field: "last_error", Label: "", Mute: true, Flex: 4, Line: 2},
		},
		RowActions: append(append([]ui.RowAction{
			{Type: "button", Label: "Remove", PostTo: mediaBackendsSource + "/remove?name={name}", Method: "POST", OnlyIf: "is_stale", Variant: "danger",
				Confirm: "Remove it from Media Generation? It no longer reads, so nothing uses it now."},
			// The row's own settings lead its second line, under its name.
			{Type: "segmented", Field: "role", Options: imageUseOptions, PostTo: mediaBackendsSource + "/role", OnlyIf: "is_image", Line: 2},
			{Type: "segmented", Field: "role", Options: offerUseOptions, PostTo: mediaBackendsSource + "/role", OnlyIf: "offer_only", Line: 2},
			{Type: "select", Label: "Used for", Field: "uses", Options: usedForOptions, PostTo: mediaBackendsSource + "/uses", OnlyIf: "both_ways", Line: 2},
		}, connectorEditRowActions()...), connectorSettingsAction(), connectorTestAction()),
		EmptyText: "Nothing makes media yet. Add AI generator… below adds Gemini, OpenAI, a local ComfyUI or A1111, and more.",
	}
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
