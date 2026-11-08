package imagefetch

// The source check: after an edit, whether what the instruction took from each
// source picture is in the result, decided by something other than the agent's
// own look.
//
// The agent's look failed with the pixels in front of it: a blend of the
// Enterprise and the Yamato came back with the Yamato kept and the Enterprise
// replaced by an invented white ship, and the agent, shown both, reported
// "both ships came through". A first fix asked a separate question per source
// with the source and the result side by side, and that failed the same way:
// shown the source first, the model named its saucer and nacelles and then
// found them in a result that had neither, or matched the Enterprise to the
// Yamato. Shown what to look for, a model sees it.
//
// So nothing that judges ever sees what it is looking for:
//
//  1. Each picture is DESCRIBED on its own, by a call that sees that one
//     picture and nothing else and does not know what it is for: the result,
//     and every source. Run together.
//  2. One text-only JUDGE reads the instruction and the descriptions and says,
//     per source, what the instruction takes from it (the whole subject, or
//     only a face, a pose, a background) and whether that is in the result.
//
// The instruction matters because an edit changes things on purpose. A face
// swap takes only the face from one source and replaces the face in the
// other; asking "is each source's subject in the result" would call the swap a
// loss and send the agent redoing a correct render.
//
// The judge thinks; the descriptions cannot (calls carrying a picture have
// thinking turned off, applyVisionDefaults). A likeness compared through words
// is weaker than one compared through pixels, and that is the trade: weaker,
// but not able to be talked into a match.

import (
	"bytes"
	"fmt"
	"image"
	"regexp"
	"strconv"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

// sourceVerdict is the check's answer about one source.
type sourceVerdict struct {
	n      int    // the source's number, as on the contact sheet
	ref    string // the reference it was passed as
	found  bool   // what the instruction took from it is in the result
	takes  string // what the instruction takes from this source
	result string // what in the result corresponds to it, or what is missing
	small  string // "421x237" when the source picture is small, else ""
}

// smallSourceSide is the long side under which a source picture is called
// small in the note. A lost ship's reference was 421x237: the edit model had
// little to draw it from, and a larger picture is the one change on this side
// of the backend that can help.
const smallSourceSide = 512

// smallSize is "WxH" for a picture whose long side is under smallSourceSide,
// or "" for a larger one or one that does not decode.
func smallSize(data []byte) string {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || max(cfg.Width, cfg.Height) >= smallSourceSide {
		return ""
	}
	return fmt.Sprintf("%dx%d", cfg.Width, cfg.Height)
}

// describeBlindPrompt describes one picture for a reader who will never see
// it. It is the same for the result and for every source, so the two sides
// are described alike, and it asks for no names: a describer that guesses
// "the Enterprise" hands the judge a label instead of a ship.
const describeBlindPrompt = "Describe this picture for someone who will compare it with other pictures without ever seeing it. " +
	"List every distinct subject in it (each person, animal, vehicle, ship, building or product) on its own line starting \"- \": " +
	"what kind of thing it is, where it is in the frame, and the parts that would tell it apart from others of its kind: " +
	"overall shape, distinctive parts, colours, markings; for a person, face shape, hair, facial hair, skin tone, apparent age and clothing. " +
	"Then one line starting \"Setting:\" for the background. Describe only what is visible. Do not name or guess who or what anything is meant to be."

// checkSourcesInResult runs the check for an edit made from sources with the
// given instruction. Only decisive answers are returned: a source whose
// description or verdict did not come back is left out, not counted either way.
// think is a judge that thinks it over (Thorough checking): about 15s more on
// a worker, for a verdict on a close call.
func checkSourcesInResult(sess *ToolSession, sources []sourceImage, result []byte, instruction string, think bool) []sourceVerdict {
	if sess == nil || sess.LLM == nil || len(sources) == 0 || len(result) == 0 {
		return nil
	}
	// 1. Every picture described on its own, together.
	descs := make([]string, len(sources)+1) // [0] the result, [i+1] source i
	var wg sync.WaitGroup
	for i := range descs {
		img, what := result, "the result"
		if i > 0 {
			img, what = sources[i-1].data, sources[i-1].ref
		}
		wg.Add(1)
		go func(i int, img []byte, what string) {
			defer wg.Done()
			descs[i] = describeBlind(sess, img, what)
		}(i, img, what)
	}
	wg.Wait()
	if descs[0] == "" {
		return nil // nothing to judge against
	}
	// 2. One judge, text only.
	var judged []int
	var b strings.Builder
	instruction = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(instruction), strings.TrimSpace(editCompositingGuard())))
	if instruction == "" {
		instruction = "(none: the pictures were combined as they are)"
	}
	fmt.Fprintf(&b, "An image edit was made from %d source picture(s). The instruction for it was:\n%s\n\n", len(sources), instruction)
	for i, d := range descs[1:] {
		if d == "" {
			continue
		}
		judged = append(judged, i+1)
		fmt.Fprintf(&b, "SOURCE %d, as described by someone who saw only that picture:\n%s\n\n", i+1, d)
	}
	if len(judged) == 0 {
		return nil
	}
	fmt.Fprintf(&b, "The RESULT, as described by someone who saw only the result and none of the sources:\n%s\n\n", descs[0])
	b.WriteString("For each source above, decide what the instruction takes from it: its main subject whole, or only a part (a face, a pose, a body, a background, a style). " +
		"Then decide whether the result description contains THAT, recognisably the same one with its distinctive parts, not a different thing of the same kind standing in for it. " +
		"A part the instruction meant to replace is not missing. Write three lines per source:\n" +
		"Source N takes: <what the instruction takes from it>\n" +
		"Source N in result: <which described subject has it, or what is missing>\n" +
		"Source N FOUND: yes or no")
	resp, err := sess.LLM.Chat(sess.Context(),
		[]Message{{Role: "user", Content: b.String()}},
		WithCaller("imagefetch/source_check"),
		WithMaxRetries(0),
		WithThink(think),
	)
	if err != nil || resp == nil {
		Debug("[imagefetch/source_check] no verdict (%v)", err)
		return nil
	}
	parsed := parseSourceVerdicts(resp.Content)
	var out []sourceVerdict
	for _, n := range judged {
		v, ok := parsed[n]
		if !ok {
			continue
		}
		v.n, v.ref, v.small = n, sources[n-1].ref, smallSize(sources[n-1].data)
		Log("[imagefetch/source_check] source %d (%s) found=%v: takes %q; in the result %q", v.n, v.ref, v.found, v.takes, v.result)
		out = append(out, v)
	}
	return out
}

