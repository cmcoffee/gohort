package sandbox

import (
	"sort"
	"strings"

	"github.com/cmcoffee/snugforge/nfo"
)

// Caller-supplied environment variables, and the one place that decides which
// of them a run may carry.
//
// The keys come from places nobody vets: the model's env map on a workspace
// run, the params a model gave its own tool, the query string of a custom
// app's data source. Inside a confining backend they are harmless, since
// whatever they change happens in there. The hazard is the LAUNCHER: bwrap and
// the podman client are ordinary dynamically linked programs that start on the
// host, before any namespace exists, and the loader reads LD_PRELOAD and its
// relatives from their environment. A variable that reached the launcher's
// environment ran code as the daemon, unconfined, with every user's data in
// reach.
//
// Two rules close that, and each would close it alone:
//
//  1. On a backend that carries the variables in its argv (bwrap --setenv,
//     podman --env), they are never put on the launcher's own environment.
//     See sandboxBackend.inheritsEnv.
//  2. Names that change which code a process loads are dropped from every run
//     on every backend, because on the backends that DO hand the launcher's
//     environment down (none, Seatbelt) there is no other way in.

// unsafeEnvName reports whether a variable can change which code a process
// loads or how the C library initializes, which no tool parameter has a
// legitimate reason to do.
func unsafeEnvName(name string) bool {
	if strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_") {
		return true
	}
	switch name {
	case "GLIBC_TUNABLES", "GCONV_PATH", "LOCPATH", "NLSPATH", "MALLOC_TRACE":
		return true
	}
	return false
}

// safeExtraEnv returns a copy of extra without the names unsafeEnvName
// refuses. A copy, since the map belongs to the caller. What it drops is
// logged by name only: the value may be a tool argument nobody should read in
// a log.
func safeExtraEnv(extra map[string]string) map[string]string {
	out := make(map[string]string, len(extra))
	var dropped []string
	for k, v := range extra {
		if unsafeEnvName(k) {
			dropped = append(dropped, k)
			continue
		}
		out[k] = v
	}
	if len(dropped) > 0 {
		sort.Strings(dropped)
		nfo.Log("[sandbox] dropped %s from a run's environment: these change which code a process loads", strings.Join(dropped, ", "))
	}
	return out
}

// withSetenv splices --setenv KEY VALUE triples into a bwrap argv before its
// "--" separator.
func withSetenv(args []string, env map[string]string) []string {
	if len(env) == 0 {
		return args
	}
	flags := make([]string, 0, len(env)*3)
	for k, v := range env {
		flags = append(flags, "--setenv", k, v)
	}
	return beforeSeparator(args, flags...)
}

// beforeSeparator inserts flags into a bwrap argv before its "--" separator.
// Past the separator bwrap treats every token as the command, so a flag there
// would be exec'd as a binary.
func beforeSeparator(args []string, flags ...string) []string {
	sepIdx := -1
	for i, a := range args {
		if a == "--" {
			sepIdx = i
			break
		}
	}
	if sepIdx < 0 {
		// Defensive: the argv lost its separator somehow. Return it
		// unmodified rather than silently corrupting the invocation.
		return args
	}
	// Copy the tail first: head and tail share a backing array, so an append
	// to head would overwrite "--", "sh", "-c" and the command in place
	// (bwrap then tried to execvp the whole shell command string).
	tail := append([]string{}, args[sepIdx:]...)
	out := make([]string, 0, len(args)+len(flags))
	out = append(out, args[:sepIdx]...)
	out = append(out, flags...)
	return append(out, tail...)
}
