// sharing.go — the storage + permission seam for SHARED records. A "shared
// record" is one a user owns (stored in that user's UserDB) that they've also
// published to every authenticated user. The owner's copy stays put; a global
// index in the app-wide store maps recordID -> owner username, so any request can
// discover it and operate it in the OWNER's context — one canonical place —
// regardless of who opened it. Presence in the index IS the shared flag; absence
// means private-to-owner.
//
// These are the generic primitives; each app keeps a thin resolve wrapper that
// loads its own record type from the owner's store (see apps/servitor/sharing.go
// and apps/guides — resolveGuide). Lifted here so servitor, guides, and any future
// app share ONE implementation instead of copy-pasting the index/permission logic.
package core

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/peershare"
	"github.com/cmcoffee/gohort/core/shareledger"
)

// SetSharedOwner adds or removes a record from a shared index. shared && owner!=""
// registers it (recordID -> owner); anything else unregisters it. appDB is the
// app-wide store (NOT a per-user UserDB); indexTable is the app's chosen index
// table name (e.g. "shared_appliances", "shared_guides").
func SetSharedOwner(appDB Database, indexTable, id, owner string, shared bool) {
	if appDB == nil || id == "" {
		return
	}
	if shared && owner != "" {
		appDB.Set(indexTable, id, owner)
	} else {
		appDB.Unset(indexTable, id)
	}
}

// LookupSharedOwner returns the owner username for a shared record ID, and whether
// the ID is currently shared.
func LookupSharedOwner(appDB Database, indexTable, id string) (string, bool) {
	if appDB == nil || id == "" {
		return "", false
	}
	var owner string
	if appDB.Get(indexTable, id, &owner) && owner != "" {
		return owner, true
	}
	return "", false
}

// ListSharedOwners returns every shared record ID -> owner username in the index.
func ListSharedOwners(appDB Database, indexTable string) map[string]string {
	out := map[string]string{}
	if appDB == nil {
		return out
	}
	for _, id := range appDB.Keys(indexTable) {
		var owner string
		if appDB.Get(indexTable, id, &owner) && owner != "" {
			out[id] = owner
		}
	}
	return out
}

// RequestIsAdmin reports whether a request is from an admin. In a no-auth /
// single-user deployment (no users configured) everyone has full rights, matching
// the framework's UserHasAppAccess posture.
func RequestIsAdmin(r *http.Request) bool {
	if AuthDB == nil {
		return true
	}
	db := AuthDB()
	if db == nil || !AuthHasUsers(db) {
		return true
	}
	return AuthIsAdmin(db, r)
}

// UserOwnedAgentRow is one user-owned agent for the admin governance console (the
// user-plane audit). Populated by orchestrate via AdminListUserOwnedAgents so the
// admin app needn't import orchestrate. SharedWith is the recipient list joined
// for display; Shared mirrors len(recipients) > 0.
type UserOwnedAgentRow struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	SharedWith string `json:"shared_with,omitempty"`
	Shared     bool   `json:"shared"`
	Exposed    bool   `json:"exposed"` // already published as a /agents/ app
}

// AdminListUserOwnedAgents / AdminRevokeAgentShare / AdminPublishAgent are wired by
// orchestrate (in an init) so the admin governance console can enumerate, un-share,
// and DELEGATE (publish) user-owned agents across the deployment without the admin
// app importing orchestrate. They take the app-wide Database (admin passes its own)
// and mirror the AdminRehomeOrphanTool hook pattern. Nil until orchestrate's init
// runs (single binary, so always set in practice). Publish is the admin's top-down
// "delegate to users" step: it flips the agent Exposed so it becomes a /agents/
// app the admin can then grant to users (the owner's peer-share stays independent).
var (
	AdminListUserOwnedAgents func(db Database) []UserOwnedAgentRow
	AdminRevokeAgentShare    func(db Database, owner, id string) error
	AdminPublishAgent        func(db Database, owner, id string) error
)

// UserOwnedPipelineRow is one user-owned pipeline for the same governance
// console. Deliberately the AGENT row minus Exposed: a pipeline has no published
// app surface to flip, so a field for it would be a control that does nothing.
type UserOwnedPipelineRow struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	SharedWith string `json:"shared_with,omitempty"`
	Shared     bool   `json:"shared"`
	Stages     int    `json:"stages"`
	// Published is the third rung: an administrator agreed every user may run
	// it. Listed beside Shared rather than folded into it, because "shared with
	// two people" and "shared with everybody" are not the same row to read.
	Published bool `json:"published"`
}

