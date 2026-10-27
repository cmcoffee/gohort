package core

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/apiclient"
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
	if got := bedrockEndpointHost("us-west-2"); got != "bedrock-mantle.us-west-2.api.aws" {
		t.Errorf("bedrockEndpointHost = %q", got)
	}
	// Blank must go through the same region precedence the client uses, or the
	// pre-flight check would validate a different host than the one dialled.
	if got := bedrockEndpointHost(""); got != "bedrock-mantle."+bedrockDefaultRegion+".api.aws" {
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
	// A peer model runs through the llama.cpp client, so it is native too;
	// judged on the raw "peer:<name>" it used to fall back to prompt-parsing.
	if !ProviderHasNativeTools("peer:basement-gpu") {
		t.Error("a peer-backed provider speaks native tool calling through llama.cpp")
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

// Proves what actually goes on the wire for the InvokeModel path: URL, method,
// headers, signature, and body. Everything AWS validates before it looks at
// permissions.
func TestBedrockRuntimeWireFormat(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRETTEST")
	t.Setenv("AWS_SESSION_TOKEN", "TOKENTEST")

	var gotPath, gotAuth, gotToken, gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotAuth = r.Header.Get("Authorization")
		gotToken = r.Header.Get("X-Amz-Security-Token")
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"msg_1","model":"m","stop_reason":"end_turn",
			"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	api := &apiclient.APIClient{URLScheme: "http", VerifySSL: false}
	llm, err := newBedrockRuntimeLLM("", "anthropic.claude-3-5-sonnet-20241022-v2:0",
		"us-west-2", "", host, api)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}

	resp, err := llm.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q", resp.Content)
	}

	wantPath := "/model/anthropic.claude-3-5-sonnet-20241022-v2%3A0/invoke"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if gotToken != "TOKENTEST" {
		t.Errorf("session token header = %q", gotToken)
	}
	for _, want := range []string{
		"AWS4-HMAC-SHA256 ",
		"/us-west-2/bedrock/aws4_request",
		"x-amz-security-token",
	} {
		if !strings.Contains(gotAuth, want) {
			t.Errorf("Authorization missing %q\n  got %s", want, gotAuth)
		}
	}
	if gotBody["anthropic_version"] != bedrockAnthropicVersion {
		t.Errorf("body anthropic_version = %v", gotBody["anthropic_version"])
	}
	if _, bad := gotBody["model"]; bad {
		t.Error("body carried a model field")
	}
	if gotBody["messages"] == nil {
		t.Error("body carried no messages")
	}
}

// Regression against a REAL, confirmed-working Bedrock call: the exact model
// id, region, envelope, and response body from a successful
// `aws bedrock-runtime invoke-model` on an account whose permission set grants
// bedrock:InvokeModel. If this passes, anything still failing in the field is
// configuration or AWS-side, not the client.
func TestBedrockRuntimeAgainstRealInvokeModelExchange(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRETTEST")

	const realResponse = `{"model":"claude-opus-4-8","id":"msg_bdrk_se7tkefzq42dbw2cjwzdbwc34ia4e6ivays5jxa7h424yc7uy65q","type":"message","role":"assistant","content":[{"type":"text","text":"Hi there! How can I help you today?"}],"stop_reason":"end_turn","stop_sequence":null,"stop_details":null,"usage":{"input_tokens":8,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":14,"output_tokens_details":{"thinking_tokens":0},"service_tier":"standard"}}`

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, realResponse)
	}))
	defer srv.Close()

	api := &apiclient.APIClient{URLScheme: "http", VerifySSL: false}
	llm, err := newBedrockRuntimeLLM("", "us.anthropic.claude-opus-4-8", "us-west-2", "",
		strings.TrimPrefix(srv.URL, "http://"), api)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	resp, err := llm.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}

	// The id has no colon, so it must NOT be mangled by the escaping added for
	// versioned ids.
	if want := "/model/us.anthropic.claude-opus-4-8/invoke"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if resp.Content != "Hi there! How can I help you today?" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q", resp.StopReason)
	}
	if resp.InputTokens != 8 || resp.OutputTokens != 14 {
		t.Errorf("tokens = %d/%d, want 8/14", resp.InputTokens, resp.OutputTokens)
	}
	if resp.Model != "claude-opus-4-8" {
		t.Errorf("model = %q", resp.Model)
	}
}

