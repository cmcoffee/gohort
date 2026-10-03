package buildledger

import (
	"sort"
	"time"
)

// An episode is one push to green: a target's verifications from the first
// after its last pass up to and including the next pass. A target verified
// clean first time is a one-attempt episode; one still failing is an open
// episode. Attempts to green is read per episode, not per target, because a
// tool fixed in July and broken again by an edit in September is two pieces
// of evidence, not one long struggle.
type Episode struct {
	Kind     string    `json:"kind"`
	Owner    string    `json:"owner"`
	Target   string    `json:"target"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Attempts int       `json:"attempts"` // verifications in the episode, the pass included
	Fails    int       `json:"fails"`
	Unproven int       `json:"unproven"`
	Green    bool      `json:"green"`
	Classes  []string  `json:"classes,omitempty"` // every failure class the episode hit
	Model    string    `json:"model,omitempty"`   // the last stamped model in it
	Tier     string    `json:"tier,omitempty"`
}

// ClassStat is one failure class across the window.
type ClassStat struct {
	Class   string    `json:"class"`
	Kind    string    `json:"kind"`
	Count   int       `json:"count"`   // verifications that hit it
	Targets int       `json:"targets"` // distinct targets that hit it
	Lead    int       `json:"lead"`    // of Count, on the lead tier
	Worker  int       `json:"worker"`  // of Count, on the worker tier
	Last    time.Time `json:"last"`
	// Verdict is fail or unproven. A class belongs to one: the verifier
	// decides whether a finding is a defect or a check it could not make.
	Verdict Verdict `json:"verdict"`
}

// TierStat is the build record of one serving tier and model.
type TierStat struct {
	Tier          string `json:"tier"`
	Model         string `json:"model"`
	Verifications int    `json:"verifications"`
	Fails         int    `json:"fails"`
	Episodes      int    `json:"episodes"`
	Green         int    `json:"green"`
	FirstTry      int    `json:"first_try"`
}

// Report is the ledger read over a window.
type Report struct {
	Since         time.Time `json:"since"`
	Verifications int       `json:"verifications"`
	Passes        int       `json:"passes"`
	Fails         int       `json:"fails"`
	Unproven      int       `json:"unproven"`
	Episodes      int       `json:"episodes"`
	Green         int       `json:"green"`
	FirstTry      int       `json:"first_try"`
	Open          int       `json:"open"`
	// MeanToGreen and MedianToGreen are over the green episodes: how many
	// verifications it took, the passing one included.
	MeanToGreen   float64     `json:"mean_to_green"`
	MedianToGreen int         `json:"median_to_green"`
	Classes       []ClassStat `json:"classes"`
	Tiers         []TierStat  `json:"tiers"`
	// Recent is every episode that ended in the window, newest first.
	Recent []Episode `json:"recent"`
}

// episodes splits one target's history.
func episodes(r row) []Episode {
	outs := append([]Outcome(nil), r.Outcomes...)
	sort.SliceStable(outs, func(i, j int) bool { return outs[i].At.Before(outs[j].At) })
	var eps []Episode
	var cur *Episode
	var classes map[string]bool
	for _, o := range outs {
		if cur == nil {
			cur = &Episode{Kind: r.Kind, Owner: r.Owner, Target: r.Target, Start: o.At}
			classes = map[string]bool{}
		}
		cur.End = o.At
		cur.Attempts++
		if o.Model != "" || o.Tier != "" {
			cur.Model, cur.Tier = o.Model, o.Tier
		}
		for _, c := range o.Classes {
			classes[c] = true
		}
		switch o.Verdict {
		case Fail:
			cur.Fails++
		case Unproven:
			cur.Unproven++
		case Pass:
			cur.Green = true
			cur.Classes = sortedKeys(classes)
			eps = append(eps, *cur)
			cur = nil
		}
	}
	if cur != nil {
		cur.Classes = sortedKeys(classes)
		eps = append(eps, *cur)
	}
	return eps
}

// Read summarizes every verification and episode since the given time. The
// zero time reads everything kept.
func Read(since time.Time) Report {
	mu.Lock()
	all := rows()
	mu.Unlock()

	rep := Report{Since: since, Classes: []ClassStat{}, Tiers: []TierStat{}, Recent: []Episode{}}
	classIdx := map[string]*ClassStat{}
	classTargets := map[string]map[string]bool{}
	tierIdx := map[string]*TierStat{}
	tierOf := func(tier, model string) *TierStat {
		if tier == "" {
			tier = "unknown"
		}
		k := tier + "|" + model
		if t := tierIdx[k]; t != nil {
			return t
		}
		t := &TierStat{Tier: tier, Model: model}
		tierIdx[k] = t
		return t
	}
	var toGreen []int

	for _, r := range all {
		for _, o := range r.Outcomes {
			if o.At.Before(since) {
				continue
			}
			rep.Verifications++
			ts := tierOf(o.Tier, o.Model)
			ts.Verifications++
			switch o.Verdict {
			case Pass:
				rep.Passes++
			case Fail:
				rep.Fails++
				ts.Fails++
			case Unproven:
				rep.Unproven++
			}
			for _, c := range o.Classes {
				k := r.Kind + "|" + c
				cs := classIdx[k]
				if cs == nil {
					cs = &ClassStat{Class: c, Kind: r.Kind, Verdict: o.Verdict}
					classIdx[k] = cs
					classTargets[k] = map[string]bool{}
				}
				cs.Count++
				switch o.Tier {
				case "lead":
					cs.Lead++
				case "worker":
					cs.Worker++
				}
				if o.At.After(cs.Last) {
					cs.Last = o.At
				}
				classTargets[k][r.Owner+"|"+r.Target] = true
			}
		}
		for _, ep := range episodes(r) {
			if ep.End.Before(since) {
				continue
			}
			rep.Episodes++
			ts := tierOf(ep.Tier, ep.Model)
			ts.Episodes++
			if ep.Green {
				rep.Green++
				ts.Green++
				toGreen = append(toGreen, ep.Attempts)
				if ep.Attempts == 1 {
					rep.FirstTry++
					ts.FirstTry++
				}
			} else {
				rep.Open++
			}
			rep.Recent = append(rep.Recent, ep)
		}
	}

	for k, cs := range classIdx {
		cs.Targets = len(classTargets[k])
		rep.Classes = append(rep.Classes, *cs)
	}
	// Most widespread first: a class that hit ten targets once each is a
	// pattern, one target failing ten times the same way is one stuck build.
	sort.Slice(rep.Classes, func(i, j int) bool {
		a, b := rep.Classes[i], rep.Classes[j]
		if a.Targets != b.Targets {
			return a.Targets > b.Targets
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Kind+a.Class < b.Kind+b.Class
	})
	for _, ts := range tierIdx {
		rep.Tiers = append(rep.Tiers, *ts)
	}
	sort.Slice(rep.Tiers, func(i, j int) bool {
		if rep.Tiers[i].Tier != rep.Tiers[j].Tier {
			return rep.Tiers[i].Tier < rep.Tiers[j].Tier
		}
		return rep.Tiers[i].Model < rep.Tiers[j].Model
	})
	sort.Slice(rep.Recent, func(i, j int) bool { return rep.Recent[i].End.After(rep.Recent[j].End) })

	if n := len(toGreen); n > 0 {
		sum := 0
		for _, a := range toGreen {
			sum += a
		}
		rep.MeanToGreen = float64(sum) / float64(n)
		sort.Ints(toGreen)
		rep.MedianToGreen = toGreen[n/2]
	}
	return rep
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
