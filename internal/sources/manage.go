package sources

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"precious/internal/auth"
	"precious/internal/clock"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/jobs"
	"precious/internal/relations"
	"precious/internal/store"
	"precious/internal/web/clientip"
)

// Audit event kinds written by this package.
const (
	AuditSourceAdded   = "source_added"
	AuditSourceRenamed = "source_renamed"
	AuditSourceRemoved = "source_removed"
	// AuditSourceScheduleSet records set-source-schedule with the previous
	// and the new schedule (r2b design D6).
	AuditSourceScheduleSet = "source_schedule_set"
	// AuditSourceWritesSet records a change of a source's write permission
	// (r3 design D1), with the previous and the new value.
	AuditSourceWritesSet = "source_writes_set"
)

// MaxLabelLen bounds a source label, in characters.
const MaxLabelLen = 100

// maxSlugLen bounds the label part of a source ID.
const maxSlugLen = 40

// Candidate is a folder add-source checked outside the transaction: where it
// is, and the volume it is on.
type Candidate struct {
	// Path is the folder, canonical and absolute.
	Path       string
	Label      string
	Volume     fsaccess.Volume
	RelRoot    []byte
	MountPoint string
	Caps       fsaccess.Capabilities
}

// PrepareAdd checks the folder handle names for add-source, outside any
// transaction (design D5): it expands the handle (invalid_request,
// outside_allowed_roots), refuses the state directory and every folder in or
// around it (outside_allowed_roots), opens the folder, reads its volume and
// capabilities, and checks that it does not overlap a source (source_exists).
// An empty label is the folder's name.
func (s *Service) PrepareAdd(ctx context.Context, handle, label string) (Candidate, error) {
	p, err := s.folder(handle)
	if err != nil {
		return Candidate{}, err
	}
	if containsPath(p, s.stateDir) || containsPath(s.stateDir, p) {
		return Candidate{}, domain.Errorf(domain.CodeOutsideAllowedRoots,
			"the folder %s holds or is inside Precious's state directory", domain.DisplayName([]byte(p)))
	}
	if strings.TrimSpace(label) == "" {
		label = domain.DisplayName([]byte(filepath.Base(p)))
	}
	if label, err = cleanLabel(label); err != nil {
		return Candidate{}, err
	}
	d, err := s.fs.OpenRoot(p)
	if err != nil {
		return Candidate{}, domain.Wrap(domain.CodeInvalidRequest, err, "the folder %s cannot be opened", domain.DisplayName([]byte(p)))
	}
	defer d.Close()
	info, err := d.FSInfo()
	if err != nil {
		return Candidate{}, domain.Wrap(domain.CodeInvalidRequest, err, "the folder %s cannot be read", domain.DisplayName([]byte(p)))
	}
	mounts, err := s.fs.Mounts()
	if err != nil {
		return Candidate{}, err
	}
	var m fsaccess.Mount
	ok := false
	if info.Mount != nil {
		m, ok = mountAt(mounts, filepath.Clean(info.Mount.MountPoint))
	}
	if !ok {
		m, ok = mountFor(mounts, p)
	}
	if !ok {
		return Candidate{}, fmt.Errorf("sources: no mount holds %s", domain.DisplayName([]byte(p)))
	}
	caps, err := s.fs.Capabilities(p)
	if err != nil {
		return Candidate{}, err
	}
	c := Candidate{Path: p, Label: label, Volume: m.Volume, RelRoot: relRoot(m, p), MountPoint: m.Point, Caps: caps}
	if err := checkOverlap(ctx, s.st.Reader(), c); err != nil {
		return Candidate{}, err
	}
	return c, nil
}

// cleanLabel trims a label and checks it: non-empty, valid UTF-8, at most
// MaxLabelLen characters, and no control characters.
func cleanLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	switch {
	case label == "":
		return "", domain.Errorf(domain.CodeInvalidRequest, "label must not be empty")
	case !utf8.ValidString(label):
		return "", domain.Errorf(domain.CodeInvalidRequest, "label must be valid UTF-8")
	case utf8.RuneCountInString(label) > MaxLabelLen:
		return "", domain.Errorf(domain.CodeInvalidRequest, "label must be at most %d characters", MaxLabelLen)
	case strings.IndexFunc(label, unicode.IsControl) >= 0:
		return "", domain.Errorf(domain.CodeInvalidRequest, "label must not contain control characters")
	}
	return label, nil
}

