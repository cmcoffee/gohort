package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

type offListApp struct{ path, name string }

func (o offListApp) WebPath() string                       { return o.path }
func (o offListApp) WebName() string                       { return o.name }
func (o offListApp) WebDesc() string                       { return "" }
func (o offListApp) RegisterRoutes(*http.ServeMux, string) {}

// The apps the access pickers offer (a person's apps, a group's, the
// defaults) mark one switched off on the Apps tab, and still list it: an app
// granted to everyone and made a default stayed off every dashboard while
// each picker showed it granted, and leaving it out would hide grants already
// saved.
func TestTheAccessPickersMarkASwitchedOffApp(t *testing.T) {
	RegisterWebApp(offListApp{"/offlist-on", "Shown"})
	RegisterWebApp(offListApp{"/offlist-off", "Switched"})
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	if err := SetAppEnabled(a.db, "/offlist-off", false); err != nil {
		t.Fatal(err)
	}
	out, err := a.handleListApps(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/apps", nil), User: "root"})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	for _, row := range out.([]appInfo) {
		names[row.Path] = row.Name
	}
	if names["/offlist-on"] != "Shown" {
		t.Errorf("an app switched on was marked: %q", names["/offlist-on"])
	}
	if got := names["/offlist-off"]; !strings.HasPrefix(got, "Switched") || !strings.Contains(got, "switched off on the Apps tab") {
		t.Errorf("a switched-off app is not listed and marked: %q", got)
	}
}
