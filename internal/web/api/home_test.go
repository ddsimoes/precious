package api

import (
	"encoding/json"
	"fmt"
	"testing"

	"precious/internal/domain"
	"precious/internal/index/indextest"
)

// homeEnv seeds detailTree in "disco", a small "pen", and a source added
// but never scanned (a root entry without dir_stats), and gives pen an
// active scan.
func homeEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	detailTree(t, e)
	indextest.Seed(t, e.st, indextest.Tree{Source: "pen", CreateSource: true, Nodes: []indextest.Node{
		{Path: "Fotos/natal.jpg", Size: 10, MTime: year(2006), FileKind: domain.FileKindImage},
	}})
	e.exec(t, `INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root, capabilities, state, created_at)
		VALUES ('novo', 'novo', 'path', 'novo', 'ext4', 0, X'', ?, 'offline', 1)`, indextest.Capabilities)
	e.exec(t, `INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
		VALUES ('novo', NULL, X'', X'', 'directory', 'present', 1, 1, 0)`)
	e.exec(t, `INSERT INTO jobs (kind, payload_version, payload, source_id, state, max_attempts, available_at,
		progress, created_at, updated_at) VALUES ('scan', 1, '{}', 'pen', 'running', 3, 1, '{"phase":1,"files":3}', 1, 1)`)
	e.exec(t, `INSERT INTO jobs (kind, payload_version, payload, source_id, state, max_attempts, available_at,
		created_at, updated_at) VALUES ('scan', 1, '{}', 'disco', 'succeeded', 3, 1, 1, 1)`)
	return e
}

func TestHome(t *testing.T) {
	e := homeEnv(t)
	var scanJob int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM jobs WHERE state = 'running'`).Scan(&scanJob); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		target string
		want   string
	}{
		{"/api/home", fmt.Sprintf(`{"totals":{"bytes":5316,"files":7,"dirs":7},`+
			`"by_family":[{"family":"personal","bytes":256,"files":5},{"family":"programs","bytes":5060,"files":2},{"family":"disposable","bytes":0,"files":0},{"family":"containers","bytes":0,"files":0}],`+
			`"by_kind":[{"kind":"image","bytes":210,"files":2},{"kind":"document","bytes":106,"files":4},{"kind":"executable","bytes":5000,"files":1}],`+
			`"by_year":[{"year":2003,"bytes":5000,"files":1},{"year":2004,"bytes":10,"files":1},{"year":2006,"bytes":240,"files":3},{"year":2009,"bytes":60,"files":1},{"year":2012,"bytes":6,"files":1}],`+
			`"decisions":{"undecided":{"bytes":5076,"files":4},"keep":{"bytes":30,"files":1},"discard":{"bytes":210,"files":2},"later":{"bytes":0,"files":0}},`+
			`"partial":true,"scans":[{"source_id":"pen","job_id":"%d","state":"running","progress":{"phase":1,"files":3}}],`+homeR2Empty+`}`, scanJob)},
		{"/api/home?source=pen", fmt.Sprintf(`{"totals":{"bytes":10,"files":1,"dirs":1},`+
			`"by_family":[{"family":"personal","bytes":10,"files":1},{"family":"programs","bytes":0,"files":0},{"family":"disposable","bytes":0,"files":0},{"family":"containers","bytes":0,"files":0}],`+
			`"by_kind":[{"kind":"image","bytes":10,"files":1}],"by_year":[{"year":2006,"bytes":10,"files":1}],`+
			`"decisions":{"undecided":{"bytes":10,"files":1},"keep":{"bytes":0,"files":0},"discard":{"bytes":0,"files":0},"later":{"bytes":0,"files":0}},`+
			`"partial":false,"scans":[{"source_id":"pen","job_id":"%d","state":"running","progress":{"phase":1,"files":3}}],`+homeR2Empty+`}`, scanJob)},
		{"/api/home?source=novo", `{"totals":{"bytes":0,"files":0,"dirs":0},` +
			`"by_family":[{"family":"personal","bytes":0,"files":0},{"family":"programs","bytes":0,"files":0},{"family":"disposable","bytes":0,"files":0},{"family":"containers","bytes":0,"files":0}],` +
			`"by_kind":[],"by_year":[],` +
			`"decisions":{"undecided":{"bytes":0,"files":0},"keep":{"bytes":0,"files":0},"discard":{"bytes":0,"files":0},"later":{"bytes":0,"files":0}},` +
			`"partial":false,"scans":[],` + homeR2Empty + `}`},
	}
	for _, c := range cases {
		var got json.RawMessage
		e.get(t, c.target, 200, &got)
		assertJSON(t, c.target, got, c.want)
	}
	e.fails(t, "/api/home?source=nada", 404, "not_found")
	e.fails(t, "/api/home?source=pen&source=disco", 400, "invalid_request")
	e.fails(t, "/api/home?fonte=pen", 400, "invalid_request")
}

// homeR2Empty is Home's R2 fields before any hashing: no coverage, seven
// empty cards in their fixed order, and no hashing job.
const homeR2Empty = `"coverage":{"candidate":{"files":0,"bytes":0},"checked":{"files":0,"bytes":0},` +
	`"unchecked":{"files":0,"bytes":0},"unreadable":{"files":0,"bytes":0}},` +
	`"cards":[{"list":"duplicates","bytes":0,"rows":0,"decided_bytes":0,"decided_rows":0,"basis":"content"},` +
	`{"list":"unpacked_archives","bytes":0,"rows":0,"decided_bytes":0,"decided_rows":0,"basis":"content"},` +
	`{"list":"system_junk","bytes":0,"rows":0,"decided_bytes":0,"decided_rows":0,"basis":"rules"},` +
	`{"list":"installers","bytes":0,"rows":0,"decided_bytes":0,"decided_rows":0,"basis":"rules"},` +
	`{"list":"programs","bytes":0,"rows":0,"decided_bytes":0,"decided_rows":0,"basis":"rules"},` +
	`{"list":"caches","bytes":0,"rows":0,"decided_bytes":0,"decided_rows":0,"basis":"rules"},` +
	`{"list":"leftovers","bytes":0,"rows":0,"decided_bytes":0,"decided_rows":0,"basis":"rules"}],"hashing":[]`

func TestTags(t *testing.T) {
	e := newEnv(t)
	var got json.RawMessage
	e.get(t, "/api/tags", 200, &got)
	assertJSON(t, "no tags", got, `{"tags":[]}`)

	s := detailTree(t, e)
	unused := s.Tag(e.st, "vazio")
	e.get(t, "/api/tags", 200, &got)
	assertJSON(t, "tags", got, fmt.Sprintf(`{"tags":[{"id":%d,"name":"familia","own_count":1},{"id":%d,"name":"texto","own_count":2},`+
		`{"id":%d,"name":"vazio","own_count":0},{"id":%d,"name":"Zeta","own_count":1}]}`,
		s.Tag(e.st, "familia"), s.Tag(e.st, "texto"), unused, s.Tag(e.st, "Zeta")))

	e.fails(t, "/api/tags?all=1", 400, "invalid_request")
}
