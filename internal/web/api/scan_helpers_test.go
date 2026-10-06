package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/fsaccess/synthfs"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/rules"
	"precious/internal/sources"
)

// posix is the capability set of a local POSIX filesystem.
var posix = fsaccess.Capabilities{
	Known: true, CaseSensitive: true, NormalizationSensitive: true, StableIdentity: true, HardLinks: true,
	TimeResolution: time.Nanosecond,
}

// scanSynth adds the synthfs root built at path as source id, on its own
// strongly identified volume, root entry included, as add-source does; runs
// one complete scan of it under a running scan job, as the runner would;
// and returns the root entry's ID.
func (e *env) scanSynth(t *testing.T, sfs *synthfs.FS, id domain.SourceID, path string, root *synthfs.Node) domain.EntryID {
	t.Helper()
	ctx := context.Background()
	dev := root.Info().Dev
	vol := fsaccess.Volume{Kind: fsaccess.VolumeUUID, ID: "uuid-" + string(id), FSType: "ext4",
		DeviceKey: "dev:" + string(id), Strong: true}
	sfs.SetVolume(dev, vol)
	sfs.SetCapabilities(dev, posix)
	caps, err := json.Marshal(posix)
	if err != nil {
		t.Fatal(err)
	}
	var rootID, job int64
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO sources (id, label, volume_kind, volume_id, fs_type, strong, rel_root,
			device_key, capabilities, state, mount_point, created_at)
			VALUES (?, ?, 'uuid', ?, 'ext4', 1, X'', ?, ?, 'online', ?, 0)`,
			string(id), string(id), vol.ID, vol.DeviceKey, string(caps), []byte(path)); err != nil {
			return err
		}
		if err := tx.QueryRow(`INSERT INTO entries (source_id, parent_id, name, path, kind, state, first_seen,
			last_seen, scan_gen) VALUES (?, NULL, X'', X'', 'directory', 'present', 0, 0, 0) RETURNING id`,
			string(id)).Scan(&rootID); err != nil {
			return err
		}
		return tx.QueryRow(`INSERT INTO jobs (kind, payload_version, payload, source_id, state, attempts,
			max_attempts, available_at, created_at, updated_at) VALUES ('scan', 1, '{}', ?, 'running', 0, 3, 0, 0, 0)
			RETURNING id`, string(id)).Scan(&job)
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := sources.New(e.st, sfs, config.Sources{AllowedRoots: []string{t.TempDir()}}, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	h := index.NewHandler(e.st, svc, rules.Default(), clock.Real{}, config.Defaults().Scan)
	j := jobs.Job{ID: domain.JobID(job), Kind: jobs.KindScan, PayloadVersion: 1, SourceID: id, Attempt: 1}
	if err := h.Run(ctx, j, scanRuntime{}); err != nil {
		t.Fatalf("scan %s: %v", id, err)
	}
	e.exec(t, `UPDATE jobs SET state = 'succeeded' WHERE id = ?`, job)
	return domain.EntryID(rootID)
}

// scanRuntime is a jobs.Runtime that never pauses the scan.
type scanRuntime struct{}

func (scanRuntime) Progress(map[string]int64)                        {}
func (scanRuntime) FSCall(string) func()                             { return func() {} }
func (scanRuntime) Yield(ctx context.Context) error                  { return ctx.Err() }
func (scanRuntime) UseSource(context.Context, domain.SourceID) error { return nil }
