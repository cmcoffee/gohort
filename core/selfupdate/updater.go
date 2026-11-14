package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// Updater is the running program's updates: where they come from, what is
// running, and the check, update and rollback an administrator presses. One
// runs at a time.
type Updater struct {
	Product string
	Version string
	Source  Source
	Install Install
	// Blocked says why this install is never offered an update ("" when it
	// is): a build the dist site's release would take something from.
	Blocked string
	// Restart shuts the program down the way an interrupt does, so its
	// databases close; the new binary is started at the very end of that,
	// by RelaunchIfPending. Nil leaves the restart to whoever runs it.
	Restart func()
	// RestartDelay is how long after installing the restart begins, so the
	// press that asked for it is answered first.
	RestartDelay time.Duration

	mu        sync.Mutex
	checked   time.Time
	latest    *Manifest
	checkErr  string
	running   string // the action running: ActionCheck, ActionUpdate or ActionRollback
	started   time.Time
	progress  string
	outcome   map[string]string
	installed string // a version put in place and waiting for the program to restart
	writable  *string
}

// The actions, by the key the admin page sends.
const (
	ActionCheck    = "check"
	ActionUpdate   = "update"
	ActionRollback = "rollback"
)

// Default is the running program's updater; nil when it has none.
var Default *Updater

// pendingRelaunch is the binary to start when shutdown ends.
var (
	pendingMu       sync.Mutex
	pendingRelaunch string
)

// RelaunchIfPending starts the installed binary in place of this process,
// when an update or rollback asked for it. The program calls it at the very
// end of its shutdown, after its databases are closed. It returns only when
// there is nothing to start, or starting failed.
func RelaunchIfPending() error {
	pendingMu.Lock()
	exe := pendingRelaunch
	pendingMu.Unlock()
	if exe == "" {
		return nil
	}
	return relaunch(exe)
}

func setPendingRelaunch(exe string) {
	pendingMu.Lock()
	pendingRelaunch = exe
	pendingMu.Unlock()
}

// ErrBusy is a press while another action is running.
var ErrBusy = errors.New("an update action is already running: wait for it to finish")

// Status is what the admin page shows.
type Status struct {
	Version     string    `json:"version"`
	Platform    string    `json:"platform"`
	Source      string    `json:"source"`
	Keys        int       `json:"keys"`
	Blocked     string    `json:"blocked,omitempty"`
	Checked     time.Time `json:"checked,omitempty"`
	Latest      string    `json:"latest,omitempty"`
	Available   bool      `json:"available"`
	CheckError  string    `json:"check_error,omitempty"`
	Previous    string    `json:"previous,omitempty"`
	HasPrevious bool      `json:"has_previous"`
	Installed   string    `json:"installed,omitempty"`
	Running     string    `json:"running,omitempty"`
}

// Status is where things stand. A check's answer is kept in memory, so it is
// there until the program restarts.
func (u *Updater) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	st := Status{
		Version:    u.Version,
		Platform:   Platform(),
		Source:     u.Source.URL,
		Keys:       len(u.Source.Keys),
		Blocked:    u.blockedLocked(),
		Checked:    u.checked,
		CheckError: u.checkErr,
		Installed:  u.installed,
		Running:    u.running,
	}
	if u.latest != nil {
		st.Latest = u.latest.Version
		st.Available = Newer(u.latest.Version, u.Version) && u.installed == ""
	}
	st.Previous, st.HasPrevious = u.Install.Previous()
	return st
}

// blockedLocked is why this install cannot update: the reason it is never
// offered one, or why its binary cannot be replaced (asked once).
func (u *Updater) blockedLocked() string {
	if u.Blocked != "" {
		return u.Blocked
	}
	if u.writable == nil {
		w := u.Install.Writable()
		u.writable = &w
	}
	return *u.writable
}

// Progress is how far the action key has got while it runs, and how the
// last one ended.
func (u *Updater) Progress(key string) (progress, outcome string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running == key {
		progress = u.progress
		if progress == "" {
			progress = "starting"
		}
		if secs := int(time.Since(u.started).Seconds()); secs >= 3 {
			progress = fmt.Sprintf("%s - %ds", progress, secs)
		}
	}
	return progress, u.outcome[key]
}

// begin marks key running, or refuses when something is.
func (u *Updater) begin(key string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running != "" {
		return ErrBusy
	}
	u.running, u.started, u.progress = key, time.Now(), ""
	return nil
}

// end clears the running mark and keeps how it went.
func (u *Updater) end(key, outcome string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.outcome == nil {
		u.outcome = map[string]string{}
	}
	u.outcome[key] = outcome
	u.running, u.progress = "", ""
}

