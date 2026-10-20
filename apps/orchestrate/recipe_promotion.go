package orchestrate

// Publishing a pipeline or a machine is the administrator's decision.
//
// The third rung for the two remaining primitives in the user plane. Both
// already reached other people by name, and both already resolved everything
// they touch in the RUNNER's namespace, so widening one is a decision about a
// way of working rather than about access — which is precisely why it can be a
// simple grant and does not need anything stripped out of it on the way.
//
// Neither record MOVES. A shared pipeline is already read out of its owner's
// store by whoever runs it, so publishing widens the grant on the one copy: the
// owner keeps editing it and what they edit is what everybody gets. That is a
// different shape from a collection or a skill, which move, and the reason is
// the storage rather than the policy — those live in per-user pools that
// nobody else reads.
//
// Taking it back stays owner-direct, exactly as revoking a share does: nobody
// needs permission to stop publishing something they wrote.

import (
	"net/http"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/promotion"
)

const (
	pipelinePromotionKind = "pipeline"
	machinePromotionKind  = "machine"
)

func registerRecipePromotions() {
	promotion.RegisterApprover(pipelinePromotionKind, publishPipelineForDeployment)
	promotion.RegisterApprover(machinePromotionKind, publishMachineForDeployment)
}

// publishPipelineForDeployment is what an administrator's Approve does. It
// resolves by id or name, because the request is FILED under the name: an
// administrator deciding whether everybody should run something is reading that
// row, and a uuid tells them nothing.
func publishPipelineForDeployment(owner, key string) error {
	def, err := findOwnedPipeline(owner, key)
	if err != nil {
		return err
	}
	if def.Published {
		return nil // already out; approving a stale duplicate request is not an error
	}
	def.Published = true
	// The recipient list stays. Everybody has it now, so it decides nothing
	// while published, but it is in the share list beside Everyone where the
	// owner sees it, and taking Everyone back leaves those people their share.
	SavePipelineDefAs(UserDB(orchestrateBaseDB, owner), def, "published deployment-wide")
	Log("[orchestrate.publish] pipeline %q by %q is now deployment-wide", def.Name, owner)
	return nil
}

func publishMachineForDeployment(owner, key string) error {
	def, err := findOwnedMachine(owner, key)
	if err != nil {
		return err
	}
	if def.Published {
		return nil
	}
	def.Published = true
	SaveMachineDefAs(UserDB(orchestrateBaseDB, owner), def, "published deployment-wide")
	Log("[orchestrate.publish] machine %q by %q is now deployment-wide", def.Name, owner)
	return nil
}

// unpublishPipeline and unpublishMachine are the way back, and they are the
// OWNER's. The record returns to its named list: the people and teams the
// owner kept in it beside Everyone.
func unpublishPipeline(owner, id string) error {
	def, err := findOwnedPipeline(owner, id)
	if err != nil {
		return err
	}
	if !def.Published {
		return Error("pipeline " + def.Name + " is not published")
	}
	def.Published = false
	SavePipelineDefAs(UserDB(orchestrateBaseDB, owner), def, "taken back from the deployment")
	Log("[orchestrate.publish] %q took pipeline %q back from the deployment", owner, def.Name)
	return nil
}

func unpublishMachine(owner, id string) error {
	def, err := findOwnedMachine(owner, id)
	if err != nil {
		return err
	}
	if !def.Published {
		return Error("machine " + def.Name + " is not published")
	}
	def.Published = false
	SaveMachineDefAs(UserDB(orchestrateBaseDB, owner), def, "taken back from the deployment")
	Log("[orchestrate.publish] %q took machine %q back from the deployment", owner, def.Name)
	return nil
}

// findOwnedPipeline resolves by id first, then by exact name, and only within
// the named owner's own store. A recipient's copy of somebody else's id is not
// a match, because this is the door that decides what an approval acts on.
func findOwnedPipeline(owner, key string) (PipelineDef, error) {
	owner, key = strings.TrimSpace(owner), strings.TrimSpace(key)
	if owner == "" || key == "" {
		return PipelineDef{}, Error("owner and pipeline are required")
	}
	udb := UserDB(orchestrateBaseDB, owner)
	if udb == nil {
		return PipelineDef{}, Error("no store for " + owner)
	}
	if def, ok := LoadPipelineDef(udb, owner, key); ok && def.Owner == owner {
		return def, nil
	}
	for _, def := range ListPipelineDefs(udb, owner) {
		if def.Owner == owner && strings.EqualFold(def.Name, key) {
			return def, nil
		}
	}
	return PipelineDef{}, Error("no pipeline " + key + " owned by " + owner +
		" (one renamed after the request was filed no longer answers to the name on it)")
}

func findOwnedMachine(owner, key string) (MachineDef, error) {
	owner, key = strings.TrimSpace(owner), strings.TrimSpace(key)
	if owner == "" || key == "" {
		return MachineDef{}, Error("owner and machine are required")
	}
	udb := UserDB(orchestrateBaseDB, owner)
	if udb == nil {
		return MachineDef{}, Error("no store for " + owner)
	}
	if def, ok := LoadMachineDef(udb, owner, key); ok && def.Owner == owner {
		return def, nil
	}
	for _, def := range ListMachineDefs(udb, owner) {
		if def.Owner == owner && strings.EqualFold(def.Name, key) {
			return def, nil
		}
	}
	return MachineDef{}, Error("no machine " + key + " owned by " + owner +
		" (one renamed after the request was filed no longer answers to the name on it)")
}