// AdminListUserOwnedPipelines / AdminRevokePipelineShare are the pipeline half of
// the pair above, wired by orchestrate in the same init. An admin who can audit
// and revoke a shared agent must be able to do both for a shared pipeline, or
// half the user plane is governable and the other half is invisible.
var (
	AdminListUserOwnedPipelines func(db Database) []UserOwnedPipelineRow
	AdminRevokePipelineShare    func(db Database, owner, id string) error
)

// UserOwnedMachineRow is one user-owned machine for the same governance
// console. The pipeline row with Steps for Stages, plus Unattended: whether a
// machine RUNS or converses is what decides where a share can be spent — a
// recipient can put an unattended one on a timetable or hand it to an agent,
// while a conversational one is only ever reached by a person talking to it.
type UserOwnedMachineRow struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	SharedWith string `json:"shared_with,omitempty"`
	Shared     bool   `json:"shared"`
	Steps      int    `json:"steps"`
	Unattended bool   `json:"unattended"`
	// Published is the third rung: an administrator agreed every user may run
	// it. Listed beside Shared rather than folded into it, because "shared with
	// two people" and "shared with everybody" are not the same row to read.
	Published bool `json:"published"`
}

// AdminListUserOwnedMachines / AdminRevokeMachineShare are the machine half of
// the same pair, wired by orchestrate in the same init. Three shareable kinds
// now exist in the user plane, and an admin who can audit two of them has a
// console that is quietly wrong about the third.
var (
	AdminListUserOwnedMachines func(db Database) []UserOwnedMachineRow
	AdminRevokeMachineShare    func(db Database, owner, id string) error
)

// CanManageShared reports whether reqUser may change sharing / edit / delete a
// record with the given owner: the owner, or an admin. Non-owners of a shared
// record can use it but not manage it. An empty owner is a legacy record with no
// owner stamp — its holder (it's in their store) manages it.
func CanManageShared(reqUser, owner string, isAdmin bool) bool {
	if isAdmin {
		return true
	}
	if owner == "" {
		return true
	}
	return owner == reqUser
}

// ----------------------------------------------------------------------
// Telling somebody that something has been shared with them
// ----------------------------------------------------------------------

// The owner has always had a report of what they just did. The people on the
// other end got nothing: no word that anything arrived, and no word that the
// thing which arrived needs something from them before it works. A share that
// tells only the person who made it is how a colleague finds out by running
// something and watching it fail.
//
// Wired here rather than inside core/shareledger so that package stays
// storage-free — it holds the registry and knows nothing about where a
// deployment keeps its notifications. In this file rather than one of its own
// because core is AT its file ceiling, and this is the file about sharing
// seams in core.

func init() {
	shareledger.CanKeep = func(kind, owner, id, user string) bool {
		if kind != "tool" || RootDB == nil {
			return false
		}
		_, err := RecreateLostTool(RootDB, user, id, false)
		return err == nil
	}
	shareledger.NotifyRecipient = func(recipient, title, intro string, needs []string) {
		if RootDB == nil || strings.TrimSpace(recipient) == "" {
			return
		}
		// The kind turns on whether there is anything for them to do. A share
		// that needs nothing is news; one that needs a credential of their own
		// is waiting on them, and filing the second as news is how it gets
		// scrolled past.
		kind := notices.KindReport
		if len(needs) > 0 {
			kind = notices.KindBlocked
		}
		notices.Record(RootDB, notices.Notice{
			Owner: recipient,
			Kind:  kind,
			Title: title,
			Body:  strings.TrimSpace(intro + " " + strings.Join(needs, " ")),
		})
	}
}

// ----------------------------------------------------------------------
// Sharing with a group
// ----------------------------------------------------------------------

// A group goes into a record's recipient list as one more entry, "group:<id>",
// beside the names already there. The list stays the one thing the owner
// edits, the index stays derived from it, and nothing about a share changes
// shape for the kinds that never offer a group.
//
// Two rules, the same ones an app shared with a group obeys. The group's
// administrator has to have let members share with it (a group that began as
// an app bundle can be close to the whole deployment, which is not a member's
// call), and the owner has to still be in it: "my team" stops being theirs to
// share with when they leave. Membership is read at the check, so somebody who
// joins later has the thing and somebody who leaves loses it.

