package customapps

// Asking for an app's records in part (docs/design/app-record-queries.md):
// the page's app.records.list(query), app.records.count(where) and
// app.shared(name, query). The host filters before anything is sent, so a
// page showing the last twenty entries is handed twenty. With no query, a
// list answers exactly as it always has: every record, oldest first.

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/recquery"
)

// answerRecords is records as a list request asks for them: all of them
// (the answer as it was), {records, next} for ?q=<query>, or {count} for
// ?count=<where>. keyField is what a page's "after" names.
func answerRecords(r *http.Request, records []map[string]any, keyField string) (any, error) {
	if raw, ok := r.URL.Query()["count"]; ok {
		var where map[string]any
		if s := strings.TrimSpace(strings.Join(raw, "")); s != "" {
			d := json.NewDecoder(strings.NewReader(s))
			d.UseNumber()
			if err := d.Decode(&where); err != nil {
				return nil, jsonapi.BadRequest("count takes a where object, like {\"status\": \"open\"}: " + err.Error())
			}
		}
		n, err := recquery.Count(records, where)
		if err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		return map[string]any{"count": n}, nil
	}
	raw, ok := r.URL.Query()["q"]
	if !ok {
		return records, nil
	}
	q, err := recquery.Parse(strings.Join(raw, ""))
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	page, next, err := recquery.Run(records, keyField, q)
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	return map[string]any{"records": page, "next": next}, nil
}

// recordKeyField is the field an app's records are keyed by.
func recordKeyField(spec AppSpec) string {
	if k := strings.TrimSpace(spec.RecordKey); k != "" {
		return k
	}
	return "id"
}
