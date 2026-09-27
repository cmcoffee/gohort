package publish

// The Publishing targets section of Extensions, next to the API credentials
// a target publishes through.

import (
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/gohort/core/ui"
)

const (
	targetsAPI          = "/publish/api/targets"
	targetsSectionTitle = "Publishing targets"
)

func init() {
	// Extensions deep-links a section by its title's slug, so a surface with
	// no targets to offer can send the person straight to where one is made.
	docs.SetPublishTargetsSetupURL("/extensions#" + ui.SectionSlug(targetsSectionTitle))
	RegisterExtensionSection(ExtensionSectionEntry{
		Build: targetsExtensionSection,
		Head:  targetPillsHead,
		Order: 5, // beside the credentials it uses, ahead of machines and pipelines
	})
}

// credentialChoices are the integrations a user can publish through: their own
// API credentials and the shared ones they may use, by name, then the MCP
// servers that offer their tools to agents, as "mcp:<server>".
func credentialChoices(user string) []ui.SelectOption {
	seen := map[string]bool{}
	var names []string
	add := func(cs []SecureCredential) {
		for _, c := range cs {
			if c.Disabled || seen[c.Name] {
				continue
			}
			seen[c.Name] = true
			names = append(names, c.Name)
		}
	}
	add(Secure().ListUser(user))
	add(Secure().List())
	sort.Strings(names)
	out := make([]ui.SelectOption, 0, len(names))
	for _, n := range names {
		out = append(out, ui.SelectOption{Value: n, Label: n})
	}
	for _, m := range MCP().List() {
		if m.Enabled && m.ExposeTools {
			out = append(out, ui.SelectOption{Value: docs.MCPIntegrationPrefix + m.Name, Label: m.Name + " (MCP server)"})
		}
	}
	return out
}

// targetFormFields is the create and edit form.
func targetFormFields(user string) []ui.FormField {
	return []ui.FormField{
		{Field: "id", Type: "hidden"},
		{Field: "label", Label: "Name", Type: "text", Required: true, Placeholder: "Team blog",
			Help: "What you pick in Scribe's Publish list."},
		{Field: "desc", Label: "What it is", Type: "text", Placeholder: "Posts go up as drafts for review"},
		{Field: "uses", Label: "Publishes through", Type: "select", Options: []ui.SelectOption{
			{Value: "api", Label: "One of your integrations (API or MCP)"},
			{Value: "agent", Label: "One of your agents"},
		}},
		{Field: "credential", Label: "Integration", Type: "select", ShowWhen: "uses:api||!uses",
			Options: credentialChoices(user),
			Help:    "A publish holds only this integration: it cannot reach anything else.",
			Detail:  "Set API integrations up under API credentials above; their own address and allowed paths still apply. An MCP server is one an administrator connected with its tools offered to agents; a publish holds only that server's tools, without any that delete, remove or archive."},
		{Field: "agent", Label: "Agent", Type: "select", ShowWhen: "uses:agent", Options: AgentNameOptions(user),
			Help: "For a place with no API of its own: the agent is handed the document and your instruction."},
		{Field: "instructions", Label: "How to publish there", Type: "textarea", Rows: 5, Required: true,
			Placeholder: "Create a draft post with the document as its body. The title is the post title. Put it in the category given.",
			Help:        "What the publish does with the document: which endpoint, draft or live, where the answers below go.",
			Detail:      "The document, its title and the answers to the questions below are handed over with this. Name the API call if you know it (\"POST /wp-json/wp/v2/posts with status draft\"); otherwise describe the result you want and the pass works it out from the API."},
		{Field: "fields", Label: "Questions to settle", Type: "rows", AddLabel: "Add a question",
			Help:   "What a publish needs to know, like a category or visibility. The Publisher asks you only what the document does not already settle.",
			Detail: "For a pick-one question, list its options (comma-separated), or give an API path on the integration to fetch them from, optionally followed by the field of each item to show (\"/wp-json/wp/v2/categories name\"). Required ones are always settled before it publishes.",
			Columns: []ui.FormField{
				{Field: "label", Label: "Question", Type: "text", Placeholder: "Category", Width: 3},
				{Field: "type", Label: "Kind", Type: "select", Width: 2, Options: []ui.SelectOption{
					{Value: "text", Label: "Short text"}, {Value: "textarea", Label: "Long text"}, {Value: "select", Label: "Pick one"},
				}},
				{Field: "required", Label: "Required", Type: "select", Width: 1, Options: []ui.SelectOption{
					{Value: "", Label: "No"}, {Value: "yes", Label: "Yes"},
				}},
				{Field: "options", Label: "Options", Type: "text", OwnLine: true, ShowWhen: "type:select",
					Placeholder: "News, Guides, Releases"},
				{Field: "options_from", Label: "Or options from the API (path, then the field to show)", Type: "text", OwnLine: true, ShowWhen: "type:select",
					Placeholder: "/wp-json/wp/v2/categories name"},
			}},
	}
}

