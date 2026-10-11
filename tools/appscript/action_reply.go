package appscript

import "sort"

// actionReplyKeys are the top-level keys of an action's printed object that
// anything reads: message, records and result ride back to the page, shared
// and shared_delete go to the store.
var actionReplyKeys = map[string]bool{"message": true, "records": true, "result": true, "shared": true, "shared_delete": true}

// DroppedActionKeys lists, sorted, the top-level keys of an action's printed
// object that nothing reads. They are dropped before the page sees the reply:
// a hot-take action printed {"take": ..., "symbol": ...}, the page got
// {"message": "Done."}, and showed "no response" though the model answered.
func DroppedActionKeys(obj map[string]any) []string {
	var out []string
	for k := range obj {
		if !actionReplyKeys[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
