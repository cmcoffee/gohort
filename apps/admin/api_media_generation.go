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
)

// mediaBackendsSource is the list's address, named once because the edits
// made from any connector row reload it (connectorListSources), and a reload
// names its source exactly.
const mediaBackendsSource = "api/media-backends"

func (a *AdminApp) registerMediaGenerationRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/"+mediaBackendsSource, jsonapi.Get(a.db, a.handleMediaBackends, a.gate()))
	sub.HandleFunc("/"+mediaBackendsSource+"/role", jsonapi.Post(a.db, a.handleMediaBackendRole, a.gate()))
	sub.HandleFunc("/"+mediaBackendsSource+"/uses", jsonapi.Post(a.db, a.handleImageBackendUses, a.gate()))
}

// usedForLabels is what a row says an image backend that both makes and
// edits pictures is used for.
var usedForLabels = map[string]string{
	ImageUseBoth:     "used for generating, editing and blending",
	ImageUseEdit:     "used for editing and blending only",
	ImageUseGenerate: "used for generating only",
}

// handleMediaBackends is every media connector as its row reads: the image
// backends, then the music connectors, then each other kind, banded.
func (a *AdminApp) handleMediaBackends(c *jsonapi.Call) (any, error) {
	rows := []imageBackendRow{}
	for _, r := range a.imageRows() {
		r.Group, r.IsImage = "Images", true
		if conn, ok := GetConnector(RootDB, r.Name); ok && conn.Kind == service.Kind &&
			service.Provides(conn, "image.generate") && service.Provides(conn, "image.edit") {
			use := ImageBackendUse(r.Name)
			r.BothWays, r.UsedFor = true, usedForLabels[use]
			r.UsedBoth, r.UsedEdit, r.UsedGenerate = use == ImageUseBoth, use == ImageUseEdit, use == ImageUseGenerate
		}
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
		Group: group, Role: "unused", IsUnused: true, HasSettings: true, LastError: conn.LastError}
	if slices.Contains(agents, conn.Name) {
		row.Role, row.IsOffered, row.IsUnused = "offered", true, false
	}
	row.EditForm, row.EditSpec, row.NeedsFix = connectorEditors(conn)
	_, row.TemplateUpdate = templateUpdate(conn)
	if service.Testable(conn) {
		row.Testable, row.TestConfirm = true, testConfirm(conn.Name)
	}
	return row
}

// noConnectorNamed refuses a row's button sent with no ?name=.
const noConnectorNamed = "name the connector: ?name="

// handleMediaBackendRole is a row's role buttons for any media connector: an
// image backend's (default, offered, off) as Image Generation had them, and
// a music or media connector's (offered, off).
func (a *AdminApp) handleMediaBackendRole(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	name, role := strings.TrimSpace(q.Get("name")), strings.TrimSpace(q.Get("role"))
	if name == "" {
		return nil, jsonapi.BadRequest(noConnectorNamed)
	}
	if role == "off" && a.dropStale(name) {
		adminChange(c.R, activity.Settings, "media generation: removed %s, which no longer reads", name)
		return jsonapi.Empty(http.StatusNoContent), nil
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
		return a.handleImageBackendRole(c, struct{}{})
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
		return nil, jsonapi.BadRequest("role is offered or off")
	}
	if a.db != nil {
		a.db.Set(table, "agents", agents)
	}
	acceptChanges(name)
	adminChange(c.R, activity.Settings, "media connector %s: %s (offered: %s)", name, role, strings.Join(agents, ", "))
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleImageBackendUses sets what image backend ?name=, one that both makes
// and edits pictures, is used for (?uses=): both, edit (editing and blending
// only) or generate (generating only). Agents' image tool then offers it for
// that alone, and a call for the other is refused.
func (a *AdminApp) handleImageBackendUses(c *jsonapi.Call, _ struct{}) (any, error) {
	q := c.R.URL.Query()
	name, use := strings.TrimSpace(q.Get("name")), strings.TrimSpace(q.Get("uses"))
	if name == "" {
		return nil, jsonapi.BadRequest(noConnectorNamed)
	}
	conn, ok := GetConnector(RootDB, name)
	if !ok || conn.Kind != service.Kind || !service.Provides(conn, "image.generate") || !service.Provides(conn, "image.edit") {
		return nil, jsonapi.BadRequest(name + " does not both make and edit pictures")
	}
	if use == "both" {
		use = ImageUseBoth
	}
	if _, known := usedForLabels[use]; !known {
		return nil, jsonapi.BadRequest("uses is both, edit or generate")
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
	adminChange(c.R, activity.Settings, "image backend %s %s", name, usedForLabels[use])
	return jsonapi.Empty(http.StatusNoContent), nil
}
