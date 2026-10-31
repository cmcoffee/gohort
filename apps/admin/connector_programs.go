package admin

// The programs System Hooks' jobs run (pandoc, ffmpeg, yt-dlp) are
// connectors too: inside, the jobs a program does; outside, the program on
// this machine and whether it has the network; its command lines, its
// endpoints. The rows are read from the jobs as they are defined, so nothing
// stored moves: a program is set up, adapted and tested from System Hooks.

import (
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/gohort/core/sandbox"
	"github.com/cmcoffee/gohort/core/service"
)

// programPrefix starts a program's row name: a colon no stored connector's
// name can hold, as for the built-in providers.
const programPrefix = "program:"

// programUse is one way a job is done with a program.
type programUse struct {
	job flows.Job
	// steps are the command lines of the flow the program is in, or nil for
	// a job gohort runs itself, driving the program.
	steps []string
	// with are the flow's other programs.
	with []string
	// chosen is whether this is the way the job is done here now.
	chosen  bool
	install string
}

// programConnector is one program's row.
type programConnector struct {
	name    string // program:<program>
	program string
	wrapper string
	status  string
	uses    []programUse
}

// programJobs are the jobs whose programs are listed: every job but those a
// test registers.
func programJobs() []flows.Job {
	var out []flows.Job
	for _, j := range flows.Jobs() {
		if !strings.HasPrefix(j.ID, "test.") {
			out = append(out, j)
		}
	}
	return out
}

// programConnectors are the programs jobs run, by name. A program not
// installed is listed only while a job it would do has no way to run here,
// so the list says what to install rather than every alternative there is.
func programConnectors(jobs []flows.Job, installed func(string) bool, refusing bool) []programConnector {
	uses := map[string][]programUse{}
	for _, j := range jobs {
		if j.Builtin {
			ready := len(j.Uses) > 0
			for _, p := range j.Uses {
				ready = ready && installed(p)
			}
			for _, p := range j.Uses {
				uses[p] = append(uses[p], programUse{job: j, with: others(j.Uses, p), chosen: ready, install: j.InstallHint()})
			}
			continue
		}
		chosen := -1
		for i, f := range j.Native {
			ok := true
			for _, p := range f.Programs() {
				ok = ok && installed(p)
			}
			if ok {
				chosen = i
				break
			}
		}
		for i, f := range j.Native {
			for _, p := range f.Programs() {
				uses[p] = append(uses[p], programUse{job: j, steps: f.Steps, with: others(f.Programs(), p), chosen: i == chosen, install: f.Install})
			}
		}
	}
	// A job with no way to run here: its programs are what to install.
	stuck := map[string]bool{}
	for _, us := range uses {
		for _, u := range us {
			if u.chosen {
				stuck[u.job.ID] = false
			} else if _, seen := stuck[u.job.ID]; !seen {
				stuck[u.job.ID] = true
			}
		}
	}
	names := make([]string, 0, len(uses))
	for p := range uses {
		names = append(names, p)
	}
	sort.Strings(names)
	var out []programConnector
	for _, p := range names {
		has := installed(p)
		if !has {
			needed := false
			for _, u := range uses[p] {
				needed = needed || stuck[u.job.ID]
			}
			if !needed {
				continue
			}
		}
		pc := programConnector{name: programPrefix + p, program: p, uses: uses[p]}
		pc.wrapper = programWrapper(p, uses[p])
		pc.status = programStatus(p, has, uses[p], refusing)
		out = append(out, pc)
	}
	return out
}

func others(list []string, p string) []string {
	var out []string
	for _, x := range list {
		if x != p {
			out = append(out, x)
		}
	}
	return out
}

// jobGives is a job as the inside of a program's line: what it does. A
// job's title names a format in the reading and writing groups ("PDF
// attachments", "Word (.docx)") and the work itself in the others ("Sound
// from video").
func jobGives(j flows.Job) string {
	switch j.Group {
	case flows.GroupWriting:
		return "Writes " + j.Title
	case flows.GroupReading:
		return "Reads " + j.Title
	}
	return j.Title
}

