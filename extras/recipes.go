// Package extras ships worked examples: pipelines, machines, and the app that
// runs a pipeline, as the same JSON a person imports and Builder authors.
//
// Embedded so a running server can hand them out. Builder starts a debate or
// a deep-research build from the shipped recipe and adapts it, rather than
// inventing the shape from the field reference each time; the files in this
// directory are both the examples a reader opens and the ones it reads.
package extras

import (
	"embed"
	"io/fs"
	"sort"
)

//go:embed *.json
var recipes embed.FS

// Recipe is one shipped file by name ("debate.pipeline.json"), and whether it
// exists.
func Recipe(name string) ([]byte, bool) {
	b, err := recipes.ReadFile(name)
	return b, err == nil
}

// Recipes is the name of every shipped file, sorted.
func Recipes() []string {
	names, _ := fs.Glob(recipes, "*.json")
	sort.Strings(names)
	return names
}
