// What the agent is told about the project before it does anything.
//
// The prompt used to be entirely static: a set of working rules and nothing
// about the tree they applied to. So every session began by rediscovering
// facts that never change — what this project is called, which language it is
// in, whether it is under git — at the cost of several tool calls and a good
// deal of context, and the conventions written down IN the repo were read only
// if the model happened to think of it.
//
// That last part is the expensive one. A project's CLAUDE.md exists precisely
// to tell whoever is editing how the codebase works, and a coding assistant
// that has to be ASKED to read it will confidently write code in the house
// style of somewhere else. Claude Code loads it automatically; there is no
// reason this should not.
//
// Everything here is derived fresh per turn rather than stored. A branch
// changes, a file count changes, CLAUDE.md gets edited — a cached snapshot of
// any of that is worse than none, because it is wrong without looking wrong.
package anvil

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/apps/customapps"
	"github.com/cmcoffee/gohort/core/appcheck"
)

// instructionFiles are read into the prompt when present, in this order. The
// first one found wins: a repo carrying both is not asking for both to be
// obeyed, it is mid-migration between two conventions.
var instructionFiles = []string{"CLAUDE.md", "AGENTS.md", "CONVENTIONS.md"}

// maxInstructionBytes caps what one of those files may contribute. Generous —
// gohort's own is a few kilobytes — but bounded, because an unbounded read
// here spends the context the actual task needs, and a truncated instruction
// file that SAYS it was truncated is recoverable where a blown window is not.
const maxInstructionBytes = 16 << 10

