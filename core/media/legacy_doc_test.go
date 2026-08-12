package media

import (
	"runtime"
	"strings"
	"testing"
)

// Homebrew dropped its antiword formula, and the code told every macOS operator
// to install it anyway — a command that no longer resolves, for a capability
// their machine already had: textutil ships with the OS and reads legacy .doc
// natively.

// TestEveryPlatformHasALegacyDocConverter — the list must contain something
// each supported platform can actually run.
func TestEveryPlatformHasALegacyDocConverter(t *testing.T) {
	var names []string
	for _, c := range legacyDocConverters {
		names = append(names, c.bin)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "textutil") {
		t.Error("no textutil in the converter list — macOS has no installable alternative " +
			"since the antiword formula was dropped")
	}
	if !strings.Contains(joined, "antiword") {
		t.Error("antiword was dropped from the list — it is still the best output on Linux " +
			"(-w 0 gives unwrapped paragraphs) and still installable there")
	}
	// antiword first: its unwrapped output embeds and searches better than
	// column-wrapped text, so where both exist it should win.
	if names[0] != "antiword" {
		t.Errorf("converter order starts with %q — antiword should be preferred where present", names[0])
	}
}

// TestNoConverterTakesTheFileFromStdin — every entry appends a PATH as its last
// argument, because that is how the caller invokes them. An entry expecting
// stdin would be handed a filename and read nothing.
func TestNoConverterTakesTheFileFromStdin(t *testing.T) {
	for _, c := range legacyDocConverters {
		for _, a := range c.args {
			if a == "-" || strings.HasSuffix(a, "stdin") {
				t.Errorf("converter %q takes stdin (%q) but is invoked with a file path", c.bin, a)
			}
		}
	}
	// textutil must write to stdout, or the caller captures nothing and the
	// converted text lands in a file nobody reads.
	for _, c := range legacyDocConverters {
		if c.bin != "textutil" {
			continue
		}
		if !strings.Contains(strings.Join(c.args, " "), "-stdout") {
			t.Errorf("textutil is not asked for stdout: %v — its output would go to a file", c.args)
		}
	}
}

// TestTheInstallHintIsPossibleOnThisPlatform — the defect in one line. A hint
// naming a package the reader cannot install is worse than no hint.
func TestTheInstallHintIsPossibleOnThisPlatform(t *testing.T) {
	hint := legacyDocInstallHint()
	if strings.TrimSpace(hint) == "" {
		t.Fatal("no install hint at all")
	}
	if runtime.GOOS == "darwin" && strings.Contains(hint, "antiword") {
		t.Errorf("macOS is told about antiword, which Homebrew no longer ships: %q", hint)
	}
	if runtime.GOOS == "darwin" && !strings.Contains(hint, "textutil") {
		t.Errorf("macOS is not pointed at the converter it already has: %q", hint)
	}
}
