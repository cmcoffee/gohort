package core

// AWS words its refusals for somebody holding the API reference. The two that
// cost the most time here each have a SETTING behind them, and neither
// mentions it.

import (
	"os"
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

// A message and a hint join without doubling the punctuation between them.
// The first version hardcoded a period at both ends and shipped "us-east-1.."
// into a warning an operator was meant to read carefully.
func TestTheHintJoinsWithoutDoublingPunctuation(t *testing.T) {
	// AWS's IAM refusals end with no punctuation; its validation errors end
	// with a period. Both have to read correctly.
	noStop := "User is not authorized to perform: bedrock:InvokeModelWithResponseStream on resource: " +
		"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-opus-5 because no identity-based policy allows the action"
	got := withBedrockHint("anthropic.claude-opus-5", noStop)
	if strings.Contains(got, "..") {
		t.Errorf("doubled punctuation: %s", got)
	}
	if !strings.Contains(got, "action. The resource") {
		t.Errorf("the two sentences did not join: %s", got)
	}

	withStop := "Invocation of model ID anthropic.claude-opus-5 with on-demand throughput isn’t supported."
	if got = withBedrockHint("anthropic.claude-opus-5", withStop); strings.Contains(got, "..") {
		t.Errorf("doubled punctuation on a message that punctuated itself: %s", got)
	}

	for _, c := range []struct{ in, want string }{
		{"no stop", "no stop."},
		{"has one.", "has one."},
		{"a question?", "a question?"},
		{"trailing space ", "trailing space."},
		{"", ""},
	} {
		if got := endSentence(c.in); got != c.want {
			t.Errorf("endSentence(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// An ARN is already fully qualified. An APPLICATION inference profile is named
// by ARN and its id is opaque - nothing in it says "anthropic" - so the
// contains-test would miss it and prepend, turning a valid ARN into a name
// Bedrock cannot resolve. That is the shape an account takes when somebody
// moves you off the system profiles onto one of their own.
func TestAnARNIsNeverPrefixed(t *testing.T) {
	for _, arn := range []string{
		"arn:aws:bedrock:us-west-2:123456789012:application-inference-profile/a1b2c3d4e5f6",
		"arn:aws:bedrock:us-west-2:123456789012:inference-profile/us.anthropic.claude-opus-5",
		"arn:aws:bedrock:us-west-2::foundation-model/anthropic.claude-opus-5",
	} {
		if got := bedrockModelID(arn); got != arn {
			t.Errorf("bedrockModelID(%q) = %q", arn, got)
		}
	}
	// The ordinary cases are unchanged.
	if got := bedrockModelID("claude-sonnet-5"); got != "anthropic.claude-sonnet-5" {
		t.Errorf("a bare model name lost its prefix: %q", got)
	}
	if got := bedrockModelID("us.anthropic.claude-opus-5"); got != "us.anthropic.claude-opus-5" {
		t.Errorf("a system profile id was rewritten: %q", got)
	}
}

// The same refusal means two different things, and telling somebody the wrong
// one costs more than saying nothing.
//
// AWS expands a cross-region profile and authorizes against the underlying
// model in EVERY region it can route to, so a us-east-1 foundation-model ARN
// can come back from a call that went to us-west-2 asking for a profile. The
// first version of this hint read that as a bare model id and told the
// operator to set a Model field that was already correct - sending them to the
// one place with no answer in it.
func TestAProfileRefusalIsNotAConfigError(t *testing.T) {
	const aws = "User is not authorized to perform: bedrock:InvokeModelWithResponseStream on resource: " +
		"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-opus-5 because no identity-based policy allows the action"

	// Configured with a profile: the ARN is a member region, not the request.
	hint := bedrockHint("us.anthropic.claude-opus-5", aws)
	if strings.Contains(hint, "set Model to") {
		t.Errorf("the hint told an already-correct config to change the model: %s", hint)
	}
	for _, want := range []string{"inference profile", "member", "us-east-1", "AWS policy change"} {
		if !strings.Contains(hint, want) {
			t.Errorf("the profile hint is missing %q: %s", want, hint)
		}
	}

	// Configured with a bare id: that IS the config error, and the old advice
	// is right.
	if hint = bedrockHint("anthropic.claude-opus-5", aws); !strings.Contains(hint, "set Model to") {
		t.Errorf("a genuinely bare model id lost its fix: %s", hint)
	}

	// An application profile is an ARN and counts the same way.
	arn := "arn:aws:bedrock:us-west-2:123456789012:application-inference-profile/a1b2c3"
	if hint = bedrockHint(arn, aws); strings.Contains(hint, "set Model to") {
		t.Errorf("an application profile was read as a bare model id: %s", hint)
	}

	for _, m := range []string{"us.anthropic.claude-opus-5", "eu.anthropic.claude-opus-5",
		"global.anthropic.claude-opus-5", arn,
		"arn:aws:bedrock:us-west-2:123456789012:inference-profile/us.anthropic.claude-opus-5"} {
		if !bedrockIsProfile(m) {
			t.Errorf("%q did not read as a profile", m)
		}
	}
	for _, m := range []string{"anthropic.claude-opus-5", "claude-sonnet-5",
		"arn:aws:bedrock:us-west-2::foundation-model/anthropic.claude-opus-5"} {
		if bedrockIsProfile(m) {
			t.Errorf("%q read as a profile", m)
		}
	}
}

// A pasted model id arrives with whatever came with it. Whitespace is the
// common case and is trimmed; anything else is left alone and reaches AWS,
// which answers about identifiers and authorization rather than about the
// characters in the field.
func TestAPastedModelIsTrimmedBeforeAnythingElse(t *testing.T) {
	const arn = "arn:aws:bedrock:us-west-2:123456789012:application-inference-profile/a1b2c3d4e5f6"
	for _, in := range []string{" " + arn, arn + "\n", "\t" + arn + " \r\n", "  " + arn + "  "} {
		if got := bedrockModelID(in); got != arn {
			t.Errorf("bedrockModelID(%q)\n got: %s\nwant: %s", in, got, arn)
		}
	}
	// Untrimmed, a leading space defeats BOTH tests that would recognise an
	// ARN: it does not start with "arn:", and an application profile's id
	// says nothing about anthropic. The result was "anthropic. arn:aws:..." on
	// the wire, and a 400 naming nothing a reader can act on.
	if got := bedrockModelID(" " + arn); strings.HasPrefix(got, bedrockModelPrefix) {
		t.Errorf("a padded ARN was treated as a bare model name: %s", got)
	}
	// The ids that DO take a prefix must not grow "anthropic. name" either.
	if got := bedrockModelID("  claude-sonnet-5 "); got != "anthropic.claude-sonnet-5" {
		t.Errorf("a padded model name: %q", got)
	}
	// Whitespace alone is "nothing set", not a model called " ".
	if got := bedrockModelID("   "); got != bedrockDefaultModel {
		t.Errorf("blank-but-padded did not fall back to the default: %q", got)
	}
}

// BOTH Bedrock modes, because they are different clients and the fixes had to
// reach both.
//
// The InvokeModel mode is its own client; the Messages-API mode is an
// anthropicClient carrying the Bedrock path prefix. They share the signer and
// the model-id rule, and they did NOT share the hints - so a deployment on the
// Messages API got AWS's raw sentence and no idea which setting it named.
func TestBothBedrockModesGetTheSameTreatment(t *testing.T) {
	const aws = "Invocation of model ID anthropic.claude-opus-5 with on-demand throughput isn’t supported."

	// Messages-API mode: an anthropicClient with the Bedrock prefix.
	bedrockish := &anthropicClient{model: "anthropic.claude-opus-5", pathPrefix: bedrockPathPrefix}
	got := bedrockish.apiError(400, aws)
	if !strings.Contains(got.Message, "us.anthropic.claude-opus-5") {
		t.Errorf("the Messages-API mode gets no hint: %s", got.Message)
	}
	// And it reports as Bedrock: saying "anthropic" about a call that went to
	// AWS sends the reader to the wrong settings page.
	if got.Provider != "bedrock" {
		t.Errorf("provider = %q, want bedrock", got.Provider)
	}

	// A REAL Anthropic client is untouched - no AWS hint on a call that never
	// went near AWS.
	plain := &anthropicClient{model: "claude-opus-5"}
	got = plain.apiError(400, aws)
	if got.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", got.Provider)
	}
	if got.Message != aws {
		t.Errorf("a direct Anthropic error was rewritten: %s", got.Message)
	}

	// The model-id rule is shared by construction: both constructors run the
	// id through bedrockModelID, so the ARN passthrough and the trim reach
	// both without either client knowing about them.
	src := readRepoSource(t, "llm_bedrock.go")
	if strings.Count(src, "bedrockModelID(model)") == 0 {
		t.Error("the Messages-API constructor stopped normalizing the model id")
	}
}

func readRepoSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
