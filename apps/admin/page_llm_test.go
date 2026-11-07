package admin

import (
	"os"
	"strings"
	"testing"
)

// The thinking switch on a model reads positively: on lets the model think.
// It stores disable_thinking, so it is inverted, on both the worker's and the
// lead's form, and the old "force think=false" wording is gone.
func TestTheThinkingSwitchReadsPositively(t *testing.T) {
	src, err := os.ReadFile("page_llm.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if n := strings.Count(s, `{Field: "disable_thinking", Label: "Let the model think", Type: "toggle", Invert: true,`); n != 2 {
		t.Errorf("the thinking switch reads positively on %d of the 2 forms", n)
	}
	if strings.Contains(s, "force think=false") {
		t.Error("the old wording is back")
	}
}
