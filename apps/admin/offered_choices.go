package admin

// Names no longer offered (core/service/offered.go): each value in use that
// its provider no longer offers (a Kokoro voice after a move to Chatterbox,
// a model the server stopped listing), where it is used, and Change to... to
// give every one of those places a value the provider offers now. Nothing is
// changed until the owner picks; in the meantime speech falls back to the
// default voice rather than to text.

import (
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
)

const offeredChoicesSource = "api/offered-choices"

// offeredChoiceRow is one stale name on the page.
type offeredChoiceRow struct {
	ID    string `json:"_id"`
	What  string `json:"what"`
	Name  string `json:"name"`
	Where string `json:"where"`
}

func (a *AdminApp) registerOfferedChoiceRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/"+offeredChoicesSource, jsonapi.Get(a.db, a.handleOfferedChoices, a.gate()))
	sub.HandleFunc("/"+offeredChoicesSource+"/options", jsonapi.Get(a.db, a.handleOfferedChoiceOptions, a.gate()))
	sub.HandleFunc("/"+offeredChoicesSource+"/map", jsonapi.Post(a.db, a.handleOfferedChoiceMap, a.gate()))
}

// handleOfferedChoices asks each provider now: the page is where the
// question is asked, and the dashboard reads what it found.
func (a *AdminApp) handleOfferedChoices(c *jsonapi.Call) ([]offeredChoiceRow, error) {
	out := []offeredChoiceRow{}
	for _, sc := range service.StaleChoices(c.R.Context()) {
		out = append(out, offeredChoiceRow{ID: sc.Key, What: sc.Label, Name: sc.Value, Where: strings.Join(sc.Uses, "; ")})
	}
	return out, nil
}

func (a *AdminApp) handleOfferedChoiceOptions(c *jsonapi.Call) ([]ui.SelectOption, error) {
	values, err := service.StaleChoiceOptions(c.R.Context(), c.R.URL.Query().Get("id"))
	if err != nil {
		return nil, jsonapi.NotFound(err.Error())
	}
	out := []ui.SelectOption{}
	for _, v := range values {
		out = append(out, ui.SelectOption{Value: v, Label: v})
	}
	return out, nil
}

func (a *AdminApp) handleOfferedChoiceMap(c *jsonapi.Call, in struct {
	To string `json:"to"`
}) (map[string]any, error) {
	n, err := service.MapStaleChoice(c.R.Context(), c.R.URL.Query().Get("id"), in.To)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"changed": n}, nil
}

// offeredChoicesSection is the page's section, on the Services tab beside
// Connectors.
func offeredChoicesSection() ui.Section {
	return ui.Section{
		Title:    service.StaleChoicesPage,
		Subtitle: "Voices and models in use that the server behind them no longer offers, after it was swapped (Kokoro for Chatterbox, say). Change each to one it offers, everywhere it is used at once.",
		Detail: "A setting that names something a provider offers (a voice, a model) holds just a name, and the next provider may not have it: a call naming it is refused. This asks each provider what it offers now and lists the names in use it does not, with every place each is used.\n\n" +
			"Change to... gives all of those places a name the provider offers. Nothing is changed until you pick. Until then speech uses the default voice rather than sending text, and the log says when it did.\n\n" +
			"A provider with no list of what it offers cannot be checked, and is not listed here.",
		Body: ui.Table{
			Source:    offeredChoicesSource,
			RowKey:    "_id",
			EmptyText: "Every voice and model in use is one its provider offers.",
			Columns: []ui.Col{
				{Field: "name", Label: "Name"},
				{Field: "what", Label: "What it is", Mute: true},
				{Field: "where", Label: "Used by", Mute: true, Flex: 2},
			},
			RowActions: []ui.RowAction{ui.ModalAction("Change to...", ui.FormPanel{
				PostURL:     offeredChoicesSource + "/map?id={_id}",
				Method:      "POST",
				SubmitLabel: "Change",
				Fields: []ui.FormField{{
					Field: "to", Type: "select", Label: "Use instead",
					OptionsSource: offeredChoicesSource + "/options?id={_id}",
					Help:          "What the provider offers now. Every place listed is changed to it.",
				}},
			})},
		},
	}
}
