package ui

// Text sizes come from the type scale (--fs-2xs … --fs-xl). The stylesheet had
// drifted to 48 sizes, with 0.70 / 0.72 / 0.74 / 0.75 / 0.76 / 0.78rem side by
// side, so two labels meant to match came out a pixel apart. A literal in the
// scale's range is that drift starting again; sizes outside it (a tiny badge
// glyph, a heading, the iOS 16px floor) are deliberate.

import (
	"regexp"
	"strconv"
	"testing"
)

func TestFontSizesComeFromTheScale(t *testing.T) {
	lit := regexp.MustCompile(`font-size:\s*(\d*\.?\d+)rem`)
	for _, m := range lit.FindAllStringSubmatch(runtimeCSS, -1) {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		if v >= 0.68 && v <= 1.15 {
			t.Errorf("%s is inside the type scale's range; use var(--fs-…)", m[0])
		}
	}
}
