// Telling "the model rejected this picture" apart from "the model never saw
// it". Both come back as ordinary prose, and every vision-reading path in the
// codebase used to treat the second as the first.
package core

import "strings"

// blindVisionPhrases are answers that only come from a model that was not shown
// the picture — no image modality, or an endpoint that dropped the attachment.
//
// Kept to unambiguous wordings. A false positive costs one discarded answer,
// which every caller already handles as "no answer"; a false negative stores a
// refusal as if it were a description, which is the failure this exists to stop.
var blindVisionPhrases = []string{
	"no image was provided", "no image provided", "there is no image",
	"i don't see an image", "i do not see an image", "i can't see an image",
	"i cannot see an image", "i can't see the image", "i cannot see the image",
	"unable to see the image", "unable to view the image", "cannot view the image",
	"i'm unable to see images", "i am unable to see images", "i cannot see images",
	"i can't see images", "i cannot process images", "i can't process images",
	"i don't have the ability to see", "i do not have the ability to see",
	"as a text-based", "text-based model", "no attached image", "image was not attached",
	"no image appears to be attached", "attachment didn't come through",
	"attachment did not come through",
}

// ModelSawNoImage reports whether a vision answer shows the model never
// received the picture it was asked about.
//
// The alternative to checking is believing it. A deployment whose model has no
// image modality answers every vision prompt with a polite "I don't see an
// image", and each caller reads that as content: the find_image screen scored
// it 0/100 and rejected good photographs, and the caption pass stored it as the
// picture's own description — so an agent later read its manifest, found "no
// image appears to be attached" against a render that had in fact succeeded,
// and told the user the image backend was broken.
func ModelSawNoImage(answer string) bool {
	low := strings.ToLower(answer)
	for _, p := range blindVisionPhrases {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}
