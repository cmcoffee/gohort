package orchestrate

// revisions / revert — reading and undoing an app's history.
//
// The listing is deliberately not a column of timestamps. What an author needs
// in order to CHOOSE is the shape of each version: how big the document was
// and how many functions it defined, next to what the current one looks like.
// A wipe is obvious at a glance in those terms (21KB and 14 functions, then
// 6KB and 3) and invisible in a list of dates.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// appDefRevisions lists the versions an app can be rolled back to.
func (t *chatTurn) appDefRevisions(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	spec, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app — check the slug (app_def action=list)")
	}
	revs := ListAppRevisions(t.user, spec.Slug)
	if len(revs) == 0 {
		return fmt.Sprintf("No earlier revisions of %q are kept yet — history starts at its next edit. The version serving now was saved %s (%s).",
			spec.Name, spec.Updated, appRevisionShape(spec)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Revisions of %q, newest first. Restore one with app_def(action=\"revert\", id=%q, to=<the # id>).\n\n", spec.Name, spec.Slug)
	fmt.Fprintf(&b, "  NOW  %s  %s  (serving)\n", spec.Updated, appRevisionShape(spec))
	for _, r := range revs {
		prior, ok := LoadAppRevision(t.user, spec.Slug, strconv.Itoa(r.Seq))
		shape := "unreadable"
		if ok {
			shape = appRevisionShape(prior)
		}
		line := fmt.Sprintf("  #%-3d %s  %s", r.Seq, r.Stamp, shape)
		if age := AppRevisionAge(r.Stamp); age != "" {
			line += "  " + age
		}
		if r.Reason != "" {
			line += "  — replaced by " + r.Reason
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nA version that is much larger than the one serving now is the signal to look at: it means an edit removed a lot of code. Compare with app_def(action=\"get\") before reverting if you're unsure.")
	return b.String(), nil
}

// appDefRevert restores a kept revision.
func (t *chatTurn) appDefRevert(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	current, ok := LoadAppSpec(t.user, key)
	if !ok {
		return "", errors.New("no matching app — check the slug (app_def action=list)")
	}
	revs := ListAppRevisions(t.user, current.Slug)
	if len(revs) == 0 {
		return "", fmt.Errorf("no earlier revisions of %q are kept — nothing to revert to. History begins at the app's next edit", current.Name)
	}

	ref := strings.TrimSpace(firstNonEmptyStr(stringArg(args, "to"), stringArg(args, "stamp"), stringArg(args, "revision"), numArgString(args, "to")))
	target, ok := FindAppRevision(t.user, current.Slug, ref)
	if !ok {
		var ids []string
		for _, r := range revs {
			ids = append(ids, "#"+strconv.Itoa(r.Seq))
		}
		return "", fmt.Errorf("no kept revision of %q matches %q. It keeps: %s — list them with app_def(action=\"revisions\", id=%q)",
			current.Name, ref, strings.Join(ids, ", "), current.Slug)
	}
	prior, ok := LoadAppRevision(t.user, current.Slug, strconv.Itoa(target.Seq))
	if !ok {
		return "", fmt.Errorf("revision #%d of %q could not be read back — it may have been stored by an older version. Try another (app_def action=\"revisions\")", target.Seq, current.Name)
	}

	// Restore the DOCUMENT, not the deployment. Whether an app is disabled,
	// shared or published is a property of this deployment right now, not of
	// the version being restored — an author undoing a bad edit is not asking
	// to un-share the app or revoke its public link.
	restored := prior
	restored.Disabled = current.Disabled
	restored.Shared = current.Shared
	restored.PublicToken = current.PublicToken
	restored.Created = current.Created

	// The version being replaced goes into history like any other edit, so a
	// revert is itself revertible — an author who reverts to the wrong one is
	// not stuck there.
	saved := SaveAppSpecAs(restored, fmt.Sprintf("revert to #%d", target.Seq))
	undo := ""
	if kept := ListAppRevisions(t.user, saved.Slug); len(kept) > 0 {
		undo = fmt.Sprintf("\n\nThe version you just replaced is itself kept as #%d, so this is undoable: app_def(action=\"revert\", id=%q, to=%d).", kept[0].Seq, saved.Slug, kept[0].Seq)
	}

	msg := fmt.Sprintf("Reverted %q to revision #%d (saved %s; now serving as revision %s). The document is %s; it was %s.",
		saved.Name, target.Seq, target.Stamp, saved.Updated, appRevisionShape(saved), appRevisionShape(current))
	if len(appSpecHTMLText(saved)) > 0 {
		if errs := appPageRuntimeErrors(t.user, saved.Slug); len(errs) > 0 {
			return msg + fmt.Sprintf("\n\nHEADS UP — the restored revision has problems of its own in a real browser:\n- %s\n\nIt is serving; revert again to a different revision (app_def action=\"revisions\") if this one isn't the good copy.", strings.Join(errs, "\n- ")) + undo, nil
		}
		msg += " It was loaded in a real browser after restoring and came up clean."
	}
	return msg + undo, nil
}

// numArgString renders a numeric argument as a string, so to=3 works the same
// as to="3" — a model that reads "#3" in a listing may send either.
func numArgString(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case float64:
		return strconv.Itoa(int(v))
	case int:
		return strconv.Itoa(v)
	}
	return ""
}

// appRevisionShape describes a version in the terms that make a wipe visible:
// how much document there is and how much of it is code.
func appRevisionShape(spec AppSpec) string {
	html := appSpecHTMLText(spec)
	if html == "" {
		return fmt.Sprintf("%d sections, no html", countSpecSections(spec))
	}
	return fmt.Sprintf("%s html, %d functions", appHumanBytes(len(html)), len(jsDefinedFunctions(html)))
}

// appHumanBytes renders a size the way an author skims it.
func appHumanBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fKB", float64(n)/1024)
}
