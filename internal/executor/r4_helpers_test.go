package executor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/instrument"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/rules"
)

// q is the quarantine folder's name.
const q = index.QuarantineName

// otherID is the tests' second source.
const otherID domain.SourceID = "other"

// diskAs is disk for the source src at path, its own device and volume.
func (e *env) diskAs(src domain.SourceID, path string, caps fsaccess.Capabilities, build func(root *synthfs.Node)) *synthfs.Node {
	e.t.Helper()
	root := e.sfs.Root(path)
	if build != nil {
		build(root)
	}
	dev := root.Info().Dev
	vol := fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: "uuid-" + string(src), FSType: "ext4",
		DeviceKey: "dev:" + string(src), Strong: true}
	e.sfs.SetVolume(dev, vol)
	e.sfs.SetCapabilities(dev, caps)
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root, device_key, capabilities,
		state, mount_point, created_at, write_enabled) VALUES (?, ?, 'uuid', ?, 'ext4', 1, X'', ?, ?, 'online', ?, 0, 1)`,
		string(src), string(src), vol.ID, vol.DeviceKey, string(capsJSON), []byte(path))
	e.exec(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen, last_seen, scan_gen)
		VALUES (?, NULL, X'', X'', 'directory', 'present', 0, 0, 0)`, string(src))
	e.scanOf(src)
	return root
}

