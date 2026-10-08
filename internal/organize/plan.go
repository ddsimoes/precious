package organize

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/sources"
)

// Item operations and the states and reasons a plan writes (Interfaces).
// record, unlink, purge, and verify are cleanup steps (r4 D3, D4, D10, D11);
// set_mtime sets a file's modification time (r5 D13, D14).
const (
	opRename   = "rename"
	opMkdir    = "mkdir"
	opRmdir    = "rmdir"
	opRecord   = "record"
	opUnlink   = "unlink"
	opPurge    = "purge"
	opVerify   = "verify"
	opSetMtime = "set_mtime"

	statePlanned  = "planned"
	stateRefused  = "refused"
	stateConflict = "conflict"

	reasonOtherSource        = "other_source"
	reasonInsideArchive      = "inside_archive"
	reasonMissing            = "missing"
	reasonSourceRoot         = "source_root"
	reasonIntoItself         = "into_itself"
	reasonAlreadyThere       = "already_there"
	reasonOtherFilesystem    = "other_filesystem"
	reasonContainsMount      = "contains_mount"
	reasonNameTaken          = "name_taken"
	reasonNameTakenInPlan    = "name_taken_in_plan"
	reasonNameTakenByMissing = "name_taken_by_missing"
	reasonPreviousFolderGone = "previous_folder_gone"
	reasonWouldLoseKeep      = "would_lose_keep"
	// R4 (r4 D13): a quarantined entry, which only an individual move takes
	// out, and the quarantine's reserved name at a source's top folder.
	reasonInQuarantine = "in_quarantine"
	reasonReservedName = "reserved_name"
	// R5 (r5 D14, D16, D17): the date plans' refusals.
	reasonNotDatedYet     = "not_dated_yet"
	reasonDateTooCoarse   = "date_too_coarse"
	reasonHardLink        = "hard_link"
	reasonNotMedia        = "not_media"
	reasonIdenticalCopy   = "identical_copy"
	reasonInvalidName     = "invalid_name"
	reasonIdentityChanged = "identity_changed"
)

const keep = string(domain.DecisionKeep)

// node is an entry as a plan reads it.
type node struct {
	id       int64
	source   domain.SourceID
	parent   int64 // 0 for a source's root
	name     []byte
	path     []byte
	kind     string
	state    string
	boundary bool  // a mount point
	mounts   int64 // mount points below a folder (dir_stats.mount_boundaries)
	decision sql.NullString
	eff      string
	bytes    int64
	files    int64
}

func (n *node) dir() bool { return n.kind == string(domain.EntryDirectory) }

// presentFolder reports whether n can receive entries: a folder the last
// scan saw.
func (n *node) presentFolder() bool { return n.dir() && n.state == "present" }

const nodeColumns = `e.id, e.source_id, e.parent_id, e.name, e.path, e.kind, e.state, e.mount_boundary,
	COALESCE(d.mount_boundaries, 0), e.decision, e.eff_decision, e.total_bytes, e.total_files`

const nodeFrom = `entries e LEFT JOIN dir_stats d ON d.entry_id = e.id`

func scanNode(s interface{ Scan(...any) error }) (*node, error) {
	var (
		n      node
		parent sql.NullInt64
	)
	if err := s.Scan(&n.id, &n.source, &parent, &n.name, &n.path, &n.kind, &n.state, &n.boundary, &n.mounts,
		&n.decision, &n.eff, &n.bytes, &n.files); err != nil {
		return nil, err
	}
	n.parent = parent.Int64
	if n.path == nil {
		n.path = []byte{}
	}
	return &n, nil
}

// loadNode returns entry id, or nil when there is none.
func loadNode(ctx context.Context, tx *sql.Tx, id int64) (*node, error) {
	n, err := scanNode(tx.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM `+nodeFrom+` WHERE e.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("organize: read entry %d: %w", id, err)
	}
	return n, nil
}

