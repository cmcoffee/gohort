package filestore

import (
	"os"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// packageTunables are the settings every test here starts from. The free-space
// floor is set low so the tests do not depend on how full the disk they run on
// is: with the 10 GB default, a build machine short of space refused the
// tests' own few-byte writes and every pull and upload test failed.
var packageTunables Database

func TestMain(m *testing.M) {
	packageTunables = &DBase{Store: kvlite.MemStore()}
	packageTunables.Set(WebTable, "tune_disk_floor_gb", float64(1))
	SetTunablesDB(packageTunables)
	os.Exit(m.Run())
}

// withTunables runs a test with these settings on top of the package's, and
// puts the package's back afterwards.
func withTunables(t *testing.T, vals map[string]float64) {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(WebTable, "tune_disk_floor_gb", float64(1))
	for k, v := range vals {
		db.Set(WebTable, k, v)
	}
	SetTunablesDB(db)
	t.Cleanup(func() { SetTunablesDB(packageTunables) })
}
