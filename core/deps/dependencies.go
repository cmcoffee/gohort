// External-binary dependency reporting. gohort shells out to a handful of
// optional host tools (yt-dlp for video, ffmpeg for media, pdftotext/pandoc for
// documents, git for repo appliances, bwrap for the shell sandbox). When one is
// missing OR stale the dependent feature degrades silently — an operator only
// finds out when, say, an inbound voice memo comes back untranscribed, or (the
// case that motivated the version/staleness fields) Instagram downloads quietly
// break because yt-dlp went months out of date. CheckDependencies probes them on
// PATH, captures each version, and flags date-stamped tools that have gone stale,
// so the admin "System Dependencies" panel and the boot log both surface it.
package deps

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/cmcoffee/snugforge/nfo"
)

// DependencyStatus reports whether one optional external binary is installed,
// where it resolved, its version, whether it looks stale, and which feature
// depends on it.
type DependencyStatus struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"` // first line of the tool's version output
	Stale   bool   `json:"stale,omitempty"`   // date-stamped version older than staleAfter
	Note    string `json:"note,omitempty"`    // staleness detail when Stale
	Enables string `json:"enables"`
	Install string `json:"install,omitempty"` // OS-appropriate install hint, "" when none applies
}

// knownDependencies is the static catalog of external binaries the SERVER
// invokes (gohort-desktop's Mac-only tools are not included). Keep this in sync
// with the actual exec.Command call sites — a new shell-out should add a row so
// the operator can see the requirement.
var knownDependencies = []struct {
	name, enables string
	apt, brew     string        // package names; brew "" => not offered on macOS
	hint          string        // overrides the apt/brew install hint when set
	versionArgs   []string      // args that print the version; nil => don't probe
	staleAfter    time.Duration // >0 => flag stale when the date-stamped version is older than this
}{
	{
		name:        "yt-dlp",
		enables:     "Downloading and analyzing videos (Instagram, TikTok, YouTube, X, Reddit). Site extractors rot fast, so keep it current.",
		hint:        "download the self-updating binary from https://github.com/yt-dlp/yt-dlp/releases to /usr/local/bin/yt-dlp (chmod +x); update with `sudo yt-dlp -U`. Avoid pip, it ties to a Python that can move.",
		versionArgs: []string{"--version"},
		staleAfter:  42 * 24 * time.Hour, // ~6 weeks; yt-dlp releases roughly weekly
	},
	{name: "ffmpeg", enables: "Video frame sampling and inbound audio/video transcription (normalizes m4a → WAV for STT)", apt: "ffmpeg", brew: "ffmpeg", versionArgs: []string{"-version"}},
	{name: "ffprobe", enables: "Inbound-video metadata (duration / resolution); ships with ffmpeg", apt: "ffmpeg", brew: "ffmpeg", versionArgs: []string{"-version"}},
	{name: "pdftotext", enables: "Text extraction from PDF attachments", apt: "poppler-utils", brew: "poppler", versionArgs: []string{"-v"}},
	{name: "pandoc", enables: "Text extraction from docx / odt / rtf attachments", apt: "pandoc", brew: "pandoc", versionArgs: []string{"--version"}},
	// macOS has no brew formula for antiword any more, and never needed one:
	// textutil ships with the OS and reads legacy .doc natively. So the
	// requirement is named per platform rather than listing a package the
	// reader cannot install — a doctor that prints an impossible command is
	// worse than one that says nothing.
	legacyDocDependency(),
	{name: "git", enables: "Cloning repository appliances (servitor repo mode)", apt: "git", brew: "git", versionArgs: []string{"--version"}},
	// Named per platform for the same reason the legacy-.doc row is: bubblewrap
	// is Linux-only and can never be installed on macOS, so a bwrap row there
	// reported a permanent failure against a package the operator could not
	// obtain — for a capability the machine provides under a different name.
	sandboxDependency(),
	{name: "python3", enables: "Python interpreter for sandboxed scripts", apt: "python3", brew: "python3", versionArgs: []string{"--version"}},
}

