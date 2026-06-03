package phantom

import "testing"

// TestStripEmojisRegionalIndicators guards the fix for lone regional
// indicators surfacing as stray boxed letters (🇪→E, 🇷→R) at the end of a
// reply — the "we filtered out the ? and got an E/R" report. A regional
// indicator is only meaningful as a pair (a flag); an unpaired one must be
// dropped, not kept.
func TestStripEmojisRegionalIndicators(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"lone indicator dropped", "the tech guy 🇪", "the tech guy "},
		{"lone indicator with question mark preserved", "the tech guy? 🇪", "the tech guy? "},
		{"flag pair kept", "winner 🇪🇷", "winner 🇪🇷"},
		{"flag split across chunks — first half", "x 🇪", "x "},
		{"flag split across chunks — second half", "🇷 y", " y"},
		{"plain question untouched", "is it him?", "is it him?"},
		{"keeps only first emoji cluster", "a 😀 b 😎", "a 😀 b "},
		{"flag as first cluster, later emoji stripped", "go 🇪🇷 now 😀", "go 🇪🇷 now "},
		{"second flag stripped (not first cluster)", "a 😀 b 🇺🇸", "a 😀 b "},
		{"no emoji", "hello world", "hello world"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripEmojis(c.in); got != c.want {
				t.Errorf("stripEmojis(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
