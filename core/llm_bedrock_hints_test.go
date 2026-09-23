package core

// AWS words its refusals for somebody holding the API reference. The two that
// cost the most time here each have a SETTING behind them, and neither
// mentions it.

import (
	"strings"
	"testing"
)

func TestTheInferenceProfileRefusalNamesTheFix(t *testing.T) {
	// The message AWS actually returns, verbatim, including its curly
	// apostrophe - a match written against a straightened copy would pass its
	// test and never fire in production.
	const aws = "Invocation of model ID anthropic.claude-opus-5 with on-demand throughput isn’t supported. " +
		"Retry your request with the ID or ARN of an inference profile that contains this model."

	hint := bedrockHint("anthropic.claude-opus-5", aws)
	if hint == "" {
		t.Fatal("the refusal that costs the most time here carries no hint")
	}
	// The exact string to paste, not a description of its shape.
	if !strings.Contains(hint, `"us.anthropic.claude-opus-5"`) {
		t.Errorf("the hint does not name the id to use: %s", hint)
	}
	// And where to put it. A correct sentence about inference profiles still
	// leaves somebody hunting for the field.
	if !strings.Contains(hint, "Model setting") || !strings.Contains(hint, "Admin -> LLMs") {
		t.Errorf("the hint does not say which setting to change: %s", hint)
	}
	// The other region groups are named, because guessing one for the
	// operator fails exactly as this did.
	if !strings.Contains(hint, "eu.") {
		t.Errorf("the hint offers only one region group: %s", hint)
	}

	// A model that ALREADY carries a prefix does not get a doubled one - that
	// produces a name Bedrock rejects with a confusing "model not found".
	if h := bedrockHint("us.anthropic.claude-opus-5", aws); strings.Contains(h, "us.us.") {
		t.Errorf("the hint doubled the prefix: %s", h)
	}

	// Appended, never substituted: AWS's own wording is what a search finds
	// and what a support case quotes.
	full := withBedrockHint("anthropic.claude-opus-5", aws)
	if !strings.HasPrefix(full, aws) {
		t.Error("the hint replaced AWS's wording instead of following it")
	}

	// Nothing to add, nothing added. A hint on every error is a hint nobody
	// reads.
	if h := bedrockHint("anthropic.claude-opus-5", "The security token included in the request is expired"); h != "" {
		t.Errorf("an unrelated error grew a hint: %s", h)
	}
	if got := withBedrockHint("m", "plain failure"); got != "plain failure" {
		t.Errorf("an unrelated message was rewritten: %q", got)
	}
}

// The other half of the same confusion: the account grants one Bedrock API and
// not the other, and the refusal names an IAM action rather than the switch
// that picks between them.
func TestTheWrongBedrockAPINamesTheSwitch(t *testing.T) {
	const aws = "User is not authorized to perform: bedrock-mantle:CreateInference on this resource"
	hint := bedrockHint("anthropic.claude-opus-5", aws)
	if !strings.Contains(hint, "InvokeModel") {
		t.Errorf("the hint does not name the other API: %s", hint)
	}
}

// "It keeps using us-east-1 even though I set us-west-2" has two causes that
// look identical from outside, and both end at the same default.
func TestTheRegionSaysWhereItCameFrom(t *testing.T) {
	if got := bedrockRegionNote("us-west-2", "us-west-2", ""); !strings.Contains(got, "this tier's AWS region setting") {
		t.Errorf("a configured region does not say so: %s", got)
	}
	// Nothing configured, nothing in the environment: the default, said as
	// one. This is the case where the region was set on the OTHER tier.
	got := bedrockRegionNote("", bedrockDefaultRegion, "")
	for _, want := range []string{"the default", "nor $AWS_REGION"} {
		if !strings.Contains(got, want) {
			t.Errorf("the default region note is missing %q: %s", want, got)
		}
	}
	// Nothing configured but the environment answered: the case where a
	// correct-looking setting on screen is not what the process is using.
	if got = bedrockRegionNote("", "eu-west-1", ""); !strings.Contains(got, "$AWS_REGION in the service environment") {
		t.Errorf("an environment region does not say so: %s", got)
	}
}

// An Endpoint wins over the region-derived host outright, and nothing on
// screen shows it: the AWS region box goes on displaying what it was set to
// while every call goes somewhere else. It is the one cause of "I set
// us-west-2 and it still goes to us-east-1" that cannot be found by looking at
// the region setting.
func TestAnEndpointSilentlyOverridesTheRegion(t *testing.T) {
	got := bedrockRegionNote("us-west-2", "us-west-2", "bedrock-runtime.us-east-1.amazonaws.com")
	if !strings.Contains(got, "an Endpoint is set") {
		t.Errorf("the note does not say what is really deciding the host: %s", got)
	}
	// It names the region the reader set, because that is the value they are
	// looking at and disbelieving.
	if !strings.Contains(got, "us-west-2") {
		t.Errorf("the note does not name the setting being overridden: %s", got)
	}
	// And it does not claim the region setting is in force.
	if strings.Contains(got, "from this tier's AWS region setting") {
		t.Errorf("the note still credits the region setting: %s", got)
	}
}
