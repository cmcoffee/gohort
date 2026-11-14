package sandbox

import "strings"

// PythonShim is the sandbox bin dir's python3 (and python): it runs the
// interpreter gohort chose for scripts (deps.Python), so every script, shim
// and `python3 ...` command inside a sandbox gets it without naming it. The
// bin dir is first on the sandbox's PATH.
//
// A sandbox that brings its own userland (a container image) may not have
// that path; the shim then runs the image's own python3, the first of the
// usual places it lives, rather than failing. It never searches PATH for
// python3, which would find itself.
func PythonShim(interpreter string) string {
	q := "'" + strings.ReplaceAll(interpreter, "'", `'\''`) + "'"
	return "#!/bin/sh\n" +
		"# The interpreter gohort chose for scripts (docs/design/sandbox-python.md).\n" +
		"if [ -x " + q + " ]; then exec " + q + " \"$@\"; fi\n" +
		"for p in /usr/local/bin/python3 /usr/bin/python3 /bin/python3; do\n" +
		"  if [ -x \"$p\" ]; then exec \"$p\" \"$@\"; fi\n" +
		"done\n" +
		"echo \"python3: not found\" >&2\n" +
		"exit 127\n"
}