// scanOf runs a complete scan of src, as a running scan job.
func (e *env) scanOf(src domain.SourceID) {
	e.t.Helper()
	h := index.NewHandler(e.st, e.src, rules.Default(), fixedClock{testNow}, config.Defaults().Scan)
	var id int64
	if err := e.st.Writer().QueryRow(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts,
		max_attempts, available_at, created_at, updated_at) VALUES ('scan', 1, '{}', ?, 'running', 0, 3, 0, 0, 0)
		RETURNING id`, string(src)).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	job := jobs.Job{ID: domain.JobID(id), Kind: jobs.KindScan, PayloadVersion: 1, Payload: json.RawMessage("{}"),
		SourceID: src, Attempt: 1}
	if err := h.Run(context.Background(), job, &fakeRuntime{}); err != nil {
		e.t.Fatalf("scan %s: %v", src, err)
	}
	e.exec(`UPDATE jobs SET state = 'succeeded' WHERE id = ?`, id)
}

// idOf returns the entry ID of a path of src.
func (e *env) idOf(src domain.SourceID, path string) int64 {
	e.t.Helper()
	var id int64
	if err := e.st.Reader().QueryRow(`SELECT id FROM entries WHERE source_id = ? AND path = ?`, string(src),
		[]byte(path)).Scan(&id); err != nil {
		e.t.Fatalf("entry %s:%q: %v", src, path, err)
	}
	return id
}

// state returns the index state of a path of src, "" when not indexed.
func (e *env) state(src domain.SourceID, path string) string {
	e.t.Helper()
	var s string
	err := e.st.Reader().QueryRow(`SELECT state FROM entries WHERE source_id = ? AND path = ?`, string(src),
		[]byte(path)).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// decide gives the entry at path of src its own decision, inherited by
// every entry below it without one, as set-decision leaves them.
func (e *env) decide(src domain.SourceID, path, decision string) {
	e.t.Helper()
	lo, hi := below([]byte(path))
	e.exec(`UPDATE entries SET decision = ?, eff_decision = ? WHERE source_id = ? AND path = ?`, decision, decision,
		string(src), []byte(path))
	e.exec(`UPDATE entries SET eff_decision = ? WHERE source_id = ? AND decision IS NULL AND path >= ? AND path < ?`,
		decision, string(src), lo, hi)
}

// hashed records the file at path of src as hashed with the digest of data,
// as the hashing job leaves it.
func (e *env) hashed(src domain.SourceID, path string, data []byte) {
	e.t.Helper()
	sum := sha256.Sum256(data)
	e.exec(`INSERT INTO contents (sha256, size) VALUES (?, ?) ON CONFLICT (sha256) DO NOTHING`, sum[:], len(data))
	e.exec(`INSERT INTO file_content (entry_id, source_id, state, size, mtime_ns, ctime_ns, ino, content_id, checked_at)
		SELECT e.id, e.source_id, 'hashed', e.size, e.mtime_ns, e.ctime_ns, e.ino, c.id, 0
		FROM entries e, contents c WHERE e.source_id = ? AND e.path = ? AND c.sha256 = ?
		ON CONFLICT (entry_id) DO UPDATE SET state = 'hashed', content_id = excluded.content_id`,
		string(src), []byte(path), sum[:])
}

// cleanupAction inserts a queued cleanup action of src with one cleanup
// item per path, as plan-cleanup and run-action leave it (r4 D3): the mkdir
// of the quarantine when the index has none, the mkdir of <plan>, then for
// the k-th path the mkdir of <k>, the rename of its entry into it with the
// draft-time identity of its index row, and the record <k>.json. It
// returns the action ID.
func (e *env) cleanupAction(src domain.SourceID, ground string, paths ...string) int64 {
	e.t.Helper()
	var action int64
	err := e.r.Write(context.Background(), func(tx *jobs.Tx) error {
		q0 := tx.SQL()
		if err := q0.QueryRow(`INSERT INTO actions (kind, source_id, state, bulk, created_at, ground)
			VALUES ('cleanup', ?, 'queued', 1, 0, ?) RETURNING id`, string(src), ground).Scan(&action); err != nil {
			return err
		}
		seq := 0
		insert := func(op string, cols map[string]any) error {
			seq++
			cols["action_id"], cols["seq"], cols["op"], cols["state"] = action, seq, op, "planned"
			var names, marks string
			var args []any
			for k, v := range cols {
				if names != "" {
					names += ", "
					marks += ", "
				}
				names += k
				marks += "?"
				args = append(args, v)
			}
			_, err := q0.Exec(`INSERT INTO action_items (`+names+`) VALUES (`+marks+`)`, args...)
			return err
		}
		var root, quarantine int64
		if err := q0.QueryRow(`SELECT id FROM entries WHERE source_id = ? AND path = X''`, string(src)).Scan(&root); err != nil {
			return err
		}
		err := q0.QueryRow(`SELECT id FROM entries WHERE source_id = ? AND path = ? AND state = 'present'`, string(src),
			[]byte(q)).Scan(&quarantine)
		planDir := map[string]any{"to_name": []byte(strconv.FormatInt(action, 10))}
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if err := insert(opMkdir, map[string]any{"to_parent": root, "to_name": []byte(q)}); err != nil {
				return err
			}
			planDir["to_dir_seq"] = seq
		case err != nil:
			return err
		default:
			planDir["to_parent"] = quarantine
		}
		if err := insert(opMkdir, planDir); err != nil {
			return err
		}
		plan := seq
		for k, p := range paths {
			var (
				id, parent                   int64
				name, path                   []byte
				kind                         string
				size, totalBytes, totalFiles int64
				dev, ino, mtime, ctime       sql.NullInt64
			)
			if err := q0.QueryRow(`SELECT id, parent_id, name, path, kind, size, total_bytes, total_files, dev, ino,
				mtime_ns, ctime_ns FROM entries WHERE source_id = ? AND path = ?`, string(src), []byte(p)).
				Scan(&id, &parent, &name, &path, &kind, &size, &totalBytes, &totalFiles, &dev, &ino, &mtime, &ctime); err != nil {
				return err
			}
			n := strconv.Itoa(k + 1)
			if err := insert(opMkdir, map[string]any{"to_dir_seq": plan, "to_name": []byte(n)}); err != nil {
				return err
			}
			ren := map[string]any{"entry_id": id, "from_parent": parent, "from_name": name, "from_path": path,
				"to_dir_seq": seq, "to_name": name, "kind": kind, "dev": dev, "ino": ino, "size": size,
				"mtime_ns": mtime, "ctime_ns": ctime}
			if kind == "directory" {
				ren["draft_bytes"], ren["draft_files"] = totalBytes, totalFiles
			}
			if err := insert(opRename, ren); err != nil {
				return err
			}
			if err := insert(opRecord, map[string]any{"entry_id": id, "to_dir_seq": plan,
				"to_name": []byte(n + ".json")}); err != nil {
				return err
			}
		}
		_, err = Enqueue(tx, src, action)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return action
}

// renameOf returns the rename item of the k-th path of a cleanup action.
func (e *env) renameOf(action int64, k int) itemRow {
	e.t.Helper()
	var seq int
	if err := e.st.Reader().QueryRow(`SELECT seq FROM action_items WHERE action_id = ? AND op = 'rename'
		ORDER BY seq LIMIT 1 OFFSET ?`, action, k-1).Scan(&seq); err != nil {
		e.t.Fatal(err)
	}
	return e.item(action, seq)
}

// cleanupStates returns, for each path of a cleanup action, the states of
// its mkdir, rename, and record, joined.
func (e *env) cleanupStates(action int64) []string {
	e.t.Helper()
	rows, err := e.st.Reader().Query(`SELECT op, state FROM action_items WHERE action_id = ? ORDER BY seq`, action)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var all [][2]string
	for rows.Next() {
		var op, st string
		if err := rows.Scan(&op, &st); err != nil {
			e.t.Fatal(err)
		}
		all = append(all, [2]string{op, st})
	}
	var out []string
	for i := 0; i < len(all); i++ {
		if all[i][0] == opMkdir && i+1 < len(all) && all[i+1][0] == opRename {
			out = append(out, all[i][1]+"/"+all[i+1][1]+"/"+all[i+2][1])
			i += 2
		}
	}
	return out
}

// quarantined builds, on the disk's root, a quarantine Precious made with
// the plan folder 7 holding the given item folders, and marks it as the
// source's quarantine after the scan (call after disk).
func (e *env) markQuarantine(src domain.SourceID) {
	e.t.Helper()
	e.exec(`UPDATE sources SET quarantine_entry_id = (SELECT id FROM entries WHERE source_id = ?1 AND path = ?2)
		WHERE id = ?1`, string(src), []byte(q))
}

// checkFiles inserts a ready check of src over the set items at paths:
// every entry at or below each recorded with its index identity, files
// unique and confirmed, everything else no_content. It returns the check.
func (e *env) check(src domain.SourceID, paths ...string) int64 {
	e.t.Helper()
	var check int64
	if err := e.st.Writer().QueryRow(`INSERT INTO purge_checks (source_id, state, created_at, finished_at)
		VALUES (?, 'ready', 0, 0) RETURNING id`, string(src)).Scan(&check); err != nil {
		e.t.Fatal(err)
	}
	for _, p := range paths {
		item := e.idOf(src, p)
		e.exec(`INSERT INTO purge_check_items (check_id, entry_id, path, readable) VALUES (?, ?, ?, 1)`, check, item,
			[]byte(p))
		lo, hi := below([]byte(p))
		e.exec(`INSERT INTO purge_check_files (check_id, item_id, entry_id, kind, path, size, mtime_ns, ctime_ns, ino,
				dev, nlink, alloc, verdict, class, confirmed_at)
			SELECT ?, ?, id, kind, path, size, mtime_ns, ctime_ns, ino, dev, nlink, alloc,
				CASE WHEN kind = 'file' AND size > 0 THEN 'unique' ELSE 'no_content' END,
				CASE WHEN kind = 'file' AND size > 0 THEN 'uncertain' END,
				CASE WHEN kind = 'file' AND size > 0 THEN 1 END
			FROM entries WHERE source_id = ? AND state = 'present' AND (path = ? OR (path >= ? AND path < ?))`,
			check, item, string(src), []byte(p), lo, hi)
	}
	return check
}

// relyOn makes the check's file at path safe through the copy at copyPath
// of copySrc, with the copy's index identity.
func (e *env) relyOn(check int64, path string, copySrc domain.SourceID, copyPath string) {
	e.t.Helper()
	e.exec(`UPDATE purge_check_files SET verdict = 'safe', class = NULL, confirmed_at = NULL,
			copy_source = c.source_id, copy_path = c.path, copy_entry = c.id, copy_size = c.size,
			copy_mtime_ns = c.mtime_ns, copy_ctime_ns = c.ctime_ns, copy_ino = c.ino, copy_dev = c.dev
		FROM (SELECT * FROM entries WHERE source_id = ? AND path = ?) AS c
		WHERE purge_check_files.check_id = ? AND purge_check_files.path = ?`,
		string(copySrc), []byte(copyPath), check, []byte(path))
}

// checkState returns a check's state and stale reason.
func (e *env) checkState(check int64) (string, string) {
	e.t.Helper()
	var (
		state  string
		reason sql.NullString
	)
	if err := e.st.Reader().QueryRow(`SELECT state, stale_reason FROM purge_checks WHERE id = ?`, check).
		Scan(&state, &reason); err != nil {
		e.t.Fatal(err)
	}
	return state, reason.String
}

// purgeAction inserts a queued purge action of src over check, as
// plan-purge and run-action leave it (r4 D11): a verify item when verify,
// then one purge item per set item path.
func (e *env) purgeAction(src domain.SourceID, check int64, verify bool, paths ...string) int64 {
	e.t.Helper()
	var action int64
	err := e.r.Write(context.Background(), func(tx *jobs.Tx) error {
		q0 := tx.SQL()
		if err := q0.QueryRow(`INSERT INTO actions (kind, source_id, state, bulk, created_at, check_id)
			VALUES ('purge', ?, 'queued', 1, 0, ?) RETURNING id`, string(src), check).Scan(&action); err != nil {
			return err
		}
		seq := 0
		if verify {
			seq++
			if _, err := q0.Exec(`INSERT INTO action_items (action_id, seq, op, state) VALUES (?, ?, 'verify', 'planned')`,
				action, seq); err != nil {
				return err
			}
		}
		for _, p := range paths {
			seq++
			if _, err := q0.Exec(`INSERT INTO action_items (action_id, seq, op, entry_id, from_parent, from_name,
					from_path, state)
				SELECT ?, ?, 'purge', id, parent_id, name, path, 'planned' FROM entries WHERE source_id = ? AND path = ?`,
				action, seq, string(src), []byte(p)); err != nil {
				return err
			}
		}
		_, err := Enqueue(tx, src, action)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return action
}

// report returns a purge action's deleted files, deleted bytes, and freed
// bytes.
func (e *env) report(action int64) (files, bytes, freed int64) {
	e.t.Helper()
	if err := e.st.Reader().QueryRow(`SELECT deleted_files, deleted_bytes, freed_bytes FROM actions WHERE id = ?`,
		action).Scan(&files, &bytes, &freed); err != nil {
		e.t.Fatal(err)
	}
	return files, bytes, freed
}

// deletions counts the Unlink and Rmdir calls.
func (e *env) deletions() int {
	return e.rec.Count(instrument.OpUnlink) + e.rec.Count(instrument.OpRmdir)
}

// readFile reads a file of the synthfs at the absolute root's rel path.
func (e *env) readFile(root, rel string) []byte {
	e.t.Helper()
	info, ok := e.lstat(root, rel)
	if !ok {
		e.t.Fatalf("%s/%s is gone", root, rel)
	}
	d, err := e.sfs.OpenRoot(root)
	if err != nil {
		e.t.Fatal(err)
	}
	defer d.Close()
	parts := splitPath(rel)
	for _, p := range parts[:len(parts)-1] {
		inf, err := d.Lstat([]byte(p))
		if err != nil {
			e.t.Fatal(err)
		}
		next, err := d.OpenDir([]byte(p), inf)
		if err != nil {
			e.t.Fatal(err)
		}
		defer next.Close()
		d = next
	}
	f, err := d.OpenFile([]byte(parts[len(parts)-1]), info)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, info.Size)
	if _, err := f.ReadAt(buf, 0); err != nil && int64(len(buf)) != info.Size {
		e.t.Fatal(err)
	}
	return buf
}

func splitPath(rel string) []string {
	var out []string
	start := 0
	for i := 0; i < len(rel); i++ {
		if rel[i] == '/' {
			out = append(out, rel[start:i])
			start = i + 1
		}
	}
	return append(out, rel[start:])
}

// mtime2004 is the modification time of the tests' files.
var mtime2004 = time.Date(2004, 7, 1, 9, 0, 0, 0, time.UTC)

// laterChanges moves the synthfs clock a day past the scan, so that a change
// made afterwards gets a change time far from the one the scan recorded
// (synthfs otherwise advances it by a nanosecond, within POSIX resolution).
func laterChanges(root *synthfs.Node) { root.Ctime(testNow.Add(24 * time.Hour)) }
