// What a tool REACHES, which is the only thing a page about securing an agent
// should sort its tools by.
//
// The list used to be flat and every registered tool read "framework", so
// read_file and a shell on a connected appliance were the same row with the
// same two controls. That is wrong twice over. It buries the handful of tools
// worth a decision under the sixty that are not, and it presents a door into
// somebody else's system as though it were part of gohort.
//
// So: four bands, in descending order of what a call can touch.
//
//   - A SYSTEM THE OWNER CONNECTED. App-provided tools (an app registers a
//     provider because the capability belongs to a machine, not to the
//     framework) and credential-backed ones. ONE BAND PER SYSTEM: an app
//     contributes a dozen tools at once, and sorting the whole band by name
//     interleaved them with every other system's, so the thing the reader came
//     to decide about - may this agent reach servitor - was spread down the
//     page instead of being one box.
//   - CODE IN THE SANDBOX. A framework tool declaring CapExecute. It sits
//     ABOVE the internet band because running code is the larger reach of the
//     two: a search sends a query out, execution can read the workspace, write
//     to it, and dial out itself wherever the workspace ceiling allows. The
//     sandbox and that ceiling are real gates and the Network tab is where
//     they are set, but they bound WHERE the code runs, not WHETHER it runs,
//     and that second question is this page's.
//   - THE OPEN INTERNET. A framework tool declaring CapNetwork with no named
//     system behind it: web_search, browse_page, fetch_url.
//   - THE OWNER'S OWN TOOLS. Somebody wrote the code these run.
//   - NOTHING OUTSIDE THIS DEPLOYMENT. Everything else, and it is ALWAYS
//     ALLOWED here: a per-call decision about read_file is a question nobody
//     can answer usefully sixty times, and the controls that matter for these
//     are elsewhere (the Tools modal decides whether the agent has it at all,
//     Guardrails read the turn).
//
// BY PROVENANCE, not by what the tool says about itself. A tool's name and its
// claimed Category are both editable by whoever wrote it; which registry
// handed it over is not. The owner's Category claim is good for organising a
// tool list and is shown on the row - it is not what decides a band, because a
// band that a tool can relabel itself out of is not a security boundary.

package orchestrate

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// The bands, in the order they render. Values are the heading text: the field
// is grouped on directly, so the words the reader sees are the words the
// server sorts by and there is no second mapping to drift.
const (
	// bandConnected is a PREFIX: the system's name completes it, so each one
	// gets its own heading and its own border. Everything that reads a band
	// back sorts and governs off the prefix, never off the whole string.
	bandConnected = "Connected system: "
	bandSandbox   = "Runs code in the sandbox"
	bandInternet  = "Reaches the open internet"
	bandOwn       = "Your own tools"
	bandInternal  = "Stays inside this deployment"
	// Off is last and is its own band rather than a badge, because a tool the
	// agent does not load is not a weaker version of one it does - there is
	// nothing to decide about it until it is on.
	bandOff = "Not loaded by this agent"
)

// bandOrder ranks a band for sorting. The table groups in record order, so
// this IS the order the headings appear in.
func bandOrder(band string) int {
	if strings.HasPrefix(band, bandConnected) {
		return 0
	}
	switch band {
	case bandSandbox:
		return 1
	case bandInternet:
		return 2
	case bandOwn:
		return 3
	case bandInternal:
		return 4
	}
	return 5
}

// classifyTool places one tool in a band and says what it reaches.
//
// provider is the app that contributed it (empty for anything else), cred the
// credential it dispatches through, and own whether it came from the owner's
// pool. caps are the tool's declared capabilities.
//
// Precedence is by consequence, not by origin: a tool the owner wrote that
// dispatches through a credential reaches that system, and the system is the
// larger fact. Its origin still says the owner wrote it.
func classifyTool(provider, cred string, own bool, caps []Capability) (band, reaches string) {
	provider = strings.TrimSpace(provider)
	cred = strings.TrimSpace(cred)
	switch {
	case provider != "":
		return bandConnected + provider, provider
	case cred != "" && !strings.EqualFold(cred, "no_auth"):
		sys := credentialSystemName(cred)
		return bandConnected + sys, sys
	case own:
		return bandOwn, ""
	// Before the network check, because a tool that does both is the more
	// consequential of the two and the band has to say the larger thing.
	case hasCap(caps, CapExecute):
		return bandSandbox, ""
	case hasCap(caps, CapNetwork):
		return bandInternet, ""
	}
	return bandInternal, ""
}

// hasCap is the plain membership test. Capabilities are a short slice on every
// tool, so a set would cost more than it saves.
func hasCap(caps []Capability, want Capability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// credentialSystemName is the credential with its owner scoping stripped, so a
// row says "jira" rather than "@u:alice:jira". The scoping is a storage detail
// and it is the same for every row on the page.
func credentialSystemName(cred string) string {
	if strings.HasPrefix(cred, "@u:") {
		if _, rest, ok := strings.Cut(cred[len("@u:"):], ":"); ok {
			return rest
		}
	}
	return cred
}

// bandGoverns says whether the per-call controls are offered in this band.
//
// The internal band is not: those tools always run. Every other band reaches
// something outside gohort, which is the whole of what makes a per-call
// decision worth making.
func bandGoverns(band string) bool { return band != bandInternal && band != bandOff }

// bandRank orders two rows on the page. The table groups in RECORD order, so
// this is what decides both which heading comes first and what sits under it.
//
// Band, then the band's own name so systems sort alphabetically among
// themselves, then the owner's CATEGORY, then the tool.
//
// The category orders rows and never decides a band. It is editable by whoever
// wrote the tool, so letting it move a tool between bands would make it a
// boundary a tool can talk its way across; letting it cluster tools inside one
// is just the owner's own filing.
func bandRank(a, b accessToolRow) bool {
	if x, y := bandOrder(a.Band), bandOrder(b.Band); x != y {
		return x < y
	}
	if a.Band != b.Band {
		return a.Band < b.Band
	}
	if !strings.EqualFold(a.Category, b.Category) {
		// A tool with no category sorts after the filed ones rather than
		// splitting them: an unfiled row between two of a category reads as
		// belonging to it.
		if a.Category == "" || b.Category == "" {
			return b.Category == ""
		}
		return strings.ToLower(a.Category) < strings.ToLower(b.Category)
	}
	return strings.ToLower(a.Name) < strings.ToLower(b.Name)
}
