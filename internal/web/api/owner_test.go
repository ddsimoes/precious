package api

import (
	"encoding/json"
	"fmt"
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
	"precious/internal/rules"
)

// r2b 2.4 (classification: an owner category is explained as the owner's):
// the detail gives the owner's category beside what the rules would set,
// with the rule that matched; a group mark alone reads the rules' category
// as the effective one.
func TestEntryOwnerClassification(t *testing.T) {
	e := newEnv(t)
	yes := true
	s := indextest.Seed(t, e.st, indextest.Tree{Source: "disco", CreateSource: true, Nodes: []indextest.Node{
		{Path: "Projetos/site_antigo", Kind: domain.EntryDirectory, Category: domain.CategorySourceProject,
			Triage: domain.TriageKeep, Group: true, Owner: domain.Override{Category: domain.CategoryDocuments}},
		{Path: "Projetos/site_antigo/index.php", Size: 40, FileKind: domain.FileKindSource},
		{Path: "Fotos/2006/Praia", Kind: domain.EntryDirectory, Category: domain.CategoryPersonalMedia,
			Triage: domain.TriageKeep, Owner: domain.Override{Group: &yes}},
		{Path: "Fotos/2006/Praia/DSC02001.JPG", Size: 70, FileKind: domain.FileKindImage},
	}})
	e.exec(t, `UPDATE entries SET rule_ids = '["source_project"]' WHERE id = ?`, int64(s.ID("Projetos/site_antigo")))
	e.exec(t, `UPDATE entries SET rule_ids = '["camera_folder"]' WHERE id = ?`, int64(s.ID("Fotos/2006/Praia")))

	var got struct {
		Classification json.RawMessage `json:"classification"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s", s.ID("Projetos/site_antigo")), 200, &got)
	sentence, _ := json.Marshal(rules.Default().Explain([]string{"source_project"})[0])
	assertJSON(t, "site_antigo", got.Classification, `{"category":"documents","family":"personal","traits":[],`+
		`"triage":"keep","group":false,"veto":false,"rules":[{"id":"source_project","explain":`+string(sentence)+`}],`+
		`"indicators":[],"owner":{"category":"documents","group":null},"rules_category":"source_project","rules_group":true}`)

	var praia struct {
		Classification struct {
			Category string `json:"category"`
			Group    bool   `json:"group"`
			Owner    struct {
				Category *string `json:"category"`
				Group    *bool   `json:"group"`
			} `json:"owner"`
			RulesCategory string `json:"rules_category"`
			RulesGroup    bool   `json:"rules_group"`
		} `json:"classification"`
	}
	e.get(t, fmt.Sprintf("/api/entries/%s", s.ID("Fotos/2006/Praia")), 200, &praia)
	c := praia.Classification
	if c.Category != "personal_media" || !c.Group || c.Owner.Category != nil || c.Owner.Group == nil || !*c.Owner.Group ||
		c.RulesCategory != "personal_media" || c.RulesGroup {
		t.Errorf("Praia classification %+v", c)
	}
}
