package imagefetch

import (
	"fmt"
	_ "image/gif"
	_ "image/png"
	"sort"
	"strconv"
	"strings"

	_ "golang.org/x/image/webp" // register the WebP decoder for image.Decode
)

// defaultEditBackend picks the editing backend when the caller names none: the
// configured default if it can edit, else the first editor. With one editor
// wired — the common case — the `backend` param isn't even advertised.
// defaultEditBackend picks the editing backend for a call carrying n source
// pictures.
//
// ROUTED BY COUNT, because a compose graph IS its input count: two LoadImage
// nodes is a different workflow from three, and since a graph must be filled
// completely (see the partial-fill guard), a two-image backend simply cannot
// serve a three-image request. Making the model choose meant it had to know
// which connector had how many inputs — a fact about someone's ComfyUI wiring
// that no prompt should be teaching it.
//
// Exact match first, then the configured default, then the first editor. The
// last two are how a single-editor deployment keeps working without anyone
// declaring counts, and how a mismatch still lands somewhere that can produce
// a specific error rather than a silent nil.
func defaultEditBackend(a imageActions, n int) string {
	if n > 0 {
		// Default wins among equals, so two connectors with the same count
		// resolve the same way every time rather than by slice order.
		var match string
		for _, e := range a.editors {
			if e.MaxImages != n {
				continue
			}
			if e.Default {
				return e.Name
			}
			if match == "" {
				match = e.Name
			}
		}
		if match != "" {
			return match
		}
	}
	for _, e := range a.editors {
		if e.Default {
			return e.Name
		}
	}
	if len(a.editors) > 0 {
		return a.editors[0].Name
	}
	return ""
}

// pluralPictures renders a count as something a sentence can contain. "1
// picture(s)" reads as a template nobody finished.
func pluralPictures(n int) string {
	if n == 1 {
		return "1 picture"
	}
	return strconv.Itoa(n) + " pictures"
}

// backendImageCount is how many source pictures one named editor takes, or 0
// when it is unknown or unconstrained.
func backendImageCount(a imageActions, name string) int {
	for _, e := range a.editors {
		if strings.EqualFold(e.Name, name) {
			return e.MaxImages
		}
	}
	return 0
}

// joinCounts renders a count list as a sentence. "1 or 2 or 3" reads as a
// machine talking.
func joinCounts(n []int) string {
	strs := make([]string, 0, len(n))
	for _, c := range n {
		strs = append(strs, strconv.Itoa(c))
	}
	switch len(strs) {
	case 0:
		return ""
	case 1:
		return strs[0]
	case 2:
		return strs[0] + " or " + strs[1]
	}
	return strings.Join(strs[:len(strs)-1], ", ") + " or " + strs[len(strs)-1]
}

// editorImageCounts lists the source-picture counts this deployment can
// actually serve, ascending and deduplicated. Used to say "this can combine 2;
// you passed 3" rather than "wrong number of images".
func editorImageCounts(a imageActions) []int {
	seen := map[int]bool{}
	var out []int
	for _, e := range a.editors {
		n := e.MaxImages
		if n <= 0 {
			n = 1
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// defaultGenerateBackend picks the backend a generate lands on when the caller
// names none: the configured default if it can generate, else the first
// generator. Mirrors defaultEditBackend.
func defaultGenerateBackend(a imageActions) string {
	for _, b := range a.backends {
		if b.Default {
			return b.Name
		}
	}
	if len(a.backends) > 0 {
		return a.backends[0].Name
	}
	return ""
}

func generatorNames(a imageActions) []string {
	out := make([]string, 0, len(a.backends))
	for _, b := range a.backends {
		out = append(out, b.Name)
	}
	return out
}

func isGenerator(a imageActions, name string) bool {
	for _, b := range a.backends {
		if b.Name == name {
			return true
		}
	}
	return false
}

func editorNames(a imageActions) []string {
	out := make([]string, 0, len(a.editors))
	for _, e := range a.editors {
		out = append(out, e.Name)
	}
	return out
}

// backendNeedsPrompt reports whether the named editing backend has a text node
// to write a prompt into.
func backendNeedsPrompt(a imageActions, name string) bool {
	for _, e := range a.editors {
		if e.Name == name {
			return e.NeedsPrompt
		}
	}
	return true
}

func isEditor(a imageActions, name string) bool {
	for _, e := range a.editors {
		if e.Name == name {
			return true
		}
	}
	return false
}

// stringsArg reads a string-array tool argument, tolerating a single string —
// models routinely send images="photo.png" instead of ["photo.png"], and
// refusing that costs a round to teach nothing.
func stringsArg(args map[string]any, key string) []string {
	var out []string
	switch v := args[key].(type) {
	case []string:
		out = v
	case []any:
		for _, item := range v {
			if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
				out = append(out, s)
			}
		}
	case string:
		for _, part := range strings.Split(v, ",") {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}
