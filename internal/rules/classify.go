package rules

import (
	"slices"

	"precious/internal/domain"
)

// KindTotals counts the files of one kind and their bytes.
type KindTotals struct{ Files, Bytes int64 }

// FileFacts is what ClassifyFile looks at: a regular file's name, its kind,
// its size, and the stems PairStem gave for the other files of its folder. A
// file whose Kind is other gets its pair kind here when SiblingStems pairs it,
// so Kind may be either FileKind or FileKindNear of the name.
type FileFacts struct {
	Name         []byte
	Kind         domain.FileKind
	Size         int64
	SiblingStems map[string]bool
}

// FolderFacts is what ClassifyFolder looks at, all of it final once the
// folder's subtree is walked (design D7):
//   - ChildSignals counts, per signal, the immediate children whose name
//     raises it (AppendNameSignals with the child's entry kind);
//   - SubtreeSignals counts the same over every entry below the folder,
//     children included and the folder itself excluded;
//   - Files and Bytes total the regular files below, and ByKind splits them
//     by file kind;
//   - Indicators counts the entries below that raise at least one indicator
//     signal (IsIndicator).
type FolderFacts struct {
	Name           []byte
	ChildSignals   map[SignalID]int
	SubtreeSignals map[SignalID]int
	Files, Bytes   int64
	ByKind         map[domain.FileKind]KindTotals
	Indicators     int
}

// Reasons a Result gives.
const (
	ReasonMatched          = "matched"
	ReasonConflictingRules = "conflicting_rules"
	ReasonNoRuleMatched    = "no_rule_matched"
)

// Result is the classification of one entry (precious-spec §6.6, design D9).
// Rules lists, highest priority first, the rules behind it: the rules that
// gave the category (several when they conflict) and every rule that added a
// trait. It is nil when no rule matched.
type Result struct {
	Category domain.Category
	Family   domain.Family
	Traits   []domain.Trait
	Triage   domain.Triage
	// Group marks a folder that is one item for review (§6.5). Veto marks a
	// folder whose discard suggestion an indicator below it turned into
	// review (§8).
	Group, Veto bool
	Rules       []string
	Reason      string
}

// groupCategories are the categories whose folders are groups (design D9).
var groupCategories = map[domain.Category]bool{
	domain.CategoryApplicationInstallation: true,
	domain.CategoryOSInstallation:          true,
	domain.CategorySourceProject:           true,
	domain.CategoryApplicationUserData:     true,
	domain.CategoryCache:                   true,
	domain.CategoryGeneratedArtifacts:      true,
	domain.CategoryBackup:                  true,
}

// triageOf is the triage a category suggests (design D9).
func triageOf(c domain.Category) domain.Triage {
	switch c {
	case domain.CategoryPersonalMedia, domain.CategoryDocuments, domain.CategorySourceProject,
		domain.CategoryApplicationUserData:
		return domain.TriageKeep
	case domain.CategorySystemJunk, domain.CategoryCache, domain.CategoryTemporaryData,
		domain.CategoryGeneratedArtifacts, domain.CategoryInstallerDownload,
		domain.CategoryApplicationInstallation:
		return domain.TriageDiscard
	default:
		return domain.TriageReview
	}
}

// ClassifyFile classifies a regular file by the file rules. A file is never a
// group and never vetoed.
func (p *Policy) ClassifyFile(f FileFacts) Result {
	kind := f.Kind
	if kind == domain.FileKindOther {
		if k, ok := p.pairKind(f.Name, f.SiblingStems); ok {
			kind = k
		}
	}
	return p.classify(p.fileRules, func(r *rule) bool {
		return (len(r.names) == 0 || matchAny(r.names, f.Name)) &&
			(r.share == nil || r.share.holds(f.Size, func(k domain.FileKind) KindTotals {
				if k == kind {
					return KindTotals{Files: 1, Bytes: f.Size}
				}
				return KindTotals{}
			}))
	})
}

