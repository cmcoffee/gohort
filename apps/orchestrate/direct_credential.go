// Direct credential access is Builder's by default.
//
// An agent that ran into a problem with one of its tools used to reach past it:
// a plain fetch_url to the credential's host was routed through the credential
// with the key attached, and a default-pool agent held a fetch_url_<cred> tool
// for every credential there was. It then guessed at the API itself, key
// placeholders in URLs and invented endpoints, instead of getting the tool
// fixed. Now an agent reaches a credential through the tools bound to it, and
// a broken or missing one is Builder's to fix. Builder keeps direct access: it
// probes an API while building against it. An owner who wants an agent to have
// the raw tool lists fetch_url_<cred> in its allowed tools, and that choice
// stands.

package orchestrate

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// directCredentialRefusal is the ToolSession.DirectCredentialRefusal for an
// agent: nil for Builder (no restriction), otherwise a check that allows the
// credentials the agent's owner granted by name and refuses the rest with what
// to use instead. The bound tools are read from the session when the refusal
// is written, so a tool loaded mid-turn is named.
func directCredentialRefusal(agent AgentRecord, sess *ToolSession) func(string) string {
	if isBuilderAgent(agent.ID) {
		return nil
	}
	granted := map[string]bool{}
	for _, n := range agent.AllowedTools {
		n = SecureToolLegacyAlias(strings.TrimSpace(n))
		if cred := strings.TrimPrefix(n, "fetch_url_"); cred != n && cred != "" {
			granted[cred] = true
		}
	}
	return func(cred string) string {
		if granted[cred] {
			return ""
		}
		bound := boundCredentialTools(sess, cred)
		msg := fmt.Sprintf("this host belongs to credential %q, and this agent reaches a credential only through the tools bound to it, not by fetching the API directly. Nothing was sent", cred)
		if len(bound) > 0 {
			msg += ". Use " + strings.Join(bound, ", ")
		} else {
			msg += ". No tool bound to it is attached to this agent"
		}
		return msg + ". If none of them does what is needed, or one is failing, do not work around it: tell the user what you were trying to do and offer to have Builder build or fix the tool"
	}
}

// boundCredentialTools names the session's tools that reach cred: an api or
// toolbox tool on it, or a script tool granted fetch_via for it.
func boundCredentialTools(sess *ToolSession, cred string) []string {
	if sess == nil {
		return nil
	}
	var out []string
	for _, tt := range sess.CopyTempTools() {
		if tt == nil {
			continue
		}
		hit := tt.Credential == cred
		for _, c := range tt.HookCapabilities {
			if c == "fetch_via:"+cred {
				hit = true
			}
		}
		if hit {
			out = append(out, tt.Name)
		}
	}
	sort.Strings(out)
	return out
}
