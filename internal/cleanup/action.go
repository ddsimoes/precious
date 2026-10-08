package cleanup

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/store"
)

// Action kinds, item ops, states, and reasons the cleanup plans write
// (r4 design D3, D5, D6, D11, Interfaces). The executor reads them as
// executor/item.go names them.
const (
	kindCleanup = "cleanup"
	kindRestore = "restore"
	kindPurge   = "purge"

	groundDiscard   = "discard"
	groundDuplicate = "duplicate"

	opRename = "rename"
	opMkdir  = "mkdir"
	opRmdir  = "rmdir"
	opRecord = "record"
	opUnlink = "unlink"
	opPurge  = "purge"
	opVerify = "verify"

	statePlanned  = "planned"
	stateRefused  = "refused"
	stateConflict = "conflict"
	stateBlocked  = "blocked"

	reasonInQuarantine       = "in_quarantine"
	reasonMissing            = "missing"
	reasonSourceRoot         = "source_root"
	reasonOtherFilesystem    = "other_filesystem"
	reasonContainsMount      = "contains_mount"
	reasonHoldsKept          = "holds_kept"
	reasonBothSides          = "both_sides"
	reasonLastCopy           = "last_copy"
	reasonNameTaken          = "name_taken"
	reasonNameTakenInPlan    = "name_taken_in_plan"
	reasonNameTakenByMissing = "name_taken_by_missing"
	reasonPreviousFolderGone = "previous_folder_gone"
	reasonReservedName       = "reserved_name"
	reasonUnreadable         = "unreadable"
)

// How long a plan can be run: a cleanup plan for 24 hours (D3); a restore
// or a purge, as an organizing plan, for an hour.
const (
	cleanupTTL = 24 * time.Hour
	restoreTTL = time.Hour
	purgeTTL   = time.Hour
)

// node is an entry as the cleanup plans read it.
type node struct {
	id         int64
	source     domain.SourceID
	parent     int64 // 0 for a source's top folder
	name, path []byte
	kind       string
	state      string
	boundary   bool  // a mount point
	mounts     int64 // mount points below a folder
	decision   sql.NullString
	eff        string
	size       int64
	bytes      int64 // total_bytes
	files      int64 // total_files
	dev, ino   sql.NullInt64
	mtime      sql.NullInt64
	ctime      sql.NullInt64
	family     sql.NullString
	veto       bool
	indicators sql.NullString // a folder's dir_stats.indicators
}

func (n *node) dir() bool { return n.kind == string(domain.EntryDirectory) }

// presentFolder reports whether n can receive entries.
func (n *node) presentFolder() bool { return n.dir() && n.state == "present" }

// discarded reports whether n has its own discard.
func (n *node) discarded() bool {
	return n.decision.Valid && n.decision.String == string(domain.DecisionDiscard)
}

const nodeCols = `e.id, e.source_id, e.parent_id, e.name, e.path, e.kind, e.state, e.mount_boundary,
	COALESCE(d.mount_boundaries, 0), e.decision, e.eff_decision, e.size, e.total_bytes, e.total_files, e.dev, e.ino,
	e.mtime_ns, e.ctime_ns, e.family, e.veto, d.indicators`

const nodeFrom = `entries e LEFT JOIN dir_stats d ON d.entry_id = e.id`

func scanNode(s interface{ Scan(...any) error }) (*node, error) {
	var (
		n      node
		parent sql.NullInt64
	)
	if err := s.Scan(&n.id, &n.source, &parent, &n.name, &n.path, &n.kind, &n.state, &n.boundary, &n.mounts,
		&n.decision, &n.eff, &n.size, &n.bytes, &n.files, &n.dev, &n.ino, &n.mtime, &n.ctime, &n.family, &n.veto,
		&n.indicators); err != nil {
		return nil, err
	}
	n.parent = parent.Int64
	if n.path == nil {
		n.path = []byte{}
	}
	return &n, nil
}

// loadNode returns entry id, or nil when there is none.
func loadNode(ctx context.Context, q store.Queryer, id int64) (*node, error) {
	n, err := scanNode(q.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM `+nodeFrom+` WHERE e.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cleanup: read entry %d: %w", id, err)
	}
	return n, nil
}

