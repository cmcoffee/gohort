package appscript

import (
	"reflect"
	"testing"
)

func TestDroppedActionKeysNamesWhatThePageNeverSees(t *testing.T) {
	got := DroppedActionKeys(map[string]any{"take": "x", "symbol": "NVDA", "message": "ok", "result": nil})
	if want := []string{"symbol", "take"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := DroppedActionKeys(map[string]any{"message": "", "records": nil, "result": 1, "shared": nil, "shared_delete": nil}); len(got) != 0 {
		t.Errorf("reply keys flagged: %q", got)
	}
}
