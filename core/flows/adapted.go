package flows

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ConnectorKind is the connector kind an adapted flow is saved as. The name
// is from when these were only file conversions; saved ones keep loading.
const ConnectorKind = "file_conversion"

// Any, as an adapted flow's Takes, means any input format no other flow for
// that kind takes: a catch-all converter.
const Any = "*"

// Adapted is an admin's own flow: the connector's spec. It fills a job of
// gohort's (Job), or adds a format gohort does not support, as a job of its
// own: an input that Takes formats and Becomes a kind, or hands its result
// on (Then) as a format something here already reads; or an export that
// Gives a format from the document as text/markdown or text/html.
type Adapted struct {
	Job     string   `json:"job,omitempty"`
	Takes   []string `json:"takes,omitempty"`
	Becomes Kind     `json:"becomes,omitempty"`
	// Then hands the result on as this format, for whatever reads it here
	// (gohort's own reader, or another converter) to finish: Pages to PDF,
	// then PDF to text by gohort. An input either Becomes a kind or Then.
	Then  string   `json:"then,omitempty"`
	Gives string   `json:"gives,omitempty"`
	Steps []string `json:"steps,omitempty"`
	// Command is a one-step flow, as saved before flows had steps.
	Command string `json:"command,omitempty"`
	// Reads are folders the flow may also read (a model file a transcriber
	// needs, say), read-only.
	Reads       []string `json:"reads,omitempty"`
	TimeoutSecs int      `json:"timeout_secs,omitempty"`
	// Prefer runs it before the native flow, for an admin who wants their
	// tool used even where the native one is installed.
	Prefer bool `json:"prefer,omitempty"`
	// Service, in place of steps, is a connector that converts files
	// (core/service, file.convert): the job's file is sent to it, asked for
	// the format the job gives, and what comes back is checked by the job
	// as a command's result is.
	Service string `json:"service,omitempty"`
}

// Flow is a's steps.
func (a Adapted) Flow() []string {
	if len(a.Steps) > 0 {
		return a.Steps
	}
	if strings.TrimSpace(a.Command) != "" {
		return []string{a.Command}
	}
	return nil
}

// Contract is the job a fills: gohort's, narrowed to the formats a names,
// or the one a defines itself.
func (a Adapted) Contract() (Job, error) {
	if a.Job != "" {
		j, ok := JobByID(a.Job)
		if !ok {
			return Job{}, fmt.Errorf("there is no job %q", a.Job)
		}
		if j.TakesFile() && len(a.Takes) > 0 && !j.IsExport() {
			j.Takes = a.Takes
		}
		return j, nil
	}
	if a.Gives != "" {
		return Job{ID: "export." + a.Gives, Group: GroupWriting, Title: OutLabel(a.Gives), Takes: a.Takes, Gives: a.Gives}, nil
	}
	if a.Then != "" {
		return Job{Group: GroupReading, Title: FormatsLabel(a.Takes), Takes: a.Takes, Then: a.Then}, nil
	}
	return Job{Group: GroupReading, Title: FormatsLabel(a.Takes), Takes: a.Takes, Becomes: a.Becomes}, nil
}

// IsOutput reports whether a writes a document as a format.
func (a Adapted) IsOutput() bool {
	j, err := a.Contract()
	return err == nil && j.IsExport()
}

// Timeouts: an adapted flow's default, and the most one may ask for.
const (
	defaultTimeout = time.Minute
	maxTimeout     = 30 * time.Minute
)

// Timeout is how long a run of a may take.
func (a Adapted) Timeout() time.Duration {
	if a.TimeoutSecs <= 0 {
		return defaultTimeout
	}
	if d := time.Duration(a.TimeoutSecs) * time.Second; d < maxTimeout {
		return d
	}
	return maxTimeout
}

