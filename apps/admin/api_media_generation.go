package admin

// Media Generation: every connector that makes media, in one list banded by
// what it makes. Images keep their default, their offered list and what a
// backend that both makes and edits is used for (ImageTable); music
// connectors are offered to agents (MusicTable); a media.generate connector,
// of whatever kind it declares, is offered to agents (MediaTable). Nothing is
// stored differently from the separate pages these replace.

import (
	"net/http"
	"slices"
	"sort"
	"strings"
	"unicode"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
)

// mediaBackendsSource is the list's address, named once because the edits
// made from any connector row reload it (connectorListSources), and a reload
// names its source exactly.
const mediaBackendsSource = "api/media-backends"

func (a *AdminApp) registerMediaGenerationRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/"+mediaBackendsSource, jsonapi.Get(a.db, a.handleMediaBackends, a.gate()))
	sub.HandleFunc("/"+mediaBackendsSource+"/role", jsonapi.Post(a.db, a.handleMediaBackendRole, a.gate()))
	sub.HandleFunc("/"+mediaBackendsSource+"/default", jsonapi.Post(a.db, a.handleMediaBackendDefault, a.gate()))
	sub.HandleFunc("/"+mediaBackendsSource+"/remove", jsonapi.Post(a.db, a.handleMediaBackendRemove, a.gate()))
	sub.HandleFunc("/"+mediaBackendsSource+"/uses", jsonapi.Post(a.db, a.handleImageBackendUses, a.gate()))
	sub.HandleFunc("/"+imageCheckingSource, jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleImageChecking, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleImageCheckingSave, a.gate()),
	}.Serve)
}

// imageCheckingSource is Media Generation's Checking setting.
const imageCheckingSource = "api/image-checking"

// imageCheckingForm is the Checking setting as its form reads and saves it.
type imageCheckingForm struct {
	Checking string `json:"checking"`
}

// imageCheckingFields is the Checking setting's one field.
func imageCheckingFields() []ui.FormField {
	return []ui.FormField{{Field: "checking", Label: "Checking", Type: "select",
		Options: []ui.SelectOption{
			{Value: ImageCheckQuick, Label: "Quick: render it, no extra checks, 1 picture"},
			{Value: ImageCheckStandard, Label: "Standard: face pass and a quick check, up to 2 pictures"},
			{Value: ImageCheckThorough, Label: "Thorough: face pass and a careful check, up to 3 pictures"},
		},
		Help: "How much an edit from source pictures is checked before its result is given, and how many pictures the same sources may give before it gives up. Agents can ask for another level on one call.",
		Detail: "A picture made from a prompt alone is not checked at any level. An edit from source pictures (a blend, a face onto a scene) is: Standard runs a second render over the faces " +
			"and has separate looks describe the result and each source, which a quick judge compares; Thorough has that judge think it over, about 15 seconds more. " +
			"Quick does neither: the render, and the agent's own look at it. When a check finds a source lost, the agent may try again, up to the level's limit " +
			"(counted per conversation, for the same source pictures), and then must deliver the best result and say what did not come through."}}
}

// handleImageChecking is the Checking setting.
func (a *AdminApp) handleImageChecking(c *jsonapi.Call) (any, error) {
	return imageCheckingForm{Checking: ImageChecking()}, nil
}