// loadNodes runs a query selecting nodeColumns from nodeFrom.
func loadNodes(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]*node, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("organize: read entries: %w", err)
	}
	defer rows.Close()
	var out []*node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("organize: read entries: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// parseEntryID parses an entry ID of a request: a member's ref ("m45") is
// invalid_request with why, any other malformed ID not_found.
func parseEntryID(field, s string) (int64, error) {
	ref, err := domain.ParseRef(s)
	if err != nil {
		return 0, notFound(field, s)
	}
	if ref.IsMember() {
		return 0, domain.Errorf(domain.CodeInvalidRequest, "%s %s is a member of an archive, which cannot be changed", field, s)
	}
	return int64(ref.Entry), nil
}

// parseID parses an action or item ID; a malformed one names nothing.
func parseID(field, s string) (int64, error) {
	id, err := domain.ParseEntryID(s) // the same decimal form
	if err != nil {
		return 0, notFound(field, s)
	}
	return int64(id), nil
}

// folderArg loads the folder named by a request's field: unknown is
// not_found; anything but a present folder (a file, an archive, a missing
// or unreadable folder), or a folder at or below a source's quarantine (r4
// D13), is invalid_request.
func folderArg(ctx context.Context, tx *sql.Tx, field, s string) (*node, error) {
	id, err := parseEntryID(field, s)
	if err != nil {
		return nil, err
	}
	n, err := loadNode(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, notFound(field, s)
	}
	if !n.presentFolder() {
		return nil, domain.Errorf(domain.CodeInvalidRequest, "%s %s is not a folder present on its disk", field, s)
	}
	if index.IsQuarantinePath(n.path) {
		return nil, domain.Errorf(domain.CodeInvalidRequest,
			"%s %s is in the quarantine, where only a cleanup plan moves items; choose a folder outside it", field, s)
	}
	return n, nil
}

func (s *Service) checkSource(ctx context.Context, tx *sql.Tx, src domain.SourceID) error {
	return CheckSource(ctx, tx, src, s.allowWrites)
}

// frozen refuses a quarantined target of a plan other than an individual
// move (r4 D13): 409 in_quarantine. A quarantined entry is restored or
// purged, or moved out on its own.
func frozen(field, s string, n *node) error {
	if index.IsQuarantinePath(n.path) {
		return domain.Errorf(domain.CodeInQuarantine,
			"%s %s is in the quarantine; restore it, or move it out on its own, first", field, s)
	}
	return nil
}

// CheckSource refuses planning or running on src: the source's write
// permission and availability (sources.CheckWrites, with allowWrites the
// configuration's [sources] allow_writes), then an item that needs the
// owner's check (D4). The cleanup plans check their source with it too.
func CheckSource(ctx context.Context, tx *sql.Tx, src domain.SourceID, allowWrites bool) error {
	if err := sources.CheckWrites(ctx, tx, src, allowWrites); err != nil {
		return err
	}
	var recovery bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM action_items i JOIN actions a ON a.id = i.action_id
		WHERE i.state = 'manual_recovery' AND a.source_id = ?)`, string(src)).Scan(&recovery); err != nil {
		return err
	}
	if recovery {
		return domain.Errorf(domain.CodeRecoveryNeeded,
			"a change on source %q needs your check in History before anything else changes there", src)
	}
	return nil
}

// Prune marks planned actions past their expiry expired, and deletes those
// expired for longer than plannedRetention with their items. Every plan,
// the cleanup plans included, calls it before it writes its action.
func Prune(ctx context.Context, tx *sql.Tx, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE actions SET state = 'expired' WHERE state = 'planned' AND expires_at <= ?`,
		clock.Millis(now)); err != nil {
		return fmt.Errorf("organize: expire plans: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM actions WHERE state = 'expired' AND expires_at <= ?`,
		clock.Millis(now.Add(-plannedRetention))); err != nil {
		return fmt.Errorf("organize: delete expired plans: %w", err)
	}
	return nil
}

