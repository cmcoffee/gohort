// Package recquery is the one way an app's records are asked for in part
// (docs/design/app-record-queries.md): by its page, by its scripts, by its
// checks. The host filters before anything is sent, so a page showing the
// last twenty entries is handed twenty, and a script is handed what it reads.
//
//	{"where": {"kind": "entry", "status": "open", "day": {">=": "2026-11-10"}},
//	 "order": "-created", "limit": 50, "after": "<key of the last record of the page before>"}
//
// where: a field equals a value, or one comparison per field (=, !=, <, <=,
// >, >=, in, contains). All must hold. No "or" and no joins. order: one
// field, "-" for newest or largest first; the store's own order (oldest
// first) otherwise, and for ties. limit: at most MaxLimit, none for all.
// after: the key to continue from, so a long list pages without offsets
// shifting under it.
//
// A leaf: it reads plain records (decoded JSON objects) and knows nothing of
// stores, so core's surface does not grow with it.
package recquery

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// MaxLimit is the most records one query returns.
const MaxLimit = 500

// Query is one ask for records.
type Query struct {
	Where map[string]any `json:"where,omitempty"`
	Order string         `json:"order,omitempty"`
	Limit int            `json:"limit,omitempty"`
	After string         `json:"after,omitempty"`
}

// ops are the comparisons a field may be held to.
var ops = map[string]bool{"=": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true, "in": true, "contains": true}

// Parse reads a query from its JSON, checked. Empty is the query for all.
func Parse(raw string) (Query, error) {
	var q Query
	if strings.TrimSpace(raw) == "" {
		return q, nil
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&q); err != nil {
		return Query{}, fmt.Errorf("a query is a JSON object {where, order, limit, after}: %v", err)
	}
	return q, q.Check()
}

// Check reports what is wrong with q, in the words an author fixes it by.
func (q Query) Check() error {
	if q.Limit < 0 || q.Limit > MaxLimit {
		return fmt.Errorf("limit is 1 to %d (or leave it out for all)", MaxLimit)
	}
	if f := strings.TrimPrefix(strings.TrimSpace(q.Order), "-"); strings.TrimSpace(q.Order) != "" && f == "" {
		return fmt.Errorf("order names one field, with - in front for newest or largest first")
	}
	return checkWhere(q.Where)
}

func checkWhere(where map[string]any) error {
	for field, cond := range where {
		if strings.TrimSpace(field) == "" {
			return fmt.Errorf("where has an empty field name")
		}
		m, ok := cond.(map[string]any)
		if !ok {
			continue // field = value
		}
		if len(m) != 1 {
			return fmt.Errorf("where.%s holds one comparison, like {\">=\": 10}; for two, it cannot (no ranges in one field yet)", field)
		}
		for op, v := range m {
			if !ops[op] {
				return fmt.Errorf("where.%s: %q is not a comparison: use =, !=, <, <=, >, >=, in or contains", field, op)
			}
			if op == "in" {
				if _, ok := v.([]any); !ok {
					return fmt.Errorf("where.%s: in takes a list of values", field)
				}
			}
		}
	}
	return nil
}

// Matches reports whether rec meets every condition of where.
func Matches(rec map[string]any, where map[string]any) bool {
	for field, cond := range where {
		got, has := rec[field]
		op, want := "=", cond
		if m, ok := cond.(map[string]any); ok && len(m) == 1 {
			for o, v := range m {
				op, want = o, v
			}
		}
		if !holds(op, got, has, want) {
			return false
		}
	}
	return true
}

func holds(op string, got any, has bool, want any) bool {
	switch op {
	case "=":
		return has && equal(got, want)
	case "!=":
		return !has || !equal(got, want)
	case "in":
		if !has {
			return false
		}
		list, _ := want.([]any)
		for _, w := range list {
			if equal(got, w) {
				return true
			}
		}
		return false
	case "contains":
		if !has {
			return false
		}
		if list, ok := got.([]any); ok {
			for _, g := range list {
				if equal(g, want) {
					return true
				}
			}
			return false
		}
		return strings.Contains(strings.ToLower(text(got)), strings.ToLower(text(want)))
	}
	if !has {
		return false
	}
	c, ok := compare(got, want)
	if !ok {
		return false
	}
	switch op {
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	}
	return false
}

// number is v as a number, when it is one: a JSON number however decoded.
// Text is not a number here, so "10" and "9" order as text, as they were
// written.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// text is v as it reads.
func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func equal(a, b any) bool {
	if x, ok := number(a); ok {
		if y, ok := number(b); ok {
			return x == y
		}
	}
	if ab, ok := a.(bool); ok {
		bb, ok := b.(bool)
		return ok && ab == bb
	}
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return text(a) == text(b)
}

// compare orders a against b: numbers by value, anything else as text (so
// an ISO date orders as a date). ok is false for a value that has no order
// (a list, an object, nothing).
func compare(a, b any) (int, bool) {
	if x, ok := number(a); ok {
		if y, ok := number(b); ok {
			switch {
			case x < y:
				return -1, true
			case x > y:
				return 1, true
			}
			return 0, true
		}
	}
	switch a.(type) {
	case []any, map[string]any, nil:
		return 0, false
	}
	switch b.(type) {
	case []any, map[string]any, nil:
		return 0, false
	}
	return strings.Compare(text(a), text(b)), true
}

// Run is q over records (in the store's order), each keyed by its keyField:
// the page of records it returns, and the key to continue after when there
// are more. A key in after that is not among the matching records is an
// error, so a page that lost its place starts again rather than skipping.
func Run(records []map[string]any, keyField string, q Query) (page []map[string]any, next string, err error) {
	if err := q.Check(); err != nil {
		return nil, "", err
	}
	var hits []map[string]any
	for _, r := range records {
		if Matches(r, q.Where) {
			hits = append(hits, r)
		}
	}
	if o := strings.TrimSpace(q.Order); o != "" {
		desc := strings.HasPrefix(o, "-")
		field := strings.TrimPrefix(o, "-")
		sort.SliceStable(hits, func(i, j int) bool {
			a, aok := hits[i][field]
			b, bok := hits[j][field]
			if !aok || !bok {
				return aok && !bok // records without the field go last
			}
			c, ok := compare(a, b)
			if !ok {
				return false
			}
			if desc {
				return c > 0
			}
			return c < 0
		})
	}
	if after := strings.TrimSpace(q.After); after != "" {
		at := -1
		for i, r := range hits {
			if text(r[keyField]) == after {
				at = i
				break
			}
		}
		if at < 0 {
			return nil, "", fmt.Errorf("no record %q among the ones this query matches, to continue after: it may have been changed or removed; ask again from the start", after)
		}
		hits = hits[at+1:]
	}
	if q.Limit > 0 && len(hits) > q.Limit {
		hits = hits[:q.Limit]
		next = text(hits[len(hits)-1][keyField])
	}
	if hits == nil {
		hits = []map[string]any{}
	}
	return hits, next, nil
}

// Count is how many of records meet where.
func Count(records []map[string]any, where map[string]any) (int, error) {
	if err := checkWhere(where); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range records {
		if Matches(r, where) {
			n++
		}
	}
	return n, nil
}