// checkOverlap refuses c when an existing source on the same volume has an
// equal, enclosing, or enclosed root. Path volumes are also compared by
// absolute folder: a backend that knows no mount table gives every root its
// own path volume, and nesting must still be refused there.
func checkOverlap(ctx context.Context, q store.Queryer, c Candidate) error {
	rows, err := q.QueryContext(ctx, `SELECT id, volume_kind, volume_id, rel_root FROM sources
		WHERE (volume_kind = ? AND volume_id = ?) OR (volume_kind = 'path' AND ? = 'path')`,
		string(c.Volume.Kind), c.Volume.ID, string(c.Volume.Kind))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, kind, volID string
			rel             []byte
		)
		if err := rows.Scan(&id, &kind, &volID, &rel); err != nil {
			return err
		}
		other := Source{Volume: fsaccess.Volume{Kind: fsaccess.VolumeKind(kind), ID: volID}, RelRoot: rel}
		same := kind == string(c.Volume.Kind) && volID == c.Volume.ID && relNested(string(rel), string(c.RelRoot))
		if !same && kind == string(fsaccess.VolumePath) {
			o := pathRoot(other)
			same = containsPath(o, c.Path) || containsPath(c.Path, o)
		}
		if same {
			return domain.Errorf(domain.CodeSourceExists, "the folder %s is, holds, or is inside the source %q",
				domain.DisplayName([]byte(c.Path)), id)
		}
	}
	return rows.Err()
}

// Add inserts the source c describes, online, with its root entry and the
// source_added audit event, in tx. It re-checks the overlap (source_exists)
// under the writer lock. The source's ID is a slug of its label, made unique
// with a numeric suffix, never one a removed source had. Add does not scan.
func (s *Service) Add(ctx context.Context, tx *sql.Tx, c Candidate) (Source, error) {
	if err := checkOverlap(ctx, tx, c); err != nil {
		return Source{}, err
	}
	id, err := newSourceID(ctx, tx, c.Label)
	if err != nil {
		return Source{}, err
	}
	caps, err := json.Marshal(c.Caps)
	if err != nil {
		return Source{}, err
	}
	now := s.clk.Now()
	strong := 0
	if c.Volume.Strong {
		strong = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sources (id, label, volume_kind, volume_id, volume_label, fs_type,
		strong, rel_root, device_key, capabilities, state, mount_point, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'online', ?, ?)`,
		string(id), c.Label, string(c.Volume.Kind), c.Volume.ID, nullString(c.Volume.Label), c.Volume.FSType,
		strong, c.RelRoot, nullString(c.Volume.DeviceKey), string(caps), []byte(c.MountPoint), clock.Millis(now))
	if isUniqueViolation(err) {
		return Source{}, domain.Errorf(domain.CodeSourceExists, "the folder %s is already a source", domain.DisplayName([]byte(c.Path)))
	}
	if err != nil {
		return Source{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO entries (source_id, parent_id, name, path, kind, state,
		first_seen, last_seen, scan_gen) VALUES (?, NULL, X'', X'', 'directory', 'present', ?, ?, 0)`,
		string(id), clock.Millis(now), clock.Millis(now)); err != nil {
		return Source{}, err
	}
	if err := s.audit(ctx, tx, AuditSourceAdded, map[string]any{
		"source_id": id, "label": c.Label, "path": domain.DisplayName([]byte(c.Path)),
		"volume_kind": c.Volume.Kind, "volume_id": c.Volume.ID,
	}); err != nil {
		return Source{}, err
	}
	return getSource(ctx, tx, id)
}

