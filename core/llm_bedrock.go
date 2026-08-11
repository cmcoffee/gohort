package core

// AWS Bedrock support, as a thin shell around the Anthropic client.
//
// Bedrock serves Claude two ways. The older one (bedrock-runtime InvokeModel)
// has its own request envelope and AWS's binary event-stream framing for
// streaming; the newer one serves the ordinary Messages API at
// /anthropic/v1/messages with ordinary SSE. This file targets the second,
// which means the entire request builder, the SSE reader, the tool
// marshalling, and the cache-breakpoint logic in llm_anthropic.go are reused
// untouched. Only three things differ: where the request goes, how it is
// signed, and an "anthropic." prefix on the model id.
//
// Two auth paths, because Bedrock offers two and operators land on different
// ones. A bearer token goes in x-api-key exactly like a first-party key. AWS
// credentials require SigV4, implemented below against crypto/hmac rather than
// pulling in the AWS SDK — the whole signature is about eighty lines and the
// SDK is a very large tree to add for one signing routine.
//
// Credentials are resolved through `aws configure export-credentials` before
// falling back to the static credentials file, which is what makes SSO work
// without reimplementing the SSO token exchange: the CLI redeems the cached
// session for role credentials and gohort just signs with them. Because those
// expire (typically hourly), credentials are cached with their expiry and
// re-resolved shortly before it rather than captured once at startup.
//
// Deliberately NOT wired: no model-list browsing (Bedrock has no Models API),
// no server-side tools, no structured outputs. gohort uses none of those on
// the Anthropic path today, so nothing degrades; if that changes, the gap is
// per-provider, not per-call.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/apiclient"
)

const (
	// bedrockService is the SigV4 service name. It is NOT "bedrock" — the
	// Messages-API endpoint signs as its own service, and signing with the
	// wrong name fails with a signature mismatch that reads like a bad
	// secret key, so it is worth stating plainly.
	bedrockService = "bedrock-mantle"

	// bedrockPathPrefix sits in front of the usual /v1/messages.
	bedrockPathPrefix = "/anthropic"

	// bedrockModelPrefix is the provider prefix Bedrock model ids carry:
	// "anthropic.claude-opus-5", not "claude-opus-5".
	bedrockModelPrefix = "anthropic."

	bedrockDefaultRegion = "us-east-1"
	bedrockDefaultModel  = "anthropic.claude-sonnet-5"
)

// awsCreds is a resolved set of AWS credentials. Session is empty for
// long-lived keys and populated for STS / SSO / assumed-role credentials.
type awsCreds struct {
	AccessKey string
	SecretKey string
	Session   string
	Source    string // where they came from, for the debug log only
}

// resolveAWSCreds resolves AWS credentials for the configured profile and
// returns them plus the moment they expire (zero = does not expire).
//
// explicit reports whether the operator NAMED a profile (admin UI / config) as
// opposed to leaving it to the environment. It changes the rules, and the
// distinction is the whole point of this function:
//
//   - Explicit profile: ambient AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY in
//     gohort's process environment are IGNORED, and a failure to resolve the
//     profile is an error rather than a fallback. Naming a profile is a
//     deliberate "use THIS identity" instruction; honouring stray env vars
//     over it, or quietly falling back to some other identity, produces the
//     worst possible failure — a 403 naming a role the operator never chose
//     and cannot find in any gohort setting. AWS's own chain puts env first,
//     but that ordering assumes no application-level profile setting exists.
//
//   - No profile: the usual chain — environment, then the CLI (which itself
//     implements the full AWS chain), then the static credentials file.
//
// The CLI step is what makes SSO, assumed roles, IMDS, and credential_process
// work: `aws configure export-credentials` resolves whatever the profile is
// configured for and hands back concrete keys. That is the officially
// supported way to feed credentials to a non-SDK tool, and it is why this
// does not need to implement the SSO token exchange itself.
func resolveAWSCreds(profile string, explicit bool) (awsCreds, time.Time, error) {
	if !explicit {
		if k, s := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"); k != "" && s != "" {
			return awsCreds{
				AccessKey: k,
				SecretKey: s,
				Session:   os.Getenv("AWS_SESSION_TOKEN"),
				Source:    "environment",
			}, time.Time{}, nil
		}
	}
	c, exp, err := awsCredsFromCLI(profile)
	if err == nil {
		return c, exp, nil
	}
	if explicit {
		// Do NOT fall back. The operator named a profile; signing as somebody
		// else because that profile could not be resolved is how you get a
		// denial that makes no sense against the configuration in front of you.
		return awsCreds{}, time.Time{}, Error("bedrock: could not resolve AWS profile " + profile +
			" — " + err.Error() + ". For SSO, run `aws sso login" + profileFlag(profile) +
			"` as the user gohort runs as (the token cache is per-user).")
	}
	Debug("[bedrock]: aws CLI credential export unavailable (%v), trying the static credentials file", err)
	if c, err := awsCredsFromFile(profile); err == nil {
		return c, time.Time{}, nil
	}
	return awsCreds{}, time.Time{}, Error("bedrock: no AWS credentials found — for SSO run `aws sso login" + profileFlag(profile) + "`, or set AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY, or configure a Bedrock bearer token as the API key")
}

