package dates

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"precious/internal/domain"
	"precious/internal/index"
	"precious/internal/media"
)

// Target bounds (D11).
const (
	maxEntryIDs  = 1000
	maxFolderIDs = 100
)

// Skip reasons ExpandTargets gives (D11).
const reasonNotMedia = "not_media"

// Targets are the entries a correction command or a date plan acts on
// (D11). Exactly one of EntryID, EntryIDs, and FolderIDs is set; IDs are
// the API's string refs (domain.ParseRef).
type Targets struct {
	EntryID   string   `json:"entry_id,omitempty"`   // single; only when the command allows it
	EntryIDs  []string `json:"entry_ids,omitempty"`  // 1–1,000
	FolderIDs []string `json:"folder_ids,omitempty"` // 1–100, one source
	CameraKey string   `json:"camera_key,omitempty"` // only with folder_ids
}

// Skip is a target left out of a bulk request, with its reason.
type Skip struct {
	Entry  domain.Ref
	Reason string // "not_media": a member ref, or an entry that is not a media file
}

// Expanded is what targets name: their source, the media files in path
// order, and the targets skipped (path order, then member refs as given).
type Expanded struct {
	Source  domain.SourceID
	Media   []domain.EntryID
	Skipped []Skip
}

// parsed is a validated Targets.
type parsed struct {
	single  *domain.Ref
	entries []domain.Ref
	folders []domain.EntryID
	camera  string
}

// Validate checks the shape of t (D11): exactly one form; entry_id only
// when single, which the correction commands pass (the date plans take
// bulk forms only); 1–1,000 entry_ids; 1–100 folder_ids; camera_key only
// with folder_ids; every ID through domain.ParseRef. A member ref is
// refused as entry_id and in folder_ids; in entry_ids ExpandTargets skips
// it as not_media. Every refusal is a domain invalid_request error.
func (t Targets) Validate(single bool) error {
	if t.EntryID != "" && !single {
		return domain.Errorf(domain.CodeInvalidRequest, "entry_id is not accepted here; use entry_ids")
	}
	_, err := t.parse()
	return err
}

func (t Targets) parse() (parsed, error) {
	var p parsed
	forms := 0
	if t.EntryID != "" {
		forms++
	}
	if t.EntryIDs != nil {
		forms++
	}
	if t.FolderIDs != nil {
		forms++
	}
	if forms != 1 {
		return p, domain.Errorf(domain.CodeInvalidRequest, "give exactly one of entry_id, entry_ids, and folder_ids")
	}
	if t.CameraKey != "" && t.FolderIDs == nil {
		return p, domain.Errorf(domain.CodeInvalidRequest, "camera_key goes with folder_ids only")
	}
	switch {
	case t.EntryID != "":
		ref, err := domain.ParseRef(t.EntryID)
		if err != nil {
			return p, err
		}
		if ref.IsMember() {
			return p, domain.Errorf(domain.CodeInvalidRequest, "entry %s is inside an archive, which has no media date", ref)
		}
		p.single = &ref
	case t.EntryIDs != nil:
		if n := len(t.EntryIDs); n < 1 || n > maxEntryIDs {
			return p, domain.Errorf(domain.CodeInvalidRequest, "entry_ids holds %d IDs; give 1 to %d", n, maxEntryIDs)
		}
		p.entries = make([]domain.Ref, 0, len(t.EntryIDs))
		for _, s := range t.EntryIDs {
			ref, err := domain.ParseRef(s)
			if err != nil {
				return p, err
			}
			p.entries = append(p.entries, ref)
		}
	default:
		if n := len(t.FolderIDs); n < 1 || n > maxFolderIDs {
			return p, domain.Errorf(domain.CodeInvalidRequest, "folder_ids holds %d IDs; give 1 to %d", n, maxFolderIDs)
		}
		p.folders = make([]domain.EntryID, 0, len(t.FolderIDs))
		for _, s := range t.FolderIDs {
			ref, err := domain.ParseRef(s)
			if err != nil {
				return p, err
			}
			if ref.IsMember() {
				return p, domain.Errorf(domain.CodeInvalidRequest, "folder %s is inside an archive", ref)
			}
			p.folders = append(p.folders, ref.Entry)
		}
		p.camera = t.CameraKey
	}
	return p, nil
}