// Tool arguments must survive the InvokeModel path. Every grouped tool in the
// fleet takes a required "action", so an input that arrives empty turns every
// call into a bare probe that answers with help text — which is precisely the
// symptom of a turn spent re-calling tools and learning nothing.
func TestBedrockRuntimeCarriesToolArguments(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SECRETTEST")

	const withToolUse = `{"model":"claude-opus-5","id":"msg_1","type":"message","role":"assistant",
		"stop_reason":"tool_use","content":[
			{"type":"text","text":"Looking that up."},
			{"type":"tool_use","id":"toolu_1","name":"archetype","input":{"action":"read","slug":"knowledge_base"}}
		],"usage":{"input_tokens":8,"output_tokens":14}}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, withToolUse)
	}))
	defer srv.Close()

	api := &apiclient.APIClient{URLScheme: "http", VerifySSL: false}
	llm, err := newBedrockRuntimeLLM("", "us.anthropic.claude-opus-5", "us-west-2", "",
		strings.TrimPrefix(srv.URL, "http://"), api)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.Name != "archetype" {
		t.Errorf("name = %q", tc.Name)
	}
	if tc.Args["action"] != "read" || tc.Args["slug"] != "knowledge_base" {
		t.Errorf("arguments were lost in transit: %+v", tc.Args)
	}
}

// A tool input the decoder cannot read must not silently become "no arguments".
// That is how a grouped tool ends up answering with its usage spec: the model
// constructed a call, the arguments were dropped in transit, and nothing said so.
func TestToolInputShapesAndFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		input   string
		want    string
		wantErr bool
	}{
		"object":         {`{"action":"read","slug":"kb"}`, "read", false},
		"string-encoded": {`"{\"action\":\"read\",\"slug\":\"kb\"}"`, "read", false},
		"empty":          {``, "", false},
		"empty string":   {`""`, "", false},
		"garbage":        {`"not json at all"`, "", true},
	} {
		t.Run(name, func(t *testing.T) {
			args, err := decodeToolInput([]byte(tc.input))
			if tc.wantErr && err == nil {
				t.Fatal("an undecodable input must report an error, not degrade to a bare call")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want != "" && args["action"] != tc.want {
				t.Errorf("action = %v, want %q", args["action"], tc.want)
			}
			if args == nil {
				t.Error("args must never be nil — callers index it directly")
			}
		})
	}
}

// A Bedrock credential failure has to say enough to tell "AWS refused" apart
// from "this process is not looking where you logged in". Those two produce
// the same non-zero exit and lead to opposite actions: go fix an SSO
// assignment, versus go look at which user the service runs as.

func TestAnOldCLIIsRecognizedAsTooOldForExportCredentials(t *testing.T) {
	for _, tc := range []struct {
		ver string
		old bool
		why string
	}{
		{"aws-cli/2.4.18", true, "2.4 predates export-credentials"},
		{"aws-cli/1.29.0", true, "every v1 predates it"},
		{"aws-cli/2.12.9", true, "2.12 is the last release without it"},
		{"aws-cli/2.13.0", false, "2.13 is where it landed"},
		{"aws-cli/2.30.1", false, "anything newer has it"},
		{"aws-cli/3.0.0", false, "a future major is not older"},
		// An unparseable version must NOT read as too old. Guessing wrong here
		// sends somebody to upgrade a CLI that was never the problem, which is
		// the failure this whole diagnosis exists to prevent.
		{"aws-cli/experimental", false, "an unparseable version is not a verdict"},
		{"", false, "no version is not a verdict"},
	} {
		if got := awsCLITooOld(tc.ver); got != tc.old {
			t.Errorf("awsCLITooOld(%q) = %v, want %v: %s", tc.ver, got, tc.old, tc.why)
		}
	}
}

// The version is read off the banner, which v2 prints to stdout and some v1
// builds print to stderr. Missing it on the exact CLI whose age IS the problem
// would make the diagnosis worse than silence.
func TestTheCLIVersionIsReadFromEitherStream(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	for _, stream := range []string{"1", "2"} {
		bin := fakeAWS(t, "echo 'aws-cli/2.4.18 Python/3.8.8 Linux/x86_64' >&"+stream)
		got, ok := awsCLIVersion(bin)
		if !ok || got != "aws-cli/2.4.18" {
			t.Errorf("version off fd %s = %q (ok=%v), want aws-cli/2.4.18", stream, got, ok)
		}
	}
}

// A binary that prints no banner leaves the version unknown rather than
// inventing one, and the diagnosis still carries the facts that do not need it.
func TestAnUnreadableVersionStillDiagnoses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	bin := fakeAWS(t, "echo 'no banner here'")
	if _, ok := awsCLIVersion(bin); ok {
		t.Error("a binary with no banner reported a version")
	}
	d := awsCLIDiagnosis(bin)
	if !strings.Contains(d, "version unknown") {
		t.Errorf("the diagnosis hides that the version could not be read: %s", d)
	}
	if !strings.Contains(d, bin) {
		t.Errorf("the diagnosis does not name which binary was run: %s", d)
	}
}

// The three facts, and the right closing advice for each case.
func TestTheDiagnosisNamesBinaryUserAndHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	t.Setenv("HOME", "/home/placeholder")

	old := awsCLIDiagnosis(fakeAWS(t, "echo 'aws-cli/2.4.18 Python/3.8.8 Linux/x86_64'"))
	if !strings.Contains(old, "aws-cli/2.4.18") {
		t.Errorf("the version is missing: %s", old)
	}
	if !strings.Contains(old, "/home/placeholder") {
		t.Errorf("HOME is missing, so nobody can see whose token cache was read: %s", old)
	}
	if !strings.Contains(old, "running as ") {
		t.Errorf("the effective user is missing: %s", old)
	}
	// Too old is a DIFFERENT instruction. Telling somebody to re-run
	// `aws sso login` when the subcommand does not exist in their CLI sends
	// them around a loop that cannot terminate.
	if !strings.Contains(old, "upgrade the CLI") {
		t.Errorf("an ancient CLI is not called out: %s", old)
	}
	if strings.Contains(old, "aws sso login") {
		t.Errorf("an ancient CLI is told to log in again, which cannot help: %s", old)
	}

	cur := awsCLIDiagnosis(fakeAWS(t, "echo 'aws-cli/2.31.0 Python/3.12.0 Linux/x86_64'"))
	if strings.Contains(cur, "upgrade the CLI") {
		t.Errorf("a current CLI is blamed for its age: %s", cur)
	}
	// The per-user token cache is the most common cause and the hardest to
	// see, because the operator's own login is genuinely fine.
	if !strings.Contains(cur, "per-user") || !strings.Contains(cur, "aws sso login") {
		t.Errorf("a current CLI does not get pointed at the per-user token cache: %s", cur)
	}
}

// fakeAWS writes an executable stub and returns its path. t.TempDir is removed
// with the test, so nothing lands in the tree.
func fakeAWS(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "aws")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// AWS words its refusals for somebody holding the API reference. The two that
// cost the most time here each have a SETTING behind them, and neither
// mentions it.

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
	// And it must not assert what the policy DOES grant. "The policy grants
	// the profile but not all of its member regions" was written from an
	// account that had that shape, but a role granted nothing fails
	// identically - as does an account whose roles are scoped to their own
	// APPLICATION inference profiles, which refuses every system profile this
	// same way (observed on a company AI permission set, where neither
	// bedrock:InvokeModel nor bedrock-mantle:CreateInference was granted on
	// anything except the account's own application profiles). Claiming a
	// partial grant sends the reader auditing member regions for an action
	// that was never there.
	if strings.Contains(hint, "policy grants the profile") {
		t.Errorf("the hint asserts a partial grant it cannot know about: %s", hint)
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

// The field refuses what AWS would only complain about later, and in terms of
// the field rather than the policy.
func TestAModelIDWithInvisibleRubbishIsRefused(t *testing.T) {
	const arn = "arn:aws:bedrock:us-west-2:123456789012:application-inference-profile/a1b2c3d4e5f6"

	// The real case: an ARN copied out of coloured terminal output, with the
	// ESC stripped somewhere and a bare "[1m" left behind. AWS answered this
	// with "Your account is not authorized to invoke this API operation",
	// which is about as misleading as an error gets - the account was fine.
	err := ValidateModelID("bedrock", arn+"[1m]")
	if err == nil {
		t.Fatal("terminal formatting was accepted")
	}
	if !strings.Contains(err.Error(), "terminal formatting") {
		t.Errorf("the refusal does not name the cause: %s", err)
	}
	// With the ESC still attached, and in the middle rather than the end.
	if ValidateModelID("bedrock", "\x1b[0;32m"+arn) == nil {
		t.Error("an ESC sequence was accepted")
	}
	if ValidateModelID("bedrock", arn+"\x1b[0m") == nil {
		t.Error("a trailing reset was accepted")
	}

	// Anything else outside the legal set is named with the character and
	// where it is, so a long ARN does not have to be eyeballed.
	err = ValidateModelID("bedrock", arn+"<")
	if err == nil {
		t.Fatal("an illegal character was accepted")
	}
	for _, want := range []string{"letters, digits", "'<'"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal is missing %q: %s", want, err)
		}
	}

	// Every shape that IS legal passes, or the guard is worse than the bug.
	for _, ok := range []string{
		arn,
		"anthropic.claude-sonnet-5",
		"us.anthropic.claude-opus-5",
		"anthropic.claude-v2:1",
		"arn:aws:bedrock:us-west-2:123456789012:inference-profile/us.anthropic.claude-opus-5",
		"  " + arn + "  ", // trimmed first, so padding is not an error
		"",                // "not set" is a valid state and not this function's business
	} {
		if err := ValidateModelID("bedrock", ok); err != nil {
			t.Errorf("ValidateModelID(%q) = %v", ok, err)
		}
	}

	// A bracket that is NOT an escape sequence is left alone: the test is for
	// "[digits m", not for the character.
	if err := ValidateModelID("ollama", "qwen3:8b[instruct]"); err != nil {
		t.Errorf("a plain bracket was read as an escape: %v", err)
	}

	// Other providers keep their own vocabularies - the charset rule is
	// Bedrock's - but no provider accepts a control character.
	if err := ValidateModelID("openai", "gpt-4o"); err != nil {
		t.Errorf("an OpenAI model was judged by Bedrock's rules: %v", err)
	}
	if ValidateModelID("ollama", "qwen3\t8b") == nil {
		t.Error("a control character was accepted for a non-Bedrock provider")
	}
}