// Rename changes only the label of source id, in tx, with the
// source_renamed audit event. An unknown ID is unknown_source.
func (s *Service) Rename(ctx context.Context, tx *sql.Tx, id domain.SourceID, label string) error {
	label, err := cleanLabel(label)
	if err != nil {
		return err
	}
	var prev string
	err = tx.QueryRowContext(ctx, `SELECT label FROM sources WHERE id = ?`, string(id)).Scan(&prev)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Errorf(domain.CodeUnknownSource, "unknown source %q", id)
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sources SET label = ? WHERE id = ?`, label, string(id)); err != nil {
		return err
	}
	return s.audit(ctx, tx, AuditSourceRenamed, map[string]any{"source_id": id, "label": label, "previous_label": prev})
}

// SetSchedule sets the rescan schedule of source id, in tx, with the
// source_schedule_set audit event naming the previous and the new schedule
// (r2b design D6). The next scan of a schedule is its first time after the
// clock's now; nil turns the schedule off and clears the next scan. A
// malformed schedule is invalid_request and an unknown ID unknown_source.
func (s *Service) SetSchedule(ctx context.Context, tx *sql.Tx, id domain.SourceID, sch *domain.Schedule) error {
	var value, next any
	if sch != nil {
		at, err := sch.Next(s.clk.Now())
		if err != nil {
			return err
		}
		b, err := json.Marshal(sch)
		if err != nil {
			return err
		}
		value, next = string(b), clock.Millis(at)
	}
	var prev sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT scan_schedule FROM sources WHERE id = ?`, string(id)).Scan(&prev)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Errorf(domain.CodeUnknownSource, "unknown source %q", id)
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sources SET scan_schedule = ?, next_scan_at = ? WHERE id = ?`,
		value, next, string(id)); err != nil {
		return err
	}
	var previous any
	if prev.Valid {
		previous = json.RawMessage(prev.String)
	}
	return s.audit(ctx, tx, AuditSourceScheduleSet, map[string]any{"source_id": id, "schedule": sch, "previous_schedule": previous})
}

// SetWrites turns the write permission of source id on or off, in tx, with
// the source_writes_set audit event naming the previous and the new value
// (r3 design D1). Turning it on is writes_unavailable while
// WritesUnavailable gives a reason, from the capabilities as last recorded;
// turning it off always succeeds. Setting the value the source already has
// changes nothing and writes no event. An unknown ID is unknown_source.
func (s *Service) SetWrites(ctx context.Context, tx *sql.Tx, id domain.SourceID, enabled bool) error {
	src, err := getSource(ctx, tx, id)
	if err != nil {
		return err
	}
	if enabled {
		if reason := WritesUnavailable(src, s.allowWrites); reason != "" {
			return domain.Errorf(domain.CodeWritesUnavailable, "source %q cannot be changed: %s", id, reason)
		}
	}
	if src.WriteEnabled == enabled {
		return nil
	}
	value := 0
	if enabled {
		value = 1
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sources SET write_enabled = ? WHERE id = ?`, value, string(id)); err != nil {
		return err
	}
	return s.audit(ctx, tx, AuditSourceWritesSet, map[string]any{"source_id": id, "enabled": enabled, "previous_enabled": src.WriteEnabled})
}