const shareGroupPrefix = "group:"

// Membership read at the check covers every share that is a list. What it
// cannot cover is a grant made to each member at the time of sharing, such
// as a credential lent for an agent shared with a team: that sits on the
// credential under each person's name. Whoever makes such a grant registers
// here and is told whenever teams change (a person's teams set, a team saved
// or deleted), to take back what no longer applies.
var (
	teamHooksMu sync.Mutex
	teamHooks   []func()
)

// OnTeamsChanged registers fn to run after any change to who is on which team
// or which teams can carry shares.
func OnTeamsChanged(fn func()) {
	teamHooksMu.Lock()
	defer teamHooksMu.Unlock()
	teamHooks = append(teamHooks, fn)
}

func teamsChanged() {
	teamHooksMu.Lock()
	hooks := append([]func(){}, teamHooks...)
	teamHooksMu.Unlock()
	for _, fn := range hooks {
		fn()
	}
}

// shareGroupOf is the group id an entry names, if it names one.
func shareGroupOf(entry string) (string, bool) {
	id, ok := strings.CutPrefix(strings.TrimSpace(entry), shareGroupPrefix)
	return id, ok && id != ""
}

func userInShareGroup(user, groupID string) bool {
	if AuthDB == nil || user == "" {
		return false
	}
	u, ok := AuthGetUser(AuthDB(), user)
	if !ok {
		return false
	}
	for _, g := range u.Groups {
		if g == groupID {
			return true
		}
	}
	return false
}

// shareGroup is a group that can carry a share right now.
func shareGroup(groupID string) (AppGroup, bool) {
	if AuthDB == nil || groupID == "" {
		return AppGroup{}, false
	}
	g, ok := LoadAppGroup(AuthDB(), groupID)
	if !ok || !g.Shareable {
		return AppGroup{}, false
	}
	return g, true
}

// ShareRecipientKeys is every recipient entry that can reach user: their own
// name, then one per shareable group they are in. A recipient-side lookup
// lists the share index under each.
func ShareRecipientKeys(user string) []string {
	user = strings.TrimSpace(user)
	if user == "" {
		return nil
	}
	keys := []string{user}
	if AuthDB == nil {
		return keys
	}
	u, ok := AuthGetUser(AuthDB(), user)
	if !ok {
		return keys
	}
	for _, id := range u.Groups {
		if _, ok := shareGroup(id); ok {
			keys = append(keys, shareGroupPrefix+id)
		}
	}
	return keys
}

// SharedRefsFor is everything shared with user through the index in store, by
// name or through a group, once each.
func SharedRefsFor(store peershare.Store, indexTable, user string) []peershare.Ref {
	if store == nil {
		return nil
	}
	seen := map[peershare.Ref]bool{}
	var out []peershare.Ref
	for _, k := range ShareRecipientKeys(user) {
		for _, ref := range peershare.List(store, indexTable, k) {
			if !seen[ref] {
				seen[ref] = true
				out = append(out, ref)
			}
		}
	}
	return out
}

// SharedWithUser says whether a recipient list an owner keeps reaches user:
// by name, or through a group that still carries shares and both are in.
func SharedWithUser(list []string, owner, user string) bool {
	user = strings.TrimSpace(user)
	if user == "" {
		return false
	}
	for _, e := range list {
		id, group := shareGroupOf(e)
		if !group {
			if strings.TrimSpace(e) == user {
				return true
			}
			continue
		}
		if _, ok := shareGroup(id); ok && userInShareGroup(owner, id) && userInShareGroup(user, id) {
			return true
		}
	}
	return false
}

// ShareMembers is the people a recipient list reaches today, groups opened up
// to their members: who to tell when a share goes away, and whose use of it
// to look for. The owner is left out.
func ShareMembers(list []string, owner string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		if u = strings.TrimSpace(u); u != "" && u != owner && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	for _, e := range list {
		id, group := shareGroupOf(e)
		if !group {
			add(e)
			continue
		}
		if _, ok := shareGroup(id); !ok || !userInShareGroup(owner, id) {
			continue
		}
		for _, u := range AuthListUsers(AuthDB()) {
			if userInShareGroup(u.Username, id) {
				add(u.Username)
			}
		}
	}
	sort.Strings(out)
	return out
}

