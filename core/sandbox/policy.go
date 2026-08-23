// Who may run unconfined, when this host cannot confine.
//
// Confinement is mandatory by default (see sandboxRequired). The bypass is not
// a boolean, because "allow unsandboxed execution" is two different decisions
// wearing one name: whether the DEPLOYMENT tolerates unconfined execution at
// all, and whether the person who triggered it is trusted to have it. A single
// on/off collapses those, and the collapsed answer is always the permissive
// one — turning it on for the operator who needs to run a shell tool on their
// laptop turns it on for every agent, every scheduled fire, and every user on
// the box.
//
// So it is a tri-state:
//
//	off    (default) nothing runs unconfined, ever
//	admin            an admin's own runs may go unconfined; everyone else refused
//	on                everything may run unconfined
//
// "admin" is the setting that makes the flip livable on a host that cannot be
// confined. The operator keeps their own shell tools working while agents,
// schedules, channel wakes and non-admin users stay fail-closed — which is the
// split that matters, because the whole reason to confine LLM-issued commands
// is that the LLM is the untrusted party, not the admin at the keyboard.
package sandbox

import (
	"context"
	"os"
	"strings"
)

// BypassPolicy is the tri-state above.
type BypassPolicy int

const (
	// BypassOff refuses every unconfinable run. The default.
	BypassOff BypassPolicy = iota
	// BypassAdmin permits unconfined runs only for a caller known to be an
	// admin. Anything with no known caller — a schedule, a channel wake, an
	// export generator, a monitor evaluator — is NOT an admin and stays
	// refused. Absence of evidence is not admin-ness.
	BypassAdmin
	// BypassOn permits unconfined runs for everyone.
	BypassOn
)

func (p BypassPolicy) String() string {
	switch p {
	case BypassAdmin:
		return "admin"
	case BypassOn:
		return "on"
	default:
		return "off"
	}
}

// bypassPolicy reads the deployment's setting.
//
// Two spellings, because the variable that expressed the old opt-IN should not
// become the opt-OUT by inversion — a deployment carrying
// GOHORT_SANDBOX_REQUIRED=1 still means exactly what it said:
//
//	GOHORT_ALLOW_UNSANDBOXED=off|admin|on   the tri-state (default off)
//	GOHORT_SANDBOX_REQUIRED=1               forces off, whatever the above says
//	GOHORT_SANDBOX_REQUIRED=0               equivalent to =on, for anyone who scripted it
//
// Set contradictorily, the CONFINING reading wins. A configuration that says
// two things is a mistake, and the safe reading of a mistake is the strict one.
//
// An unrecognized value is also off. A typo in a security switch must not be
// the permissive answer — "GOHORT_ALLOW_UNSANDBOXED=yes-please" should refuse,
// not open the host.
func bypassPolicy() BypassPolicy {
	if envTruthy("GOHORT_SANDBOX_REQUIRED") {
		return BypassOff
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOHORT_ALLOW_UNSANDBOXED"))) {
	case "on", "1", "true", "yes", "all":
		return BypassOn
	case "admin", "admins", "admin_only", "admin-only", "adminonly":
		return BypassAdmin
	case "off", "0", "false", "no", "none":
		return BypassOff
	}
	if envFalsy("GOHORT_SANDBOX_REQUIRED") {
		return BypassOn
	}
	return BypassOff
}

// envTruthy / envFalsy read one env var as a tri-state: set-and-affirmative,
// set-and-negative, or absent. Absent is neither, which is what lets the two
// variables above compose without a third "unset" sentinel value.
func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func envFalsy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

type callerCtxKey struct{}

// WithAdminCaller marks a context as belonging to an admin's own run, which is
// what BypassAdmin keys on.
//
// Stamped rather than looked up, because by the time a command reaches the
// sandbox there is nothing left to look it up FROM: no request, no cookie, no
// session — just a context and a string of shell. The stamp has to be applied
// where the caller is still known (see ToolSession.ContextWithSandboxCaller),
// and everything that forgets to apply it is treated as non-admin, which is
// the direction a mistake should fall.
func WithAdminCaller(ctx context.Context, admin bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, callerCtxKey{}, admin)
}

// CallerIsAdmin reports whether this run was stamped as an admin's. Unstamped
// is false — see WithAdminCaller.
//
// Exported so the stamp can be READ back by whoever applied it. A caller that
// can set a security-relevant flag and cannot check it has to assert on its
// own inputs instead of on the value the sandbox will act upon, which is how a
// stamp and its reader drift apart without a test noticing.
func CallerIsAdmin(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	admin, _ := ctx.Value(callerCtxKey{}).(bool)
	return admin
}

// sandboxRequired reports whether an unconfinable run must be REFUSED rather
// than dropped to a subshell at the service account's full privilege.
//
// It defaults to TRUE, and that inversion is the point. The old default was
// fail-open: a host with no backend ran every LLM-issued shell command
// unconfined, and the only thing standing between that and a hardened
// deployment was an env var nobody knew to set. Getting it wrong was silent
// and the failure was total — the whole reason the sandbox exists, absent,
// with a one-line log entry days in the scrollback as the only notice.
//
// The argument for fail-open was that a host with no sandbox would otherwise
// have no working shell tools at all, with nothing installable to fix it on
// macOS. That is true and it is not a reason to default open, because the
// remedy was never "install something" — it is "say you accept the risk",
// which is one flag. An operator who wants unconfined execution can have it by
// asking for it; an operator who never thought about it gets the safe answer.
// Dangerous by default and safe on request is the wrong way round.
func sandboxRequired(ctx context.Context) bool {
	switch bypassPolicy() {
	case BypassOn:
		return false
	case BypassAdmin:
		return !CallerIsAdmin(ctx)
	}
	return true
}
