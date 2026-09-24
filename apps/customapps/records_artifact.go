package customapps

// A custom app's DATA as a portable artifact, travelling with the app only
// when asked for (opt-in: the export dialog offers it unticked, and the
// server leaves it out of any export that does not name it). The app's spec
// is a recipe anybody may be handed; the rows are what somebody typed into it.
//
// Only the exporter's own rows travel. Every visitor of a shared app writes
// into their own sub-store, so reading the owner's store is structurally the
// owner's rows and nobody else's. The app's settings do not travel (a script
// setting can be a token), nor its co-author state.
//
// Import writes into the importer's own copy of the app (which may arrive in
// the same bundle, hence ImportsLate), and only into an EMPTY table: rows are
// never merged into, or written over, data already there.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxExportedRecords bounds one app's rows in a bundle. Past it the export
// says so rather than producing a file the importer's size cap would refuse.
const maxExportedRecords = 20000

type appRecordsRecipe struct {
	Schema    int              `json:"app_records"` // sniff key
	Name      string           `json:"name"`        // the app's slug
	RecordKey string           `json:"record_key,omitempty"`
	Records   []map[string]any `json:"records"`
}

type appRecordsArtifact struct{ app *CustomApps }

func (*appRecordsArtifact) ArtifactType() string  { return "app_records" }
func (*appRecordsArtifact) UserImportable() bool  { return true }
func (*appRecordsArtifact) OptInDependency() bool { return true }
func (*appRecordsArtifact) ImportsLate() bool     { return true }

// ContentKind: rows somebody typed, not a recipe.
func (*appRecordsArtifact) ContentKind() bool { return true }

func (*appRecordsArtifact) SniffsRecipe(fields map[string]json.RawMessage) bool {
	_, ok := fields["app_records"]
	return ok
}

// ownRows reads the owner's rows of their own app, sorted by key.
func (a *appRecordsArtifact) ownRows(owner, slug string) (AppSpec, []map[string]any, bool) {
	spec, ok := LoadAppSpec(owner, strings.TrimSpace(slug))
	if !ok || spec.Owner != owner || a.app == nil || a.app.DB == nil {
		return AppSpec{}, nil, false
	}
	udb := a.app.recordBase(spec, owner)
	tbl := recTable(spec.Slug)
	keys := udb.Keys(tbl)
	sort.Strings(keys)
	var rows []map[string]any
	for _, k := range keys {
		var rec map[string]any
		if udb.Get(tbl, k, &rec) && rec != nil {
			rows = append(rows, rec)
		}
	}
	return spec, rows, len(rows) > 0
}

func (a *appRecordsArtifact) ListArtifacts(_ Database) []ArtifactSel {
	if AuthDB == nil {
		return nil
	}
	adb := AuthDB()
	if adb == nil {
		return nil
	}
	var out []ArtifactSel
	for _, u := range AuthListUsers(adb) {
		for _, spec := range ListAppSpecs(u.Username) {
			if _, _, has := a.ownRows(u.Username, spec.Slug); has {
				out = append(out, ArtifactSel{Type: "app_records", Name: spec.Slug, Owner: u.Username})
			}
		}
	}
	return out
}

// ExportArtifact errors for an app with no rows, so a closure never carries
// an empty table and the existence probe agrees with export.
func (a *appRecordsArtifact) ExportArtifact(_ Database, name, owner string) (json.RawMessage, error) {
	spec, rows, has := a.ownRows(owner, name)
	if !has {
		return nil, fmt.Errorf("app %q has no records of yours to export", name)
	}
	if len(rows) > maxExportedRecords {
		return nil, fmt.Errorf("app %q has %d records, more than the %d an export carries", name, len(rows), maxExportedRecords)
	}
	return json.Marshal(appRecordsRecipe{Schema: 1, Name: spec.Slug, RecordKey: spec.RecordKey, Records: rows})
}

// Dependencies: the rows belong to their app, so exporting them alone brings
// the app too.
func (a *appRecordsArtifact) Dependencies(_ Database, name, owner string) []ArtifactSel {
	return []ArtifactSel{{Type: "custom_app", Name: strings.TrimSpace(name), Owner: owner}}
}

func (a *appRecordsArtifact) RecipeDependencies(_ Database, recipe json.RawMessage, owner string, _ func(typ, name string) bool) []ArtifactSel {
	var r appRecordsRecipe
	if json.Unmarshal(recipe, &r) != nil || strings.TrimSpace(r.Name) == "" {
		return nil
	}
	return []ArtifactSel{{Type: "custom_app", Name: strings.TrimSpace(r.Name), Owner: owner}}
}

func (a *appRecordsArtifact) ImportArtifact(_ Database, recipe json.RawMessage, owner string) (string, string, error) {
	var r appRecordsRecipe
	if err := json.Unmarshal(recipe, &r); err != nil {
		return "", "", fmt.Errorf("invalid app records: %w", err)
	}
	slug := strings.TrimSpace(r.Name)
	if slug == "" {
		return "", "", Error("app records name no app")
	}
	spec, ok := LoadAppSpec(owner, slug)
	if !ok || spec.Owner != owner || a.app == nil || a.app.DB == nil {
		return slug, "no app " + slug + " of yours here: import or create the app first", nil
	}
	udb := a.app.recordBase(spec, owner)
	tbl := recTable(spec.Slug)
	if len(udb.Keys(tbl)) > 0 {
		return slug, "app " + slug + " already has records here, so these were not merged into them", nil
	}
	key := strings.TrimSpace(spec.RecordKey)
	if key == "" {
		key = strings.TrimSpace(r.RecordKey)
	}
	written := 0
	for _, rec := range r.Records {
		if rec == nil {
			continue
		}
		id, _ := rec[key].(string)
		if strings.TrimSpace(id) == "" {
			continue
		}
		udb.Set(tbl, id, rec)
		written++
	}
	Log("[customapps] %s imported %d record(s) into %q", owner, written, slug)
	return slug, "", nil
}

// registerRecordsArtifact registers the type and offers an app's rows as a
// dependency of the app itself.
func (T *CustomApps) registerRecordsArtifact() {
	ra := &appRecordsArtifact{app: T}
	RegisterArtifactType(ra)
	RegisterArtifactDependencies("custom_app", func(name, owner string) []ArtifactSel {
		spec, ok := LoadAppSpec(owner, strings.TrimSpace(name))
		if !ok {
			return nil
		}
		if _, _, has := ra.ownRows(owner, spec.Slug); !has {
			return nil
		}
		return []ArtifactSel{{Type: "app_records", Name: spec.Slug, Owner: owner}}
	})
}