// loadNodes runs a query selecting nodeCols from nodeFrom.
func loadNodes(ctx context.Context, q store.Queryer, query string, args ...any) ([]*node, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cleanup: read entries: %w", err)
	}
	defer rows.Close()
	var out []*node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("cleanup: read entries: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// parseEntryID parses an entry ID of a request: a member's ref is
// invalid_request, any other malformed ID not_found.
func parseEntryID(field, s string) (int64, error) {
	ref, err := domain.ParseRef(s)
	if err != nil {
		return 0, notFound(field, s)
	}
	if ref.IsMember() {
		return 0, domain.Errorf(domain.CodeInvalidRequest, "%s %s is a member of an archive, which is not quarantined", field, s)
	}
	return int64(ref.Entry), nil
}

// parseID parses a check, action, or file ID; a malformed one names
// nothing.
func parseID(field, s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id < 1 {
		return 0, notFound(field, s)
	}
	return id, nil
}

// notFound is the error of an ID that names nothing.
func notFound(what, id string) error {
	return domain.Errorf(domain.CodeNotFound, "%s %q not found", what, id)
}

// joinPath is the path of name inside the folder at dir.
func joinPath(dir, name []byte) []byte {
	if len(dir) == 0 {
		return bytes.Clone(name)
	}
	out := make([]byte, 0, len(dir)+1+len(name))
	out = append(out, dir...)
	out = append(out, '/')
	return append(out, name...)
}

// atOrBelow reports whether path p is dir or lies below it; every path
// lies below the empty one, a source's top folder.
func atOrBelow(p, dir []byte) bool {
	return len(dir) == 0 || bytes.Equal(p, dir) || len(p) > len(dir) && p[len(dir)] == '/' && bytes.HasPrefix(p, dir)
}

// keptWithin reports an entry of src at or below path whose effective
// decision is keep, in any state (I5), as the executor re-checks it.
func keptWithin(ctx context.Context, q store.Queryer, src domain.SourceID, path []byte) (bool, error) {
	lo, hi := bounds(path)
	var kept bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM entries WHERE source_id = ? AND eff_decision = 'keep'
		AND (path = ? OR (path >= ? AND path < ?)))`, string(src), path, lo, hi).Scan(&kept)
	if err != nil {
		return false, fmt.Errorf("cleanup: kept entries below %q: %w", domain.DisplayName(path), err)
	}
	return kept, nil
}

// step is one action item a plan writes.
type step struct {
	op                 string
	entry              int64
	fromParent         int64
	fromName, fromPath []byte
	toParent           int64
	toDirSeq           int
	toName, toPath     []byte
	bytes, files       int64
	state, reason      string
	// The draft-time identity of a cleanup rename (D3).
	kind                         string
	dev, ino, size, mtime, ctime sql.NullInt64
	draftBytes, draftFiles       sql.NullInt64
}

// action is the actions row a plan writes, in state planned.
type action struct {
	kind        string
	src         domain.SourceID
	bulk        bool
	destination int64
	ground      string
	list        string
	check       int64
	ttl         time.Duration
}

// insertAction writes a, planned and expiring after its ttl, and returns
// its ID.
func insertAction(ctx context.Context, tx *jobs.Tx, a action) (int64, error) {
	now := tx.Now()
	var id int64
	err := tx.SQL().QueryRowContext(ctx, `INSERT INTO actions (kind, source_id, state, bulk, destination_id,
		created_at, expires_at, ground, list, check_id) VALUES (?, ?, 'planned', ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		a.kind, string(a.src), a.bulk, nullID(a.destination), clock.Millis(now), clock.Millis(now.Add(a.ttl)),
		nullString(a.ground), nullString(a.list), nullID(a.check)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("cleanup: insert action: %w", err)
	}
	return id, nil
}

// insertSteps writes the items of action in order, seq from 1.
func insertSteps(ctx context.Context, tx *sql.Tx, action int64, steps []*step) error {
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO action_items (action_id, seq, op, entry_id, from_parent,
		from_name, from_path, to_parent, to_dir_seq, to_name, to_path, bytes, files, state, reason, kind, dev, ino,
		size, mtime_ns, ctime_ns, draft_bytes, draft_files) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, it := range steps {
		var toSeq any
		if it.toDirSeq != 0 {
			toSeq = it.toDirSeq
		}
		if _, err := stmt.ExecContext(ctx, action, i+1, it.op, nullID(it.entry), nullID(it.fromParent), it.fromName,
			it.fromPath, nullID(it.toParent), toSeq, it.toName, it.toPath, it.bytes, it.files, it.state,
			nullString(it.reason), nullString(it.kind), it.dev, it.ino, it.size, it.mtime, it.ctime, it.draftBytes,
			it.draftFiles); err != nil {
			return fmt.Errorf("cleanup: insert item %d: %w", i+1, err)
		}
	}
	return nil
}

