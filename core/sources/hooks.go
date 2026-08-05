// Source classification, injected. The classifier lives in the hub's search
// code (it shares the domain tables the search pipeline is built around) and
// can't follow this leaf out, so it arrives as three func vars core wires.
// Each accessor no-ops safely when unwired: an unclassified source is the
// same answer the classifier gives for a URL it doesn't recognize.

package sources

var (
	// ClassifySourceFunc tags a URL ("blog", "press release", …).
	ClassifySourceFunc func(rawURL string) string
	// CleanSourceTitleFunc normalizes a citation title, falling back to a URL.
	CleanSourceTitleFunc func(title, fallback string) string
	// IsNonArticleURLFunc reports a URL that isn't an article at all.
	IsNonArticleURLFunc func(rawURL string) bool
)

func classifySource(rawURL string) string {
	if ClassifySourceFunc == nil {
		return ""
	}
	return ClassifySourceFunc(rawURL)
}

func cleanSourceTitle(title, fallback string) string {
	if CleanSourceTitleFunc == nil {
		return title
	}
	return CleanSourceTitleFunc(title, fallback)
}

func isNonArticleURL(rawURL string) bool {
	if IsNonArticleURLFunc == nil {
		return false
	}
	return IsNonArticleURLFunc(rawURL)
}