// handleImageCheckingSave stores the Checking setting.
func (a *AdminApp) handleImageCheckingSave(c *jsonapi.Call, f imageCheckingForm) (any, error) {
	level, ok := ImageCheckLevel(f.Checking)
	if !ok {
		return nil, jsonapi.BadRequest("checking is quick, standard or thorough")
	}
	if a.db != nil {
		a.db.Set(ImageTable, "checking", level)
	}
	adminChange(c.R, activity.Settings, "image checking: %s", level)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// usedForOptions are the Used for choices on a row for an image backend
// that both makes and edits pictures: the image tool's jobs it is used for.
// The values are what the row sends; "both" is stored as ImageUseBoth.
var usedForOptions = []ui.SelectOption{
	{Value: "both", Label: "Generating, editing and blending"},
	{Value: ImageUseEdit, Label: "Editing and blending"},
	{Value: ImageUseGenerate, Label: "Generating"},
}

// oneJobOptions are the jobs a backend that does one can show in its row's
// Used for, greyed: a ComfyUI workflow or an A1111 mode does one job, set up
// with it (Advanced, Edit setup), so its row says it rather than offering a
// choice. The first three are Used for's own words.
var oneJobOptions = []ui.SelectOption{
	{Value: "both", Label: "Generating, editing and blending"},
	{Value: ImageUseEdit, Label: "Editing and blending"},
	{Value: ImageUseGenerate, Label: "Generating"},
	{Value: ComfyTypeBlend, Label: "Blending"},
	{Value: ComfyTypeCustom, Label: "Its own workflow"},
}

// oneJob is the job of an image backend that does one, as oneJobOptions
// names it. A connector's capability says whether it makes or edits (an
// A1111 mode reads as a workflow, whose outline always says generate); its
// outline (does) adds a ComfyUI workflow's blend or custom.
func oneJob(does string, gen, edit bool) string {
	switch {
	case does == ComfyTypeBlend || does == ComfyTypeCustom:
		return does
	case edit:
		return ImageUseEdit
	case gen:
		return ImageUseGenerate
	case does == ComfyTypeEdit:
		return ImageUseEdit
	case does == "generate and edit":
		return "both"
	}
	return ImageUseGenerate
}

// usedForValue is the Used for choice use (ImageBackendUse) reads as.
func usedForValue(use string) string {
	if use == ImageUseBoth {
		return "both"
	}
	return use
}

// handleMediaBackends is every media connector as its row reads: the image
// backends, then the music connectors, then each other kind, banded.
func (a *AdminApp) handleMediaBackends(c *jsonapi.Call) (any, error) {
	rows := []imageBackendRow{}
	for _, r := range a.imageRows() {
		r.Group, r.IsImage = "Images", true
		r.Use, r.CanMakeDefault = "offered", r.Role == "offered"
		if r.Role == "unused" {
			r.Use = "unused"
		}
		conn, ok := GetConnector(RootDB, r.Name)
		gen := ok && conn.Kind == service.Kind && service.Provides(conn, "image.generate")
		edit := ok && conn.Kind == service.Kind && service.Provides(conn, "image.edit")
		if gen && edit {
			r.BothWays, r.Uses = true, usedForValue(ImageBackendUse(r.Name))
		} else {
			r.OneJob, r.Job = true, oneJob(r.Does, gen, edit)
		}
		// An image row says its job as Used for, a choice or the one it does;
		// Does is for the other kinds, where it is what they make.
		r.Does = ""
		rows = append(rows, r)
	}
	var music, other []imageBackendRow
	for _, conn := range ListConnectors(RootDB) {
		if conn.Kind != service.Kind {
			continue
		}
		switch {
		case service.Provides(conn, "music.generate"):
			music = append(music, a.offeredRow(conn, service.MusicTable, "Music", "music"))
		case service.Provides(conn, "media.generate"):
			kind := service.MediaKindOf(conn)
			other = append(other, a.offeredRow(conn, service.MediaTable, bandName(kind), kind))
		}
	}
	rank := map[string]int{"offered": 0, "unused": 1}
	sort.SliceStable(music, func(i, j int) bool { return rank[music[i].Role] < rank[music[j].Role] })
	sort.SliceStable(other, func(i, j int) bool {
		if other[i].Group != other[j].Group {
			return other[i].Group < other[j].Group
		}
		return rank[other[i].Role] < rank[other[j].Role]
	})
	out := append([]imageBackendRow{}, staleRows()...)
	return append(append(append(out, rows...), music...), other...), nil
}

// staleBand is the band of choices that no longer read, drawn first.
const staleBand = "Needs setting again"

// mediaTableJob is what a Media Generation table offers agents, as a stale
// row says it.
var mediaTableJob = map[string]string{ImageTable: "images", service.MusicTable: "music", service.MediaTable: "media"}

// staleRows are the choices Media Generation holds that no longer read
// (service.StaleMediaChoices), one row per name with every reason, so the
// page says why its dashboard card sent the reader here, and Remove clears
// the name. A name stale nowhere gives no row.
func staleRows() []imageBackendRow {
	var out []imageBackendRow
	at := map[string]int{}
	for _, st := range service.StaleMediaChoices(RootDB) {
		why := "Offered to agents for " + mediaTableJob[st.Table] + ": " + st.Why + "."
		if st.Role == "default" {
			why = "The default image backend: " + st.Why + "."
		}
		if i, seen := at[st.Name]; seen {
			out[i].Stale += " " + why
			continue
		}
		at[st.Name] = len(out)
		out = append(out, imageBackendRow{Name: st.Name, Title: st.Name, Group: staleBand, Role: st.Role, Stale: why, IsStale: true})
	}
	return out
}

// dropStale takes name off every Media Generation choice where it no longer
// reads, answering whether it held one.
func (a *AdminApp) dropStale(name string) bool {
	dropped := false
	for _, st := range service.StaleMediaChoices(RootDB) {
		if st.Name != name || a.db == nil {
			continue
		}
		dropped = true
		if st.Role == "default" {
			a.db.Set(st.Table, "provider", "")
			continue
		}
		var agents []string
		a.db.Get(st.Table, "agents", &agents)
		a.db.Set(st.Table, "agents", slices.DeleteFunc(agents, func(n string) bool { return n == name }))
	}
	return dropped
}

// rowTitle is connector conn as its row names it, saying when a change
// waits on it: a row's role buttons accept it, as the page's save did.
func rowTitle(conn Connector) string {
	if _, waiting := service.PendingChange(RootDB, conn.Name); waiting {
		return conn.Title() + " (changed, waiting to be accepted)"
	}
	return conn.Title()
}

// bandName is a media kind as a band's heading: "3D model", "Video".
func bandName(kind string) string {
	r := []rune(strings.TrimSpace(kind))
	if len(r) == 0 {
		return "Media"
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// offeredRow is a music or media connector's row: offered to agents (its
// table's "agents") or not, with the shared connector actions.
func (a *AdminApp) offeredRow(conn Connector, table, group, does string) imageBackendRow {
	var agents []string
	if a.db != nil {
		a.db.Get(table, "agents", &agents)
	}
	row := imageBackendRow{Name: conn.Name, Title: rowTitle(conn), Summary: ConnectorSummary(conn), Does: does,
		Group: group, Role: "unused", OfferOnly: true, HasSettings: true, LastError: conn.LastError}
	if slices.Contains(agents, conn.Name) {
		row.Role = "offered"
	}
	row.Use = row.Role
	row.EditForm, row.EditSpec, row.NeedsFix = connectorEditors(conn)
	row.TemplateUpdate, row.TemplateConfirm, row.TemplateUndo = templateRowState(conn)
	if service.Testable(conn) {
		row.Testable, row.TestConfirm = true, testConfirm(conn.Name)
	}
	return row
}

// useOptions is every media row's switch: Enabled (offered to agents; for
// an image backend, the default is enabled too) or Disabled ("unused").
// Which image backend is the default is its own button, Make default, and
// its row's badge: one switch holding both questions read as three ranks
// (Primary, Secondary, Disabled) of one thing.
var useOptions = []ui.SelectOption{
	{Value: "offered", Label: "Enabled"},
	{Value: "unused", Label: "Disabled"},
}

// roleForm is what a row's role control sends: the connector (the row's key)
// and its role.
type roleForm struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// handleMediaBackendRole is a row's role for any media connector: an image
// backend's (default, offered, Disabled), and a music or media connector's
// (offered, Disabled). Disabled comes as "unused".
func (a *AdminApp) handleMediaBackendRole(c *jsonapi.Call, req roleForm) (any, error) {
	name, role := strings.TrimSpace(req.Name), strings.TrimSpace(req.Role)
	if name == "" {
		return nil, jsonapi.BadRequest("name the connector: name")
	}
	if role == "unused" {
		role = "off"
	}
	conn, ok := GetConnector(RootDB, name)
	table := ""
	if ok && conn.Kind == service.Kind {
		switch {
		case service.Provides(conn, "music.generate"):
			table = service.MusicTable
		case service.Provides(conn, "media.generate"):
			table = service.MediaTable
		}
	}
	if table == "" {
		return a.setImageRole(c.R, name, role)
	}
	var agents []string
	if a.db != nil {
		a.db.Get(table, "agents", &agents)
	}
	agents = slices.DeleteFunc(agents, func(n string) bool { return n == name })
	switch role {
	case "offered":
		agents = append(agents, name)
	case "off":
	default:
		return nil, jsonapi.BadRequest("role is offered or unused")
	}
	if a.db != nil {
		a.db.Set(table, "agents", agents)
	}
	acceptChanges(name)
	adminChange(c.R, activity.Settings, "media connector %s: %s (offered: %s)", name, role, strings.Join(agents, ", "))
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleMediaBackendDefault is an enabled image backend's Make default:
// ?name= becomes what agents' image tools use unless told otherwise, and
// the one it replaces stays enabled.
func (a *AdminApp) handleMediaBackendDefault(c *jsonapi.Call, _ struct{}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("name the image backend: ?name=")
	}
	return a.setImageRole(c.R, name, "default")
}

// handleMediaBackendRemove is a stale row's Remove: ?name= taken off every
// Media Generation choice where it no longer reads.
func (a *AdminApp) handleMediaBackendRemove(c *jsonapi.Call, _ struct{}) (any, error) {
	name := strings.TrimSpace(c.R.URL.Query().Get("name"))
	if name == "" {
		return nil, jsonapi.BadRequest("name the connector: ?name=")
	}
	if !a.dropStale(name) {
		return nil, jsonapi.BadRequest(name + " holds no choice here that needs setting again")
	}
	adminChange(c.R, activity.Settings, "media generation: removed %s, which no longer reads", name)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleImageBackendUses sets what image backend ?name=, one that both makes
// and edits pictures, is used for (?uses=): both, edit (editing and blending
// only) or generate (generating only). Agents' image tool then offers it for
// that alone, and a call for the other is refused.
func (a *AdminApp) handleImageBackendUses(c *jsonapi.Call, req usesForm) (any, error) {
	name, use := strings.TrimSpace(req.Name), strings.TrimSpace(req.Uses)
	if name == "" {
		return nil, jsonapi.BadRequest("name the connector: name")
	}
	conn, ok := GetConnector(RootDB, name)
	if !ok || conn.Kind != service.Kind || !service.Provides(conn, "image.generate") || !service.Provides(conn, "image.edit") {
		return nil, jsonapi.BadRequest(name + " does not both make and edit pictures")
	}
	label := ""
	for _, o := range usedForOptions {
		if o.Value == use {
			label = o.Label
		}
	}
	if label == "" {
		return nil, jsonapi.BadRequest("uses is both, edit or generate")
	}
	if use == "both" {
		use = ImageUseBoth
	}
	uses := map[string]string{}
	if a.db != nil {
		a.db.Get(ImageTable, "uses", &uses)
		if uses == nil {
			uses = map[string]string{}
		}
		if use == ImageUseBoth {
			delete(uses, name)
		} else {
			uses[name] = use
		}
		a.db.Set(ImageTable, "uses", uses)
	}
	adminChange(c.R, activity.Settings, "image backend %s used for %s", name, strings.ToLower(label))
	return jsonapi.Empty(http.StatusNoContent), nil
}

// usesForm is what a row's Used for sends: the backend (the row's key) and
// the choice.
type usesForm struct {
	Name string `json:"name"`
	Uses string `json:"uses"`
}