func targetsExtensionSection(r *http.Request, user string) (ui.Section, bool) {
	if strings.TrimSpace(user) == "" {
		return ui.Section{}, false
	}
	form := func(source string) ui.FormPanel {
		return ui.FormPanel{
			Source: source, PostURL: targetsAPI, SubmitLabel: "Save",
			Invalidate: []string{targetsAPI},
			Fields:     targetFormFields(user),
		}
	}
	return ui.Section{
		Title:    targetsSectionTitle,
		Wide:     true,
		Subtitle: "Places your documents can be published: one of your API integrations, or an agent, plus how to publish there.",
		Detail: "Each target appears in Scribe's Publish list. Picking one opens the Publisher, which settles the questions below with you (only the ones the document does not answer), then hands the document, your instruction and the answers to a pass that holds only that target's integration. Where it landed is recorded against the document, so publishing it there again updates the same page. " +
			"Switch agents on under Agents to let them publish there too, through a publish tool.",
		Body: ui.Stack{Children: []ui.Component{
			ui.ModalButton{
				Label: "New target", Title: "A place to publish",
				Subtitle: "An API integration or an agent, and how to publish there.",
				Width:    "620px",
				Body:     form(""),
			},
			ui.Table{
				Source:    targetsAPI,
				RowKey:    "id",
				EmptyText: "No publishing targets yet. Make one from an API integration you have, like a blog or a wiki.",
				Columns: []ui.Col{
					{Field: "label", Label: "Name"},
					{Field: "uses", Label: "Publishes through", Mute: true, Flex: 2},
					{Field: "asks", Label: "Asks", Mute: true, Flex: 2},
					{Field: "agents", Label: "Agents that may use it", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{
					ui.ModalAction("Edit", form(targetsAPI+"/{id}")),
					{Type: "button", Label: "Agents", Method: "client", PostTo: "publish_target_agents", Compact: true},
					{Type: "button", Label: "Delete", Method: "DELETE", PostTo: targetsAPI + "/{id}", Variant: "danger", Compact: true,
						Confirm: "Delete this publishing target? Documents already published there stay where they are."},
				},
			},
		}},
	}, true
}

// targetPillsHead registers the Agents row action: the framework's pill
// renderer pointed at the target's endpoint.
const targetPillsHead = `<script>
(function(){
  function register(){
    if (!window.uiRegisterClientAction || !window.uiOpenSimpleModal) { setTimeout(register, 50); return; }
    window.uiRegisterClientAction('publish_target_agents', function(ctx){
      var r = (ctx && ctx.record) || {};
      if (!r.id) return;
      var base = '` + targetsAPI + `/' + encodeURIComponent(r.id) + '/agents';
      var reload = ctx && ctx.reload;
      window.uiOpenSimpleModal({
        title: 'Agents that may publish to ' + (r.label || 'this target'),
        width: '560px',
        mount: function(body){
          var host = document.createElement('div');
          body.appendChild(host);
          window.uiRenderScopePills(host, {
            load: function(){
              return fetch(base + '?pills=1', {cache:'no-store'}).then(function(res){
                if (!res.ok) return res.text().then(function(t){ throw new Error(t || ('HTTP ' + res.status)); });
                return res.json();
              });
            },
            toggle: function(key, on){
              return fetch(base, {method:'POST', headers:{'Content-Type':'application/json'},
                body: JSON.stringify({target: key, on: on})}).then(function(res){
                if (!res.ok) return res.text().then(function(t){ throw new Error(t || ('HTTP ' + res.status)); });
                if (reload) reload();
              });
            }
          });
        }
      });
    });
  }
  register();
})();
</script>`