// profileFlag renders the --profile argument for an error message, omitted
// when the default profile is in play.
func profileFlag(profile string) string {
	if profile == "" || profile == "default" {
		return ""
	}
	return " --profile " + profile
}

// awsCredsFromCLI shells out to `aws configure export-credentials`, which
// resolves the profile through the AWS SDK's own chain and prints concrete
// credentials as JSON. This is the SSO path: the CLI redeems the cached SSO
// token for role credentials, refreshing it if it can.
//
// An expired SSO session surfaces here as a non-zero exit with the CLI's own
// message, which already tells the operator to re-run `aws sso login`. That
// message is passed through rather than replaced, since it names the profile
// and the session.
func awsCredsFromCLI(profile string) (awsCreds, time.Time, error) {
	bin, err := exec.LookPath("aws")
	if err != nil {
		return awsCreds{}, time.Time{}, Error("aws CLI not found in PATH")
	}
	args := []string{"configure", "export-credentials", "--format", "process"}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	// Bounded: a token refresh does a network round trip, but this sits in
	// front of an LLM call and must not hang it indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return awsCreds{}, time.Time{}, Error("aws configure export-credentials failed: " + msg)
	}

	var out struct {
		Version         int    `json:"Version"`
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		SessionToken    string `json:"SessionToken"`
		Expiration      string `json:"Expiration"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return awsCreds{}, time.Time{}, Error("aws configure export-credentials returned unparseable output")
	}
	if out.AccessKeyID == "" || out.SecretAccessKey == "" {
		return awsCreds{}, time.Time{}, Error("aws configure export-credentials returned no credentials")
	}
	var expires time.Time
	if out.Expiration != "" {
		// Absent or unparseable expiry is treated as "no expiry" rather than
		// an error: the credentials themselves are valid, and a bad clock
		// should not take Bedrock down. Worst case is one 403 that the next
		// refresh clears.
		if t, err := time.Parse(time.RFC3339, out.Expiration); err == nil {
			expires = t
		}
	}
	src := "aws CLI"
	if profile != "" {
		src += " [" + profile + "]"
	}
	return awsCreds{
		AccessKey: out.AccessKeyID,
		SecretKey: out.SecretAccessKey,
		Session:   out.SessionToken,
		Source:    src,
	}, expires, nil
}

// bedrockCreds caches resolved credentials and re-resolves them when they are
// close to expiring. Static keys never expire and are resolved once; SSO and
// assumed-role credentials are typically good for an hour, so a long-running
// gohort has to refresh rather than sign with a dead session.
type bedrockCreds struct {
	mu sync.Mutex
	// profile is the resolved profile name; explicit records whether it was
	// NAMED by the operator rather than inherited from AWS_PROFILE. See
	// resolveAWSCreds — an explicit profile suppresses every fallback.
	profile  string
	explicit bool
	cur      awsCreds
	expires  time.Time
}

// credRefreshMargin is how far ahead of expiry a refresh is triggered. Wide
// enough to cover a slow CLI round trip plus clock skew between this host and
// AWS, since signing with credentials that expire mid-flight fails the whole
// request.
const credRefreshMargin = 5 * time.Minute

func (c *bedrockCreds) get() (awsCreds, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fresh := c.cur.AccessKey != "" &&
		(c.expires.IsZero() || time.Now().Before(c.expires.Add(-credRefreshMargin)))
	if fresh {
		return c.cur, nil
	}
	creds, expires, err := resolveAWSCreds(c.profile, c.explicit)
	if err != nil {
		return awsCreds{}, err
	}
	c.cur, c.expires = creds, expires
	if expires.IsZero() {
		Debug("[bedrock]: credentials from %s (no expiry)", creds.Source)
	} else {
		Debug("[bedrock]: credentials from %s, expire %s", creds.Source, expires.Format(time.RFC3339))
	}
	return creds, nil
}

// awsCredsFromFile reads ~/.aws/credentials (or $AWS_SHARED_CREDENTIALS_FILE)
// for the named profile, defaulting to "default". Static keys only: a profile
// configured for SSO has no keys here, which is what the CLI step above is for.
//
// Hand-parsed rather than run through snugforge/cfg because that store owns a
// file it can also write, and this one belongs to the AWS CLI: gohort has no
// business rewriting it, and a parser that cannot write cannot corrupt it.
func awsCredsFromFile(profile string) (awsCreds, error) {
	path := os.Getenv("AWS_SHARED_CREDENTIALS_FILE")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return awsCreds{}, err
		}
		path = filepath.Join(home, ".aws", "credentials")
	}
	f, err := os.Open(path)
	if err != nil {
		return awsCreds{}, err
	}
	defer f.Close()

	want := profile
	if want == "" {
		want = "default"
	}

	var in bool
	out := awsCreds{Source: path + " [" + want + "]"}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			// A profile in this file may be written either bare or with a
			// "profile " prefix depending on which AWS tool wrote it.
			name := strings.TrimSpace(strings.Trim(line, "[]"))
			name = strings.TrimPrefix(name, "profile ")
			in = name == want
			continue
		}
		if !in {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "aws_access_key_id":
			out.AccessKey = strings.TrimSpace(value)
		case "aws_secret_access_key":
			out.SecretKey = strings.TrimSpace(value)
		case "aws_session_token":
			out.Session = strings.TrimSpace(value)
		}
	}
	if err := scan.Err(); err != nil {
		return awsCreds{}, err
	}
	if out.AccessKey == "" || out.SecretKey == "" {
		return awsCreds{}, Error(fmt.Sprintf("bedrock: profile %q in %s has no credentials", want, path))
	}
	return out, nil
}

// bedrockRegion picks the region: explicit config first, then the two
// environment variables the AWS tools set, then a default. Region is part of
// both the hostname and the signature, so a wrong one fails at the signature
// rather than with a DNS error.
func bedrockRegion(configured string) string {
	if configured != "" {
		return configured
	}
	if r := os.Getenv("AWS_REGION"); r != "" {
		return r
	}
	if r := os.Getenv("AWS_DEFAULT_REGION"); r != "" {
		return r
	}
	return bedrockDefaultRegion
}

// BedrockEndpointHost returns the Messages-API Bedrock hostname for a region.
//
// Not every region AWS documents for Claude on Bedrock actually has one of
// these hosts: the published region table describes where the service is
// reachable via routing, and the regional endpoint exists in a subset. A
// region without one fails as a DNS error, which reads like a network problem
// rather than a configuration one — hence CheckBedrockEndpoint below, so the
// admin connectivity test can say what is actually wrong. us-west-1 is the
// one that catches people out: N. California has no endpoint, us-west-2 does.
func BedrockEndpointHost(region string) string {
	return fmt.Sprintf("bedrock-mantle.%s.api.aws", bedrockRegion(region))
}

// CheckBedrockEndpoint reports whether a Bedrock endpoint exists for a region,
// by resolving the host. Used as a pre-flight in the admin connectivity test,
// where a precise message is worth a DNS lookup; the client itself does not
// call it, so a transient resolver failure can never block startup.
func CheckBedrockEndpoint(region string) error {
	host := BedrockEndpointHost(region)
	if _, err := net.LookupHost(host); err != nil {
		return Error("no Bedrock endpoint in region " + bedrockRegion(region) + " (" + host +
			" does not resolve). The Messages-API endpoint exists in a subset of the regions AWS lists for Bedrock; us-east-1, us-east-2, us-west-2, eu-west-1, eu-central-1, and ap-northeast-1 all have one.")
	}
	return nil
}

// DescribeBedrockCredentials reports which credential source would be used for
// a profile, e.g. "aws CLI [sso-dev]" or "environment". The admin connectivity
// test surfaces it, because "denied" is unactionable until you know WHICH
// identity was denied — the single most expensive question in a Bedrock setup.
func DescribeBedrockCredentials(profile string) (string, error) {
	creds, _, err := resolveAWSCreds(bedrockProfile(profile), profile != "")
	if err != nil {
		return "", err
	}
	return creds.Source, nil
}

// bedrockProfile picks the AWS profile: explicit config first, then the
// environment, then empty (which lets the AWS tooling pick its own default).
// Configurable rather than env-only because gohort usually runs as a service:
// requiring a unit-file edit and a daemon-reload to change profile, when the
// region next to it is a form field, is the kind of asymmetry that wastes an
// afternoon. Credentials themselves stay out of the config, in the AWS chain.
func bedrockProfile(configured string) string {
	if configured != "" {
		return configured
	}
	return os.Getenv("AWS_PROFILE")
}

// bedrockModelID applies the provider prefix Bedrock expects, leaving a model
// that already carries one alone. Inference-profile ids ("us.anthropic.…",
// "global.anthropic.…") contain the prefix mid-string, so the test is
// contains-not-prefix: prepending to those would produce a name Bedrock
// rejects with a confusing "model not found".
//
// The region-prefixed form is not exotic. Plenty of accounts route Claude
// through a US/EU inference profile and DENY the bare id, which surfaces as a
// 403 on bedrock-mantle:CreateInference rather than anything mentioning the
// model — so "us.anthropic.claude-opus-4-8" being passed through untouched is
// load-bearing, not a nicety.
func bedrockModelID(model string) string {
	if model == "" {
		return bedrockDefaultModel
	}
	if strings.Contains(model, bedrockModelPrefix) {
		return model
	}
	return bedrockModelPrefix + model
}

// newBedrockLLM builds an LLM speaking the Messages API to Bedrock. bearer is
// a Bedrock bearer token and may be empty, in which case AWS credentials are
// resolved and requests are SigV4-signed. endpoint overrides the derived host
// for a private link or a VPC endpoint; it is a host, not a URL.
func newBedrockLLM(bearer, model, region, profile, endpoint string, api *apiclient.APIClient) (LLM, error) {
	region = bedrockRegion(region)

	host := endpoint
	if host == "" {
		host = BedrockEndpointHost(region)
	}
	// Tolerate an operator pasting a full URL into a host field.
	host = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://"), "/")

	if api == nil {
		api = &apiclient.APIClient{
			VerifySSL:      true,
			ConnectTimeout: llmConnectTimeout(),
			RequestTimeout: llmRequestTimeout(),
		}
	}
	api.Server = host

	var provider *bedrockCreds
	if bearer == "" {
		provider = &bedrockCreds{profile: bedrockProfile(profile), explicit: profile != ""}
		// Resolve once up front so a misconfigured box fails at startup with a
		// clear message instead of on the first chat turn. The result is
		// cached; later calls only re-resolve near expiry.
		if _, err := provider.get(); err != nil {
			return nil, err
		}
	} else {
		Debug("[bedrock]: using bearer-token auth (region=%s)", region)
	}

	// AuthFunc runs at send time, after doRequest has attached the body and
	// GetBody, which is what makes SigV4 possible here at all: the signature
	// covers a hash of the payload, so it cannot be computed when the request
	// is first built.
	api.AuthFunc = func(req *http.Request) {
		req.Header.Set("anthropic-version", anthropicAPIVersion)
		if bearer != "" {
			req.Header.Set("x-api-key", bearer)
			return
		}
		var payload []byte
		if req.GetBody != nil {
			if rc, err := req.GetBody(); err == nil {
				payload, _ = io.ReadAll(rc)
				rc.Close()
			}
		}
		creds, err := provider.get()
		if err != nil {
			Debug("[bedrock]: credential refresh failed, sending unsigned: %v", err)
			return
		}
		if err := signAWSV4(req, payload, creds, region, bedrockService, time.Now().UTC()); err != nil {
			// Send unsigned rather than silently hanging: Bedrock answers
			// with a 403 naming the problem, which is a better diagnostic
			// than a request that never leaves.
			Debug("[bedrock]: SigV4 signing failed, sending unsigned: %v", err)
		}
	}

	return &anthropicClient{
		apiKey:      bearer,
		model:       bedrockModelID(model),
		api:         api,
		pathPrefix:  bedrockPathPrefix,
		hoistSystem: true,
	}, nil
}

// signAWSV4 signs req in place using AWS Signature Version 4.
//
// The steps are fixed by AWS and the order matters everywhere: headers are
// signed lowercased and sorted, the signed-header list must match the headers
// actually sent, and the payload hash is of the exact bytes on the wire. A
// mismatch anywhere returns 403 with "signature we calculated does not match",
// which names none of these.
func signAWSV4(req *http.Request, payload []byte, creds awsCreds, region, service string, now time.Time) error {
	if creds.AccessKey == "" || creds.SecretKey == "" {
		return Error("bedrock: incomplete AWS credentials")
	}

	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := hex.EncodeToString(sha256sum(payload))

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if creds.Session != "" {
		req.Header.Set("X-Amz-Security-Token", creds.Session)
	}

	// Sign the small fixed set rather than every header present. Anything not
	// listed here is excluded from the signature, so a proxy that adds a
	// header in flight cannot invalidate it.
	signed := []string{"content-type", "host", "x-amz-content-sha256", "x-amz-date"}
	if creds.Session != "" {
		signed = append(signed, "x-amz-security-token")
	}
	sort.Strings(signed)

	var canonHeaders strings.Builder
	for _, h := range signed {
		value := req.Header.Get(h)
		if h == "host" {
			value = host
		}
		canonHeaders.WriteString(h)
		canonHeaders.WriteString(":")
		canonHeaders.WriteString(strings.TrimSpace(value))
		canonHeaders.WriteString("\n")
	}
	signedHeaders := strings.Join(signed, ";")

	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonicalRequest := strings.Join([]string{
		req.Method,
		path,
		req.URL.RawQuery,
		canonHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(sha256sum([]byte(canonicalRequest))),
	}, "\n")

	key := hmacSHA256([]byte("AWS4"+creds.SecretKey), dateStamp)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		creds.AccessKey, scope, signedHeaders, signature))
	return nil
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