// sharesLost is who an edit of a recipient list took the thing away from:
// reached before, not reached after, groups opened up on both sides so that
// dropping a group does not tell a member who still has it by name.
func sharesLost(before, after []string, owner string) []string {
	return recipientsLost(ShareMembers(before, owner), ShareMembers(after, owner))
}

// ShareRecipientsText is a recipient list as words: names as they are, a
// group by its name.
func ShareRecipientsText(list []string) string {
	parts := make([]string, 0, len(list))
	for _, e := range list {
		id, group := shareGroupOf(e)
		if !group {
			if strings.TrimSpace(e) == ShareEveryone {
				e = "everyone"
			}
			parts = append(parts, e)
			continue
		}
		if AuthDB != nil {
			if g, ok := LoadAppGroup(AuthDB(), id); ok {
				parts = append(parts, "the "+g.Name+" team")
				continue
			}
		}
		parts = append(parts, "a removed team")
	}
	return strings.Join(parts, ", ")
}

// ShareEveryone is the share picker's Everyone: every signed-in user. It is
// offered only by a kind that can be shared that widely, and that kind maps it
// to its own everyone setting when it saves. As an entry in a recipient list
// it names nobody, so SharedWithUser never reads it as everyone: a list that
// picked it up where it was not offered fails closed.
const ShareEveryone = "*"

// ShareCandidatesJSON is a share picker's candidates: with everyone (a kind
// that can be shared with every signed-in user), Everyone first; then each
// person but owner; then, with groups (a kind that can be shared with one),
// each group owner may share with, named for what it is and how many it
// reaches today.
func ShareCandidatesJSON(owner string, groups, everyone bool) []byte {
	label := ""
	if everyone {
		label = "Everyone"
	}
	return ShareCandidatesFor(owner, groups, label)
}

// EveryoneChoiceLabel is how a share picker names Everyone where widening to
// it is an administrator's call (promotion.AskEveryone): plainly once it is
// granted or for an administrator, else as the request it files or is
// waiting on.
func EveryoneChoiceLabel(granted, pending, admin bool) string {
	switch {
	case granted || admin:
		return "Everyone"
	case pending:
		return "Everyone (waiting for an administrator)"
	}
	return "Everyone (an administrator approves)"
}

// ShareCandidatesFor is ShareCandidatesJSON with Everyone first under
// everyoneLabel, or left out when it is "".
func ShareCandidatesFor(owner string, groups bool, everyoneLabel string) []byte {
	// Named is set on a person and a team, never on Everyone: a picker
	// whose higher level Everyone may not hold offers it only on these
	// (ui.ACLPickerConfig.FlagWhenField "named").
	type candidate struct {
		Value string `json:"value"`
		Label string `json:"label"`
		Desc  string `json:"desc,omitempty"`
		Named bool   `json:"named,omitempty"`
	}
	var out []candidate
	if AuthDB == nil {
		return []byte("[]")
	}
	json.Unmarshal(UserCandidatesJSON(AuthDB(), owner), &out)
	for i := range out {
		out[i].Named = true
	}
	if everyoneLabel != "" {
		out = append([]candidate{{Value: ShareEveryone, Label: everyoneLabel,
			Desc: "every signed-in user, " + strconv.Itoa(len(out)) + " besides you today"}}, out...)
	}
	if u, ok := AuthGetUser(AuthDB(), owner); ok && groups {
		users := AuthListUsers(AuthDB())
		var groups []candidate
		for _, id := range u.Groups {
			g, ok := shareGroup(id)
			if !ok {
				continue
			}
			n := 0
			for _, m := range users {
				if !m.Pending && m.Username != owner && userInShareGroup(m.Username, id) {
					n++
				}
			}
			groups = append(groups, candidate{
				Value: shareGroupPrefix + id,
				Label: "Team: " + g.Name,
				Desc:  strconv.Itoa(n) + " other " + map[bool]string{true: "member", false: "members"}[n == 1],
				Named: true,
			})
		}
		sort.Slice(groups, func(i, j int) bool { return strings.ToLower(groups[i].Label) < strings.ToLower(groups[j].Label) })
		out = append(out, groups...)
	}
	if out == nil {
		out = []candidate{}
	}
	b, _ := json.Marshal(out)
	return b
}