// Validate checks a against its job: a contract that makes sense, folders
// it may read, a time limit in range, and steps that keep the contract.
func (a Adapted) Validate() error {
	if a.Job == "" {
		switch {
		case a.Gives != "" && (a.Becomes != "" || a.Then != ""):
			return errors.New("an export names the format it gives, not what it becomes")
		case a.Then != "":
			if a.Becomes != "" {
				return errors.New("it either becomes an image or text, or hands its result on as a format: not both")
			}
			if a.Then == Any || !strings.Contains(a.Then, "/") {
				return fmt.Errorf("%q is not a format to hand it on as: name one gohort reads, like pdf", a.Then)
			}
			if len(a.Takes) == 0 {
				return errors.New("say which formats it takes")
			}
			if contains(a.Takes, a.Then) {
				return errors.New("it cannot hand on the format it takes: that would read it again")
			}
		case a.Gives != "":
			if !extRE.MatchString(a.Gives) {
				return fmt.Errorf("name the format it gives by its extension, like docx or epub, not %q", a.Gives)
			}
			if len(a.Takes) != 1 || (a.Takes[0] != "text/markdown" && a.Takes[0] != "text/html") {
				return errors.New("an export takes the document as markdown or as html")
			}
		default:
			if !a.Becomes.Valid() {
				return fmt.Errorf("say what it becomes: %s or %s", KindImage.Label(), KindText.Label())
			}
			if len(a.Takes) == 0 {
				return errors.New("say which formats it takes")
			}
		}
	}
	for _, t := range a.Takes {
		if t != Any && !strings.Contains(t, "/") {
			return fmt.Errorf("%q is not a format: name one like heic, pdf or image/heic", t)
		}
	}
	j, err := a.Contract()
	if err != nil {
		return err
	}
	if err := checkReads(a.Reads); err != nil {
		return err
	}
	if a.TimeoutSecs < 0 || time.Duration(a.TimeoutSecs)*time.Second > maxTimeout {
		return fmt.Errorf("the time limit is 1 to %d seconds", int(maxTimeout/time.Second))
	}
	if strings.TrimSpace(a.Service) != "" {
		return checkService(j, a)
	}
	return checkFlow(j, a.Flow())
}

// checkService checks a flow done by a connector: it has no commands of its
// own, and its job is one a converter can do, a file in and one file out.
func checkService(j Job, a Adapted) error {
	switch {
	case len(a.Flow()) > 0:
		return errors.New("a connector does this one: it has no commands of its own")
	case len(a.Reads) > 0:
		return errors.New("a connector does this one: it reads no folders here")
	case !j.TakesFile() && !j.IsExport():
		return fmt.Errorf("%s does not start from a file, so a converter cannot do it", j.Title)
	case j.Files:
		return fmt.Errorf("%s gives several files, and a converter gives one", j.Title)
	}
	return nil
}

var (
	placeholderRE = regexp.MustCompile(`\{[^{}]*\}`)
	outRE         = regexp.MustCompile(`^\{out(\.[a-z0-9]{1,6})?\}$`)
)