// CheckDependencies probes each known external binary on PATH and returns its
// status (presence, version, staleness). Cheap enough to run live per request —
// a PATH lookup plus a short version invocation each — so an install or update
// then refresh reflects immediately.
func CheckDependencies() []DependencyStatus {
	out := make([]DependencyStatus, 0, len(knownDependencies))
	for _, d := range knownDependencies {
		st := DependencyStatus{Name: d.name, Enables: d.enables, Install: d.hint}
		if st.Install == "" {
			st.Install = dependencyInstallHint(d.apt, d.brew)
		}
		if p, err := exec.LookPath(d.name); err == nil {
			st.Present = true
			st.Path = p
			if len(d.versionArgs) > 0 {
				st.Version = probeVersion(d.name, d.versionArgs)
				if d.staleAfter > 0 {
					if stale, note := dateVersionStale(st.Version, d.staleAfter); stale {
						st.Stale = true
						st.Note = note
					}
				}
			}
		}
		out = append(out, st)
	}
	return out
}

// LogDependencyHealth probes the external binaries at boot and logs a one-line
// summary, warning on anything missing or stale, so operators see it in the log
// instead of only when a feature silently fails at point of use.
func LogDependencyHealth() {
	deps := CheckDependencies()
	present := 0
	var missing, stale []string
	for _, d := range deps {
		if !d.Present {
			missing = append(missing, d.Name)
			continue
		}
		present++
		if d.Stale {
			stale = append(stale, fmt.Sprintf("%s (%s, %s)", d.Name, d.Version, d.Note))
		}
	}
	nfo.Log("external dependencies: %d/%d present", present, len(deps))
	if len(missing) > 0 {
		nfo.Warn("missing optional dependencies (dependent features degrade): %s", strings.Join(missing, ", "))
	}
	if len(stale) > 0 {
		nfo.Warn("stale dependencies, update recommended: %s", strings.Join(stale, "; "))
	}
}

// probeVersion runs the tool's version command with a short timeout and returns
// the first line of output (trimmed). Empty when the tool errors or hangs.
func probeVersion(name string, args []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, name, args...).CombinedOutput()
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	return line
}

// dateVersionStale flags a date-stamped version (yt-dlp uses YYYY.MM.DD[.N]) as
// stale when its leading date is older than staleAfter. Returns false for any
// version that isn't a parseable leading date, so non-date-versioned tools are
// never marked stale.
func dateVersionStale(version string, staleAfter time.Duration) (bool, string) {
	if len(version) < 10 {
		return false, ""
	}
	t, err := time.Parse("2006.01.02", version[:10])
	if err != nil {
		return false, ""
	}
	if age := time.Since(t); age > staleAfter {
		return true, fmt.Sprintf("%d days old", int(age.Hours()/24))
	}
	return false, ""
}

// dependencyInstallHint returns an OS-appropriate one-line install command, or
// "" when the package isn't offered for this platform (e.g. bwrap on macOS).
func dependencyInstallHint(apt, brew string) string {
	switch runtime.GOOS {
	case "darwin":
		if brew == "" {
			return ""
		}
		return "brew install " + brew
	default:
		if apt == "" {
			return ""
		}
		return "apt install " + apt
	}
}

// legacyDocDependency describes whatever reads a legacy binary .doc on THIS
// platform.
//
// On macOS that is textutil, which is part of the OS — so the row exists to
// confirm it is there, not to sell a package. Homebrew dropped its antiword
// formula, and the old row told every Mac operator to run an install command
// that no longer resolves, for a capability their machine already had.
func legacyDocDependency() struct {
	name, enables string
	apt, brew     string
	hint          string
	versionArgs   []string
	staleAfter    time.Duration
} {
	const enables = "Text extraction from legacy binary .doc attachments (pandoc only reads .docx)"
	if runtime.GOOS == "darwin" {
		return struct {
			name, enables string
			apt, brew     string
			hint          string
			versionArgs   []string
			staleAfter    time.Duration
		}{
			name: "textutil", enables: enables,
			hint: "ships with macOS at /usr/bin/textutil — nothing to install",
		}
	}
	return struct {
		name, enables string
		apt, brew     string
		hint          string
		versionArgs   []string
		staleAfter    time.Duration
	}{
		name: "antiword", enables: enables, apt: "antiword",
		hint:        "apt install antiword (or catdoc — either is used, whichever is present)",
		versionArgs: []string{"-h"},
	}
}

