// share_status.go — what the owner cannot otherwise see about their own app
// once sharing is a decision someone else makes: where a request stands, who
// the administrator let in, and who turned the app off. Rendered as plain
// lines in the Share modal's status block. Computed here, not in the
// browser, so the wording is one place and a test can read it.
package customapps

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appadmin"
)

// promotionState reports a request's current state for (kind, owner, slug)
// and who decided it. "" when no request was ever filed.
func promotionState(owner, kind, slug string) (state, decidedBy string) {
	if AuthDB == nil {
		return "", ""
	}
	req, ok := GetPromotionRequest(AuthDB(), PromotionRequestKey(kind, owner, slug))
	if !ok {
		return "", ""
	}
	return req.State, req.DecidedBy
}

// requestLine words one publish request's standing for the owner. An
// approved request is not news — the thing it asked for is on — so only a
// pending or denied one gets a line, and only while the thing is still off.
func requestLine(what string, on bool, state, decidedBy string) string {
	if on {
		return ""
	}
	switch state {
	case PromotionPendingState:
		return what + ": requested — awaiting an administrator."
	case PromotionDeniedState:
		by := "an administrator"
		if decidedBy != "" {
			by = decidedBy
		}
		return what + ": request denied by " + by + ". You can ask again."
	}
	return ""
}

// shareStatusLines are the Share modal's status block for an app the owner
// holds. Empty when there is nothing the owner cannot already see from the
// toggles themselves.
func shareStatusLines(spec AppSpec) []string {
	var lines []string
	st := appadmin.Load(RootDB, spec.Owner, spec.Slug)
	if spec.Disabled {
		if st.DisabledBy != "" {
			lines = append(lines, "Disabled by "+st.DisabledBy+". Nobody can open it until it is enabled again.")
		} else {
			lines = append(lines, "Disabled — review its scripts, then Enable.")
		}
	}
	state, by := promotionState(spec.Owner, "app", spec.Slug)
	if l := requestLine("Share with signed-in users", spec.Shared, state, by); l != "" {
		lines = append(lines, l)
	}
	if spec.Shared {
		if len(st.AllowedUsers) > 0 {
			who := "an administrator"
			if st.UpdatedBy != "" {
				who = st.UpdatedBy
			}
			lines = append(lines, "Audience: "+strings.Join(st.AllowedUsers, ", ")+" — narrowed by "+who+".")
		} else {
			lines = append(lines, "Audience: every signed-in user.")
		}
	}
	state, by = promotionState(spec.Owner, "public_link", spec.Slug)
	if l := requestLine("Public link", spec.PublicToken != "", state, by); l != "" {
		lines = append(lines, l)
	}
	return lines
}
