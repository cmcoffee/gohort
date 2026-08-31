package guides

import (
	"os"
	"testing"
)

// readSource reads a file from this package for the wiring guards — the checks
// that a declared URL and the route serving it did not drift apart.
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