// sandboxDependency describes whatever confines shell tools on THIS platform.
//
// Linux confines with bubblewrap, macOS with Seatbelt (sandbox-exec), and the
// two are not substitutes an operator can install for each other. A single
// bwrap row showed every Mac a permanent red mark against a package that does
// not exist there, while the tool that actually does the job — shipped with the
// OS, sitting at /usr/bin/sandbox-exec — went unmentioned.
//
// The row reports PRESENCE only. Whether the profile this build generates is
// accepted is a separate question, answered at startup by the probe in
// core/sandbox/seatbelt.go and reported in Admin → System Status; a dependency
// check has no business running a sandbox to find out.
func sandboxDependency() struct {
	name, enables string
	apt, brew     string
	hint          string
	versionArgs   []string
	staleAfter    time.Duration
} {
	const enables = "Sandbox isolation for run_local and skill shell tools (without it, shell falls back to unsandboxed sh -c)"
	if runtime.GOOS == "darwin" {
		return struct {
			name, enables string
			apt, brew     string
			hint          string
			versionArgs   []string
			staleAfter    time.Duration
		}{
			name: "sandbox-exec", enables: enables,
			hint: "ships with macOS at /usr/bin/sandbox-exec — nothing to install. See Admin → System Status for whether it is actually confining.",
		}
	}
	return struct {
		name, enables string
		apt, brew     string
		hint          string
		versionArgs   []string
		staleAfter    time.Duration
	}{
		name: "bwrap", enables: enables, apt: "bubblewrap",
		versionArgs: []string{"--version"},
	}
}

// StaleNote returns a short parenthetical naming an out-of-date dependency as
// the likely cause of a failure, or "" when the tool is current, absent, or not
// date-versioned.
//
// It exists because the server already KNEW. yt-dlp went 56 days out of date,
// LogDependencyHealth warned about it every few minutes, YouTube started
// answering with HTTP 403, and the agent told the user it was "usually a
// temporary glitch on their end or a regional block". That guess was not the
// model being careless: nothing put the staleness anywhere the failure could
// see it, so an invented explanation was the only kind available.
//
// Site extractors rot on a schedule, so for yt-dlp specifically an old build is
// not one possible cause among many, it is the first thing to check. Saying so
// in the error costs one exec on a path that only runs when something already
// failed.
//
// Deliberately a SUFFIX rather than a replacement: the tool's own message is
// what says which URL and which site, and losing that to a tidier sentence
// would trade a specific failure for a general one.
func StaleNote(name string) string {
	for _, d := range knownDependencies {
		if d.name != name || d.staleAfter <= 0 || len(d.versionArgs) == 0 {
			continue
		}
		if _, err := exec.LookPath(d.name); err != nil {
			return "" // absent is a different error, and its own message says so
		}
		v := probeVersion(d.name, d.versionArgs)
		stale, note := dateVersionStale(v, d.staleAfter)
		if !stale {
			return ""
		}
		hint := d.hint
		if hint == "" {
			hint = dependencyInstallHint(d.apt, d.brew)
		}
		return staleNoteText(d.name, v, note, hint)
	}
	return ""
}

// staleNoteText is the wording, split from the probe so it can be checked
// without depending on what happens to be installed. The first version of this
// test could only SKIP on a healthy host, which meant it asserted nothing on
// exactly the machine where someone had just fixed the problem.
func staleNoteText(name, version, age, hint string) string {
	return fmt.Sprintf(" (the installed %s is %s, %s, which is the usual cause of a site suddenly failing; %s)",
		name, version, age, hint)
}