func (u *Updater) say(line string) {
	u.mu.Lock()
	u.progress = line
	u.mu.Unlock()
}

// fetch asks the dist site for its newest release and keeps the answer.
func (u *Updater) fetch(ctx context.Context) (Manifest, error) {
	m, err := u.Source.Latest(ctx)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.checked = time.Now()
	if err != nil {
		u.checkErr = err.Error()
		u.latest = nil
		return Manifest{}, err
	}
	u.checkErr = ""
	u.latest = &m
	return m, nil
}

// Check asks the dist site for its newest release, and says what it found.
func (u *Updater) Check(ctx context.Context) (string, error) {
	if err := u.begin(ActionCheck); err != nil {
		return "", err
	}
	u.say("asking " + u.Source.Host())
	m, err := u.fetch(ctx)
	if err != nil {
		u.end(ActionCheck, err.Error())
		return "", err
	}
	msg := fmt.Sprintf("Up to date: %s is the newest release.", u.Version)
	if Newer(m.Version, u.Version) {
		msg = fmt.Sprintf("%s is available (running %s).", m.Version, u.Version)
	}
	u.end(ActionCheck, msg)
	return msg, nil
}

// Update installs the newest release: it checks the release list's
// signature again, downloads this platform's binary, checks it against the
// list, runs it once, swaps it in with the running one kept beside it, and
// then restarts onto it. Anything that fails before the swap leaves the
// program as it was.
func (u *Updater) Update(ctx context.Context) (string, error) {
	if err := u.begin(ActionUpdate); err != nil {
		return "", err
	}
	msg, err := u.update(ctx)
	if err != nil {
		u.end(ActionUpdate, "Not updated: "+err.Error())
		return "", err
	}
	u.end(ActionUpdate, msg)
	return msg, nil
}

func (u *Updater) update(ctx context.Context) (string, error) {
	u.mu.Lock()
	blocked, installed := u.blockedLocked(), u.installed
	u.mu.Unlock()
	switch {
	case blocked != "":
		return "", errors.New(blocked)
	case installed != "":
		return "", fmt.Errorf("%s is installed already: restart gohort to run it", installed)
	}
	u.say("checking the release list")
	m, err := u.fetch(ctx)
	if err != nil {
		return "", err
	}
	if !Newer(m.Version, u.Version) {
		return "", fmt.Errorf("%s is the newest release, and %s is running", m.Version, u.Version)
	}
	path, err := u.Source.Download(ctx, m, Platform(), u.Install.Dir(), func(done, total int64) {
		u.say(fmt.Sprintf("downloading %s: %.1f of %.1f MB", m.Version, mb(done), mb(total)))
	})
	if err != nil {
		return "", err
	}
	u.say("running " + m.Version + " once to check it starts")
	if err := u.Install.Prove(ctx, path, m.Version); err != nil {
		os.Remove(path)
		return "", err
	}
	u.say("putting " + m.Version + " in place")
	if err := u.Install.Swap(path, u.Version); err != nil {
		os.Remove(path)
		return "", err
	}
	u.mu.Lock()
	u.installed = m.Version
	u.mu.Unlock()
	return u.restartOnto("Installed " + m.Version), nil
}

// Rollback puts the previous binary back and restarts onto it.
func (u *Updater) Rollback(ctx context.Context) (string, error) {
	if err := u.begin(ActionRollback); err != nil {
		return "", err
	}
	u.mu.Lock()
	blocked := u.blockedLocked()
	u.mu.Unlock()
	if blocked != "" {
		u.end(ActionRollback, "Not rolled back: "+blocked)
		return "", errors.New(blocked)
	}
	was, err := u.Install.Rollback(u.Version)
	if err != nil {
		u.end(ActionRollback, "Not rolled back: "+err.Error())
		return "", err
	}
	if was == "" {
		was = "the previous version"
	}
	u.mu.Lock()
	u.installed = was
	u.mu.Unlock()
	msg := u.restartOnto("Rolled back to " + was)
	u.end(ActionRollback, msg)
	return msg, nil
}

// restartOnto starts the restart, when this platform and program can, and
// says what happens next.
func (u *Updater) restartOnto(done string) string {
	if u.Restart == nil || !CanRelaunch {
		return done + ". Restart gohort to run it."
	}
	setPendingRelaunch(u.Install.Exe)
	delay := u.RestartDelay
	if delay <= 0 {
		delay = time.Second
	}
	restart := u.Restart
	time.AfterFunc(delay, restart)
	return done + ". gohort is restarting onto it, down for a few seconds."
}

func mb(n int64) float64 { return float64(n) / (1 << 20) }