// programWrapper is a program's line, read inside to outside: the jobs it
// does, then the program on this machine and whether it has the network.
// Past three jobs it names two and counts the rest.
func programWrapper(p string, uses []programUse) string {
	var gives []string
	network := ""
	for _, u := range uses {
		g := jobGives(u.job)
		if !slices.Contains(gives, g) {
			gives = append(gives, g)
		}
		if u.job.Network {
			network = "network for " + u.job.Title
		}
	}
	inside := strings.Join(gives, "; ")
	if len(gives) > 3 {
		inside = fmt.Sprintf("%s; %s; and %d more", gives[0], gives[1], len(gives)-2)
	}
	if network == "" {
		network = "no network"
	}
	return fmt.Sprintf("%s ⇐ %s on this machine (%s)", inside, p, network)
}

// programStatus says whether the program is here, what it does now and what
// it stands by for, and where it is set up.
func programStatus(p string, has bool, uses []programUse, refusing bool) string {
	var now, standby []string
	for _, u := range uses {
		g := jobGives(u.job)
		if u.chosen {
			if !slices.Contains(now, g) {
				now = append(now, g)
			}
		} else if !slices.Contains(standby, g) {
			standby = append(standby, g)
		}
	}
	var parts []string
	switch {
	case !has:
		hint := ""
		for _, u := range uses {
			if u.install != "" {
				hint = u.install
				break
			}
		}
		parts = append(parts, "Not installed. Install "+hint+" for: "+strings.Join(standby, "; ")+".")
	default:
		where := p
		if path, err := exec.LookPath(p); err == nil {
			where = path
		}
		parts = append(parts, "Installed at "+where+".")
		if len(now) > 0 {
			parts = append(parts, "Does: "+strings.Join(now, "; ")+".")
		}
		if len(standby) > 0 {
			parts = append(parts, "Standing by for: "+strings.Join(standby, "; ")+".")
		}
	}
	if refusing {
		parts = append(parts, "Nothing runs: the sandbox is not available on this machine.")
	}
	parts = append(parts, "Set up under Admin > Capabilities > System Hooks.")
	return strings.Join(parts, " ")
}

// programEndpoints are a program's command lines: each job's, with what it
// is given and what comes of it.
func programEndpoints(pc programConnector) []service.Endpoint {
	var out []service.Endpoint
	for _, u := range pc.uses {
		e := service.Endpoint{Name: u.job.ID, Address: strings.Join(u.steps, "  ⇒  "), UsedBy: jobGives(u.job)}
		if u.steps == nil {
			e.Address = "run by gohort, driving " + strings.Join(append([]string{pc.program}, u.with...), " and ")
		}
		if len(u.with) > 0 && u.steps != nil {
			e.UsedBy += ", with " + strings.Join(u.with, " and ")
		}
		if !u.chosen {
			e.UsedBy += " (standing by)"
		}
		e.Sends, e.Reads = jobSends(u.job), jobReads(u.job)
		out = append(out, e)
	}
	return out
}

func jobSends(j flows.Job) string {
	var parts []string
	switch {
	case j.IsExport():
		if len(j.Takes) > 0 {
			parts = append(parts, "{in} ← the document, as "+flows.FormatLabel(j.Takes[0]))
		}
	case j.TakesFile():
		var formats []string
		for _, t := range j.Takes {
			formats = append(formats, flows.FormatLabel(t))
		}
		if len(formats) > 3 {
			formats = append(formats[:3], "others")
		}
		parts = append(parts, "{in} ← the file ("+strings.Join(formats, ", ")+")")
	}
	for _, p := range j.Params {
		parts = append(parts, "{"+p.Name+"}")
	}
	return strings.Join(parts, "; ")
}

func jobReads(j flows.Job) string {
	switch {
	case j.IsExport():
		return "{out} → a ." + j.Gives + " file"
	case j.HandsOff():
		return "{out} → " + flows.FormatLabel(j.Then) + ", handed on"
	case j.Files:
		return "files in {outdir} → each " + j.Becomes.Label()
	case j.Becomes != "":
		return "{out} → " + j.Becomes.Label()
	}
	return ""
}

// programRows are the programs' rows as they are now.
func programRows() []programConnector {
	return programConnectors(programJobs(), flows.Installed, sandbox.GetSandboxStatus().Refusing)
}

// programByName is the row named name (program:<program>).
func programByName(name string) (programConnector, bool) {
	for _, pc := range programRows() {
		if pc.name == name {
			return pc, true
		}
	}
	return programConnector{}, false
}
