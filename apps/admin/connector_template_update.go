package admin

import (
	"encoding/json"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/recipes"
	"github.com/cmcoffee/gohort/core/service"
)

// A connector made from a template follows its template's revisions on its
// own, while nobody has changed it.
//
// Update from template, a button, was the only way a connector got what its
// template learned (a job, a fixed request), and it could not tell a
// connector that was simply made by an older version of its template from
// one someone had fixed or edited: both just "differ from what the template
// writes now". Pressing it on the second kind replaced the fix.
//
// So the spec a template wrote a connector as is kept beside it
// (templateSpecTable). At start, a connector that still holds exactly that,
// and whose template now writes something else, is updated, the spec it had
// kept for one Undo; one that holds anything else was changed by a person or
// the Fix assistant, and keeps its button, which says it replaces the edits.
//
// A different API's version of a service is a different template, under its
// own id: nothing here moves a connector from one template to another.
const (
	// templateSpecTable is, by connector name, the spec its template last
	// wrote it as: at install, at an update, or when it was first seen
	// matching its template.
	templateSpecTable = "connector_template_specs"
	// templateUndoTable is, by connector name, the spec it held before an
	// automatic update, for its row's Undo.
	templateUndoTable = "connector_template_undo"
)

// templateWrote is the spec connector name's template last wrote it as.
func templateWrote(name string) (json.RawMessage, bool) {
	var raw json.RawMessage
	if RootDB == nil || !RootDB.Get(templateSpecTable, name, &raw) || len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

// asItsTemplateWrote reports whether conn holds the spec its template last
// wrote it as. Compared as parsed, so a spec stored before a field gained a
// default still reads as the same spec.
func asItsTemplateWrote(conn Connector) bool {
	wrote, ok := templateWrote(conn.Name)
	if !ok {
		return false
	}
	held, ok1 := canonicalSpec(conn.Spec)
	was, ok2 := canonicalSpec(wrote)
	return ok1 && ok2 && held == was
}

// noteTemplateSpecs records, for each templated connector that holds exactly
// what its template writes now and has no record yet, that spec as what its
// template wrote it as. This is how a connector made before the record
// existed, or made by any path that adds a template, comes to follow its
// template: from the first time it is seen matching it.
func noteTemplateSpecs() {
	if RootDB == nil {
		return
	}
	for _, conn := range ListConnectors(RootDB) {
		if conn.Kind != service.Kind || strings.TrimSpace(conn.Template) == "" {
			continue
		}
		if _, ok := templateWrote(conn.Name); ok {
			continue
		}
		if _, differs := templateUpdate(conn); differs {
			continue
		}
		if _, ok := recipes.UpdatableConnectorSpec(RootDB, conn.Template); ok {
			RootDB.Set(templateSpecTable, conn.Name, conn.Spec)
		}
	}
}

// updateFromTemplates is the start-up pass: each connector that still holds
// what its template last wrote, where the template now writes something
// else, takes the new spec. Its settings and key are kept (they live apart
// from the spec). It returns the connectors it updated.
func updateFromTemplates() []string {
	if RootDB == nil {
		return nil
	}
	noteTemplateSpecs()
	var updated []string
	for _, conn := range ListConnectors(RootDB) {
		now, differs := templateUpdate(conn)
		if !differs || !asItsTemplateWrote(conn) {
			continue
		}
		prev := conn.Spec
		conn.Spec = json.RawMessage(now)
		if err := SaveConnector(RootDB, conn); err != nil {
			Warn("[connectors] %s: its template %s changed, and the update did not save: %v", conn.Name, conn.Template, err)
			continue
		}
		RootDB.Set(templateUndoTable, conn.Name, prev)
		RootDB.Set(templateSpecTable, conn.Name, conn.Spec)
		acceptChanges(conn.Name)
		detail := "connector " + conn.Name + " updated from its template " + conn.Template + " (it had not been changed since the template wrote it); Undo on its row puts the spec back"
		Log("[connectors] %s", detail)
		RecordActivity(nil, activity.Settings, "", detail)
		updated = append(updated, conn.Name)
	}
	return updated
}

// templateUndone is connector conn's spec from before an automatic update,
// when there is one to go back to.
func templateUndone(conn Connector) (json.RawMessage, bool) {
	var raw json.RawMessage
	if RootDB == nil || !RootDB.Get(templateUndoTable, conn.Name, &raw) || len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

// handleConnectorTemplateUndo puts connector ?name= back as it was before
// its automatic update. It then no longer follows its template on its own:
// someone chose the older spec, and the next start must not undo the Undo.
// Update from template still offers the newer one.
func (a *AdminApp) handleConnectorTemplateUndo(c *jsonapi.Call, _ struct{}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	conn, ok := GetConnector(RootDB, name)
	if !ok {
		return nil, jsonapi.NotFound("no connector named " + name)
	}
	prev, ok := templateUndone(conn)
	if !ok {
		return nil, jsonapi.BadRequest(name + " has no automatic update to undo")
	}
	conn.Spec = prev
	if err := SaveConnector(RootDB, conn); err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	RootDB.Unset(templateUndoTable, name)
	RootDB.Unset(templateSpecTable, name)
	acceptChanges(name)
	adminChange(c.R, activity.Settings, "undid the automatic template update of connector %s", name)
	return map[string]any{"message": "Put back as it was. It no longer follows its template on its own; Update from template offers the newer one."}, nil
}

// templateUpdateConfirm is what Update from template asks first: plainly
// when the connector holds what its template wrote, and that it replaces
// the edits when it does not.
func templateUpdateConfirm(conn Connector) string {
	if asItsTemplateWrote(conn) {
		return "Update " + conn.Name + " to what its template writes now? Its settings and key are kept."
	}
	return "Update " + conn.Name + " from its template? It has been changed since its template wrote it (by hand or by Fix), and this replaces those changes with what the template writes now. Its settings and key are kept."
}

// templateRowState is what a connector's row says about its template: an
// update it can take (and the question its button asks), and an automatic
// update it can undo.
func templateRowState(conn Connector) (update bool, confirm string, undo bool) {
	if _, update = templateUpdate(conn); update {
		confirm = templateUpdateConfirm(conn)
	}
	_, undo = templateUndone(conn)
	return update, confirm, undo
}
