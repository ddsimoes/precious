package rules

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/BurntSushi/toml"

	"precious/internal/domain"
)

// target is what a rule classifies.
type target uint8

const (
	targetFile target = iota
	targetFolder
)

// rule is one validated rule of the rules file (design D9).
type rule struct {
	id       string
	priority int
	order    int // position in the rules file, for stable ordering
	names    []pattern
	// child signals: any of childAny (when listed), all of childAll, none of
	// childNone, among the immediate children.
	childAny, childAll, childNone []SignalID
	subtree                       []signalCount
	share                         *kindShare
	category                      domain.Category // "" for a rule that only adds traits
	review                        bool            // triage review whatever the category's triage
	traits                        []domain.Trait
	explain                       string
}

// signalCount asks for at least min entries anywhere below a folder that
// raise signal.
type signalCount struct {
	signal SignalID
	min    int
}

// kindShare asks for at least minFiles files of the listed kinds, holding at
// least minBytesShare of the bytes.
type kindShare struct {
	kinds         []domain.FileKind
	minFiles      int64
	minBytesShare float64
}

// rulesFile is the TOML shape of a rules file.
type rulesFile struct {
	Version string     `toml:"version"`
	Rules   []ruleFile `toml:"rules"`
}

type ruleFile struct {
	ID             string              `toml:"id"`
	Target         string              `toml:"target"`
	Name           []string            `toml:"name"`
	ChildSignals   *childSignalsFile   `toml:"child_signals"`
	SubtreeSignals []subtreeSignalFile `toml:"subtree_signals"`
	KindShare      *kindShareFile      `toml:"kind_share"`
	Priority       *int                `toml:"priority"`
	Category       string              `toml:"category"`
	Triage         string              `toml:"triage"`
	Traits         []string            `toml:"traits"`
	Explain        string              `toml:"explain"`
}

type childSignalsFile struct {
	Any  []string `toml:"any"`
	All  []string `toml:"all"`
	None []string `toml:"none"`
}

type subtreeSignalFile struct {
	Signal   string `toml:"signal"`
	MinCount *int   `toml:"min_count"`
}

type kindShareFile struct {
	Kinds         []string `toml:"kinds"`
	MinFiles      *int64   `toml:"min_files"`
	MinBytesShare *float64 `toml:"min_bytes_share"`
}

