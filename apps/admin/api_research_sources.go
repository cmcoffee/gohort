package admin

// Research Sources: the tie-in that puts search connectors to use for
// research and for agents. A source is a search connector, the topics
// research asks it on (or always), and, when it is offered to agents, the
// name and description query_source shows for it. Adding one that is on
// puts its connector to use; the connector itself stays how the service is
// reached and read, under Connectors.

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
)

func (a *AdminApp) registerResearchSourcesRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/api/research-sources", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleResearchSources, a.gate()),
		"POST":   a.researchSourcePost(),
		"DELETE": jsonapi.Delete(a.db, a.handleResearchSourceDelete, a.gate()),
	}.Serve)
}

// researchSourceForm is a source as its form reads and saves it.
type researchSourceForm struct {
	Connector       string   `json:"connector"`
	Topics          []string `json:"topics"`
	Always          bool     `json:"always"`
	Offer           bool     `json:"offer"`
	ToolName        string   `json:"tool_name"`
	ToolDescription string   `json:"tool_description"`
	Off             bool     `json:"off"`
}

func researchFormOf(rs service.ResearchSource) researchSourceForm {
	return researchSourceForm{Connector: rs.Connector, Topics: rs.Topics, Always: rs.Always, Offer: rs.ToolName != "",
		ToolName: rs.ToolName, ToolDescription: rs.ToolDescription, Off: rs.Off}
}

// handleResearchSources is every source as its row reads (?name= one
// source, as its form reads).
func (a *AdminApp) handleResearchSources(c *jsonapi.Call) (any, error) {
	if name := strings.TrimSpace(c.R.URL.Query().Get("name")); name != "" {
		rs, ok := service.GetResearchSource(RootDB, name)
		if !ok {
			return nil, jsonapi.Status(http.StatusNotFound, "no research source on "+name)
		}
		return researchFormOf(rs), nil
	}
	rows := []map[string]any{}
	for _, rs := range service.ListResearchSources(RootDB) {
		asked := "on " + strings.Join(rs.Topics, ", ")
		switch {
		case rs.Always:
			asked = "on every research run"
		case len(rs.Topics) == 0:
			asked = "not by research"
		}
		agents := "not offered to agents"
		if rs.ToolName != "" {
			agents = "offered to agents as " + rs.ToolName
		}
		summary := ""
		if conn, ok := GetConnector(RootDB, rs.Connector); ok {
			summary = ConnectorSummary(conn)
		} else {
			summary = "its connector is gone"
		}
		rows = append(rows, map[string]any{"connector": rs.Connector, "on": !rs.Off,
			"use": "Research asks it " + asked + "; " + agents + ".", "summary": summary})
	}
	return rows, nil
}

// researchSourcePost tells a switch (?action=on|off&name=, no body) from a
// save of the whole record.
func (a *AdminApp) researchSourcePost() http.HandlerFunc {
	toggle := jsonapi.Post(a.db, a.handleResearchSourceSwitch, a.gate())
	save := jsonapi.Post(a.db, a.handleResearchSourceSave, a.gate())
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action") != "" {
			toggle(w, r)
			return
		}
		save(w, r)
	}
}

// handleResearchSourceSwitch turns a source on, which puts its connector to
// use, or off.
func (a *AdminApp) handleResearchSourceSwitch(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	rs, ok := service.GetResearchSource(RootDB, q.Get("name"))
	if !ok {
		return nil, jsonapi.Status(http.StatusNotFound, "no such research source")
	}
	rs.Off = q.Get("action") == "off"
	if err := service.SaveResearchSource(RootDB, rs); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	adminChange(c.R, activity.Settings, "turned research source %s %s", rs.Connector, q.Get("action"))
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleResearchSourceSave saves a source, which puts its connector to use
// when it is on.
func (a *AdminApp) handleResearchSourceSave(c *jsonapi.Call, req researchSourceForm) (any, error) {
	rs := service.ResearchSource{Connector: req.Connector, Topics: req.Topics, Always: req.Always, Off: req.Off}
	if req.Offer {
		rs.ToolName, rs.ToolDescription = req.ToolName, req.ToolDescription
		if strings.TrimSpace(rs.ToolName) == "" {
			rs.ToolName = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(req.Connector)), "-", "_") + "_search"
		}
	}
	if err := service.SaveResearchSource(RootDB, rs); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	adminChange(c.R, activity.Settings, "saved research source %s", rs.Connector)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleResearchSourceDelete stops asking a connector as a research source.
func (a *AdminApp) handleResearchSourceDelete(c *jsonapi.Call, _ struct{}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	service.DeleteResearchSource(RootDB, name)
	adminChange(c.R, activity.Settings, "removed research source %s", name)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// researchSourceOptions are the search connectors a source can be: every
// one that gives web.search, since choosing it here is what references it.
func researchSourceOptions() []ui.SelectOption {
	var out []ui.SelectOption
	for _, c := range ListConnectors(RootDB) {
		if c.Kind == service.Kind && service.Provides(c, "web.search") {
			out = append(out, ui.SelectOption{Value: c.Name, Label: c.Title(), Help: ConnectorSummary(c)})
		}
	}
	return out
}