// ExpandTargets resolves t, read in tx, into its media files (D11):
//   - entry_id and entry_ids name files: each media file is kept; a member
//     ref, a folder, or any entry MediaCond leaves out (missing, not media)
//     is skipped not_media;
//   - folder_ids expand to the media files at or below them, or, with
//     camera_key, to the photos directly in them whose camera key
//     (media.CameraKey of their read metadata) is camera_key, as D8's events
//     count them; nothing in the quarantine.
//
// Refusals, as domain errors: an unknown ID, or a folder that is not
// present, is not_found; a target that is itself quarantined (the
// quarantine folder included) is in_quarantine; a folder ID of a file,
// targets on two sources, and more than max media are invalid_request. t
// is validated as Validate(true) does.
func ExpandTargets(ctx context.Context, tx *sql.Tx, t Targets, max int) (Expanded, error) {
	p, err := t.parse()
	if err != nil {
		return Expanded{}, err
	}
	var out Expanded
	switch {
	case p.single != nil:
		out, err = expandEntries(ctx, tx, []domain.Ref{*p.single})
	case p.entries != nil:
		out, err = expandEntries(ctx, tx, p.entries)
	default:
		out, err = expandFolders(ctx, tx, p.folders, p.camera, max)
	}
	if err != nil {
		return Expanded{}, err
	}
	if len(out.Media) > max {
		return Expanded{}, tooMany(max)
	}
	return out, nil
}

func tooMany(max int) error {
	return domain.Errorf(domain.CodeInvalidRequest, "the targets hold more than %d media files; choose fewer", max)
}

// target is an entry a target names.
type target struct {
	id     domain.EntryID
	source domain.SourceID
	path   []byte
	kind   string
	state  string
	media  bool
}