// Everyone is one more choice in a pipeline's or a machine's share list, and
// the list's door turns it into the publish steps above through the shared
// rule (promotion.AskEveryone): an administrator's Everyone publishes at
// once, anybody else's files the request, and taking it back out unpublishes
// (owner-direct) or withdraws a request still waiting. The list itself never
// stores it; Published and the request queue do. The names beside it are
// kept either way.

// splitEveryone is the list without Everyone, and whether it was in it.
func splitEveryone(in []string) ([]string, bool) {
	out := make([]string, 0, len(in))
	want := false
	for _, u := range in {
		if strings.TrimSpace(u) == ShareEveryone {
			want = true
			continue
		}
		out = append(out, u)
	}
	return out, want
}

// withEveryone is the list as the picker reads it: Everyone first when the
// record is published or asked to be.
func withEveryone(list []string, on bool) []string {
	if !on {
		return list
	}
	return append([]string{ShareEveryone}, list...)
}

// recipeShare is a pipeline's or machine's share door after the owner check:
// GET reads the list with Everyone in it, POST applies one. publish, unpublish,
// load and save are the kind's own.
type recipeShare struct {
	kind      string // pipelinePromotionKind or machinePromotionKind
	owner     string
	name      string
	published bool
	before    []string // the stored list, without Everyone
	editors   []string // who among them may edit
	publish   func() error
	unpublish func() error
	// save stores names as the list and editors as who among them may
	// edit, and returns the record to answer with, re-read after a publish
	// step so it carries what that step changed.
	save func(names, editors []string) any
}

func (rs recipeShare) pending() bool { return publishPending(rs.kind, rs.owner, rs.name) }

// ask is the save as a request: filed under the record's name, which is what
// an administrator reads and what the /publish door has always filed under.
func (rs recipeShare) ask(r *http.Request, want bool) promotion.EveryoneAsk {
	a := promotion.EveryoneAsk{
		Kind: rs.kind, Owner: rs.owner, ID: rs.name, Admin: requestIsAdminAgent(r),
		Note: strings.ToUpper(rs.kind[:1]) + rs.kind[1:] + " " + strconv.Quote(rs.name) + ": everyone can run it",
	}
	if want {
		a.Want = promotion.EveryoneOn
	}
	if rs.published {
		a.Has = promotion.EveryoneOn
	}
	return a
}

// publishPending reports whether owner's request to publish name is waiting.
func publishPending(kind, owner, name string) bool {
	return AuthDB != nil && PendingPromotion(AuthDB(), owner, kind, name)
}

// list is what GET answers: {"allowed_users": [...], "editors": [...]}.
func (rs recipeShare) list() map[string]any {
	return map[string]any{"allowed_users": withEveryone(nonNilUsers(rs.before), rs.published || rs.pending()),
		"editors": nonNilUsers(rs.editors)}
}

// apply settles the Everyone the posted list asks for, then stores the names
// and, of them, the editors (posted, or the current ones when the body sent
// none). Everyone runs a recipe and never edits it (cleanEditors).
func (rs recipeShare) apply(r *http.Request, posted []string, editors *[]string) (any, error) {
	names, want := splitEveryone(posted)
	ed := rs.editors
	if editors != nil {
		ed = *editors
	}
	ed = cleanEditors(ed, normalizeShareList(names, rs.owner), rs.owner)
	var db promotion.Store
	if AuthDB != nil {
		db = AuthDB()
	}
	level, err := promotion.AskEveryone(db, rs.ask(r, want))
	if err != nil {
		return nil, err
	}
	switch on := level != ""; {
	case on && !rs.published:
		if err := rs.publish(); err != nil {
			return nil, err
		}
	case !on && rs.published:
		if err := rs.unpublish(); err != nil {
			return nil, err
		}
	}
	return rs.save(names, ed), nil
}

// everyoneParam is the share picker's ?everyone=: "on" once it is published,
// so the choice reads plainly, else "1", worded as the request it files.
func everyoneParam(published bool) string {
	if published {
		return "on"
	}
	return "1"
}

// recipeShareSubtitle says who the list reaches now, and where Everyone stands.
func recipeShareSubtitle(noun string, published, pending bool) string {
	switch {
	case published:
		return "Published: every user of the deployment can run this " + noun + ". Take Everyone out of the list to make it private again."
	case pending:
		return "Everyone is waiting for an administrator to approve it. Until then, the people and teams in the list can run it."
	}
	return "Everyone, people, or a team you are on. Empty means private to you."
}

// recipeEveryoneDetail is the share section's word on Everyone.
const recipeEveryoneDetail = "Everyone reaches every account in the deployment, so it is requested rather than applied: an administrator approves it from the pending queue. " +
	"Taking it back out is yours and takes effect at once; anyone who only had it through Everyone loses it, including any schedule they armed against it. " +
	"The people and teams in the list keep it either way."
