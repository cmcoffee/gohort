package sources

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// What a pipeline run read, numbered, so its stages can cite it and a later
// stage can check what they cited.
//
// Two apps (a debate and a deep-research run) each built this by hand: number
// every page fetched, hand the numbered list to the writer, keep only what it
// cited, and check the citations against the text that was fetched. A pipeline
// gets it as data instead: a stage that reads pages adds them here as it goes,
// {sources} hands the list to a prompt, cite tidies a stage's citations, and a
// verify stage checks them.

// Fetched is one page a tool read: what to call it, where it is, and the text
// it was read as (kept so a claim citing it can be checked against it).
type Fetched struct {
	Title string
	URL   string
	Text  string
}

// Extractor turns one tool call into the pages it read. args are the call's
// arguments, result what the tool returned.
type Extractor func(args map[string]any, result string) []Fetched

var (
	extractorMu sync.RWMutex
	extractors  = map[string]Extractor{}
)

// RegisterExtractor declares how a tool's results become sources. A tool that
// reads pages (a search, a fetch) registers one where it is defined, so the
// tool owns the knowledge of its own output format; a run never parses tool
// output by name.
func RegisterExtractor(tool string, fn Extractor) {
	extractorMu.Lock()
	defer extractorMu.Unlock()
	extractors[tool] = fn
}

// Extract is the pages one tool call read, or nil for a tool that reads none.
func Extract(tool string, args map[string]any, result string) []Fetched {
	extractorMu.RLock()
	fn := extractors[tool]
	extractorMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(args, result)
}

// maxSourceText caps the text kept per source: enough to check a claim
// against, not a second copy of every page a long run read.
const maxSourceText = 20000

// RunSources is one run's sources, numbered in the order they were first
// read. Safe for parallel stages (fanout branches add at once).
type RunSources struct {
	reg  SourceRegistry
	mu   sync.Mutex
	text map[string]string // normalized URL -> the text it was read as
}

// NewRunSources is an empty source list.
func NewRunSources() *RunSources { return &RunSources{text: map[string]string{}} }

// Add records a page and returns its number. A page read twice keeps its
// first number, and keeps the longer of the two texts (a search snippet
// first, the full fetch later).
func (rs *RunSources) Add(f Fetched) int {
	if rs == nil || strings.TrimSpace(f.URL) == "" {
		return 0
	}
	title := strings.TrimSpace(f.Title)
	if title == "" {
		title = f.URL
	}
	n := rs.reg.Register(title, f.URL)
	if t := strings.TrimSpace(f.Text); t != "" {
		if len(t) > maxSourceText {
			t = t[:maxSourceText]
		}
		key := NormalizeURL(f.URL)
		rs.mu.Lock()
		if len(t) > len(rs.text[key]) {
			rs.text[key] = t
		}
		rs.mu.Unlock()
	}
	return n
}

// Len is how many sources the run has read.
func (rs *RunSources) Len() int {
	if rs == nil {
		return 0
	}
	return rs.reg.Len()
}

// List is the numbered list a prompt cites from, "[N] Title - URL" per line,
// or a line saying there are none yet.
func (rs *RunSources) List() string {
	if rs.Len() == 0 {
		return "(no sources have been read in this run yet)"
	}
	var b strings.Builder
	for i, r := range rs.reg.Refs() {
		fmt.Fprintf(&b, "[%d] %s - %s\n", i+1, r.Title, r.URL)
	}
	return strings.TrimSpace(b.String())
}

// Source is source n (1-based) and the text it was read as.
func (rs *RunSources) Source(n int) (SourceRef, string, bool) {
	refs := rs.reg.Refs()
	if n < 1 || n > len(refs) {
		return SourceRef{}, "", false
	}
	r := refs[n-1]
	rs.mu.Lock()
	t := rs.text[NormalizeURL(r.URL)]
	rs.mu.Unlock()
	return r, t, true
}

// Texts is every source's text, for a check that looks across all of them.
func (rs *RunSources) Texts() []string {
	if rs == nil {
		return nil
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]string, 0, len(rs.text))
	for _, t := range rs.text {
		out = append(out, t)
	}
	return out
}

// citeGroupRE is one citation: [3] or [1, 4, 7].
var citeGroupRE = regexp.MustCompile(`\[(\d+(?:\s*,\s*\d+)*)\]`)

// Citations is every source number text cites, in order of first mention.
func Citations(text string) []int {
	var out []int
	seen := map[int]bool{}
	for _, m := range citeGroupRE.FindAllStringSubmatch(text, -1) {
		for _, part := range strings.Split(m[1], ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err == nil && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// Cite tidies a stage's citations: it appends a Sources section listing
// exactly the sources text cites (as links), and reports the citations that
// name no source in this run. The text itself is left as written: a citation
// that resolves to nothing is reported, not quietly removed, because removing
// it would hide that the writer cited something it never read.
func (rs *RunSources) Cite(text string) (string, []int) {
	var cited, unknown []int
	for _, n := range Citations(text) {
		if n >= 1 && n <= rs.Len() {
			cited = append(cited, n)
		} else {
			unknown = append(unknown, n)
		}
	}
	if len(cited) == 0 {
		return text, unknown
	}
	sort.Ints(cited)
	refs := rs.reg.Refs()
	var b strings.Builder
	b.WriteString(strings.TrimRight(text, "\n"))
	b.WriteString("\n\n## Sources\n")
	for _, n := range cited {
		r := refs[n-1]
		fmt.Fprintf(&b, "\n[%d] [%s](%s)", n, r.Title, r.URL)
	}
	return b.String() + "\n", unknown
}