// ClassifyFolder classifies a folder by the folder rules. A folder whose
// category is a group category is a group. A folder whose triage would be
// discard while user material is below it (Indicators > 0) gets review and
// the veto.
func (p *Policy) ClassifyFolder(f FolderFacts) Result {
	res := p.classify(p.folderRules, func(r *rule) bool {
		return (len(r.names) == 0 || matchAny(r.names, f.Name)) &&
			r.childSignalsHold(f.ChildSignals) &&
			r.subtreeSignalsHold(f.SubtreeSignals) &&
			(r.share == nil || r.share.holds(f.Bytes, func(k domain.FileKind) KindTotals { return f.ByKind[k] }))
	})
	res.Group = groupCategories[res.Category]
	if res.Triage == domain.TriageDiscard && f.Indicators > 0 {
		res.Triage, res.Veto = domain.TriageReview, true
	}
	return res
}

// classify applies rules, sorted by priority, to one entry: the highest
// priority among the matching rules with a category decides it, two
// categories at that priority conflict, and traits are the union over every
// matching rule.
func (p *Policy) classify(rules []rule, match func(*rule) bool) Result {
	var (
		res    Result
		top    = -1
		review bool
		traits uint8 // bit i: domain.Traits[i]
	)
	for i := range rules {
		r := &rules[i]
		if !match(r) {
			continue
		}
		decides := r.category != "" && (top < 0 || r.priority == top)
		if decides {
			switch {
			case top < 0:
				top, res.Category = r.priority, r.category
			case r.category != res.Category:
				res.Reason = ReasonConflictingRules
			}
			review = review || r.review
		}
		if !decides && len(r.traits) == 0 {
			continue
		}
		res.Rules = append(res.Rules, r.id)
		for _, t := range r.traits {
			traits |= 1 << slices.Index(domain.Traits, t)
		}
	}
	switch {
	case top < 0:
		res.Category, res.Reason = domain.CategoryUnknown, ReasonNoRuleMatched
	case res.Reason == ReasonConflictingRules:
		res.Category = domain.CategoryUnknown
	default:
		res.Reason = ReasonMatched
	}
	res.Family = domain.FamilyOf(res.Category)
	res.Triage = triageOf(res.Category)
	if review {
		res.Triage = domain.TriageReview
	}
	for i, t := range domain.Traits {
		if traits&(1<<i) != 0 {
			res.Traits = append(res.Traits, t)
		}
	}
	return res
}

func (r *rule) childSignalsHold(children map[SignalID]int) bool {
	for _, s := range r.childAll {
		if children[s] == 0 {
			return false
		}
	}
	for _, s := range r.childNone {
		if children[s] > 0 {
			return false
		}
	}
	if len(r.childAny) == 0 {
		return true
	}
	for _, s := range r.childAny {
		if children[s] > 0 {
			return true
		}
	}
	return false
}

func (r *rule) subtreeSignalsHold(subtree map[SignalID]int) bool {
	for _, sc := range r.subtree {
		if subtree[sc.signal] < sc.min {
			return false
		}
	}
	return true
}

// holds reports whether the listed kinds have at least minFiles of files and
// minBytesShare of bytes. With no bytes at all, only a zero share holds.
func (ks *kindShare) holds(bytes int64, byKind func(domain.FileKind) KindTotals) bool {
	var t KindTotals
	for _, k := range ks.kinds {
		kt := byKind(k)
		t.Files += kt.Files
		t.Bytes += kt.Bytes
	}
	if t.Files < ks.minFiles {
		return false
	}
	if bytes == 0 {
		return ks.minBytesShare == 0
	}
	return float64(t.Bytes) >= ks.minBytesShare*float64(bytes)
}

// Explain returns, for each rule ID, the plain-language sentence the rules
// file gives it, for the detail panel. An ID this policy does not define (a
// rule of earlier rules, from a scan before they changed) gives "".
func (p *Policy) Explain(ruleIDs []string) []string {
	out := make([]string, len(ruleIDs))
	for i, id := range ruleIDs {
		out[i] = p.explain[id]
	}
	return out
}
