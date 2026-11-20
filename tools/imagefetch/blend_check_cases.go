package imagefetch

// The source check's check tests (core/checktest): an edit's instruction,
// each source picture and the result as separate looks described them, and
// per source whether what the instruction takes from it is in the result,
// asked through the judge's own prompt (sourceJudgePrompt). The looks are
// written here, so a case tests the judge alone, which is the half of the
// check that decides.

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checktest"
)

func init() {
	checktest.Register(checktest.Check{
		Key:   "imagefetch/source_check",
		Label: "Image source check",
		Cases: sourceCheckCases,
		Run:   runSourceCheckCase,
	})
}

// Two looks used by more than one case.
const (
	// lookWarship is a long grey warship.
	lookWarship = "- A long grey battleship seen from the side, centre-right: a tall pagoda-like bridge tower amidships, three large triple-gun turrets (two forward, one aft), a single raked funnel, a red waterline stripe.\nSetting: open sea under a grey sky."
	// lookSaucerShip is a white saucer-and-nacelle starship.
	lookSaucerShip = "- A white spacecraft, left of centre: a large flat circular disc at the front joined by a short angled neck to a cylindrical lower hull, and two long tube-shaped engines on swept-back pylons above and behind it; blue glowing ends at the front of the engines, a red dome on the front of each.\nSetting: black space with stars."
)

// sourceCheckCases: Input instruction, s1 and s2 (each source as its own
// look described it), result (the result as its own look described it).
// Want: per source, "N yes" or "N no", in order.
func sourceCheckCases() []checktest.Case {
	c := func(id, title, want, instruction, s1, s2, result string) checktest.Case {
		return checktest.Case{ID: id, Title: title, Want: want, Input: map[string]string{
			"instruction": instruction, "s1": s1, "s2": s2, "result": result}}
	}
	return []checktest.Case{
		c("blend-both", "Both ships came through", "1 yes, 2 yes",
			"Show the two ships from the pictures flying side by side above a planet.",
			lookSaucerShip, lookWarship,
			"- On the left, a white spacecraft with a large flat circular disc at the front, a short neck to a cylindrical hull, and two long tube engines on swept-back pylons with blue glowing front ends.\n"+
				"- On the right, a long grey battleship with a tall pagoda-like bridge tower, three triple-gun turrets and a single raked funnel, floating in space.\nSetting: a blue-green planet below, black space with stars."),
		c("blend-one-lost", "A different ship stands in for one source", "1 no, 2 yes",
			"Show the two ships from the pictures flying side by side above a planet.",
			lookSaucerShip, lookWarship,
			"- On the left, a sleek white wedge-shaped spaceship with a pointed nose, a flat underside and a single engine glow at the back; no disc and no separate engines.\n"+
				"- On the right, a long grey battleship with a tall pagoda-like bridge tower, three triple-gun turrets and a raked funnel.\nSetting: a planet below, stars."),
		c("face-swap", "A face swap: the face replaced is not missing", "1 yes, 2 yes",
			"Put the face of the man in picture 1 on the woman in picture 2, keeping everything else in picture 2.",
			"- A man, head and shoulders, centre: round face, short dark beard, thick eyebrows, brown eyes, tanned skin, short black hair, about forty; grey crew-neck shirt.\nSetting: a plain white wall.",
			"- A woman standing on sand, centre: long red summer dress, straw hat, slim build, holding sandals in her left hand; narrow face, freckles, pale skin.\nSetting: a beach at sunset, sea behind.",
			"- A person standing on sand, centre: long red summer dress, straw hat, holding sandals in the left hand; round face with a short dark beard, thick eyebrows, brown eyes, tanned skin.\nSetting: a beach at sunset, sea behind."),
		c("face-swap-failed", "A face swap that kept the original face", "1 no, 2 yes",
			"Put the face of the man in picture 1 on the woman in picture 2, keeping everything else in picture 2.",
			"- A man, head and shoulders, centre: round face, short dark beard, thick eyebrows, brown eyes, tanned skin, short black hair, about forty; grey crew-neck shirt.\nSetting: a plain white wall.",
			"- A woman standing on sand, centre: long red summer dress, straw hat, slim build, holding sandals in her left hand; narrow face, freckles, pale skin.\nSetting: a beach at sunset, sea behind.",
			"- A woman standing on sand, centre: long red summer dress, straw hat, holding sandals in her left hand; narrow face, freckles, pale skin, clean-shaven.\nSetting: a beach at sunset, sea behind."),
		c("background-lost", "The setting the instruction took did not come through", "1 yes, 2 no",
			"Put the dog from picture 1 in the snowy setting of picture 2.",
			"- A dog sitting, centre: a corgi, orange and white coat, large upright ears, short legs, white chest, red collar.\nSetting: a living room carpet.",
			"- No distinct subjects.\nSetting: a wooden cabin among snow-covered pine trees, deep snow on the ground, a mountain behind, overcast sky.",
			"- A dog sitting, centre: a corgi, orange and white coat, large upright ears, short legs, white chest, red collar.\nSetting: a sandy desert with dunes and a clear blue sky."),
		c("same-kind-person", "A different man in the same pose", "1 no, 2 yes",
			"Show the man from picture 1 sitting at the cafe table in picture 2.",
			"- An elderly man, centre: white full beard, round wire glasses, bald on top with white hair at the sides, deep wrinkles, about seventy; brown tweed jacket.\nSetting: a library.",
			"- No distinct subjects.\nSetting: a cafe terrace, a small round marble table with two cups, green awning, a street behind.",
			"- A young man sitting at a small round marble table, centre: clean-shaven, thick dark hair, no glasses, about twenty-five; brown tweed jacket.\nSetting: a cafe terrace with a green awning, a street behind."),
		c("pose-only", "Only a pose was taken from one source", "1 yes, 2 yes",
			"Draw the cat from picture 2 in the pose of the dancer in picture 1.",
			"- A ballet dancer, centre: standing on one leg, the other leg raised straight behind, arms stretched forward and back; a woman in a white leotard, hair in a bun.\nSetting: a stage with a dark curtain.",
			"- A cat lying down, centre: grey tabby with dark stripes, white paws, green eyes, a notch in the left ear.\nSetting: a windowsill.",
			"- A grey tabby cat with dark stripes, white paws, green eyes and a notch in its left ear, balanced on one hind leg with the other hind leg raised straight behind and front legs stretched forward and back.\nSetting: a stage with a dark curtain."),
	}
}

// runSourceCheckCase asks one case through the judge's own prompt and reads
// its lines as the check does.
func runSourceCheckCase(ctx context.Context, llm LLM, tc checktest.Case) checktest.Outcome {
	in := tc.Input
	prompt, judged := sourceJudgePrompt(in["instruction"], []string{in["s1"], in["s2"]}, in["result"])
	reply, err := askSourceJudge(ctx, llm, prompt, false)
	if err != nil {
		return checktest.Outcome{Got: ScanNoVerdict, Reason: err.Error()}
	}
	parsed := parseSourceVerdicts(reply)
	var got, why []string
	for _, n := range judged {
		v, ok := parsed[n]
		switch {
		case !ok:
			got = append(got, fmt.Sprintf("%d none", n))
		case v.found:
			got = append(got, fmt.Sprintf("%d yes", n))
		default:
			got = append(got, fmt.Sprintf("%d no", n))
		}
		if ok && v.result != "" {
			why = append(why, fmt.Sprintf("source %d: %s", n, v.result))
		}
	}
	return checktest.Outcome{Got: strings.Join(got, ", "), Reason: strings.Join(why, "; ")}
}