// sourceFS reads src's recorded capabilities (D8): whether names differ by
// letter case, and, for the date plans, its time resolution and local time
// (r5 D14); and its filesystem type.
func sourceFS(ctx context.Context, tx *sql.Tx, src domain.SourceID) (caps fsaccess.Capabilities, fsType string, err error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT capabilities, fs_type FROM sources WHERE id = ?`, string(src)).
		Scan(&raw, &fsType); err != nil {
		return caps, "", fmt.Errorf("organize: capabilities of %q: %w", src, err)
	}
	if err := json.Unmarshal([]byte(raw), &caps); err != nil {
		return caps, "", fmt.Errorf("organize: capabilities of %q: %w", src, err)
	}
	return caps, fsType, nil
}

// sameName compares two names as the source's filesystem does (D8): bytes,
// or after Unicode simple case folding on a case-insensitive one.
func sameName(sensitive bool, a, b []byte) bool {
	if sensitive {
		return bytes.Equal(a, b)
	}
	return bytes.EqualFold(a, b)
}

// foldKey is a key equal for names bytes.EqualFold finds equal: each rune
// (U+FFFD for a byte that is not UTF-8, as EqualFold reads it) becomes the
// smallest rune of its simple folding orbit.
func foldKey(name []byte) string {
	var b strings.Builder
	b.Grow(len(name))
	for len(name) > 0 {
		r, n := utf8.DecodeRune(name)
		name = name[n:]
		m := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			m = min(m, f)
		}
		b.WriteRune(m)
	}
	return b.String()
}

// names is a set of names in one folder, compared as sameName does; each
// name carries the entry holding it (0 for an item of the plan).
type names struct {
	sensitive bool
	m         map[string][]named
}

type named struct {
	name []byte
	id   int64
}

func newNames(sensitive bool) *names { return &names{sensitive: sensitive, m: map[string][]named{}} }

func (s *names) key(name []byte) string {
	if s.sensitive {
		return string(name)
	}
	return foldKey(name)
}

func (s *names) add(name []byte, id int64) {
	k := s.key(name)
	s.m[k] = append(s.m[k], named{name: name, id: id})
}

// holder returns the entry holding name other than except, and whether one
// does.
func (s *names) holder(name []byte, except int64) (int64, bool) {
	for _, n := range s.m[s.key(name)] {
		if (n.id == 0 || n.id != except) && sameName(s.sensitive, n.name, name) {
			return n.id, true
		}
	}
	return 0, false
}

// dest is where an item goes: a present folder (id), or the folder a mkdir
// item of the same plan makes (seq).
type dest struct {
	id   int64
	seq  int
	path []byte
	// eff is the effective decision the folder passes on (a new folder
	// inherits its parent's).
	eff string
}

func folderDest(n *node) dest { return dest{id: n.id, path: n.path, eff: n.eff} }

// key identifies the folder among a plan's destinations.
func (d dest) key() int64 {
	if d.id != 0 {
		return d.id
	}
	return -int64(d.seq)
}

// item is one planned step.
type item struct {
	op                   string
	entry                int64
	fromParent           int64
	fromName, fromPath   []byte
	toParent             int64
	toDirSeq             int
	toName, toPath       []byte
	bytes, files         int64
	state, reason, after string
	reverses             int64
	// R5: a set_mtime's time to write (r5 D13), the entry a refused
	// identical_copy duplicates (D17), and the detail a planned date
	// organize item shows: the siblings it leaves behind (D16).
	newMtime *int64
	copyOf   int64
	detail   string
}

// plan collects the items of one action on one source.
type plan struct {
	ctx  context.Context
	tx   *sql.Tx
	src  domain.SourceID
	bulk bool
	// sensitive is the source's case sensitivity, caps its recorded
	// capabilities, and fsType its filesystem type.
	sensitive bool
	caps      fsaccess.Capabilities
	fsType    string
	// template and rename are a date organize's (r5 D16), written with
	// its action.
	template string
	rename   bool
	// moveOut is set for an individual plan-move, the only plan that may
	// take a quarantined entry out of the quarantine (r4 D13).
	moveOut  bool
	items    []*item
	keptLost int
	// children are the names of the entries that are not missing in each
	// folder, read once; planned are the names the plan's items take.
	children map[int64]*names
	planned  map[int64]*names
}

func newPlan(ctx context.Context, tx *sql.Tx, src domain.SourceID, bulk bool) (*plan, error) {
	caps, fsType, err := sourceFS(ctx, tx, src)
	if err != nil {
		return nil, err
	}
	return &plan{ctx: ctx, tx: tx, src: src, bulk: bulk, sensitive: caps.CaseSensitive, caps: caps, fsType: fsType,
		children: map[int64]*names{}, planned: map[int64]*names{}}, nil
}

// tooMany refuses a plan over maxItems items.
func tooMany(n int) error {
	return domain.Errorf(domain.CodeInvalidRequest,
		"this change would hold %d items or more, over the limit of %d; choose fewer entries", n, maxItems)
}

func (p *plan) push(it *item) error {
	if len(p.items) >= maxItems {
		return tooMany(len(p.items) + 1)
	}
	p.items = append(p.items, it)
	return nil
}

// childNames returns the names in folder d: none for a folder the plan
// makes.
func (p *plan) childNames(d dest) (*names, error) {
	if d.id == 0 {
		return newNames(p.sensitive), nil
	}
	if s, ok := p.children[d.id]; ok {
		return s, nil
	}
	s := newNames(p.sensitive)
	rows, err := p.tx.QueryContext(p.ctx, `SELECT id, name FROM entries WHERE parent_id = ? AND state <> 'missing'`, d.id)
	if err != nil {
		return nil, fmt.Errorf("organize: names in folder %d: %w", d.id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id   int64
			name []byte
		)
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		s.add(name, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	p.children[d.id] = s
	return s, nil
}

func (p *plan) plannedNames(d dest) *names {
	s, ok := p.planned[d.key()]
	if !ok {
		s = newNames(p.sensitive)
		p.planned[d.key()] = s
	}
	return s
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

// below reports whether path p is dir or lies below it.
func below(p, dir []byte) bool {
	return bytes.Equal(p, dir) || len(dir) == 0 || len(p) > len(dir) && p[len(dir)] == '/' && bytes.HasPrefix(p, dir)
}

// refusal is why n cannot move into d under name, whatever the names there
// (D7, Interfaces), or "". A quarantined entry moves only out of the
// quarantine, in an individual move (r4 D13), and nothing takes the
// quarantine's name at a source's top folder.
func (p *plan) refusal(n *node, d dest, name []byte) string {
	switch {
	case n.source != p.src:
		return reasonOtherSource
	case index.IsQuarantinePath(n.path) && (!p.moveOut || index.IsQuarantinePath(d.path)):
		return reasonInQuarantine
	case n.state == "missing":
		return reasonMissing
	case n.parent == 0:
		return reasonSourceRoot
	case n.boundary:
		return reasonOtherFilesystem
	case n.dir() && n.mounts > 0:
		return reasonContainsMount
	case n.dir() && below(d.path, n.path):
		return reasonIntoItself
	case d.id == n.parent && bytes.Equal(name, n.name):
		return reasonAlreadyThere
	case len(d.path) == 0 && string(name) == index.QuarantineName:
		return reasonReservedName
	}
	if p.bulk && n.eff == keep && p.after(n, d) != "" {
		return reasonWouldLoseKeep
	}
	return ""
}

// after is n's effective decision once in d, when it differs from its
// current one and n has none of its own (D14), else "".
func (p *plan) after(n *node, d dest) string {
	if n.decision.Valid || d.eff == n.eff {
		return ""
	}
	return d.eff
}

// conflict is why name is taken in d for n (D8): a present entry there,
// another item of the plan, or a missing entry there with the owner's
// intent (D6 step 1), or "".
func (p *plan) conflict(n *node, d dest, name []byte) (string, error) {
	children, err := p.childNames(d)
	if err != nil {
		return "", err
	}
	if _, taken := children.holder(name, n.id); taken {
		return reasonNameTaken, nil
	}
	if _, taken := p.plannedNames(d).holder(name, -1); taken {
		return reasonNameTakenInPlan, nil
	}
	missing, err := index.MissingIntentAt(p.ctx, p.tx, p.src, joinPath(d.path, name))
	if err != nil {
		return "", err
	}
	if missing {
		return reasonNameTakenByMissing, nil
	}
	return "", nil
}

// move plans the rename of n into d under name: refused, in conflict, or
// planned, with the decision it will read there (D14).
func (p *plan) move(n *node, d dest, name []byte, reverses int64) (*item, error) {
	it := &item{op: opRename, entry: n.id, fromParent: n.parent, fromName: n.name, fromPath: n.path,
		toParent: d.id, toDirSeq: d.seq, toName: name, toPath: joinPath(d.path, name),
		bytes: n.bytes, files: n.files, state: statePlanned, reverses: reverses}
	if r := p.refusal(n, d, name); r != "" {
		it.state, it.reason = stateRefused, r
		return it, p.push(it)
	}
	it.after = p.after(n, d)
	r, err := p.conflict(n, d, name)
	if err != nil {
		return nil, err
	}
	if r != "" {
		it.state, it.reason = stateConflict, r
		return it, p.push(it)
	}
	p.plannedNames(d).add(name, 0)
	if it.after != "" && n.eff == keep {
		p.keptLost++
	}
	return it, p.push(it)
}

// mkdir plans a new folder name in d and returns it as a destination.
func (p *plan) mkdir(d dest, name []byte) (dest, error) {
	it := &item{op: opMkdir, toParent: d.id, toDirSeq: d.seq, toName: name, toPath: joinPath(d.path, name),
		state: statePlanned}
	if err := p.push(it); err != nil {
		return dest{}, err
	}
	p.plannedNames(d).add(name, 0)
	return dest{seq: len(p.items), path: it.toPath, eff: d.eff}, nil
}

// refused records an item refused before any destination applies.
func (p *plan) refused(it *item, reason string) error {
	it.state, it.reason = stateRefused, reason
	return p.push(it)
}

// insert writes the action, in state planned and expiring after actionTTL,
// with its items in order, and returns its ID. A date organize's template
// and rename go with the action (r5 D16).
func (p *plan) insert(tx *jobs.Tx, kind string, destination, undoOf int64) (int64, error) {
	now := tx.Now()
	var id int64
	err := p.tx.QueryRowContext(p.ctx, `INSERT INTO actions (kind, source_id, state, bulk, destination_id, undo_of,
		kept_lost, created_at, expires_at, template, rename) VALUES (?, ?, 'planned', ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		kind, string(p.src), p.bulk, nullID(destination), nullID(undoOf), p.keptLost, clock.Millis(now),
		clock.Millis(now.Add(actionTTL)), nullString(p.template), p.rename).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("organize: insert action: %w", err)
	}
	stmt, err := p.tx.PrepareContext(p.ctx, `INSERT INTO action_items (action_id, seq, op, entry_id, from_parent,
		from_name, from_path, to_parent, to_dir_seq, to_name, to_path, bytes, files, decision_after, reverses, state,
		reason, new_mtime_ns, copy_of, detail) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for i, it := range p.items {
		var toSeq, newMtime any
		if it.toDirSeq != 0 {
			toSeq = it.toDirSeq
		}
		if it.newMtime != nil {
			newMtime = *it.newMtime
		}
		if _, err := stmt.ExecContext(p.ctx, id, i+1, it.op, nullID(it.entry), nullID(it.fromParent), it.fromName,
			it.fromPath, nullID(it.toParent), toSeq, it.toName, it.toPath, it.bytes, it.files, nullString(it.after),
			nullID(it.reverses), it.state, nullString(it.reason), newMtime, nullID(it.copyOf),
			nullString(it.detail)); err != nil {
			return 0, fmt.Errorf("organize: insert item %d: %w", i+1, err)
		}
	}
	return id, nil
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

// candidate is one target of a move: an entry, or an archive member, which
// is refused (member holds its item).
type candidate struct {
	src    domain.SourceID
	path   []byte
	n      *node
	member *item
}

func entryCandidate(n *node) candidate { return candidate{src: n.source, path: n.path, n: n} }

// compareCandidates orders candidates by source, then path.
func compareCandidates(a, b candidate) int {
	if c := strings.Compare(string(a.src), string(b.src)); c != 0 {
		return c
	}
	return bytes.Compare(a.path, b.path)
}

// fold drops every entry of cs (sorted by compareCandidates) that lies
// inside another one that moves: a folder of the same source that movable
// accepts. Only those folders absorb what is inside them, so an entry
// inside a folder the plan refuses still moves on its own.
func fold(cs []candidate, movable func(n *node) bool) []candidate {
	out := cs[:0]
	absorbing := map[string]bool{} // source + "\x00" + path
	key := func(src domain.SourceID, path []byte) string { return string(src) + "\x00" + string(path) }
	for _, c := range cs {
		if c.n != nil {
			inside := false
			for i := len(c.path) - 1; i > 0 && !inside; i-- {
				inside = c.path[i] == '/' && absorbing[key(c.src, c.path[:i])]
			}
			if inside {
				continue
			}
			if c.n.dir() && movable(c.n) {
				absorbing[key(c.src, c.path)] = true
			}
		}
		out = append(out, c)
	}
	return out
}
