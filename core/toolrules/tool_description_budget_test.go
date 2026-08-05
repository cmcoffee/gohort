package toolrules

import (
	"strings"
	"testing"
)

func longText(n int) string { return strings.Repeat("a", n) }

func TestAuthoredDescriptionsWithinBudgetPass(t *testing.T) {
	args := map[string]any{
		"description": "Get a GitHub issue by number, including title, state, body and author.",
		"params": map[string]any{
			"number": map[string]any{"type": "string", "description": "Issue number, e.g. 1421."},
		},
		"actions": []any{
			map[string]any{"name": "get_issue", "description": "Fetch one issue."},
		},
	}
	if err := CheckAuthoredToolText(args); err != nil {
		t.Fatalf("a concise tool was refused: %v", err)
	}
}

func TestOverlongToolDescriptionIsRefused(t *testing.T) {
	err := CheckAuthoredToolText(map[string]any{"description": longText(MaxToolDescription + 1)})
	if err == nil {
		t.Fatal("a description past the cap must be refused — this text is re-sent on every turn forever")
	}
	// The error has to say what to DO, or the retry is the same text reworded.
	for _, want := range []string{"cap is", "one or two sentences"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("error should tell the author how to fix it; missing %q in: %v", want, err)
		}
	}
}

func TestOverlongActionAndParamDescriptionsAreRefused(t *testing.T) {
	cases := []struct {
		name, wantIn string
		args         map[string]any
	}{
		{
			name:   "toolbox action",
			wantIn: "actions[0] (list_issues)",
			args: map[string]any{"actions": []any{
				map[string]any{"name": "list_issues", "description": longText(MaxActionDescription + 1)},
			}},
		},
		{
			name:   "top-level param",
			wantIn: "params.number",
			args: map[string]any{"params": map[string]any{
				"number": map[string]any{"description": longText(MaxParamDescription + 1)},
			}},
		},
		{
			name:   "param nested in an action",
			wantIn: "actions[0] (list_issues) params.state",
			args: map[string]any{"actions": []any{
				map[string]any{"name": "list_issues", "params": map[string]any{
					"state": map[string]any{"description": longText(MaxParamDescription + 1)},
				}},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckAuthoredToolText(tc.args)
			if err == nil {
				t.Fatal("over-long description accepted")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error must name the offender %q, got: %v", tc.wantIn, err)
			}
		})
	}
}

// The check runs on update too, where args carry only the fields being
// patched. A call that never mentions a description must not be refused over
// one it inherited — otherwise a legacy tool authored before the cap can
// never be fixed.
func TestAbsentFieldsAreNotChecked(t *testing.T) {
	args := map[string]any{
		"name":         "get_issue",
		"url_template": "https://api.github.com/repos/{owner}/{repo}/issues/{number}",
	}
	if err := CheckAuthoredToolText(args); err != nil {
		t.Fatalf("a patch touching no descriptions was refused: %v", err)
	}
}

// A retry has to see the same offender first, or the cap reads as moving.
func TestOffenderReportingIsStable(t *testing.T) {
	args := map[string]any{"params": map[string]any{
		"alpha": map[string]any{"description": longText(MaxParamDescription + 1)},
		"beta":  map[string]any{"description": longText(MaxParamDescription + 1)},
		"gamma": map[string]any{"description": longText(MaxParamDescription + 1)},
	}}
	first := CheckAuthoredToolText(args)
	if first == nil {
		t.Fatal("expected a refusal")
	}
	for i := 0; i < 20; i++ {
		if got := CheckAuthoredToolText(args); got.Error() != first.Error() {
			t.Fatalf("offender order is unstable:\n%v\nvs\n%v", first, got)
		}
	}
}
