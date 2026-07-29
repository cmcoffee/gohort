package orchestrate

import "os"

// readRunnerSource reads runner.go so the turn-closed wiring can be asserted
// structurally. These are plumbing invariants — which loop declares which
// control tools, and who reads the silence flag — that have no runtime seam to
// exercise without standing up a whole turn, and that broke silently once
// already by simply never being wired.
func readRunnerSource() (string, error) {
	b, err := os.ReadFile("runner.go")
	return string(b), err
}

// sectionAfter returns up to n bytes following the first occurrence of marker.
func sectionAfter(src, marker string, n int) string {
	i := indexOf(src, marker)
	if i < 0 {
		return ""
	}
	end := i + n
	if end > len(src) {
		end = len(src)
	}
	return src[i:end]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
