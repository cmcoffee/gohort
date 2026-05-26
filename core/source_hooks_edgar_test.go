package core

import (
	"strings"
	"testing"
)

// Regression test for the EDGAR query sanitizer. All input queries are
// from the gohort.log — LLM-generated EDGAR queries that returned empty
// because EDGAR's API doesn't parse field-tag syntax. The sanitizer
// should strip the scaffolding and produce valid keyword-style queries.
func TestSanitizeEDGARQuery_productionFailures(t *testing.T) {
	cases := []struct {
		name         string
		in           string
		wantContains []string // must appear in output
		wantOmits    []string // must NOT appear in output
	}{
		{
			name:         "Baidu multi-form multi-keyword",
			in:           `company:"Baidu" AND (form type:"10-K" OR form type:"20-F") AND (keyword:"artificial intelligence" OR keyword:"AI") AND (keyword:"research and development" OR keyword:"capital expenditure")`,
			wantContains: []string{`"Baidu"`, `"10-K"`, `"20-F"`, `"artificial intelligence"`, `"AI"`, `"research and development"`, `"capital expenditure"`},
			wantOmits:    []string{"company:", "form type:", "keyword:", "formtype:"},
		},
		{
			name:         "simplified Baidu variant",
			in:           `company:Baidu form type:10-K form type:20-F keyword:artificial intelligence keyword:AI keyword:research and development keyword:capital expenditure`,
			wantContains: []string{"Baidu", "10-K", "20-F", "artificial intelligence", "research and development"},
			wantOmits:    []string{"company:", "form type:", "keyword:"},
		},
		{
			name:         "Alibaba variant",
			in:           `company:"Alibaba" AND (form type:"10-K" OR form type:"20-F") AND (keyword:"artificial intelligence" OR keyword:"AI") AND (keyword:"research and development" OR keyword:"capital expenditure")`,
			wantContains: []string{`"Alibaba"`, `"10-K"`},
			wantOmits:    []string{"company:", "form type:", "keyword:"},
		},
		{
			name:         "Google with 10-Q instead of 20-F",
			in:           `company:"Google" AND (form type:"10-K" OR form type:"10-Q") AND (keyword:"artificial intelligence" OR keyword:"AI") AND (keyword:"research and development" OR keyword:"capital expenditure")`,
			wantContains: []string{`"Google"`, `"10-K"`, `"10-Q"`},
			wantOmits:    []string{"company:", "form type:", "keyword:"},
		},
		{
			name:         "Nvidia with GPU keyword",
			in:           `company:"Nvidia" AND (form type:"10-K" OR form type:"10-Q") AND (keyword:"artificial intelligence" OR keyword:"AI") AND (keyword:"data center" OR keyword:"GPU")`,
			wantContains: []string{`"Nvidia"`, `"data center"`, `"GPU"`},
			wantOmits:    []string{"company:", "form type:", "keyword:"},
		},
		{
			name:         "ASML Holding single form",
			in:           `company:"ASML Holding" AND (form type:"20-F") AND (keyword:"artificial intelligence" OR keyword:"AI") AND (keyword:"capital expenditure")`,
			wantContains: []string{`"ASML Holding"`, `"20-F"`, `"capital expenditure"`},
			wantOmits:    []string{"company:", "form type:", "keyword:"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeEDGARQuery(c.in)
			for _, want := range c.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("expected output to contain %q, got: %q", want, got)
				}
			}
			for _, omit := range c.wantOmits {
				if strings.Contains(strings.ToLower(got), strings.ToLower(omit)) {
					t.Errorf("expected output to omit %q, got: %q", omit, got)
				}
			}
			// Result should not be empty — every input has real content.
			if got == "" {
				t.Errorf("sanitizer returned empty string for non-trivial input")
			}
			// Should not start or end with dangling operators.
			if strings.HasPrefix(got, ")") || strings.HasPrefix(got, "AND") || strings.HasPrefix(got, "OR") {
				t.Errorf("output has leading operator/paren: %q", got)
			}
			if strings.HasSuffix(got, "(") || strings.HasSuffix(got, "AND") || strings.HasSuffix(got, "OR") {
				t.Errorf("output has trailing operator/paren: %q", got)
			}
		})
	}
}

// Ensure valid EDGAR-style queries pass through unchanged.
func TestSanitizeEDGARQuery_validQueriesPreserved(t *testing.T) {
	cases := []string{
		`"artificial intelligence" AND Nvidia`,
		`"data center" GPU`,
		`Intel OR Microsoft`,
		`Google "machine learning" 2025`,
	}
	for _, in := range cases {
		got := sanitizeEDGARQuery(in)
		if got == "" {
			t.Errorf("sanitizer emptied valid query %q", in)
		}
		// Normalize whitespace for comparison
		want := strings.Join(strings.Fields(in), " ")
		if got != want {
			t.Errorf("valid query mangled:\n  in:   %q\n  got:  %q\n  want: %q", in, got, want)
		}
	}
}
