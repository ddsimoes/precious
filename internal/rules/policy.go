// Package rules is precious's deterministic classification (precious-spec §8,
// design D9). It loads two versioned policy files: the markers file, whose
// signals and file kinds are observations about names, and the rules file,
// which turns a file's name and kind, or a folder's facts, into a category,
// traits, a triage suggestion, the group flag, and the veto.
//
// The package is pure: it reads no filesystem and keeps no state beyond the
// loaded policy, which is immutable and safe for concurrent use.
package rules

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"precious/internal/domain"
	"precious/policies"
)

// Policy is a validated markers file together with a rules file.
type Policy struct {
	markersVersion string
	signals        []signalRule
	signalIdx      map[SignalID]int
	exactIdx       map[string]signalSet // lower-case name -> signals with that exact pattern
	extIdx         map[string]signalSet // lower-case extension -> signals with a "*.ext" pattern
	kindNames      []kindPattern
	kindExt        map[string]domain.FileKind // lower-case extension -> kind
	pairs          []kindPair

	rulesVersion string
	fileRules    []rule // by priority, highest first, then file order
	folderRules  []rule
	explain      map[string]string // rule ID -> explain sentence
	byID         map[string]*rule  // rule ID -> its rule, for Recall
}

// Load decodes and validates a markers file and a rules file. Every problem
// of either file is reported, each named by its file and key; the rules may
// name only the markers' signals.
func Load(markers, rules []byte) (*Policy, error) {
	p := &Policy{}
	var problems []string
	in := func(file string) func(string, ...any) {
		return func(format string, args ...any) {
			problems = append(problems, file+": "+fmt.Sprintf(format, args...))
		}
	}
	p.loadMarkers(markers, in("markers"))
	p.loadRules(rules, in("rules"))
	if len(problems) > 0 {
		return nil, errors.New("rules: invalid policy: " + strings.Join(problems, "; "))
	}
	return p, nil
}

// Version names both files, as "rules-v2+markers-v3". Every scan records it.
func (p *Policy) Version() string { return p.rulesVersion + "+" + p.markersVersion }

// defaultPolicy is the embedded policy, loaded once.
var defaultPolicy = sync.OnceValue(func() *Policy {
	p, err := Load(policies.Markers, policies.Rules)
	if err != nil {
		panic(err)
	}
	return p
})

// Default returns the embedded policy (markers v3 and rules v2). The package
// tests validate the files, so a load failure is a build defect and panics.
func Default() *Policy { return defaultPolicy() }