// loadTargets reads the entries ids, refusing an unknown one.
func loadTargets(ctx context.Context, tx *sql.Tx, ids []domain.EntryID) ([]target, error) {
	out := make([]target, 0, len(ids))
	for chunk := range slices.Chunk(ids, 500) {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = int64(id)
		}
		rows, err := tx.QueryContext(ctx, `SELECT e.id, e.source_id, e.path, e.kind, e.state, `+MediaCond("e")+`
			FROM entries e WHERE e.id IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("dates: read the targets: %w", err)
		}
		for rows.Next() {
			var tg target
			if err := rows.Scan(&tg.id, &tg.source, &tg.path, &tg.kind, &tg.state, &tg.media); err != nil {
				rows.Close()
				return nil, fmt.Errorf("dates: read the targets: %w", err)
			}
			out = append(out, tg)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("dates: read the targets: %w", err)
		}
	}
	found := make(map[domain.EntryID]bool, len(out))
	for _, tg := range out {
		found[tg.id] = true
	}
	for _, id := range ids {
		if !found[id] {
			return nil, domain.Errorf(domain.CodeNotFound, "entry %d not found", id)
		}
	}
	return out, nil
}

// oneSource refuses targets on two sources, and any target that is itself
// quarantined; it returns their source.
func oneSource(tgs []target) (domain.SourceID, error) {
	var src domain.SourceID
	for _, tg := range tgs {
		if src != "" && tg.source != src {
			return "", domain.Errorf(domain.CodeInvalidRequest, "the targets lie on two sources, %q and %q; choose one", src, tg.source)
		}
		src = tg.source
	}
	for _, tg := range tgs {
		if index.IsQuarantinePath(tg.path) {
			return "", domain.Errorf(domain.CodeInQuarantine, "%s is in the quarantine; restore it first",
				domain.DisplayName(tg.path))
		}
	}
	return src, nil
}

func expandEntries(ctx context.Context, tx *sql.Tx, refs []domain.Ref) (Expanded, error) {
	var (
		ids     []domain.EntryID
		members []Skip
		seen    = map[domain.Ref]bool{}
	)
	for _, r := range refs {
		if seen[r] {
			continue
		}
		seen[r] = true
		if r.IsMember() {
			members = append(members, Skip{Entry: r, Reason: reasonNotMedia})
			continue
		}
		ids = append(ids, r.Entry)
	}
	tgs, err := loadTargets(ctx, tx, ids)
	if err != nil {
		return Expanded{}, err
	}
	src, err := oneSource(tgs)
	if err != nil {
		return Expanded{}, err
	}
	slices.SortFunc(tgs, func(a, b target) int { return bytes.Compare(a.path, b.path) })
	out := Expanded{Source: src}
	for _, tg := range tgs {
		if tg.media {
			out.Media = append(out.Media, tg.id)
		} else {
			out.Skipped = append(out.Skipped, Skip{Entry: domain.Ref{Entry: tg.id}, Reason: reasonNotMedia})
		}
	}
	out.Skipped = append(out.Skipped, members...)
	return out, nil
}

func expandFolders(ctx context.Context, tx *sql.Tx, ids []domain.EntryID, camera string, max int) (Expanded, error) {
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	tgs, err := loadTargets(ctx, tx, ids)
	if err != nil {
		return Expanded{}, err
	}
	for _, tg := range tgs {
		if tg.kind != string(domain.EntryDirectory) {
			return Expanded{}, domain.Errorf(domain.CodeInvalidRequest, "%s is not a folder", domain.DisplayName(tg.path))
		}
		if tg.state != "present" {
			return Expanded{}, domain.Errorf(domain.CodeNotFound, "folder %s is not on the disk", domain.DisplayName(tg.path))
		}
	}
	src, err := oneSource(tgs)
	if err != nil {
		return Expanded{}, err
	}
	out := Expanded{Source: src}
	if camera != "" {
		out.Media, err = cameraPhotos(ctx, tx, tgs, camera)
		return out, err
	}
	// The folders not below another target, in path order: their subtrees
	// are disjoint ranges of paths, so their files come in path order.
	slices.SortFunc(tgs, func(a, b target) int { return bytes.Compare(a.path, b.path) })
	var tops []target
	for _, tg := range tgs {
		if n := len(tops); n > 0 && below(tg.path, tops[n-1].path) {
			continue
		}
		tops = append(tops, tg)
	}
	for _, top := range tops {
		where, args := `e.source_id = ?`, []any{string(src)}
		if len(top.path) > 0 {
			lo := append(append([]byte{}, top.path...), '/')
			hi := append(append([]byte{}, top.path...), '0')
			where, args = where+` AND e.path >= ? AND e.path < ?`, append(args, lo, hi)
		}
		args = append(args, max+1-len(out.Media))
		rows, err := tx.QueryContext(ctx, `SELECT e.id FROM entries e WHERE `+where+` AND `+MediaCond("e")+`
			ORDER BY e.path LIMIT ?`, args...)
		if err != nil {
			return Expanded{}, fmt.Errorf("dates: expand the folders: %w", err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return Expanded{}, fmt.Errorf("dates: expand the folders: %w", err)
			}
			out.Media = append(out.Media, domain.EntryID(id))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return Expanded{}, fmt.Errorf("dates: expand the folders: %w", err)
		}
		if len(out.Media) > max {
			return Expanded{}, tooMany(max)
		}
	}
	return out, nil
}

// below reports whether path lies strictly below the folder at dir (the
// source's top when dir is empty).
func below(path, dir []byte) bool {
	return len(dir) == 0 || len(path) > len(dir) && path[len(dir)] == '/' && bytes.HasPrefix(path, dir)
}

// cameraPhotos returns the media files directly in folders whose read
// metadata has the camera key camera, in path order.
func cameraPhotos(ctx context.Context, tx *sql.Tx, folders []target, camera string) ([]domain.EntryID, error) {
	type photo struct {
		id   domain.EntryID
		path []byte
	}
	var found []photo
	for chunk := range slices.Chunk(folders, 500) {
		args := make([]any, len(chunk))
		for i, f := range chunk {
			args[i] = int64(f.id)
		}
		rows, err := tx.QueryContext(ctx, `SELECT e.id, e.path, m.make, m.model, m.serial FROM entries e
			JOIN media_meta m ON m.entry_id = e.id AND m.state = 'read'
			WHERE e.parent_id IN (`+placeholders(len(chunk))+`) AND `+MediaCond("e"), args...)
		if err != nil {
			return nil, fmt.Errorf("dates: expand the camera's photos: %w", err)
		}
		for rows.Next() {
			var (
				p                 photo
				mk, model, serial sql.NullString
			)
			if err := rows.Scan(&p.id, &p.path, &mk, &model, &serial); err != nil {
				rows.Close()
				return nil, fmt.Errorf("dates: expand the camera's photos: %w", err)
			}
			if media.CameraKey(mk.String, model.String, serial.String) == camera {
				found = append(found, p)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("dates: expand the camera's photos: %w", err)
		}
	}
	slices.SortFunc(found, func(a, b photo) int { return bytes.Compare(a.path, b.path) })
	out := make([]domain.EntryID, len(found))
	for i, p := range found {
		out[i] = p.id
	}
	return out, nil
}

// placeholders returns n comma-separated SQL placeholders.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
