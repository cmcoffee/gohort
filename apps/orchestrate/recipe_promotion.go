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
	// The recipient list goes. Everybody has it now, so a list naming three
	// people decides nothing, and leaving it would have a later un-publishing
	// silently restore an ACL the owner had forgotten.
	def.AllowedUsers = nil
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
	def.AllowedUsers = nil
	SaveMachineDefAs(UserDB(orchestrateBaseDB, owner), def, "published deployment-wide")
	Log("[orchestrate.publish] machine %q by %q is now deployment-wide", def.Name, owner)
	return nil
}

// unpublishPipeline and unpublishMachine are the way back, and they are the
// OWNER's. The record returns to being private, with no recipients, because the
// alternative is guessing which of the deployment's users they meant to keep.
func unpublishPipeline(owner, id string) error {
	def, err := findOwnedPipeline(owner, id)
	if err != nil {
		return err
	}
	if !def.Published {
		return Error("pipeline " + def.Name + " is not published")
	}
	def.Published = false
	def.AllowedUsers = nil
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
	def.AllowedUsers = nil
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