// projectContext renders the block appended to the agent's standing prompt.
func (T *Anvil) projectContext(p Project, base string) string {
	var b strings.Builder
	b.WriteString("\n\n## The project you are working in\n\n")
	fmt.Fprintf(&b, "Name: %s\n", p.Name)
	fmt.Fprintf(&b, "Path: %s\n", base)
	switch {
	case p.Kind == KindClone && p.Source == sourceNew:
		b.WriteString("A repository its owner started in Anvil. Your changes reach nobody else until the owner commits and publishes them, which only they do: you do not commit or push.\n")
	case p.Kind == KindClone && p.Source == sourceBundle:
		b.WriteString("A checkout made from a git bundle its owner brought. Your changes reach nobody else until the owner commits and publishes them, which only they do: you do not commit or push.\n")
	case p.Kind == KindClone && p.Source == sourceApp:
		fmt.Fprintf(&b, "The files of the owner's app %s. Your changes reach nobody until the owner installs them.\n", p.AppSlug)
		// The page's whole backend is window.app. Unsaid, the agent searched
		// the web for it and guessed a call that does not exist.
		b.WriteString("Its page reaches its backend only through window.app, which offers exactly these calls (nothing else exists, so never search for more):\n\n" + customapps.PageAPI + "\n\n" +
			"localStorage in the page is already the person's own, kept on the server for each person and app, so it follows them to every device: use it for where they were (a tab, a filter, recent picks), and records for the app's data.\n")
		// Unsaid, asked about the header's Reset view, the assistant took it
		// for the app's own and wrote code that emptied the person's stored
		// records whenever their view looked reset.
		b.WriteString("The bar above the page (the app's name, Reset view, the link to change it) is the framework's, not the app's: nothing in these files draws it or can remove it. Reset view clears where the person was in the app (its localStorage) and nothing else. When asked to change something there, say it is the framework's rather than working around it in the page.\n")
		// Unsaid, one assistant wrote its own runner for checks.json and
		// another spent its turn looking for an install command.
		b.WriteString("app_guide says how the folder is built (app.json, data/, actions/, lib/, how a script reads its records and what it prints): read it before writing a script.\n")
		b.WriteString("Its checks are in checks.json; run them with app_checks after a change and before you say the work is done, and add one for each thing you build. The format:\n\n" + appcheck.Reference + "\n\n")
		if p.InstallOnSave {
			b.WriteString("The owner turned on Install on every save: when your turn ends with changes, they are installed through the same checks, and a refused install comes back to you to fix. There is no install command to run.\n")
		} else {
			b.WriteString("Installing is the owner's: the Install button on this project's bar puts your changes live, through the same checks. There is no install command or CLI to find, so do not look for one; say what you changed and that it is ready to install.\n")
		}
	case p.Kind == KindClone:
		fmt.Fprintf(&b, "A checkout Anvil made of %s. Changes here affect nobody else until you push them, which Anvil does not do.\n", p.RepoURL)
	default:
		b.WriteString("A directory on this host, registered by an operator. Edits here change the files somebody is actually using; there is no copy.\n")
	}
	// A project brought another way that holds an app's folder. Unsaid, the
	// assistant has no way to know the folder is a gohort app, what its page
	// may call, or how it gets installed.
	if p.Kind == KindClone && p.Source != sourceApp && holdsAppFolder(base) {
		b.WriteString("It holds a gohort app's folder (app.json at its top). It becomes one of the owner's apps only when they press Install as a new app (on this project's bar, or in its Project menu), through the checks every install runs; there is no install command to find. Its page reaches its backend only through window.app, which offers exactly these calls:\n\n" + customapps.PageAPI + "\n\napp_guide says how the rest of the folder is built (app.json, its scripts, checks.json): read it before writing them.\n")
	}
	// Searches leave the deployment, and nothing else tells the agent that
	// this work is not public (searchPrivateTerms is what they are checked
	// against).
	b.WriteString("Web searches go to an outside search engine. Search for the general concepts this work uses (the language, a library, the technique, an error message with this project's names taken out), never this project's own names, its ticket or issue IDs, its hosts, or the details of a flaw being fixed here.\n")

	if info := inspectRepo(base); info.IsRepo {
		where := info.Branch
		if where == "" {
			where = "a detached head at " + info.Head
		} else {
			where = "branch " + where
		}
		state := "clean"
		if info.Dirty {
			state = fmt.Sprintf("%d uncommitted change(s)", info.Changed)
		}
		fmt.Fprintf(&b, "Git: %s, %s.", where, state)
		if info.Prefix != "" {
			fmt.Fprintf(&b, " This project is the %s/ subdirectory of a larger repository, so git answers are scoped to it.", info.Prefix)
		}
		if isShallowRepo(base) {
			b.WriteString(" The clone is shallow: there is no history to read, and blame is unavailable.")
		}
		b.WriteString("\n")
	} else {
		b.WriteString("Git: this project is not under version control, so there is no history and no way to see a diff of what you change. The edit journal is the only undo.\n")
	}

	if langs := detectLanguages(base); langs != "" {
		fmt.Fprintf(&b, "Mostly %s.\n", langs)
	}

	// Name the operator's commands. They are already tools, but a tool the
	// model has to notice in a catalog is not the same as being told the
	// project HAS a build — which is what decides whether it checks its work.
	if acts := p.agentInvokableActions(); len(acts) > 0 {
		var names []string
		for _, a := range acts {
			names = append(names, a.Label)
		}
		fmt.Fprintf(&b, "Commands available to you: %s. Use them to check your work.\n", strings.Join(names, ", "))
	}

	if cmd := strings.TrimSpace(p.CheckCommand); cmd != "" {
		if p.CheckManual {
			fmt.Fprintf(&b, "This project's check is `%s` (its build and tests). Run it with the check tool after a change and before you say the work is done.\n", cmd)
		} else {
			fmt.Fprintf(&b, "This project's check is `%s` (its build and tests). Run it with the check tool as you go. After you change files, Anvil runs it before your turn ends, and a failing check sends your reply back.\n", cmd)
		}
	}

	if name, body, ok := readInstructionFile(base); ok {
		fmt.Fprintf(&b, "\n## %s: this project's own instructions\n\n"+
			"These are the project's rules for whoever edits it. They OUTRANK your general habits: where they and your usual practice disagree, follow them and say so.\n\n%s\n", name, body)
	}
	if notes := strings.TrimSpace(p.Instructions); notes != "" {
		b.WriteString("\n## Notes from the operator\n\n" +
			"Set on this project specifically, and not necessarily written down in the repository.\n\n" + notes + "\n")
	}
	return b.String()
}

