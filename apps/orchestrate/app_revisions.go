package orchestrate

// revisions / revert — reading and undoing an app's history.
//
// The listing is deliberately not a column of timestamps. What an author needs
// in order to CHOOSE is the shape of each version: how big the document was
// and how many functions it defined, next to what the current one looks like.
// A wipe is obvious at a glance in those terms (21KB and 14 functions, then
// 6KB and 3) and invisible in a list of dates.

import (
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
		return "", appNotFound(args, "")
	}
	revs := ListAppRevisions(t.user, spec.Slug)
	// With git, the versions are its history's (app_history_view.go), and
	// the revisions kept before it began are listed after them.
	history, herr := AppHistoryLog(spec.Owner, spec.Slug, 50)
	if herr == nil && len(history) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "Versions of %q in its history, newest first. Go back to one with app_def(action=\"revert\", id=%q, to=<its hash>): the app as that version had it, a new change of its own.\n\n", spec.Name, spec.Slug)
		for _, c := range history {
			who := c.Author
			if c.Agent != "" {
				who += " with " + c.Agent
			}
			line := fmt.Sprintf("  %s  %s  %s  %q", c.Short, c.When, who, c.Subject)
			if c.Live {
				line += "  (live)"
			}
			b.WriteString(line + "\n")
		}
		if len(revs) > 0 {
			fmt.Fprintf(&b, "\nOlder revisions, kept before its history began (to=<the # id>): %d, the newest saved %s.\n", len(revs), revs[0].Stamp)
		}
		if t.hasAppDraft(spec.Owner, spec.Slug) {
			b.WriteString("\nYour draft holds changes not in the history yet: they go in with your commit.\n")
		}
		return b.String(), nil
	}
	if len(revs) == 0 {
		return fmt.Sprintf("No earlier revisions of %q are kept yet: history starts at its next edit. The version serving now was saved %s (%s).",
			spec.Name, spec.Updated, appRevisionShape(spec)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Revisions of %q, newest first. Restore one with app_def(action=\"revert\", id=%q, to=<the # id>).\n\n", spec.Name, spec.Slug)
	now := fmt.Sprintf("  NOW  %s  %s  (serving)", spec.Updated, appRevisionShape(spec))
	if n := revisionSubject(spec.ChangeNote); n != "" {
		now += "  " + strconv.Quote(n)
	}
	b.WriteString(now + "\n")
	for _, r := range revs {
		prior, ok := LoadAppRevision(t.user, spec.Slug, strconv.Itoa(r.Seq))
		shape := "unreadable"
		note := ""
		if ok {
			shape = appRevisionShape(prior)
			note = revisionSubject(prior.ChangeNote)
		}
		line := fmt.Sprintf("  #%-3d %s  %s", r.Seq, r.Stamp, shape)
		if age := AppRevisionAge(r.Stamp); age != "" {
			line += "  " + age
		}
		// The version's own note first (why it was made), then what replaced it.
		if note != "" {
			line += "  " + strconv.Quote(note)
		}
		if r.Reason != "" {
			line += ", replaced by " + r.Reason
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nStatus now: " + spec.VerifyStatus() + ".\n")
	b.WriteString("\nA version that is much larger than the one serving now is the signal to look at: it means an edit removed a lot of code. Compare with app_def(action=\"get\") before reverting if you're unsure.")
	return b.String(), nil
}

// revisionSubject is a revision note's first line, for the list: a note is
// written as a commit message, a subject and then why, and the why is in get.
func revisionSubject(note string) string {
	note = strings.TrimSpace(note)
	subject, rest, _ := strings.Cut(note, "\n")
	subject = strings.TrimSpace(subject)
	if strings.TrimSpace(rest) != "" {
		subject += " (more in get)"
	}
	return subject
}

// appDefRevert restores a kept revision.
func (t *chatTurn) appDefRevert(args map[string]any) (string, error) {
	key := slugify(firstNonEmptyStr(stringArg(args, "id"), stringArg(args, "slug"), stringArg(args, "name")))
	current, _, ok := t.appForTurn(key)
	if !ok {
		return "", appNotFound(args, "")
	}
	revs := ListAppRevisions(t.user, current.Slug)
	ref := strings.TrimSpace(firstNonEmptyStr(stringArg(args, "to"), stringArg(args, "stamp"), stringArg(args, "revision"), numArgString(args, "to")))
	target, ok := FindAppRevision(t.user, current.Slug, ref)
	// A version in the app's history, by its hash (app_history_view.go).
	if !ok && appCommitHash.MatchString(strings.ToLower(ref)) {
		if _, herr := appHistoryDir(current.Owner, current.Slug); herr == nil {
			return t.revertToVersion(current, ref)
		}
	}
	if len(revs) == 0 {
		return "", fmt.Errorf("no earlier revisions of %q are kept: nothing to revert to. History begins at the app's next edit", current.Name)
	}
	if !ok {
		var ids []string
		for _, r := range revs {
			ids = append(ids, "#"+strconv.Itoa(r.Seq))
		}
		return "", fmt.Errorf("no kept revision of %q matches %q. It keeps: %s, list them with app_def(action=\"revisions\", id=%q)",
			current.Name, ref, strings.Join(ids, ", "), current.Slug)
	}
	prior, ok := LoadAppRevision(t.user, current.Slug, strconv.Itoa(target.Seq))
	if !ok {
		return "", fmt.Errorf("revision #%d of %q could not be read back: it may have been stored by an older version. Try another (app_def action=\"revisions\")", target.Seq, current.Name)
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
	// A verdict belongs to the revision it checked; the restored document
	// carries the note of THIS edit, not the one it was written with.
	restored.Verify = nil
	restored.ChangeNote = strings.TrimSpace(stringArg(args, "note"))
	if restored.ChangeNote == "" {
		restored.ChangeNote = fmt.Sprintf("reverted to #%d", target.Seq)
	}

	// The version being replaced goes into history like any other edit, so a
	// revert is itself revertible — an author who reverts to the wrong one is
	// not stuck there.
	if msg := t.appDelegationRefusal(restored); msg != "" {
		return "", fmt.Errorf("%s", msg)
	}
	saved := t.saveAppEdit(restored, fmt.Sprintf("revert to #%d", target.Seq))
	undo := ""
	if t.hasAppDraft(saved.Owner, saved.Slug) {
		undo = t.appDraftNote(saved)
	} else if kept := ListAppRevisions(t.user, saved.Slug); len(kept) > 0 {
		undo = fmt.Sprintf("\n\nThe version you just replaced is itself kept as #%d, so this is undoable: app_def(action=\"revert\", id=%q, to=%d).", kept[0].Seq, saved.Slug, kept[0].Seq)
	}

	msg := fmt.Sprintf("Reverted %q to revision #%d (saved %s; now serving as revision %s). The document is %s; it was %s.",
		saved.Name, target.Seq, target.Stamp, saved.Updated, appRevisionShape(saved), appRevisionShape(current))
	if len(appSpecHTMLText(saved)) > 0 {
		errs, checked := t.appPageErrors(saved)
		if len(errs) > 0 {
			return msg + fmt.Sprintf("\n\nHEADS UP, the restored revision has problems of its own in a real browser:\n- %s\n\nIt is serving; revert again to a different revision (app_def action=\"revisions\") if this one isn't the good copy.", strings.Join(errs, "\n- ")) + undo, nil
		}
		if checked {
			t.recordVerdict(saved, true, "PASS: loaded in a real browser after the revert, no JS errors")
			msg += " It was loaded in a real browser after restoring and came up clean."
		} else {
			msg += " No browser check could run on it: run app_def(action=\"verify\") before telling the user it works."
		}
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