// loadRules decodes and validates a rules file into p, whose markers are
// already loaded. Unknown keys, a missing version, duplicate rule IDs, unknown
// targets, signals, kinds, categories, and traits, conditions a target cannot
// have, a rule without a condition, a result, or an explanation, and
// out-of-range numbers are reported through add.
func (p *Policy) loadRules(data []byte, add func(string, ...any)) {
	var f rulesFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		add("%v", err)
		return
	}
	for _, k := range md.Undecoded() {
		add("unknown key %q", k.String())
	}

	p.rulesVersion = f.Version
	if !versionPattern.MatchString(f.Version) {
		add("version %q must be a non-empty lowercase identifier such as \"rules-v2\"", f.Version)
	}

	p.explain = make(map[string]string, len(f.Rules))
	for i, rf := range f.Rules {
		key := fmt.Sprintf("rules[%d]", i)
		r := rule{id: rf.ID, order: i, explain: rf.Explain}
		_, dup := p.explain[rf.ID]
		switch {
		case !idPattern.MatchString(rf.ID):
			add("%s.id %q must match %s", key, rf.ID, idPattern)
		case dup:
			add("%s.id %q is used by another rule", key, rf.ID)
		default:
			p.explain[rf.ID] = rf.Explain
		}

		var tgt target
		switch rf.Target {
		case "file":
			tgt = targetFile
			if rf.ChildSignals != nil || rf.SubtreeSignals != nil {
				add("%s: a file rule cannot have child_signals or subtree_signals; a file has nothing below it", key)
			}
		case "folder":
			tgt = targetFolder
		default:
			add("%s.target %q must be \"file\" or \"folder\"", key, rf.Target)
		}

		r.names = patterns(key+".name", rf.Name, add)
		if rf.ChildSignals != nil {
			cs := rf.ChildSignals
			r.childAny = p.signalList(key+".child_signals.any", cs.Any, add)
			r.childAll = p.signalList(key+".child_signals.all", cs.All, add)
			r.childNone = p.signalList(key+".child_signals.none", cs.None, add)
			if len(cs.Any)+len(cs.All)+len(cs.None) == 0 {
				add("%s.child_signals must list a signal under any, all, or none", key)
			}
		}
		for j, ss := range rf.SubtreeSignals {
			skey := fmt.Sprintf("%s.subtree_signals[%d]", key, j)
			sc := signalCount{signal: SignalID(ss.Signal), min: 1}
			if _, ok := p.signalIdx[sc.signal]; !ok {
				add("%s.signal %q is not a signal of the markers file", skey, ss.Signal)
			}
			if ss.MinCount != nil {
				sc.min = *ss.MinCount
			}
			if sc.min < 1 {
				add("%s.min_count %d must be at least 1", skey, sc.min)
			}
			r.subtree = append(r.subtree, sc)
		}
		if rf.KindShare != nil {
			r.share = kindShareOf(key+".kind_share", rf.KindShare, add)
		}
		if len(r.names) == 0 && rf.ChildSignals == nil && len(r.subtree) == 0 && r.share == nil {
			add("%s must have a condition: name, child_signals, subtree_signals, or kind_share", key)
		}

		if rf.Priority == nil {
			add("%s.priority is missing", key)
		} else if r.priority = *rf.Priority; r.priority < 0 {
			add("%s.priority %d must not be negative", key, r.priority)
		}
		if rf.Category != "" {
			c, err := domain.ParseCategory(rf.Category)
			if err != nil || c == domain.CategoryUnknown {
				add("%s.category %q must be a category other than unknown", key, rf.Category)
			}
			r.category = c
		}
		switch rf.Triage {
		case "":
		case string(domain.TriageReview):
			r.review = true
			if rf.Category == "" {
				add("%s.triage needs a category to apply to", key)
			}
		default:
			add("%s.triage %q can only be review: a rule may hold a category for review, never suggest more", key, rf.Triage)
		}
		for j, t := range rf.Traits {
			tr, err := domain.ParseTrait(t)
			switch {
			case err != nil:
				add("%s.traits[%d] %q is not a trait", key, j, t)
			case slices.Contains(r.traits, tr):
				add("%s.traits[%d] %q is listed twice", key, j, t)
			default:
				r.traits = append(r.traits, tr)
			}
		}
		if rf.Category == "" && len(rf.Traits) == 0 {
			add("%s must give a category, traits, or both", key)
		}
		if rf.Explain == "" {
			add("%s.explain is missing", key)
		}

		if tgt == targetFile {
			p.fileRules = append(p.fileRules, r)
		} else {
			p.folderRules = append(p.folderRules, r)
		}
	}
	byPriority := func(a, b rule) int {
		return cmp.Or(cmp.Compare(b.priority, a.priority), cmp.Compare(a.order, b.order))
	}
	slices.SortFunc(p.fileRules, byPriority)
	slices.SortFunc(p.folderRules, byPriority)
	p.byID = make(map[string]*rule, len(p.fileRules)+len(p.folderRules))
	for _, rs := range [][]rule{p.fileRules, p.folderRules} {
		for i := range rs {
			p.byID[rs[i].id] = &rs[i]
		}
	}
}

// signalList validates signal IDs listed under key against the markers file.
func (p *Policy) signalList(key string, ids []string, add func(string, ...any)) []SignalID {
	out := make([]SignalID, 0, len(ids))
	for j, id := range ids {
		if _, ok := p.signalIdx[SignalID(id)]; !ok {
			add("%s[%d] %q is not a signal of the markers file", key, j, id)
			continue
		}
		out = append(out, SignalID(id))
	}
	return out
}

// kindShareOf validates a kind_share condition.
func kindShareOf(key string, f *kindShareFile, add func(string, ...any)) *kindShare {
	ks := &kindShare{minFiles: 1}
	if len(f.Kinds) == 0 {
		add("%s.kinds must list at least one file kind", key)
	}
	for j, k := range f.Kinds {
		kind, err := domain.ParseFileKind(k)
		if err != nil {
			add("%s.kinds[%d] %q is not a file kind", key, j, k)
			continue
		}
		ks.kinds = append(ks.kinds, kind)
	}
	if f.MinFiles != nil {
		ks.minFiles = *f.MinFiles
	}
	if ks.minFiles < 1 {
		add("%s.min_files %d must be at least 1", key, ks.minFiles)
	}
	if f.MinBytesShare != nil {
		ks.minBytesShare = *f.MinBytesShare
	}
	if !(ks.minBytesShare >= 0 && ks.minBytesShare <= 1) {
		add("%s.min_bytes_share %v must be between 0 and 1", key, ks.minBytesShare)
	}
	return ks
}
