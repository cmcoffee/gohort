// External-binary dependency reporting. gohort shells out to a handful of
// optional host tools (ffmpeg for media, pdftotext/pandoc for documents, bwrap
// for the shell sandbox). When one is missing the dependent feature degrades
// silently — an operator only finds out when, say, an inbound voice memo comes
// back untranscribed. CheckDependencies probes them on PATH so the admin
// "System Dependencies" panel can show what's present and what each one gates.
package core

import (
	"os/exec"
	"runtime"
)

// DependencyStatus reports whether one optional external binary is installed,
// where it resolved, and which feature depends on it.
type DependencyStatus struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Path    string `json:"path,omitempty"`
	Enables string `json:"enables"`
	Install string `json:"install,omitempty"` // OS-appropriate install hint, "" when none applies
}

// knownDependencies is the static catalog of external binaries the SERVER
// invokes (gohort-desktop's Mac-only tools are not included). Keep this in sync
// with the actual exec.Command call sites — a new shell-out should add a row so
// the operator can see the requirement.
var knownDependencies = []struct {
	name, enables string
	apt, brew     string // package names; brew "" => not offered on macOS
}{
	{"ffmpeg", "Video frame sampling and inbound audio/video transcription (normalizes m4a → WAV for STT)", "ffmpeg", "ffmpeg"},
	{"ffprobe", "Inbound-video metadata (duration / resolution); ships with ffmpeg", "ffmpeg", "ffmpeg"},
	{"pdftotext", "Text extraction from PDF attachments", "poppler-utils", "poppler"},
	{"pandoc", "Text extraction from docx / odt / rtf attachments", "pandoc", "pandoc"},
	{"bwrap", "Sandbox isolation for run_local and skill shell tools (without it, shell falls back to unsandboxed sh -c)", "bubblewrap", ""},
	{"python3", "Python interpreter for sandboxed scripts", "python3", "python3"},
}

// CheckDependencies probes each known external binary on PATH and returns its
// status. Cheap (a PATH lookup each) so it runs live per request — an install
// then refresh reflects immediately.
func CheckDependencies() []DependencyStatus {
	out := make([]DependencyStatus, 0, len(knownDependencies))
	for _, d := range knownDependencies {
		st := DependencyStatus{Name: d.name, Enables: d.enables, Install: dependencyInstallHint(d.apt, d.brew)}
		if p, err := exec.LookPath(d.name); err == nil {
			st.Present = true
			st.Path = p
		}
		out = append(out, st)
	}
	return out
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
