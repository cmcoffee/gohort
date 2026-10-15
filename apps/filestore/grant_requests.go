package filestore

// An app asking for a store, through the approvals queue.
//
// A grant is an admin's decision (app_grants.go), and it used to be made only
// by hand on the store's page, after Builder told the person what to set. Now
// Builder files the request (app_def request_store) and the admin answers it:
// on the chat's card when the person building is an admin, otherwise under
// Admin, Governance, Pending promotions. The request names what is asked for
// (appstores.GrantRequestName), and approving applies exactly that, added to
// whatever the app already holds there.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/gohort/core/promotion"
)

func registerGrantRequests(T *FileStoreApp) {
	// Checked when filed: the store exists and the app is the requester's.
	promotion.RegisterRequestHook(appstores.GrantRequestKind, func(owner, name string) error {
		_, _, err := T.grantRequest(owner, name)
		return err
	})
	promotion.RegisterApprover(appstores.GrantRequestKind, func(owner, name string) error {
		st, g, err := T.grantRequest(owner, name)
		if err != nil {
			return err
		}
		merged := g
		kept := st.AppGrants[:0:0]
		for _, old := range st.AppGrants {
			if old.App == g.App {
				merged.Write = merged.Write || old.Write
				merged.Commands = merged.Commands || old.Commands
				merged.Pull = merged.Pull || old.Pull
				continue
			}
			kept = append(kept, old)
		}
		st.AppGrants = append(kept, merged)
		if _, err := SaveStore(T.DB, st); err != nil {
			return err
		}
		Log("[filestore] approved app %s on %s: %s", merged.App, st.Name, merged.rights())
		return nil
	})
}

// grantRequest reads a store-grant request into the store and the grant it
// asks for, or says why it cannot stand.
func (T *FileStoreApp) grantRequest(owner, name string) (Store, AppGrant, error) {
	store, slug, write, commands, pull, ok := appstores.ParseGrantRequestName(name)
	if !ok {
		return Store{}, AppGrant{}, fmt.Errorf("not a store request: %q", name)
	}
	st, ok := LoadStore(T.DB, strings.TrimSpace(store))
	if !ok {
		return Store{}, AppGrant{}, fmt.Errorf("there is no file store %q", store)
	}
	if _, ok := LoadAppSpec(owner, strings.TrimSpace(slug)); !ok {
		return Store{}, AppGrant{}, fmt.Errorf("%s has no app %q", owner, slug)
	}
	return st, AppGrant{App: appKey(owner, strings.TrimSpace(slug)), Write: write, Commands: commands, Pull: pull}, nil
}
