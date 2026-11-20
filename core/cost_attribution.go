// Who caused a cost, when it was not the person looking at the bill.
//
// The ledger already prices credential-dispatched calls: a REST image connector
// bound to a credential with CostPerCall lands in it on every render. What it
// could not say is WHO. A peer borrowing an image backend spends the operator's
// vendor quota, and the row read "cred:openai_images" exactly as it does when
// the operator renders something themselves — so the one question worth asking
// of a shared resource ("what has lending this cost me, and to whom?") had no
// answer in the place cost is recorded.
//
// Attribution rides the CONTEXT rather than being threaded through every call
// site, for the same reason the embed and render callers do: the cost is
// recorded several layers below the handler that knows a peer is responsible,
// and the layers in between have no business learning about peers.
package core

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/core/costledger"
	"github.com/cmcoffee/gohort/core/notices"
)

type costAttributionKey struct{}

// WithCostAttribution labels everything billable done under ctx as caused by
// `who` — "peer:studio-mac" and the like.
//
// A label, not an identity: it appears in the ledger and in the admin cost
// chart, so it should read as something an operator recognizes.
func WithCostAttribution(ctx context.Context, who string) context.Context {
	if who = strings.TrimSpace(who); who == "" || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, costAttributionKey{}, who)
}

// costAttribution reads the label, empty when the work is the operator's own.
func costAttribution(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if s, ok := ctx.Value(costAttributionKey{}).(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// RecordExternalCostFor prices a call and attributes it to whoever caused it.
//
// Attributed work gets its OWN ledger row rather than being folded into the
// source's total. Folding would keep the sum right and answer nothing: the
// reason to record this at all is to separate what a peer spent from what the
// operator spent, and a combined row cannot be split back apart afterwards.
// Each call is still recorded exactly once, so totals stay correct.
//
// Falls through to the unattributed path when nothing claimed responsibility,
// which is every existing caller.
func RecordExternalCostFor(ctx context.Context, sourceID, label string, costPerCall float64) {
	// And to the person it was done for, when one was.
	costledger.Charge(ctx, sourceID, costPerCall)
	who := costAttribution(ctx)
	if who == "" {
		RecordExternalCost(sourceID, label, costPerCall)
		return
	}
	RecordExternalCost(sourceID+" - "+who, label+" (via "+who+")", costPerCall)
}

// chargeModelUse charges one model call, priced at the deployment's rates as
// served by tier, to the person it ran for (costledger.Charge). A tier with
// no price, a local model, costs nothing and charges nothing.
func chargeModelUse(ctx context.Context, tier LLMTier, resp *Response) {
	name := "worker"
	if tier == LEAD {
		name = "lead"
	}
	costledger.Charge(ctx, "model:"+name, priceResponse(tier, resp))
}

// A person whose month's spending crossed 80% of their cap is told so on
// their bell, once a month.
func init() {
	costledger.OnWarn = func(person string, spent, cap float64) {
		if RootDB == nil {
			return
		}
		notices.Record(RootDB, notices.Notice{
			Owner: person, About: "spending:" + costledger.ThisMonth(), Kind: notices.KindReport,
			Title: "80% of your monthly spending limit is used",
			Body:  fmt.Sprintf("You have spent $%.2f of your $%.2f limit this month. Once it is reached, work that costs money stops until next month or until the limit is raised (Account, Spending).", spent, cap),
		})
	}
}

// withRequestSpender is r's context with who pays for its work: the signed-in
// person, else the owner of the key it carries; and that key when it is a
// personal API key, the kind a cap is set on.
func withRequestSpender(r *http.Request) context.Context {
	ctx := r.Context()
	person := AuthCurrentUser(r)
	if person == "" {
		// The owner of the key it carries, whatever kind of key.
		person = APIKeyUser(r)
	}
	key := ""
	if tok := AccountTokenFromRequest(r); tok != nil {
		key = tok.ID
	}
	if person == "" {
		return ctx
	}
	return costledger.WithSpender(ctx, costledger.Spender{Person: person, Key: key})
}