// Remove deletes source id and its whole index (entries, folder aggregates,
// decisions, tag assignments, name index rows, selection rows, content data,
// history of actions, and its jobs), in tx, with the source_removed audit
// event. Nothing on disk is touched. An unknown ID is unknown_source. It
// refuses, changing nothing (r3 design D15):
//   - job_active while a scan of the source is queued, running, or paused, or
//     an action of the source is queued or running;
//   - recovery_needed while an item of the source's actions has its intent
//     recorded or awaits manual recovery, so the only record of a step in
//     flight is never deleted;
//   - quarantine_not_empty while the quarantine Precious made at the
//     source's top (sources.quarantine_entry_id) holds an entry that is not
//     missing (r4 D14): its index is what restores or purges those files.
//
// Any other active job of the source (its hashing) is cancelled, a running
// attempt as soon as tx commits, and goes away with it; relations are marked
// for a refresh, so the other sources' relations and review rows drop the
// removed copies.
func (s *Service) Remove(ctx context.Context, tx *jobs.Tx, id domain.SourceID) error {
	var label string
	err := tx.SQL().QueryRowContext(ctx, `SELECT label FROM sources WHERE id = ?`, string(id)).Scan(&label)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Errorf(domain.CodeUnknownSource, "unknown source %q", id)
	}
	if err != nil {
		return err
	}
	var job int64
	err = tx.SQL().QueryRowContext(ctx, `SELECT id FROM jobs WHERE source_id = ? AND kind = ?
		AND state IN ('queued', 'running', 'paused') ORDER BY id LIMIT 1`, string(id), string(jobs.KindScan)).Scan(&job)
	switch {
	case err == nil:
		return domain.Errorf(domain.CodeJobActive, "source %q has the active job %d; cancel it first", id, job)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	var action int64
	err = tx.SQL().QueryRowContext(ctx, `SELECT id FROM actions WHERE source_id = ?
		AND state IN ('queued', 'running') ORDER BY id LIMIT 1`, string(id)).Scan(&action)
	switch {
	case err == nil:
		return domain.Errorf(domain.CodeJobActive, "source %q has the active action %d; cancel it first", id, action)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	var item int64
	err = tx.SQL().QueryRowContext(ctx, `SELECT i.id FROM action_items i JOIN actions a ON a.id = i.action_id
		WHERE a.source_id = ? AND i.state IN ('intent', 'manual_recovery') ORDER BY i.id LIMIT 1`, string(id)).Scan(&item)
	switch {
	case err == nil:
		return domain.Errorf(domain.CodeRecoveryNeeded, "source %q has the change %d awaiting recovery; resolve it first", id, item)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	var held []byte
	err = tx.SQL().QueryRowContext(ctx, `SELECT e.path FROM sources s JOIN entries q ON q.id = s.quarantine_entry_id
		JOIN entries e ON e.source_id = s.id AND e.path > ? AND e.path < ? AND e.state <> 'missing'
		WHERE s.id = ? AND q.source_id = s.id AND q.path = ? ORDER BY e.path LIMIT 1`,
		[]byte(quarantineName+"/"), []byte(quarantineName+"0"), string(id), []byte(quarantineName)).Scan(&held)
	switch {
	case err == nil:
		return domain.Errorf(domain.CodeQuarantineNotEmpty,
			"source %q still holds items in its quarantine, such as %q; restore or delete them first", id,
			domain.DisplayName(held))
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if err := cancelJobs(ctx, tx, id); err != nil {
		return err
	}
	if err := relations.RequestRefresh(tx); err != nil {
		return err
	}
	var entries int64
	if err := tx.SQL().QueryRowContext(ctx, `SELECT count(*) FROM entries WHERE source_id = ?`, string(id)).Scan(&entries); err != nil {
		return err
	}
	// The name index and selections do not reference entries by foreign key.
	for _, q := range []string{
		`DELETE FROM entry_names WHERE rowid IN (SELECT id FROM entries WHERE source_id = ?)`,
		`DELETE FROM selection_entries WHERE entry_id IN (SELECT id FROM entries WHERE source_id = ?)`,
		`DELETE FROM sources WHERE id = ?`,
	} {
		if _, err := tx.SQL().ExecContext(ctx, q, string(id)); err != nil {
			return err
		}
	}
	return s.audit(ctx, tx.SQL(), AuditSourceRemoved, map[string]any{"source_id": id, "label": label, "entries": entries})
}

// cancelJobs cancels every active job of source id in tx.
func cancelJobs(ctx context.Context, tx *jobs.Tx, id domain.SourceID) error {
	rows, err := tx.SQL().QueryContext(ctx, `SELECT id FROM jobs WHERE source_id = ?
		AND state IN ('queued', 'running', 'paused') ORDER BY id`, string(id))
	if err != nil {
		return err
	}
	var ids []domain.JobID
	for rows.Next() {
		var job int64
		if err := rows.Scan(&job); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, domain.JobID(job))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, job := range ids {
		if _, err := tx.Cancel(job); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) audit(ctx context.Context, tx *sql.Tx, kind string, detail map[string]any) error {
	ev := auth.AuditEvent{At: s.clk.Now(), Kind: kind, Actor: auth.ActorAdmin, Detail: detail}
	if info, ok := clientip.From(ctx); ok {
		ev.ClientAddr = info.Addr
	}
	return auth.WriteAudit(ctx, tx, ev)
}

// newSourceID returns the first of slug, slug-2, slug-3, … that no source
// has and no removed source had.
func newSourceID(ctx context.Context, tx *sql.Tx, label string) (domain.SourceID, error) {
	base := slug(label)
	for n := 1; ; n++ {
		id := base
		if n > 1 {
			id += "-" + strconv.Itoa(n)
		}
		var used int
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sources WHERE id = ?1)
			OR EXISTS (SELECT 1 FROM audit_events WHERE kind = ?2 AND json_extract(detail, '$.source_id') = ?1)`,
			id, AuditSourceAdded).Scan(&used)
		if err != nil {
			return "", err
		}
		if used == 0 {
			return domain.SourceID(id), nil
		}
	}
}

// slug makes a source ID from a label: lower-case ASCII letters and digits,
// accented Latin letters without their accents, and single hyphens between
// words; "source" when nothing is left.
func slug(label string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(label) {
		var part string
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			part = string(r)
		default:
			part = foldLatin(r)
		}
		if part == "" {
			hyphen = b.Len() > 0
			continue
		}
		if hyphen {
			b.WriteByte('-')
			hyphen = false
		}
		b.WriteString(part)
	}
	s := b.String()
	if len(s) > maxSlugLen {
		s = strings.TrimRight(s[:maxSlugLen], "-")
	}
	if s == "" {
		return "source"
	}
	return s
}

// latinFold maps lower-case accented Latin letters to ASCII.
var latinFold = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'ā': "a", 'ă': "a", 'ą': "a",
	'æ': "ae", 'ç': "c", 'ć': "c", 'č': "c", 'ď': "d", 'đ': "d", 'ð': "d",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ė': "e", 'ę': "e", 'ě': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ī': "i", 'į': "i", 'ı': "i",
	'ł': "l", 'ľ': "l", 'ñ': "n", 'ń': "n", 'ň': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ō': "o", 'ő': "o", 'œ': "oe",
	'ř': "r", 'ś': "s", 'š': "s", 'ş': "s", 'ß': "ss", 'ť': "t", 'ţ': "t", 'þ': "th",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ū': "u", 'ů': "u", 'ű': "u", 'ų': "u",
	'ý': "y", 'ÿ': "y", 'ź': "z", 'ż': "z", 'ž': "z",
}

func foldLatin(r rune) string { return latinFold[r] }

func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}
