package datemath

import (
	"strings"
	"testing"
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
