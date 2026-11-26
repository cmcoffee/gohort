// Package appreach carries an app's credential rules on a run's context.
//
// An app's script sets them on its own session (tools/appscript), but the
// agent or pipeline a script runs (gohort.run_agent, run_pipeline) builds
// sessions of its own, many of them, delegates included, and none of those
// knew the run was an app's for someone else: it reached the owner's keys and
// sign-ins as the owner, past the choices on the app's What it reaches. The
// rules ride on the context instead, the way the non-owner mark does, so
// every session the run makes reads them where it decides on a credential
// (docs/design/app-credentials.md).
//
// A leaf, so core and the apps host both import it without either importing
// the other.
package appreach

import (
	"context"
	"fmt"
	"strings"
)

// Rules are how an app's run reaches its owner's credentials.
type Rules struct {
	// Person is whom the run is for when that is not the owner, else "".
	Person string
	// Lend is the app's choice for a credential (or MCP sign-in, MCPKey):
	// "read", "write", "own" or "withhold". nil and "" mean "read".
	Lend func(name string) string
	// Off reports whether the app keeps name out of every run, the owner's
	// included. nil keeps nothing out.
	Off func(name string) bool
	// Used hears each of the owner's credentials (or sign-ins) a call goes
	// out through. nil hears nothing.
	Used func(name string)
	// ConnectNeeded hears that a call waited on the person's own account for
	// the owner's credential name. nil hears nothing.
	ConnectNeeded func(owner, name string)
}

type rulesKey struct{}

// With puts r on ctx. The first rules named win: a run that starts more work
// keeps the app's rules at the top.
func With(ctx context.Context, r Rules) context.Context {
	if _, ok := From(ctx); ok {
		return ctx
	}
	return context.WithValue(ctx, rulesKey{}, r)
}

// From is the rules With put on ctx.
func From(ctx context.Context) (Rules, bool) {
	if ctx == nil {
		return Rules{}, false
	}
	r, ok := ctx.Value(rulesKey{}).(Rules)
	return r, ok
}

// MCPKey is how the rules name the owner's sign-in to MCP server:
// "mcp:<server>", a name no credential has, so one key never means two kinds
// of thing.
func MCPKey(server string) string { return "mcp:" + server }

// Choice is the app's choice for name: "read" when none was made.
func (r Rules) Choice(name string) string {
	if r.Lend != nil {
		if c := strings.TrimSpace(r.Lend(name)); c != "" {
			return c
		}
	}
	return "read"
}

// IsOff reports whether the app keeps name out of every run.
func (r Rules) IsOff(name string) bool { return r.Off != nil && r.Off(name) }

// MCPRefusal says why a run may not reach the owner's sign-in to MCP server,
// or nil. The sign-in is kept out of every run when the app turns it off, and
// out of a run for someone else unless it is lent for reads, the only lend a
// sign-in is offered: anything else reads as Disallow. A run that reaches it
// is heard by Used.
func (r Rules) MCPRefusal(server string) error {
	key := MCPKey(server)
	if r.IsOff(key) {
		return fmt.Errorf("this app is kept from %s: its owner turned it off for the app", server)
	}
	if strings.TrimSpace(r.Person) != "" && r.Choice(key) != "read" {
		return fmt.Errorf("%s is not reached when someone else uses this app: its owner keeps their sign-in to it for their own runs", server)
	}
	if r.Used != nil {
		r.Used(key)
	}
	return nil
}