// checkFlow checks steps keep j's contract: each a program and its
// arguments; only j's placeholders; {in} in the first step when j reads a
// file, and in every later step (the step before's result); {outdir} in the
// last step when j gives several files, and nowhere when it does not; each
// step naming its result one way.
func checkFlow(j Job, steps []string) error {
	if len(steps) == 0 {
		return errors.New("give at least one command")
	}
	allowed := map[string]bool{"{in}": true, "{outdir}": j.Files}
	for _, p := range j.Params {
		allowed["{"+p.Name+"}"] = true
	}
	for i, step := range steps {
		where := ""
		if len(steps) > 1 {
			where = fmt.Sprintf("step %d: ", i+1)
		}
		words, err := splitArgs(step)
		if err != nil {
			return errors.New(where + err.Error())
		}
		if len(words) == 0 {
			return errors.New(where + "the command is empty")
		}
		if strings.Contains(words[0], "{") {
			return errors.New(where + "a command has to start with the program to run")
		}
		in, outdir, outExt := false, false, ""
		for _, w := range words {
			for _, ph := range placeholderRE.FindAllString(w, -1) {
				switch {
				case ph == "{in}":
					in = true
				case ph == "{outdir}":
					outdir = true
				case outRE.MatchString(ph):
					ext := strings.TrimSuffix(strings.TrimPrefix(ph, "{out"), "}") + "|"
					if outExt != "" && outExt != ext {
						return errors.New(where + "name the result one way: every {out} the same")
					}
					outExt = ext
				case allowed[ph]:
				default:
					return fmt.Errorf("%s%s is not a placeholder here: this job's are %s", where, ph, strings.Join(j.Placeholders(), " "))
				}
			}
		}
		if ph := "{in}"; !in && (i > 0 || j.TakesFile()) {
			if i > 0 {
				return fmt.Errorf("%sa later step needs %s: the step before's result", where, ph)
			}
			return fmt.Errorf("%sthe command needs %s, the file it reads", where, ph)
		}
		last := i == len(steps)-1
		switch {
		case outdir && !j.Files:
			return errors.New(where + "this job gives one result, not a folder of them: use {out}")
		case outdir && !last:
			return errors.New(where + "only the last step writes {outdir}")
		case last && j.Files && !outdir:
			return errors.New(where + "this job gives several files: the last step writes them into {outdir}")
		}
	}
	return nil
}

// checkReads checks the folders a flow may also read: absolute, and not
// the whole disk or gohort's own data, which hold what no command should
// see.
func checkReads(reads []string) error {
	baseMu.RLock()
	b := base
	baseMu.RUnlock()
	for _, r := range reads {
		if !filepath.IsAbs(r) {
			return fmt.Errorf("%q is not a full path: give the folder from /", r)
		}
		c := filepath.Clean(r)
		if c == "/" {
			return errors.New("a flow cannot read the whole disk: name the folder it needs")
		}
		if b != "" {
			data := filepath.Dir(filepath.Dir(b)) // the data folder above the workspaces
			if within(c, data) || within(data, c) {
				return fmt.Errorf("%s holds gohort's own data, which no command may read", c)
			}
		}
	}
	return nil
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

var (
	adaptedMu sync.RWMutex
	adapted   = map[string]Adapted{}
)

// SetAdapted brings an adapted flow live under name.
func SetAdapted(name string, a Adapted) error {
	if err := a.Validate(); err != nil {
		return err
	}
	adaptedMu.Lock()
	adapted[name] = a
	adaptedMu.Unlock()
	return nil
}

// RemoveAdapted takes the adapted flow called name out.
func RemoveAdapted(name string) {
	adaptedMu.Lock()
	delete(adapted, name)
	adaptedMu.Unlock()
}

// namedAdapted is a live adapted flow and its name.
type namedAdapted struct {
	Name string
	A    Adapted
	J    Job
}

// live are the live adapted flows match accepts, with prefer as asked: the
// exact ones first, then catch-alls (Takes "*"), each by name.
func live(prefer bool, match func(a Adapted, j Job) (exact, any bool)) []namedAdapted {
	adaptedMu.RLock()
	defer adaptedMu.RUnlock()
	var exact, anyOf []namedAdapted
	for name, a := range adapted {
		if a.Prefer != prefer {
			continue
		}
		j, err := a.Contract()
		if err != nil {
			continue
		}
		switch e, c := match(a, j); {
		case e:
			exact = append(exact, namedAdapted{name, a, j})
		case c:
			anyOf = append(anyOf, namedAdapted{name, a, j})
		}
	}
	byName := func(s []namedAdapted) {
		sort.Slice(s, func(i, k int) bool { return s[i].Name < s[k].Name })
	}
	byName(exact)
	byName(anyOf)
	return append(exact, anyOf...)
}
