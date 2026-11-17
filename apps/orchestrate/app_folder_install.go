package orchestrate

// An app folder installed, or built for a preview, without a Builder chat
// (docs/design/apps-as-repositories.md). Anvil's Install and Preview work on
// an app's folder for the person who pressed them; there is no chat turn, no
// agent making the change, and no card to approve it on. Both go through the
// same app_def create or update Builder's publish runs, so every check and
// gate an app has applies however the change arrives: the refusals, the
// script checks, the shared-app approval (SaveAppSpecAs), and the page
// checks that put the previous version back.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// personTurn is the turn a person's own Install or Preview runs on: their
// store and name, and nothing of a chat. Every chat-only part of the app_def
// path reads absent as "not in a chat": no app-building session, no agent
// whose edits are limited, no session to note unrun actions against.
func (T *OrchestrateApp) personTurn(ctx context.Context, user string) *chatTurn {
	if ctx == nil {
		ctx = context.Background()
	}
	return &chatTurn{app: T, user: user, udb: UserDB(T.DB, user), ctx: ctx}
}

// InstallAppFolder installs the app in the folder dir as user: their own
// app, created or updated. It is refused when the live app changed since the
// folder was written or last installed (the folder would undo it), and by
// any check or gate a Builder publish would be refused by. The person
// pressing Install is the approval of the change. note goes in the app's
// history.
func (T *OrchestrateApp) InstallAppFolder(ctx context.Context, user, dir, note string) (string, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return "", fmt.Errorf("no one to install it as")
	}
	return T.personTurn(ctx, user).publishAppFolder(dir, filepath.Base(dir), folderPublish{note: note})
}

// BuildAppFolder builds the app in the folder dir as it would be installed
// for user, and saves nothing: what a preview serves. An update starts from
// the live app, as an install would, so what the folder leaves out the live
// app still has.
func (T *OrchestrateApp) BuildAppFolder(ctx context.Context, user, dir string) (AppSpec, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return AppSpec{}, fmt.Errorf("no one to build it for")
	}
	t := T.personTurn(ctx, user)
	in, m, err := t.appFolderRead(dir, filepath.Base(dir))
	if err != nil {
		return AppSpec{}, err
	}
	_, exists := LoadAppSpec(user, m.Slug)
	if exists {
		in["id"] = m.Slug
	}
	aw := &appWrite{t: t, args: in, isUpdate: exists}
	if !aw.load() || !aw.readMeta() || !aw.readParts() {
		if aw.retErr != nil {
			return AppSpec{}, aw.retErr
		}
		return AppSpec{}, fmt.Errorf("%s", strings.TrimSpace(aw.ret))
	}
	if msg := t.appDelegationRefusal(aw.spec); msg != "" {
		return AppSpec{}, fmt.Errorf("%s", msg)
	}
	spec := aw.spec
	spec.Owner = user
	return spec, nil
}

// WriteAppFolder writes user's app slug into dir as its folder (app.json,
// page, scripts, shared modules, assets, notes, checks), marked as holding
// the live app as it is, so an Install of it is not taken for undoing a
// change made elsewhere. It is user's own app; dir is made if it is missing.
func WriteAppFolder(user, slug, dir string) error {
	spec, ok := LoadAppSpec(strings.TrimSpace(user), strings.TrimSpace(slug))
	if !ok {
		return fmt.Errorf("you have no app %q", slug)
	}
	assets := map[string][]byte{}
	if names, err := ListAppAssets(spec.Owner, spec.Slug); err == nil {
		for _, n := range names {
			if data, _, err := ReadAppAsset(spec.Owner, spec.Slug, n); err == nil {
				assets[n] = data
			}
		}
	}
	_, err := writeAppFolder(dir, spec, assets, appLiveFingerprint(spec))
	return err
}