// readInstructionFile returns the first instruction file present at the
// project root, truncated if oversized.
//
// Root only, not a walk. A nested CLAUDE.md governs its own subtree and
// pulling every one of them into the prompt would both blow the budget and
// apply a subdirectory's rules to the whole project.
func readInstructionFile(base string) (name, body string, ok bool) {
	for _, candidate := range instructionFiles {
		full := filepath.Join(base, candidate)
		st, err := os.Stat(full)
		if err != nil || st.IsDir() {
			continue
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		text := string(raw)
		if len(text) > maxInstructionBytes {
			text = text[:maxInstructionBytes] +
				"\n\n...(truncated; read " + candidate + " directly if you need the rest)"
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		return candidate, text, true
	}
	return "", "", false
}

// detectLanguages summarizes the project's language mix by counting source
// files, so the agent knows what it is looking at before it opens anything.
//
// Counted rather than guessed from a manifest: a repository with a go.mod and
// forty thousand lines of TypeScript is a TypeScript project that happens to
// ship a Go tool, and the manifest would have said Go.
func detectLanguages(base string) string {
	byExt := map[string]int{}
	names := map[string]string{
		".go": "Go", ".py": "Python", ".js": "JavaScript", ".ts": "TypeScript",
		".tsx": "TypeScript", ".jsx": "JavaScript", ".rs": "Rust", ".java": "Java",
		".rb": "Ruby", ".php": "PHP", ".c": "C", ".h": "C", ".cc": "C++",
		".cpp": "C++", ".cs": "C#", ".swift": "Swift", ".kt": "Kotlin",
		".sh": "shell", ".sql": "SQL", ".html": "HTML", ".css": "CSS",
	}
	seen := 0
	_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || seen > 20000 {
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] && path != base {
				return filepath.SkipDir
			}
			return nil
		}
		if lang, ok := names[strings.ToLower(filepath.Ext(path))]; ok {
			byExt[lang]++
			seen++
		}
		return nil
	})
	if len(byExt) == 0 {
		return ""
	}
	type pair struct {
		lang string
		n    int
	}
	var all []pair
	total := 0
	for l, n := range byExt {
		all = append(all, pair{l, n})
		total += n
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].lang < all[j].lang
	})
	var out []string
	for i, p := range all {
		// Three at most, and nothing under a twentieth of the tree: a listing
		// that includes every language present describes no project.
		if i >= 3 || p.n*20 < total {
			break
		}
		out = append(out, fmt.Sprintf("%s (%d files)", p.lang, p.n))
	}
	return strings.Join(out, ", ")
}

// publicForges are hosts whose name says nothing about whose code it is, so
// a repository there is named by its path alone.
var publicForges = map[string]bool{
	"github.com": true, "gitlab.com": true, "bitbucket.org": true,
	"codeberg.org": true, "git.sr.ht": true,
}

// searchPrivateTerms are the names that identify this project, which its
// turns' web searches are checked against (outbound.WithPrivateTerms): its name,
// its app, its repository and that repository's host where the host is not a
// public forge, the folder a directory project lives in. Over-inclusive on
// purpose: a public repository's name is no secret, but Anvil cannot tell
// which a repository is, and a search for the concept rather than the name
// loses nothing.
func searchPrivateTerms(p Project) []string {
	terms := []string{p.Name, p.AppSlug}
	for _, raw := range []string{p.RepoURL, p.PushURL} {
		host, path := repoHostPath(raw)
		if path != "" {
			terms = append(terms, path, filepath.Base(path))
		}
		if host != "" && !publicForges[host] {
			terms = append(terms, host)
		}
	}
	if p.Kind == KindRoot && strings.TrimSpace(p.Root) != "" {
		terms = append(terms, filepath.Base(filepath.Clean(p.Root)))
	}
	var out []string
	for _, t := range terms {
		if t = strings.TrimSpace(t); t != "" && t != "." && t != "/" {
			out = append(out, t)
		}
	}
	return out
}

// repoHostPath splits a repository address into its host and its path
// ("owner/name", without .git), for https and scp-style (git@host:owner/name)
// addresses alike. A local path has no host.
func repoHostPath(raw string) (host, path string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if !strings.Contains(raw, "://") {
		if at := strings.Index(raw, "@"); at >= 0 {
			if colon := strings.Index(raw[at:], ":"); colon > 0 {
				host = raw[at+1 : at+colon]
				path = raw[at+colon+1:]
			}
		}
	} else if u, err := url.Parse(raw); err == nil {
		host = u.Hostname()
		path = u.Path
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	return strings.ToLower(host), path
}
