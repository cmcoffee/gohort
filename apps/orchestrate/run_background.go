// Telling work you started from work that started itself.
//
// The live surfaces knew a run's kind all along — chat, scheduled, standing,
// task, dispatch, channel — and every one of them ended up rendered the same
// green. So a pill lighting up while you sat reading said only "something is
// happening", when the thing worth saying is WHICH KIND of something: your own
// turn is expected and needs no announcement; a scheduled fire, an inbound
// message, or a standing agent waking on its own is news.
//
// THE KIND IS DECIDED AT THE ROOT, not per run. A sub-agent dispatched inside a
// chat turn is work you are waiting on, however deep it nests, and marking each
// dispatch background on its own would have an ordinary turn light the pill as
// if the machine had acted unprompted. Walking to the root asks the only
// question that matters: did a person start this, or did it start itself.
package orchestrate

// backgroundRootKinds are the run kinds that begin without anyone watching.
//
// An allowlist of FOREGROUND kinds would be the shorter list, and is exactly
// the wrong shape: a kind added later would default to foreground and quietly
// stop announcing itself, which is the failure this whole surface exists to
// prevent. Unknown kinds are background — the direction where being wrong is
// visible (an extra indigo pill) rather than silent.
func isBackgroundKind(kind string) bool { return kind != "chat" }

// rootKindOf walks a run to its top-level ancestor and returns that kind.
//
// byID must hold every snapshot in the set being rendered. A parent missing
// from it — completed and swept while its child still runs — leaves the walk
// where it stopped, which is the right answer: the deepest run we can actually
// see becomes the root, and a dispatch whose parent has vanished is judged on
// its own kind rather than on a guess.
//
// Bounded by the size of the set, so a cycle in the parent links (which the
// tree ordering already guards against, but which nothing here can assume)
// terminates rather than hanging the poll that feeds the pill.
func rootKindOf(s RunSnapshot, byID map[string]RunSnapshot) string {
	kind, seen := s.Kind, map[string]bool{s.ID: true}
	for cur := s; cur.ParentID != ""; {
		parent, ok := byID[cur.ParentID]
		if !ok || seen[parent.ID] {
			break
		}
		seen[parent.ID] = true
		cur, kind = parent, parent.Kind
	}
	return kind
}

// markBackground answers, for each snapshot, whether it belongs to work that
// started on its own. Returned as a map keyed by run id so callers can look up
// while building rows in their own order.
func markBackground(snaps []RunSnapshot) map[string]bool {
	byID := make(map[string]RunSnapshot, len(snaps))
	for _, s := range snaps {
		byID[s.ID] = s
	}
	out := make(map[string]bool, len(snaps))
	for _, s := range snaps {
		out[s.ID] = isBackgroundKind(rootKindOf(s, byID))
	}
	return out
}

// descendantsOf returns one run and everything beneath it, preserving the order
// it was given — which is already the parent-then-children tree order, so a
// focused view reads the same way the full list did.
//
// An id that names nothing returns nothing rather than everything. A filter
// that silently degrades to "no filter" would show a viewer the whole
// deployment's activity while their URL claimed to be about one run.
func descendantsOf(snaps []RunSnapshot, rootID string) []RunSnapshot {
	if rootID == "" {
		return snaps
	}
	keep := map[string]bool{}
	var out []RunSnapshot
	for _, s := range snaps {
		switch {
		case s.ID == rootID:
			keep[s.ID] = true
		case s.ParentID != "" && keep[s.ParentID]:
			// Parents precede children in tree order, so one forward pass
			// reaches every generation without a second walk.
			keep[s.ID] = true
		default:
			continue
		}
		out = append(out, s)
	}
	return out
}
