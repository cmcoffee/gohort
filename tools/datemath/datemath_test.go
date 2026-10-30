package datemath

import (
	"strings"
	"testing"
	"time"
)

func TestDateMath_diffISO(t *testing.T) {
	tool := &DateMathTool{}
	out, err := tool.Run(map[string]any{
		"operation": "diff",
		"date1":     "2026-04-16",
		"date2":     "2026-06-01",
	})
	if err != nil {
		t.Fatalf("diff failed: %v", err)
	}
	if !strings.Contains(out, "46 days") {
		t.Errorf("expected 46 days in output, got %q", out)
	}
	if !strings.Contains(out, "2026-04-16") || !strings.Contains(out, "2026-06-01") {
		t.Errorf("expected both dates in output, got %q", out)
	}
}

func TestDateMath_diffNegative(t *testing.T) {
	tool := &DateMathTool{}
	out, err := tool.Run(map[string]any{
		"operation": "diff",
		"date1":     "2026-06-01",
		"date2":     "2026-04-16",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "-46 days") {
		t.Errorf("expected -46 days, got %q", out)
	}
}

func TestDateMath_diffLongForm(t *testing.T) {
	tool := &DateMathTool{}
	out, err := tool.Run(map[string]any{
		"operation": "diff",
		"date1":     "April 16, 2026",
		"date2":     "December 25, 2026",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "253 days") {
		t.Errorf("expected 253 days, got %q", out)
	}
}

func TestDateMath_addPositive(t *testing.T) {
	tool := &DateMathTool{}
	out, err := tool.Run(map[string]any{
		"operation": "add",
		"date1":     "2026-04-16",
		"days":      30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2026-05-16") {
		t.Errorf("expected 2026-05-16, got %q", out)
	}
}

func TestDateMath_addNegative(t *testing.T) {
	tool := &DateMathTool{}
	out, err := tool.Run(map[string]any{
		"operation": "add",
		"date1":     "2026-04-16",
		"days":      -46,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2026-03-01") {
		t.Errorf("expected 2026-03-01, got %q", out)
	}
}

func TestDateMath_addStringDays(t *testing.T) {
	// PromptTools path passes args as strings.
	tool := &DateMathTool{}
	out, err := tool.Run(map[string]any{
		"operation": "add",
		"date1":     "2026-04-16",
		"days":      "7",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2026-04-23") {
		t.Errorf("expected 2026-04-23, got %q", out)
	}
}

func TestDateMath_addFloatDays(t *testing.T) {
	// JSON tool-use path gives numbers as float64.
	tool := &DateMathTool{}
	out, err := tool.Run(map[string]any{
		"operation": "add",
		"date1":     "2026-04-16",
		"days":      float64(14),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2026-04-30") {
		t.Errorf("expected 2026-04-30, got %q", out)
	}
}

func TestDateMath_addAliasKey(t *testing.T) {
	// LLM may pick "date" instead of "date1".
	tool := &DateMathTool{}
	out, err := tool.Run(map[string]any{
		"operation": "add",
		"date":      "2026-04-16",
		"days":      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2026-04-17") {
		t.Errorf("expected 2026-04-17, got %q", out)
	}
}

func TestDateMath_missingOperation(t *testing.T) {
	tool := &DateMathTool{}
	_, err := tool.Run(map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing operation")
	}
}

func TestDateMath_unknownOperation(t *testing.T) {
	tool := &DateMathTool{}
	_, err := tool.Run(map[string]any{"operation": "multiply"})
	if err == nil {
		t.Fatal("expected error for unknown operation")
	}
}

func TestDateMath_badDateFormat(t *testing.T) {
	tool := &DateMathTool{}
	_, err := tool.Run(map[string]any{
		"operation": "diff",
		"date1":     "not a date",
		"date2":     "2026-04-16",
	})
	if err == nil {
		t.Fatal("expected error for bad date")
	}
}

func TestDateMath_weekdayInOutput(t *testing.T) {
	tool := &DateMathTool{}
	out, err := tool.Run(map[string]any{
		"operation": "add",
		"date1":     "2026-04-16",
		"days":      0,
	})
	if err != nil {
		t.Fatal(err)
	}
	// April 16, 2026 is a Thursday.
	if !strings.Contains(out, "Thu") {
		t.Errorf("expected Thu in weekday output, got %q", out)
	}
}

// A Unix timestamp reads as a date and time in the reader's zone, with how
// long ago it was, whatever its unit; a date reads back as its timestamp.
func TestDateMath_unix(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("no zone data")
	}
	prev := now
	now = func() time.Time { return time.Unix(1793305971, 0).Add(2 * time.Hour) }
	t.Cleanup(func() { now = prev })
	tool := &DateMathTool{}
	for _, v := range []string{"1793305971", "1793305971000", "1793305971000000", "1793305971.123456"} {
		out, err := tool.run(map[string]any{"operation": "unix", "value": v}, la)
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if !strings.Contains(out, "Thu, Oct 29, 2026 1:32:51 PM PDT") || !strings.Contains(out, "2026-10-29 20:32:51 UTC") || !strings.Contains(out, "2 hours ago") {
			t.Errorf("%s: %q", v, out)
		}
	}
	for _, c := range []struct{ in, want string }{
		{"2026-10-29 13:32:51", "1793305971 (seconds)"},
		{"2026-10-29T20:32:51Z", "1793305971 (seconds)"},
		{"2026-10-29", "1793257200 (seconds)"},
	} {
		out, err := tool.run(map[string]any{"operation": "unix", "value": c.in}, la)
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("%s: %q %v", c.in, out, err)
		}
	}
	if _, err := tool.run(map[string]any{"operation": "unix", "value": "soon"}, la); err == nil {
		t.Error("a value that is neither should be refused")
	}
	if _, err := tool.run(map[string]any{"operation": "unix"}, la); err == nil {
		t.Error("no value should be refused")
	}

	// In diff and add, a timestamp is the day it falls on for the reader:
	// 8:32 PM UTC on Oct 29 is still Oct 29 in Los Angeles, Oct 30 in Tokyo.
	out, err := tool.run(map[string]any{"operation": "diff", "date1": "2026-10-01", "date2": "1793305971"}, la)
	if err != nil || !strings.Contains(out, "2026-10-29") || !strings.Contains(out, "= 28 days") {
		t.Errorf("diff with a timestamp: %q %v", out, err)
	}
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	out, _ = tool.run(map[string]any{"operation": "add", "date1": "1793305971", "days": 1}, tokyo)
	if !strings.Contains(out, "2026-10-30 (Fri) +1 days = 2026-10-31") {
		t.Errorf("add from a timestamp in Tokyo: %q", out)
	}
	if got := ago(time.Unix(0, 0), time.Unix(0, 0).Add(-3*24*time.Hour)); got != "in 3 days" {
		t.Errorf("a time after now: %q", got)
	}
}
