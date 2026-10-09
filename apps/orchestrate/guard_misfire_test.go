package orchestrate

import (
	"strings"
	"testing"
)

// A guard row warns once most of its recent judged corrections look like
// misfires, and not on a single one.
func TestAGuardRowWarnsWhenItMostlyMisfires(t *testing.T) {
	if guardMisfireWarning(1, 1) != "" {
		t.Error("one misfire is not a pattern")
	}
	if guardMisfireWarning(2, 6) != "" {
		t.Error("two of six is not most")
	}
	if w := guardMisfireWarning(3, 4); !strings.Contains(w, "3 of its last 4") || !strings.Contains(w, "Shadow") {
		t.Errorf("warning = %q", w)
	}
}
