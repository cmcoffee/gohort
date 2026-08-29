package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func bfConnector(t *testing.T, owner string, spec BotFrameworkSpec) Connector {
	t.Helper()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return Connector{Name: "teams-bot", Kind: BotFrameworkConnectorKind, Owner: owner, Spec: raw}
}

func bfValidSpec() BotFrameworkSpec {
	return BotFrameworkSpec{AppID: "00000000-1111-2222-3333-444444444444", Credential: "bf_outbound"}
}

// --- defaults ----------------------------------------------------------------

func TestBotFrameworkSpecDefaults(t *testing.T) {
	s, err := botFrameworkHandler{}.parse(bfConnector(t, "craig", bfValidSpec()))
	if err != nil {
		t.Fatal(err)
	}
	if s.Service != "teams" {
		t.Errorf("service = %q, want teams", s.Service)
	}
	if s.ServiceHost != botFrameworkServiceHost {
		t.Errorf("service_host = %q, want the public-cloud host", s.ServiceHost)
	}
}

func TestBotFrameworkSpecKeepsOverrides(t *testing.T) {
	spec := bfValidSpec()
	spec.Service = "Teams_Gov"
	spec.ServiceHost = "https://smba.infra.gov.microsoftazure.us/"
	s, err := botFrameworkHandler{}.parse(bfConnector(t, "craig", spec))
	if err != nil {
		t.Fatal(err)
	}
	if s.Service != "teams_gov" {
		t.Errorf("service = %q, want it lowercased", s.Service)
	}
	// A trailing slash here would produce "…us//amer/v3/…" at send time.
	if s.ServiceHost != "https://smba.infra.gov.microsoftazure.us" {
		t.Errorf("service_host = %q, want the trailing slash trimmed", s.ServiceHost)
	}
}

func TestBotFrameworkSpecRejectsGarbage(t *testing.T) {
	c := Connector{Name: "x", Kind: BotFrameworkConnectorKind, Owner: "craig", Spec: json.RawMessage(`{`)}
	if _, err := (botFrameworkHandler{}).parse(c); err == nil {
		t.Fatal("an unparseable spec was accepted")
	}
}

// --- validation --------------------------------------------------------------

