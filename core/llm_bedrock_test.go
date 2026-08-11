package core

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBedrockModelID(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"", bedrockDefaultModel},
		{"claude-sonnet-5", "anthropic.claude-sonnet-5"},
		{"anthropic.claude-opus-5", "anthropic.claude-opus-5"},
		// Inference-profile ids carry the prefix mid-string. Prepending to
		// these produces a name Bedrock rejects, so contains beats prefix.
		{"us.anthropic.claude-opus-4-6-v1", "us.anthropic.claude-opus-4-6-v1"},
		{"global.anthropic.claude-sonnet-5", "global.anthropic.claude-sonnet-5"},
	} {
		if got := bedrockModelID(tc.in); got != tc.want {
			t.Errorf("bedrockModelID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBedrockRegionPrecedence(t *testing.T) {
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_DEFAULT_REGION", "ap-south-1")
	if got := bedrockRegion("us-west-2"); got != "us-west-2" {
		t.Errorf("configured region should win, got %q", got)
	}
	if got := bedrockRegion(""); got != "eu-west-1" {
		t.Errorf("AWS_REGION should win over AWS_DEFAULT_REGION, got %q", got)
	}
	t.Setenv("AWS_REGION", "")
	if got := bedrockRegion(""); got != "ap-south-1" {
		t.Errorf("AWS_DEFAULT_REGION should be the fallback, got %q", got)
	}
	t.Setenv("AWS_DEFAULT_REGION", "")
	if got := bedrockRegion(""); got != bedrockDefaultRegion {
		t.Errorf("default region, got %q", got)
	}
}

// newSignableRequest mirrors what doRequest builds, since the signature
// depends on the body being reachable through GetBody.
func newSignableRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest("POST", "https://bedrock-mantle.us-east-1.api.aws/anthropic/v1/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return req
}

func TestSignAWSV4(t *testing.T) {
	creds := awsCreds{AccessKey: "AKIDEXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	when := time.Date(2026, 8, 10, 14, 30, 0, 0, time.UTC)
	body := []byte(`{"model":"anthropic.claude-sonnet-5"}`)

	req := newSignableRequest(t, body)
	if err := signAWSV4(req, body, creds, "us-east-1", bedrockService, when); err != nil {
		t.Fatalf("sign: %v", err)
	}

	auth := req.Header.Get("Authorization")
	// Scope and signed-header list are the two things a server rejects
	// loudest when wrong, and both are silent client-side.
	for _, want := range []string{
		"AWS4-HMAC-SHA256 ",
		"Credential=AKIDEXAMPLE/20260810/us-east-1/bedrock-mantle/aws4_request",
		"SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date",
		"Signature=",
	} {
		if !strings.Contains(auth, want) {
			t.Errorf("Authorization missing %q\n  got: %s", want, auth)
		}
	}
	if got := req.Header.Get("X-Amz-Date"); got != "20260810T143000Z" {
		t.Errorf("X-Amz-Date = %q", got)
	}
	if req.Header.Get("X-Amz-Security-Token") != "" {
		t.Error("no session token was supplied, header should be absent")
	}

	// Same inputs must produce the same signature, or retries break.
	again := newSignableRequest(t, body)
	if err := signAWSV4(again, body, creds, "us-east-1", bedrockService, when); err != nil {
		t.Fatal(err)
	}
	if again.Header.Get("Authorization") != auth {
		t.Error("signature is not deterministic for identical inputs")
	}

	// The payload is signed, so a changed body must change the signature.
	// If this ever passes, the request hash is not covering the body.
	other := []byte(`{"model":"anthropic.claude-opus-5"}`)
	changed := newSignableRequest(t, other)
	if err := signAWSV4(changed, other, creds, "us-east-1", bedrockService, when); err != nil {
		t.Fatal(err)
	}
	if changed.Header.Get("Authorization") == auth {
		t.Error("signature did not change with the payload")
	}
}

func TestSignAWSV4SessionToken(t *testing.T) {
	creds := awsCreds{AccessKey: "AKID", SecretKey: "SECRET", Session: "TOKEN123"}
	body := []byte(`{}`)
	req := newSignableRequest(t, body)
	if err := signAWSV4(req, body, creds, "us-west-2", bedrockService, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Amz-Security-Token") != "TOKEN123" {
		t.Error("session token header not set")
	}
	if !strings.Contains(req.Header.Get("Authorization"), "x-amz-security-token") {
		t.Error("session token must be in SignedHeaders when present")
	}
}

func TestSignAWSV4RejectsEmptyCreds(t *testing.T) {
	body := []byte(`{}`)
	req := newSignableRequest(t, body)
	if err := signAWSV4(req, body, awsCreds{}, "us-east-1", bedrockService, time.Now().UTC()); err == nil {
		t.Error("expected an error for empty credentials")
	}
}

func TestProfileFlag(t *testing.T) {
	for in, want := range map[string]string{
		"":        "",
		"default": "",
		"sso-dev": " --profile sso-dev",
	} {
		if got := profileFlag(in); got != want {
			t.Errorf("profileFlag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCredsFromEnvironment(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRETENV")
	t.Setenv("AWS_SESSION_TOKEN", "SESSIONENV")

	creds, expires, err := resolveAWSCreds("", false)
	if err != nil {
		t.Fatalf("resolveAWSCreds: %v", err)
	}
	if creds.AccessKey != "AKIDENV" || creds.SecretKey != "SECRETENV" || creds.Session != "SESSIONENV" {
		t.Errorf("environment credentials not picked up: %+v", creds)
	}
	if !expires.IsZero() {
		t.Error("environment credentials should carry no expiry")
	}
}

// The cache must not re-shell to the AWS CLI on every request, and must
// re-resolve once the credentials are close to expiring.
func TestBedrockCredsCaching(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKID1")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRET1")

	c := &bedrockCreds{} // no explicit profile: the environment is allowed to win
	first, err := c.get()
	if err != nil {
		t.Fatal(err)
	}

	// A cached, unexpired value is returned even though the environment
	// changed underneath: proof the second call did not re-resolve.
	t.Setenv("AWS_ACCESS_KEY_ID", "AKID2")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRET2")
	second, err := c.get()
	if err != nil {
		t.Fatal(err)
	}
	if second.AccessKey != first.AccessKey {
		t.Errorf("cache miss: got %q, want the cached %q", second.AccessKey, first.AccessKey)
	}

	// Inside the refresh margin, it must re-resolve and pick up the change.
	c.expires = time.Now().Add(credRefreshMargin / 2)
	third, err := c.get()
	if err != nil {
		t.Fatal(err)
	}
	if third.AccessKey != "AKID2" {
		t.Errorf("expiring credentials were not refreshed: got %q", third.AccessKey)
	}
}

func TestAWSCredsFromFileProfile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/credentials"
	if err := os.WriteFile(path, []byte(`
[default]
aws_access_key_id = DEFAULTKEY
aws_secret_access_key = DEFAULTSECRET

; a profile written by a tool that uses the "profile " prefix
[profile work]
aws_access_key_id = WORKKEY
aws_secret_access_key = WORKSECRET
aws_session_token = WORKTOKEN
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", path)

	got, err := awsCredsFromFile("")
	if err != nil || got.AccessKey != "DEFAULTKEY" {
		t.Errorf("default profile: %+v, err=%v", got, err)
	}
	got, err = awsCredsFromFile("work")
	if err != nil || got.AccessKey != "WORKKEY" || got.Session != "WORKTOKEN" {
		t.Errorf("named profile: %+v, err=%v", got, err)
	}
	if _, err = awsCredsFromFile("nope"); err == nil {
		t.Error("expected an error for a missing profile")
	}
}

func TestBedrockEndpointHost(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	if got := BedrockEndpointHost("us-west-2"); got != "bedrock-mantle.us-west-2.api.aws" {
		t.Errorf("BedrockEndpointHost = %q", got)
	}
	// Blank must go through the same region precedence the client uses, or the
	// pre-flight check would validate a different host than the one dialled.
	if got := BedrockEndpointHost(""); got != "bedrock-mantle."+bedrockDefaultRegion+".api.aws" {
		t.Errorf("blank region: %q", got)
	}
}

func TestBedrockProfilePrecedence(t *testing.T) {
	t.Setenv("AWS_PROFILE", "from-env")
	if got := bedrockProfile("from-config"); got != "from-config" {
		t.Errorf("configured profile should win, got %q", got)
	}
	if got := bedrockProfile(""); got != "from-env" {
		t.Errorf("AWS_PROFILE fallback, got %q", got)
	}
	t.Setenv("AWS_PROFILE", "")
	if got := bedrockProfile(""); got != "" {
		t.Errorf("unset should stay empty so AWS picks its own default, got %q", got)
	}
}

// An operator who NAMES a profile must not be silently signed in as whatever
// ambient credentials happen to be in gohort's environment. This is the bug
// that produced a 403 naming a role that appeared nowhere in the config.
func TestExplicitProfileIgnoresAmbientEnvCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDAMBIENT")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRETAMBIENT")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent")
	t.Setenv("PATH", "") // no aws CLI: the profile cannot resolve

	// Explicit profile → must fail loudly rather than sign as the env identity.
	creds, _, err := resolveAWSCreds("named-profile", true)
	if err == nil {
		t.Fatalf("explicit profile silently fell back to %q (%s)", creds.AccessKey, creds.Source)
	}
	if !strings.Contains(err.Error(), "named-profile") {
		t.Errorf("error should name the profile that failed, got: %v", err)
	}

	// No explicit profile → the environment is the documented AWS behaviour.
	creds, _, err = resolveAWSCreds("", false)
	if err != nil {
		t.Fatalf("without an explicit profile the environment should be used: %v", err)
	}
	if creds.AccessKey != "AKIDAMBIENT" {
		t.Errorf("expected the ambient env credentials, got %q", creds.AccessKey)
	}
}

// The prompt-tools fallback describes tools in the system prompt and recovers
// calls from <tool_call> tags. Routing a native-tools API through it recovers
// the tool NAME and drops every argument, which presents as "the model keeps
// sending empty parameters" across every tool at once — including the
// framework's own. The condition that chose it named a single provider, so
// native_tools being unset (its zero value, and the default on a newly
// configured provider) silently caught the rest.
func TestProviderHasNativeTools(t *testing.T) {
	for _, p := range []string{"anthropic", "bedrock", "openai", "gemini", "llama.cpp"} {
		if !ProviderHasNativeTools(p) {
			t.Errorf("%s speaks native tool calling — it must never use the prompt fallback", p)
		}
	}
	// Ollama depends on the model, so the operator toggle still decides.
	if ProviderHasNativeTools("ollama") {
		t.Error("ollama must keep honoring the native_tools toggle")
	}
	// Case and spacing come from stored config, not a literal.
	if !ProviderHasNativeTools("  Bedrock  ") {
		t.Error("provider matching must tolerate stored whitespace and case")
	}
	// Unknown providers stay conservative.
	if ProviderHasNativeTools("some-future-thing") {
		t.Error("an unrecognized provider must default to honoring the toggle")
	}
}
