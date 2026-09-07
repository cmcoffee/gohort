package orchestrate

import (
	"strings"
	"testing"
)

// TestMissingArgsNamesOnlyTheEmptyOne is the whole point of the helper. The
// error it replaced listed every required argument on any failure, and a live
// agent that had sent url and compare_op correctly read that as a verdict on
// all three: six rounds of re-sending the same call and dropping arguments
// that were never the problem. An argument the caller DID supply must never
// appear in the complaint.
func TestMissingArgsNamesOnlyTheEmptyOne(t *testing.T) {
	err := missingArgs("http_poll monitor \"status\"",
		reqArg{"url", "https://example.invalid/status", "the address fetched"},
		reqArg{"compare_op", "==", "one of < > <= >= == != contains"},
		reqArg{"threshold", "", "the value compared against"},
	)
	if err == nil {
		t.Fatal("an empty threshold must still be an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "threshold") {
		t.Errorf("the missing argument is not named: %s", msg)
	}
	for _, supplied := range []string{"url", "compare_op"} {
		if strings.Contains(msg, supplied+" ") || strings.Contains(msg, " "+supplied) {
			t.Errorf("%q was supplied but the error still complains about it: %s", supplied, msg)
		}
	}
	// The hint belongs to the missing argument only, so the caller is told what
	// to put there rather than left to guess the format.
	if !strings.Contains(msg, "the value compared against") {
		t.Errorf("the missing argument's hint is not shown: %s", msg)
	}
	if strings.Contains(msg, "the address fetched") {
		t.Errorf("a supplied argument's hint is being shown: %s", msg)
	}
	// And it says the rest of the call survived, because the caller cannot see
	// that: without it the natural repair is to rebuild the call from scratch.
	if !strings.Contains(msg, "everything else you sent is fine") {
		t.Errorf("the error does not say the rest of the call was accepted: %s", msg)
	}
}

// TestMissingArgsWritesAListLikeASentence: several missing arguments read as
// prose, and whitespace-only counts as missing (an LLM filling a required
// field with " " has not supplied it).
func TestMissingArgsWritesAListLikeASentence(t *testing.T) {
	err := missingArgs("poll monitor \"x\"",
		reqArg{"check_agent", "  ", ""},
		reqArg{"check", "", ""},
	)
	if err == nil {
		t.Fatal("whitespace is not a supplied value")
	}
	if !strings.Contains(err.Error(), "check_agent and check") {
		t.Errorf("two missing arguments should read as a list: %s", err)
	}
	if !strings.Contains(err.Error(), "them") {
		t.Errorf("the plural repair instruction is wrong: %s", err)
	}
}

// TestMissingArgsIsSilentWhenNothingIsMissing keeps the call site a one-liner.
func TestMissingArgsIsSilentWhenNothingIsMissing(t *testing.T) {
	if err := missingArgs("watch monitor \"x\"", reqArg{"tool_name", "read_chat", "hint"}); err != nil {
		t.Errorf("a complete call must not error: %v", err)
	}
}