// Everything here fires before the credential lookup, so it needs no store.
func TestBotFrameworkValidateEarlyChecks(t *testing.T) {
	h := botFrameworkHandler{}

	tests := []struct {
		name    string
		owner   string
		mutate  func(*BotFrameworkSpec)
		wantErr string
	}{
		{"no owner", "", nil, "requires an owner"},
		{"no app id", "craig", func(s *BotFrameworkSpec) { s.AppID = "" }, "app_id is required"},
		{"no credential", "craig", func(s *BotFrameworkSpec) { s.Credential = "" }, "credential is required"},
		{"bad service name", "craig", func(s *BotFrameworkSpec) { s.Service = "teams bot!" }, "tool-namespace-safe"},
		{"bad channel type", "craig", func(s *BotFrameworkSpec) {
			s.AcceptChannelTypes = []string{"personal", "sms"}
		}, "unknown accept_channel_types"},
		{"plaintext service host", "craig", func(s *BotFrameworkSpec) {
			s.ServiceHost = "http://smba.trafficmanager.net"
		}, "must be https"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := bfValidSpec()
			if tc.mutate != nil {
				tc.mutate(&spec)
			}
			err := h.Validate(bfConnector(t, tc.owner, spec))
			if err == nil {
				t.Fatalf("expected an error containing %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestBotFrameworkValidateAcceptsEveryChannelType(t *testing.T) {
	spec := bfValidSpec()
	spec.AcceptChannelTypes = []string{"personal", "channel", "groupChat"}
	err := botFrameworkHandler{}.Validate(bfConnector(t, "craig", spec))
	// The credential does not exist in this test's store, so validation stops
	// there. What matters is that it got past the channel-type check.
	if err != nil && strings.Contains(err.Error(), "accept_channel_types") {
		t.Fatalf("a valid channel-type list was rejected: %v", err)
	}
}

// --- outbound reach ----------------------------------------------------------

// The region is a path segment under one host, so a base URL of the host with
// no endpoint list has to cover every region at once. Getting this wrong is the
// confusing failure: the bridge receives fine and every reply is refused by our
// own allow-list rather than by Microsoft.
func TestBotFrameworkOutboundReach(t *testing.T) {
	host := botFrameworkServiceHost

	tests := []struct {
		name string
		cred SecureCredential
		want bool
	}{
		{"host base, no endpoints", SecureCredential{BaseURL: host}, true},
		{"host base with trailing slash", SecureCredential{BaseURL: host + "/"}, true},
		{"wrong host", SecureCredential{BaseURL: "https://graph.microsoft.com"}, false},
		{"endpoints too narrow", SecureCredential{
			BaseURL: host, AllowedEndpoints: []string{"/emea/*"},
		}, false},
		{"endpoint covering the region", SecureCredential{
			BaseURL: host, AllowedEndpoints: []string{"/amer/*"},
		}, true},
		{"legacy pattern", SecureCredential{AllowedURLPattern: host + "/**"}, true},
		{"no allow-list at all", SecureCredential{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := botFrameworkOutboundReachable(tc.cred, host); got != tc.want {
				t.Errorf("reachable = %v, want %v (probe %s)", got, tc.want, botFrameworkProbeURL(host))
			}
		})
	}
}

func TestBotFrameworkProbeURLHasNoDoubleSlash(t *testing.T) {
	if got := botFrameworkProbeURL("https://host/"); strings.Contains(strings.TrimPrefix(got, "https://"), "//") {
		t.Errorf("probe url = %q", got)
	}
}

// --- channel-type gate -------------------------------------------------------

func TestBotFrameworkAccepts(t *testing.T) {
	// A connector authored before the field existed accepts everything.
	open := BotFrameworkSpec{}
	for _, ct := range []string{"personal", "channel", "groupChat", "anything"} {
		if !open.Accepts(ct) {
			t.Errorf("empty list rejected %q", ct)
		}
	}

	narrowed := BotFrameworkSpec{AcceptChannelTypes: []string{"personal"}}
	if !narrowed.Accepts("personal") {
		t.Error("personal was rejected")
	}
	if !narrowed.Accepts("PERSONAL") {
		t.Error("conversationType should match case-insensitively")
	}
	if narrowed.Accepts("channel") {
		t.Error("channel was accepted despite a personal-only list")
	}
}

// --- lifecycle ---------------------------------------------------------------

// With no bridges app loaded the kind must stay inert rather than error: the
// same soft-landing rest_messaging gets, so a connector can be approved before
// the app that runs it is up.
func TestBotFrameworkLifecycleWithoutBridgesApp(t *testing.T) {
	prevBridge, prevProvisioner := botBridge, bridgeProvisioner
	botBridge, bridgeProvisioner = nil, nil
	defer func() { botBridge, bridgeProvisioner = prevBridge, prevProvisioner }()

	c := bfConnector(t, "craig", bfValidSpec())
	if err := (botFrameworkHandler{}).Materialize(c); err != nil {
		t.Fatalf("materialize without a bridges app: %v", err)
	}
	if err := (botFrameworkHandler{}).Teardown(c); err != nil {
		t.Fatalf("teardown without a bridges app: %v", err)
	}
}

func TestBotFrameworkLifecycleDrivesTheSeams(t *testing.T) {
	prevBridge, prevProvisioner := botBridge, bridgeProvisioner
	defer func() { botBridge, bridgeProvisioner = prevBridge, prevProvisioner }()

	var started []bool
	var provisioned []string
	botBridge = func(c Connector, start bool) error {
		started = append(started, start)
		return nil
	}
	bridgeProvisioner = func(owner, service string, enabled bool) error {
		provisioned = append(provisioned, owner+"/"+service)
		return nil
	}

	c := bfConnector(t, "craig", bfValidSpec())
	if err := (botFrameworkHandler{}).Materialize(c); err != nil {
		t.Fatal(err)
	}
	if err := (botFrameworkHandler{}).Teardown(c); err != nil {
		t.Fatal(err)
	}

	if len(provisioned) != 1 || provisioned[0] != "craig/teams" {
		t.Errorf("provisioned = %v, want one craig/teams", provisioned)
	}
	if len(started) != 2 || !started[0] || started[1] {
		t.Errorf("bridge start flags = %v, want [true false]", started)
	}
}

// --- registration and summary ------------------------------------------------

func TestBotFrameworkKindIsRegistered(t *testing.T) {
	h, ok := ConnectorHandlerFor(BotFrameworkConnectorKind)
	if !ok {
		t.Fatal("bot_framework did not register a handler")
	}
	// Approval must be mandatory: it routes a tenant's conversations to an agent
	// and posts back under the bot's identity.
	if connectorAutoApproves(h) {
		t.Error("bot_framework auto-approves; it must wait for an admin")
	}
}

func TestBotFrameworkSummaryNamesWhatAnAdminNeeds(t *testing.T) {
	spec := bfValidSpec()
	spec.TenantID = "tenant-9"
	spec.AcceptChannelTypes = []string{"personal"}
	got := botFrameworkHandler{}.Summary(bfConnector(t, "craig", spec))

	for _, want := range []string{spec.AppID, "tenant-9", "personal", "bf_outbound", "/bridges/api/bot/teams-bot"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q is missing %q", got, want)
		}
	}
}

func TestBotFrameworkVerifierIsSharedAndPinned(t *testing.T) {
	v := BotFrameworkVerifier()
	if v == nil {
		t.Fatal("no verifier")
	}
	if v != BotFrameworkVerifier() {
		t.Error("a second call returned a different verifier; the key cache would not be shared")
	}
	if v.Issuer != botFrameworkIssuer {
		t.Errorf("issuer = %q, want %q", v.Issuer, botFrameworkIssuer)
	}
	if !strings.HasPrefix(v.MetadataURL, "https://") {
		t.Errorf("metadata url must be https, got %q", v.MetadataURL)
	}
}
