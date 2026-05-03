// Package datemath provides a chat tool for computing date differences
// and adding/subtracting day offsets from a given date. Complements the
// localtime tool: localtime answers "when is now", datemath answers
// "how far between X and Y" and "X plus N days."
package datemath

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(new(DateMathTool)) }

// DateMathTool handles two operations: "diff" (days between two dates)
// and "add" (offset days from a base date). Kept as a single tool with
// a mode argument rather than split into two to keep the tool registry
// compact and to let the LLM remember one name.
type DateMathTool struct{}

func (t *DateMathTool) Name() string { return "date_math" }
func (t *DateMathTool) Caps() []Capability { return nil } // pure transform — no side effects

func (t *DateMathTool) Desc() string {
	return `Compute date arithmetic. Two operations:
- operation="diff", date1, date2 → days between the two dates (date2 relative to date1).
- operation="add", date1, days → add days to date1 (days may be negative to subtract).
Dates accept YYYY-MM-DD, "April 16, 2026", "Apr 16, 2026", or "04/16/2026".`
}

func (t *DateMathTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"operation": {Type: "string", Description: `"diff" for days between two dates, or "add" for offset from a date.`},
		"date1":     {Type: "string", Description: "First date (for diff) or base date (for add). YYYY-MM-DD or common formats."},
		"date2":     {Type: "string", Description: "Second date — diff only."},
		"days":      {Type: "integer", Description: "Days to add — add only. Negative subtracts."},
	}
}

func (t *DateMathTool) Run(args map[string]any) (string, error) {
	op := strings.ToLower(strings.TrimSpace(asString(args["operation"])))
	if op == "" {
		return "", fmt.Errorf(`operation is required ("diff" or "add")`)
	}

	switch op {
	case "diff":
		d1str := asString(args["date1"])
		d2str := asString(args["date2"])
		if d1str == "" || d2str == "" {
			return "", fmt.Errorf("diff requires date1 and date2")
		}
		d1, err := parseDate(d1str)
		if err != nil {
			return "", fmt.Errorf("date1: %w", err)
		}
		d2, err := parseDate(d2str)
		if err != nil {
			return "", fmt.Errorf("date2: %w", err)
		}
		// Day-granularity difference. Computed as calendar-day delta
		// from d1's date to d2's date, ignoring clock time so DST
		// transitions don't flip results by ±1.
		days := int(d2.Sub(d1).Round(24*time.Hour) / (24 * time.Hour))
		absDays := days
		if absDays < 0 {
			absDays = -absDays
		}
		weeks := float64(absDays) / 7.0
		return fmt.Sprintf("%s to %s = %d days (%.1f weeks)",
			d1.Format("2006-01-02 (Mon)"),
			d2.Format("2006-01-02 (Mon)"),
			days,
			weeks,
		), nil

	case "add":
		// Accept "date1" or "date" as the base-date key — the LLM
		// sometimes picks the shorter name naturally.
		dstr := asString(args["date1"])
		if dstr == "" {
			dstr = asString(args["date"])
		}
		if dstr == "" {
			return "", fmt.Errorf(`add requires date1 (or "date")`)
		}
		days, ok := asInt(args["days"])
		if !ok {
			return "", fmt.Errorf("days is required (integer; negative subtracts)")
		}
		d, err := parseDate(dstr)
		if err != nil {
			return "", fmt.Errorf("date1: %w", err)
		}
		result := d.AddDate(0, 0, days)
		sign := "+"
		if days < 0 {
			sign = ""
		}
		return fmt.Sprintf("%s %s%d days = %s",
			d.Format("2006-01-02 (Mon)"),
			sign,
			days,
			result.Format("2006-01-02 (Mon)"),
		), nil
	}

	return "", fmt.Errorf(`unsupported operation %q (use "diff" or "add")`, op)
}

// parseDate accepts several common formats. Order matters — more
// specific layouts come first so an ambiguous input (e.g., a plain
// "2026-1-2") resolves to the intended interpretation.
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	layouts := []string{
		"2006-01-02",      // ISO strict
		"2006-1-2",        // ISO loose
		"2006/01/02",      // ISO with slashes
		"2006/1/2",        // ISO loose with slashes
		"January 2, 2006", // "April 16, 2026"
		"Jan 2, 2006",     // "Apr 16, 2026"
		"2 January 2006",  // "16 April 2026"
		"2 Jan 2006",      // "16 Apr 2026"
		"01/02/2006",      // US strict
		"1/2/2006",        // US loose
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf(`unrecognized date format %q (try YYYY-MM-DD, "April 16, 2026", or "04/16/2026")`, s)
}

// asString coerces whatever the LLM passed into a string. Tool args
// arrive as map[string]any with concrete types depending on whether
// the call came through native tool-use (typed) or the PromptTools
// <tool_call> XML path (always string).
func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}

// asInt coerces an arg value to int, returning ok=false when the value
// is absent or not convertible. Handles the three realistic shapes: a
// real int/int64 from native tool use, a float64 from JSON unmarshal,
// and a string from PromptTools XML.
func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}
