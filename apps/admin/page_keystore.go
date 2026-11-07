package admin

import (
	"github.com/cmcoffee/gohort/core/keystore"
	"github.com/cmcoffee/gohort/core/ui"
)

// keystoreSections is the Keystore part of the admin page: one key per
// provider account, kept once and named by everything that talks to it.
func (a *AdminApp) keystoreSections() []ui.Section {
	return []ui.Section{{
		Title:    "Keystore",
		Subtitle: "Provider keys, kept once and used by every model and connector that talks to that provider.",
		Detail: "Add a key here for each provider account you use: a Google Gemini key, an OpenAI key, an Anthropic key, or the AWS sign-in Bedrock uses. A model tier or a connector that talks to that provider then names the key instead of holding a copy of it, so a key is typed once and rotated in one place.\n\n" +
			"A key is sent only to the addresses it is given for. Changing those addresses means giving the key again. The key is never shown back: its row says which one is kept and when it was given.\n\n" +
			"The first key typed for a provider anywhere (a connector's settings, a model tier) lands here by itself, and the next connector or model for that provider is offered it. A key typed where one already fits stays that connector's or model's own. Keys kept before the Keystore are listed below its entries, held where they are, with Move to Keystore.\n\n" +
			"A key something uses cannot be deleted: its row says what uses it. Keys agents' API tools call are a different store, under API Credentials.",
		Body: ui.Stack{Children: []ui.Component{
			ui.Table{
				Source: keystoreSource,
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "name", Flex: 2},
					{Field: "kind", Type: "badge", Label: "", Badges: []ui.BadgeMapping{
						{Value: "Key", Label: "Key", Color: "info"},
						{Value: "AWS signing", Label: "AWS signing", Color: "info"},
						{Value: "Held by a connector", Label: "Held by a connector", Color: "mute"},
						{Value: "Held by a model tier", Label: "Held by a model tier", Color: "mute"},
					}},
					{Field: "key", Label: "", Mute: true, Flex: 3},
					{Field: "addresses", Label: "", Mute: true, Flex: 4, Line: 2},
					{Field: "used_by", Label: "", Mute: true, Flex: 4, Line: 2},
				},
				RowActions: []ui.RowAction{
					ui.ModalActionIf("Edit", "entry", "", ui.FormPanel{
						Source:      keystoreSource + "?id={id}",
						PostURL:     keystoreSource,
						Method:      "POST",
						SubmitLabel: "Save",
						Invalidate:  []string{keystoreSource},
						Fields:      keystoreFields(true),
					}),
					{Type: "button", Label: "Move to Keystore", PostTo: keystoreSource + "/move?holder={id}", Method: "POST", OnlyIf: "held", Variant: "primary",
						Confirm: "Move this key into the Keystore? It becomes an entry (or joins one already holding it) that other connectors and models talking to this provider can pick, and this one names it from now on."},
					{Type: "button", Label: "Delete", PostTo: keystoreSource + "?id={id}", Method: "DELETE", OnlyIf: "entry", HideIf: "in_use",
						Confirm: "Delete this key? It is removed from this instance; the account at the provider is not changed.", Variant: "danger"},
				},
				EmptyText: "No keys kept yet. Add one below for each provider account you use.",
			},
			ui.ModalButton{
				Label:    "Add a key",
				Title:    "Add a key",
				Subtitle: "One per provider account. Pick the provider to fill in where its key may be sent.",
				Variant:  "primary",
				Width:    "640px",
				Body: ui.FormPanel{
					PostURL:        keystoreSource,
					Method:         "POST",
					SubmitLabel:    "Add key",
					Invalidate:     []string{keystoreSource},
					Templates:      keystoreTemplates(),
					TemplatesLabel: "Provider",
					Fields:         keystoreFields(false),
				},
			},
		}},
	}}
}

// keystoreTemplates are the big providers, so their name and address are
// one pick away.
func keystoreTemplates() []ui.FormTemplate {
	key := func(name, addr string) ui.FormTemplate {
		return ui.FormTemplate{Label: name, Values: map[string]any{"name": name, "kind": keystore.KindKey, "addresses": []string{addr}}}
	}
	return []ui.FormTemplate{
		key("Google Gemini", "https://generativelanguage.googleapis.com"),
		key("OpenAI", "https://api.openai.com"),
		key("Anthropic", "https://api.anthropic.com"),
		{Label: "Amazon Bedrock", Values: map[string]any{"name": "Amazon Bedrock", "kind": keystore.KindAWS, "addresses": []string{keystore.BedrockAddress}}},
	}
}

// keystoreFields is an entry's form. Its kind is fixed once it is made.
func keystoreFields(editing bool) []ui.FormField {
	kind := ui.FormField{Field: "kind", Label: "Kind", Type: "select", Options: []ui.SelectOption{
		{Value: keystore.KindKey, Label: "A key (an API key or token)"},
		{Value: keystore.KindAWS, Label: "AWS signing (Bedrock)"},
	}, Help: "Bedrock signs each request with an AWS key pair or a profile on this machine; every other provider takes a key."}
	keepHelp := ""
	if editing {
		kind = ui.FormField{Field: "kind", Label: "Kind", Type: "readonly"}
		keepHelp = " Leave it blank to keep the one kept."
	}
	fields := []ui.FormField{
		{Field: "id", Type: "hidden"},
		{Field: "name", Label: "Name", Type: "text", Required: true, SingleLine: true, Placeholder: "e.g. Google Gemini",
			Help: "What model tiers and connectors list it as: usually the provider, or the account if you keep more than one."},
		kind,
		{Field: "addresses", Label: "May be sent to", Type: "tags", Required: true,
			Help:   "The provider's address, such as https://api.openai.com, or bedrock for Bedrock in any region.",
			Detail: "Only a model tier or connector whose address is one of these can use this key. A key is never sent anywhere else, and changing these addresses means giving the key again."},
		{Field: "key", Label: "Key", Type: "password", ShowWhen: "kind:" + keystore.KindKey, SingleLine: true,
			Help: "Kept encrypted on this instance and never shown back." + keepHelp},
		{Field: "aws_pair", Label: "Access key pair", Type: "password", ShowWhen: "kind:" + keystore.KindAWS, SingleLine: true, Placeholder: "ACCESS_KEY_ID:SECRET_ACCESS_KEY",
			Help: "An IAM key pair, written as the key ID and the secret with a colon between. Or leave it blank and name a profile below." + keepHelp},
		{Field: "profile", Label: "AWS profile", Type: "text", ShowWhen: "kind:" + keystore.KindAWS, SingleLine: true, Placeholder: "e.g. work",
			Help: "A profile in this machine's AWS configuration (an SSO sign-in, say), used when no key pair is given."},
	}
	if editing {
		fields = append(fields, ui.FormField{Field: "kept", Label: "Kept now", Type: "readonly"})
	}
	return fields
}