// describeBlind describes one picture with describeBlindPrompt, or "" when no
// description came back or the model never saw the picture.
func describeBlind(sess *ToolSession, img []byte, what string) string {
	resp, err := sess.LLM.Chat(sess.Context(),
		[]Message{{Role: "user", Content: describeBlindPrompt, Images: [][]byte{img}}},
		WithCaller("imagefetch/source_check"),
		WithMaxRetries(0),
	)
	if err != nil || resp == nil {
		Debug("[imagefetch/source_check] %s: no description (%v)", what, err)
		return ""
	}
	if ModelSawNoImage(resp.Content) {
		Log("[imagefetch/source_check] %s: described without seeing the picture: %q", what, truncate(strings.TrimSpace(resp.Content), 160))
		return ""
	}
	return strings.TrimSpace(resp.Content)
}

// sourceLineRE reads one of the judge's lines: "Source 2 FOUND: no".
var sourceLineRE = regexp.MustCompile(`(?i)^source\s+(\d+)\s+(takes|in result|found)\s*:\s*(.*)$`)

// parseSourceVerdicts reads the judge's lines by source number. A source with
// no FOUND line has no verdict.
func parseSourceVerdicts(text string) map[int]sourceVerdict {
	all := map[int]*sourceVerdict{}
	decided := map[int]bool{}
	for _, line := range strings.Split(text, "\n") {
		m := sourceLineRE.FindStringSubmatch(unmark(line))
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		v := all[n]
		if v == nil {
			v = &sourceVerdict{}
			all[n] = v
		}
		val := unmark(m[3])
		switch strings.ToLower(m[2]) {
		case "takes":
			v.takes = val
		case "in result":
			v.result = val
		case "found":
			switch strings.ToLower(strings.Trim(val, ".!")) {
			case "yes":
				v.found, decided[n] = true, true
			case "no":
				v.found, decided[n] = false, true
			}
		}
	}
	out := map[int]sourceVerdict{}
	for n, v := range all {
		if decided[n] {
			out[n] = *v
		}
	}
	return out
}

// unmark strips the markdown a model wraps a labelled line or value in
// ("**Source 1 FOUND:** yes").
func unmark(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.Trim(strings.TrimSpace(s), "*_`"), "**", ""))
}

// sourceCheckNote is what the check found, for the tool result: every source
// whose part did not make it into the result, named with what the instruction
// took from it and what is there instead, or that each was found. Empty when
// nothing decisive came back.
//
// A lost source outranks the agent's own look, and says so: the agent had that
// look and reported the lost ship as present.
//
// What it does NOT do is prescribe a cleverer retry. The lost ship was the
// edit model failing to draw it from a small reference, with both pictures
// delivered; no rewording or extra pass on this side changes what the model
// can draw. One plain retry, then the truth, and a word when the source
// picture is small, which is the one thing worth changing.
//
// last is a result that was the last picture its sources may give
// (ImageCheckAttempts): it says not to try again, rather than once more.
func sourceCheckNote(verdicts []sourceVerdict, total int, held, last bool) string {
	if len(verdicts) == 0 {
		return ""
	}
	var lost []string
	small := false
	for _, v := range verdicts {
		if v.found {
			continue
		}
		what := fmt.Sprintf("source %d (%s)", v.n, v.ref)
		if v.takes != "" {
			what += ", from which the instruction takes " + v.takes
		}
		if v.result != "" {
			what += "; in the result: " + v.result
		}
		if v.small != "" {
			what += "; its picture is only " + v.small
			small = true
		}
		lost = append(lost, what)
	}
	if len(lost) == 0 {
		if len(verdicts) < total {
			return ""
		}
		return " AUTOMATIC CHECK: the result was described by a separate look that had not seen the sources, and what the instruction takes from each source was found in it."
	}
	next := " Try once more at most, with that named as the thing to keep. If it still does not come through, tell the person plainly what was lost: the image model did not draw it from the picture."
	if last {
		next = " That was the last try these sources allow: do not render them again. Tell the person plainly what was lost: the image model did not draw it from the picture."
	}
	if small {
		next += " Say that a larger, clearer picture of it may come through better."
	}
	if held {
		next += " Do NOT build the next pass on this step."
	}
	return " AUTOMATIC CHECK, made from descriptions written by separate looks that had not seen each other's pictures, which outranks your own look: NOT in the result: " +
		strings.Join(lost, "; ") + ". Do NOT present this picture as containing it." + next
}