// tooMany refuses a plan of n steps or more, over maxSteps.
func tooMany(n int) error {
	return domain.Errorf(domain.CodeInvalidRequest,
		"this plan would hold %d steps, over the limit of %d (%d entries); choose a narrower scope", n, maxSteps,
		(maxSteps-2)/3)
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// The quarantine (D1). Its top items are the entries at
// .precious-quarantine/<plan>/<seq>/<name>: below a <seq> folder, below a
// plan folder, below the quarantine folder Precious made.

// quarantineOf returns the source's quarantine folder that Precious made
// (sources.quarantine_entry_id, a present folder at the reserved name), 0
// when there is none, and whether an entry at the reserved name that is not
// missing is not that folder: the owner's own (D1, A6).
func quarantineOf(ctx context.Context, q store.Queryer, src domain.SourceID) (root int64, taken bool, err error) {
	var (
		id, recorded sql.NullInt64
		kind, state  sql.NullString
	)
	err = q.QueryRowContext(ctx, `SELECT e.id, e.kind, e.state, s.quarantine_entry_id FROM sources s
		LEFT JOIN entries e ON e.source_id = s.id AND e.path = ? WHERE s.id = ?`, []byte(index.QuarantineName),
		string(src)).Scan(&id, &kind, &state, &recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, domain.Errorf(domain.CodeUnknownSource, "unknown source %q", src)
	}
	if err != nil {
		return 0, false, fmt.Errorf("cleanup: quarantine of %q: %w", src, err)
	}
	switch {
	case !id.Valid || state.String == "missing":
		return 0, false, nil
	case recorded.Valid && recorded.Int64 == id.Int64 && kind.String == string(domain.EntryDirectory) &&
		state.String == "present":
		return id.Int64, false, nil
	}
	return 0, true, nil
}

// topItem is a quarantined top item: the entry, its <seq> folder, and its
// plan folder.
type topItem struct {
	n          *node
	seq, plan  *node
	planName   []byte
	quarantine int64
}

// loadTop returns entry id as a quarantined top item of its source, or nil
// when it is none: not below a <seq> folder, a plan folder, and the
// quarantine Precious made, or missing.
func loadTop(ctx context.Context, q store.Queryer, id int64) (*topItem, error) {
	n, err := loadNode(ctx, q, id)
	if err != nil || n == nil || n.state == "missing" || !index.IsQuarantinePath(n.path) || n.parent == 0 {
		return nil, err
	}
	seq, err := loadNode(ctx, q, n.parent)
	if err != nil || seq == nil || !seq.dir() || seq.parent == 0 {
		return nil, err
	}
	plan, err := loadNode(ctx, q, seq.parent)
	if err != nil || plan == nil || !plan.dir() || plan.parent == 0 {
		return nil, err
	}
	root, _, err := quarantineOf(ctx, q, n.source)
	if err != nil || root == 0 || plan.parent != root {
		return nil, err
	}
	return &topItem{n: n, seq: seq, plan: plan, planName: plan.name, quarantine: root}, nil
}

// topItems returns the quarantined top items that entry_ids name, each
// once: an unknown ID is not_found; an entry that is no quarantined top
// item, or one of another source than the first, is invalid_request.
func topItems(ctx context.Context, q store.Queryer, field string, ids []string) ([]*topItem, error) {
	seen := map[int64]bool{}
	var out []*topItem
	for _, s := range ids {
		id, err := parseEntryID(field, s)
		if err != nil {
			return nil, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		t, err := loadTop(ctx, q, id)
		if err != nil {
			return nil, err
		}
		if t == nil {
			n, err := loadNode(ctx, q, id)
			if err != nil {
				return nil, err
			}
			if n == nil {
				return nil, notFound("entry", s)
			}
			return nil, domain.Errorf(domain.CodeInvalidRequest, "entry %s (%s) is not an item in the quarantine", s,
				domain.DisplayName(n.path))
		}
		if len(out) > 0 && t.n.source != out[0].n.source {
			return nil, domain.Errorf(domain.CodeInvalidRequest, "the items are on more than one source; choose items of one source")
		}
		out = append(out, t)
	}
	return out, nil
}

// origin is where a quarantined top item came from: the done cleanup
// rename of its entry into its path (A5).
type origin struct {
	plan       int64
	at         sql.NullInt64
	fromParent int64
	fromName   []byte
	fromPath   []byte
}

// originOf returns the origin of the top item n, or nil for an unknown
// origin: a file a scan found there (D4).
func originOf(ctx context.Context, q store.Queryer, n *node) (*origin, error) {
	var (
		o      origin
		parent sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `SELECT a.id, i.finished_at, i.from_parent, i.from_name, i.from_path
		FROM action_items i JOIN actions a ON a.id = i.action_id
		WHERE i.entry_id = ? AND i.op = 'rename' AND i.state = 'done' AND i.to_path = ? AND a.kind = 'cleanup'
			AND a.source_id = ?
		ORDER BY i.id DESC LIMIT 1`, n.id, n.path, string(n.source)).Scan(&o.plan, &o.at, &parent, &o.fromName,
		&o.fromPath)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cleanup: origin of %q: %w", domain.DisplayName(n.path), err)
	}
	o.fromParent = parent.Int64
	if o.fromPath == nil {
		o.fromPath = []byte{}
	}
	return &o, nil
}

// sourceOnline refuses a source that is not online: source_offline, or
// unknown_source.
func sourceOnline(ctx context.Context, q store.Queryer, src domain.SourceID) error {
	var state string
	err := q.QueryRowContext(ctx, `SELECT state FROM sources WHERE id = ?`, string(src)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Errorf(domain.CodeUnknownSource, "unknown source %q", src)
	}
	if err != nil {
		return err
	}
	if state != "online" {
		return domain.Errorf(domain.CodeSourceOffline, "source %q is %s", src, state)
	}
	return nil
}
