// Whether every model this deployment talks to stays under the operator's
// control — and what that unlocks.
//
// Some apps are pinned to the worker tier because they handle material that
// must not reach a third-party model: servitor holds SSH credentials, log
// contents and runtime facts, so RouteStage.Private locks it down regardless of
// what the routing menu says.
//
// That pin assumes the LEAD is remote. When it is not — a local llama.cpp, an
// ollama on the same box, a peer instance the operator also owns — the reason
// for the pin has evaporated while the pin remains, and the effect is that the
// most sensitive app in the system is the one permanently denied the better
// reasoner. Private mode is not a restriction here; it is the condition under
// which a restriction can be LIFTED.
//
// WHY THIS IS NOT INFERRED SILENTLY. Getting it wrong sends SSH credentials to
// a cloud provider, and there is no way to un-send them. An endpoint on a
// private address is good evidence and not proof: it may be a tunnel, a proxy,
// or a gateway that forwards to a hosted model. So the deployment infers a
// RECOMMENDATION and the operator makes the claim; nothing here decides on its
// own that a stage may escalate.
package core

import (
	"net"
	"net/url"
	"strings"
)

// allLLMsPrivateKey stores the operator's assertion in the web settings table.
const allLLMsPrivateKey = "all_llms_private"

// AllLLMsPrivate reports whether the operator has declared that every LLM this
// deployment uses stays under their control.
//
// Defaults to FALSE, and a lookup that cannot reach the database returns false
// too. The safe direction is unambiguous: false keeps the existing pins, which
// is the behavior every deployment has had until now.
func AllLLMsPrivate() bool {
	if AuthDB == nil {
		return false
	}
	db := AuthDB()
	if db == nil {
		return false
	}
	var v bool
	db.Get(WebTable, allLLMsPrivateKey, &v)
	return v
}

// SetAllLLMsPrivate records the operator's assertion.
func SetAllLLMsPrivate(on bool) {
	if AuthDB == nil {
		return
	}
	if db := AuthDB(); db != nil {
		db.Set(WebTable, allLLMsPrivateKey, on)
	}
}

// LLMPrivacyVerdict describes one configured tier for the admin panel.
type LLMPrivacyVerdict struct {
	Tier     string // "Lead" | "Worker"
	Provider string
	Endpoint string
	Private  bool
	Reason   string
}

// ProviderLooksPrivate judges one provider/endpoint pair.
//
// Hosted providers are decided by name — anthropic, openai, gemini and bedrock
// are third parties whatever endpoint is configured, and an operator pointing
// one at localhost has built a proxy to a third party rather than a local
// model. Everything else is judged by where the endpoint points.
func ProviderLooksPrivate(provider, endpoint string) (bool, string) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "openai", "gemini", "bedrock":
		return false, "a hosted provider — prompts leave this deployment"
	}
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		// llama.cpp and ollama default to a loopback endpoint when unset.
		return true, "local by default (no endpoint configured)"
	}
	host := endpointHost(ep)
	if host == "" {
		return false, "endpoint could not be parsed, so it cannot be judged private"
	}
	if isLoopbackHost(host) {
		return true, "loopback endpoint"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsPrivate() {
		return true, "private-network endpoint"
	}
	if strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
		return true, "endpoint on the local network"
	}
	return false, "endpoint is a public host"
}

// endpointHost extracts a hostname from a configured endpoint, tolerating a
// bare host:port with no scheme.
func endpointHost(ep string) string {
	if !strings.Contains(ep, "://") {
		ep = "http://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	return strings.ToLower(strings.TrimSpace(host))
}

// RecommendAllLLMsPrivate judges the configured tiers and reports whether the
// assertion looks justified, with a verdict per tier so the admin panel can
// show WHY rather than just a yes or no.
//
// Advice only. The operator's setting is what RouteToLead consults.
//
// Reads the tiers itself rather than taking them as arguments: the caller is a
// UI, and a UI passing in a stale or partially-filled config would produce a
// confident verdict about a configuration that is not the running one.
func RecommendAllLLMsPrivate() (recommended bool, verdicts []LLMPrivacyVerdict) {
	recommended = true
	for _, t := range []struct{ name, table string }{
		{"Worker", LLMTable},
		{"Lead", LeadLLMTable},
	} {
		provider, endpoint := tierProviderEndpoint(t.table)
		// An unconfigured lead means lead stages fall through to the worker,
		// so there is no second endpoint to judge and the worker's verdict is
		// the whole story.
		if t.name == "Lead" && strings.TrimSpace(provider) == "" {
			verdicts = append(verdicts, LLMPrivacyVerdict{
				Tier: t.name, Private: true,
				Reason: "not configured — lead stages run on the worker model",
			})
			continue
		}
		// A peer provider inherits the peer's own judgement. The endpoint is
		// this instance reaching another gohort, and what matters is whether
		// THAT instance runs a local model — which it does, because inference
		// sharing lends llama.cpp and ollama only and never relays a hosted
		// provider (see peer_models.go).
		if p, ok := PeerFromProvider(provider); ok {
			verdicts = append(verdicts, LLMPrivacyVerdict{
				Tier: t.name, Provider: provider, Endpoint: p.BaseURL, Private: true,
				Reason: "a peer instance, which lends only its own local models",
			})
			continue
		}
		ok, why := ProviderLooksPrivate(provider, endpoint)
		verdicts = append(verdicts, LLMPrivacyVerdict{
			Tier: t.name, Provider: provider, Endpoint: endpoint, Private: ok, Reason: why,
		})
		if !ok {
			recommended = false
		}
	}
	return recommended, verdicts
}

// tierProviderEndpoint reads the two fields the privacy judgement needs.
func tierProviderEndpoint(table string) (provider, endpoint string) {
	if AuthDB == nil {
		return "", ""
	}
	db := AuthDB()
	if db == nil {
		return "", ""
	}
	db.Get(table, "provider", &provider)
	db.Get(table, "endpoint", &endpoint)
	return strings.TrimSpace(provider), strings.TrimSpace(endpoint)
}
