package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// app.json written by a model quotes min and max as often as not; both
// spellings read as the number, and a non-number is refused by name.
func TestAppSettingBoundsReadQuotedOrNot(t *testing.T) {
	var s AppSetting
	if err := json.Unmarshal([]byte(`{"name":"n","default":"10","min":"1","max":50}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Name != "n" || s.Default != "10" || s.Min != 1 || s.Max != 50 {
		t.Fatalf("read %+v", s)
	}
	if err := json.Unmarshal([]byte(`{"name":"n","min":"ten"}`), &s); err == nil || !strings.Contains(err.Error(), "min") {
		t.Fatalf("a non-number min: err=%v", err)
	}
}
