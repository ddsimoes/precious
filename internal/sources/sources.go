// Package sources is the source registry (§6.1, design D4/D5): the sources
// table, where each source's volume is mounted now, the picker that is the
// only way to name a folder, and the add-source, rename-source,
// remove-source, and set-source-schedule commands.
//
// A source is recorded as a volume identity plus its root folder relative to
// that volume, never as an absolute path. Resolving it reads the current mount
// table: the source is online when a mount of its volume is present and its
// root opens, unavailable when the mount is present but the root cannot be
// opened, and offline when no mount of the volume is present. Availability is
// refreshed every minute (Run), whenever the sources list is read, and
// whenever a source is opened; a refresh writes only state, state_reason,
// mount_point, capabilities, and device_key, and never touches entries.
package sources

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"precious/internal/clock"
	"precious/internal/config"
	"precious/internal/domain"
	"precious/internal/fsaccess"
	"precious/internal/jobs"
	"precious/internal/store"
)

// State is a source's availability.
type State string

const (
	// StateOnline: the volume is mounted and the root folder opens.
	StateOnline State = "online"
	// StateOffline: no mount of the volume is present.
	StateOffline State = "offline"
	// StateUnavailable: the volume is mounted but the root folder cannot be
	// opened.
	StateUnavailable State = "unavailable"
)

// State reasons, stored in sources.state_reason and shown with the state.
const (
	// ReasonNotMounted: no mount of the source's volume is present.
	ReasonNotMounted = "volume_not_mounted"
	// ReasonRootMissing: the volume is mounted but the root folder is gone.
	ReasonRootMissing = "root_missing"
	// ReasonRootUnreadable: the root folder exists but may not be read.
	ReasonRootUnreadable = "root_unreadable"
	// ReasonRootUnavailable: opening the root folder failed with an I/O,
	// stale, or disconnected-mount error.
	ReasonRootUnavailable = "root_unavailable"
	// ReasonRootCovered: the root folder's path leads to another filesystem,
	// mounted over it or reached through a symbolic link.
	ReasonRootCovered = "root_covered"
)

// RefreshInterval is how often Run refreshes availability.
const RefreshInterval = time.Minute

// Source is one row of the registry.
type Source struct {
	ID    domain.SourceID
	Label string
	// Volume is the identity the source was added on. DeviceKey is the claim
	// key of the device it was last seen on.
	Volume fsaccess.Volume
	// RelRoot is the root folder below the volume's root, '/'-joined, empty
	// for the volume root. For a path volume it is relative to the mount point,
	// which is the volume's only identity.
	RelRoot     []byte
	State       State
	StateReason string
	// MountPoint is where the volume is mounted; empty when offline.
	MountPoint string
	Caps       fsaccess.Capabilities
	RootEntry  domain.EntryID
	ScanGen    int64
	LastScanAt *time.Time
	// Schedule is the rescan schedule, nil when off (r2b design D6), and
	// NextScanAt its next due time, nil when off.
	Schedule   *domain.Schedule
	NextScanAt *time.Time
	// SkippedAt is the last due time that was skipped, and SkipReason why:
	// the source's state then. Both are unset when the last due time ran.
	SkippedAt  *time.Time
	SkipReason string
	// WriteEnabled is the owner's write permission (r3 design D1), off
	// until the owner turns it on. Writes also need WritesUnavailable to be
	// empty; CheckWrites checks both.
	WriteEnabled bool
}

// Opened is an online source with its root folder open. The caller closes
// Root.
type Opened struct {
	Source  Source
	Root    fsaccess.Dir
	AbsRoot string
}

// Service is the source registry. It implements jobs.Registry.
type Service struct {
	st  *store.Store
	fs  fsaccess.FS
	clk clock.Clock

	// roots are the allowed roots, cleaned and resolved through symlinks.
	roots []string
	// stateDir is the resolved state directory, which no source may touch.
	stateDir string
	// key signs picker handles; it is made at start, so a restart
	// invalidates every handle.
	key []byte

	// refresh coalesces concurrent refreshes (Refresh).
	refreshMu sync.Mutex
	running   bool
	pending   *flight
}

// flight is one refresh that callers wait for.
type flight struct {
	done chan struct{}
	err  error
}

var _ jobs.Registry = (*Service)(nil)

// New returns the registry over st and fsys. Allowed roots are cfg's, or the
// platform defaults that exist when cfg has none, resolved through symlinks
// once here. The picker's signing key is generated here.
func New(st *store.Store, fsys fsaccess.FS, cfg config.Sources, clk clock.Clock) (*Service, error) {
	roots, err := allowedRoots(cfg.AllowedRoots)
	if err != nil {
		return nil, err
	}
	stateDir, err := filepath.EvalSymlinks(filepath.Dir(st.Path()))
	if err != nil {
		return nil, fmt.Errorf("sources: resolve the state directory: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("sources: picker key: %w", err)
	}
	if clk == nil {
		clk = clock.Real{}
	}
	return &Service{st: st, fs: fsys, clk: clk, roots: roots, stateDir: stateDir, key: key}, nil
}

// AllowedRoots returns the resolved allowed roots, in the order the picker
// lists them.
func (s *Service) AllowedRoots() []string { return append([]string(nil), s.roots...) }

const sourceColumns = `s.id, s.label, s.volume_kind, s.volume_id, s.volume_label, s.fs_type, s.strong,
	s.rel_root, s.device_key, s.capabilities, s.state, s.state_reason, s.mount_point, s.scan_gen,
	s.last_scan_at, (SELECT e.id FROM entries e WHERE e.source_id = s.id AND e.path = X''),
	s.scan_schedule, s.next_scan_at, s.schedule_skipped_at, s.schedule_skip_reason, s.write_enabled`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSource(r rowScanner) (Source, error) {
	var (
		src                            Source
		kind, caps, state              string
		volumeLabel, deviceKey, reason sql.NullString
		mountPoint                     []byte
		strong                         int64
		lastScanAt, rootEntry          sql.NullInt64
		schedule, skipReason           sql.NullString
		nextScanAt, skippedAt          sql.NullInt64
	)
	err := r.Scan(&src.ID, &src.Label, &kind, &src.Volume.ID, &volumeLabel, &src.Volume.FSType, &strong,
		&src.RelRoot, &deviceKey, &caps, &state, &reason, &mountPoint, &src.ScanGen, &lastScanAt, &rootEntry,
		&schedule, &nextScanAt, &skippedAt, &skipReason, &src.WriteEnabled)
	if err != nil {
		return Source{}, err
	}
	src.Volume.Kind = fsaccess.VolumeKind(kind)
	src.Volume.Label = volumeLabel.String
	src.Volume.Strong = strong != 0
	src.Volume.DeviceKey = deviceKey.String
	if src.RelRoot == nil {
		src.RelRoot = []byte{}
	}
	if err := json.Unmarshal([]byte(caps), &src.Caps); err != nil {
		return Source{}, fmt.Errorf("sources: source %s capabilities: %w", src.ID, err)
	}
	src.State = State(state)
	src.StateReason = reason.String
	src.MountPoint = string(mountPoint)
	if lastScanAt.Valid {
		t := clock.FromMillis(lastScanAt.Int64)
		src.LastScanAt = &t
	}
	src.RootEntry = domain.EntryID(rootEntry.Int64)
	if schedule.Valid {
		var sch domain.Schedule
		if err := json.Unmarshal([]byte(schedule.String), &sch); err != nil {
			return Source{}, fmt.Errorf("sources: source %s schedule: %w", src.ID, err)
		}
		src.Schedule = &sch
	}
	src.NextScanAt = optTime(nextScanAt)
	src.SkippedAt = optTime(skippedAt)
	src.SkipReason = skipReason.String
	return src, nil
}

// optTime is a stored Unix-millisecond time, nil when NULL.
func optTime(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	t := clock.FromMillis(ms.Int64)
	return &t
}

// List returns every source, by label then ID, as last recorded.
func (s *Service) List(ctx context.Context) ([]Source, error) {
	return listSources(ctx, s.st.Reader())
}

func listSources(ctx context.Context, q store.Queryer) ([]Source, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+sourceColumns+` FROM sources s ORDER BY s.label, s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// Get returns one source as last recorded; an unknown ID is a domain
// unknown_source error.
func (s *Service) Get(ctx context.Context, id domain.SourceID) (Source, error) {
	return getSource(ctx, s.st.Reader(), id)
}

func getSource(ctx context.Context, q store.Queryer, id domain.SourceID) (Source, error) {
	src, err := scanSource(q.QueryRowContext(ctx, `SELECT `+sourceColumns+` FROM sources s WHERE s.id = ?`, string(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Source{}, domain.Errorf(domain.CodeUnknownSource, "unknown source %q", id)
	}
	return src, err
}

// Open resolves the source's volume in the current mount table and opens its
// root folder, recording the availability it observed. A source that is not
// online is a domain source_offline error; an unknown ID is unknown_source.
func (s *Service) Open(ctx context.Context, id domain.SourceID) (Opened, error) {
	src, err := s.Get(ctx, id)
	if err != nil {
		return Opened{}, err
	}
	view, err := s.mountView()
	if err != nil {
		return Opened{}, err
	}
	obs := s.observe(src, view)
	if err := s.record(ctx, []Source{src}, []observation{obs}); err != nil {
		obs.close()
		return Opened{}, err
	}
	src = obs.applyTo(src)
	if obs.state != StateOnline {
		return Opened{}, domain.Errorf(domain.CodeSourceOffline, "source %q is %s (%s)", id, obs.state, obs.reason)
	}
	return Opened{Source: src, Root: obs.dir, AbsRoot: obs.abs}, nil
}

// Refresh re-evaluates the availability of every source. A call never joins
// a refresh that started before it, so the result reflects the mounts present
// when it was called; concurrent calls share one refresh. It returns ctx's
// error when ctx ends first, while the refresh completes in the background.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	if s.pending == nil {
		s.pending = &flight{done: make(chan struct{})}
	}
	f := s.pending
	if !s.running {
		s.startRefreshLocked()
	}
	s.refreshMu.Unlock()
	select {
	case <-f.done:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startRefreshLocked runs the pending flight. Callers hold refreshMu.
func (s *Service) startRefreshLocked() {
	f := s.pending
	s.pending, s.running = nil, true
	go func() {
		f.err = s.refreshAll(context.Background())
		close(f.done)
		s.refreshMu.Lock()
		s.running = false
		if s.pending != nil {
			s.startRefreshLocked()
		}
		s.refreshMu.Unlock()
	}()
}

func (s *Service) refreshAll(ctx context.Context) error {
	srcs, err := s.List(ctx)
	if err != nil || len(srcs) == 0 {
		return err
	}
	view, err := s.mountView()
	if err != nil {
		return err
	}
	obs := make([]observation, len(srcs))
	for i, src := range srcs {
		obs[i] = s.observe(src, view)
		obs[i].close()
	}
	return s.record(ctx, srcs, obs)
}

// Run refreshes availability every RefreshInterval, starting at once, until
// ctx ends. Failures are logged.
func (s *Service) Run(ctx context.Context, log *slog.Logger) {
	s.run(ctx, log, RefreshInterval)
}

func (s *Service) run(ctx context.Context, log *slog.Logger, every time.Duration) {
	if log == nil {
		log = slog.Default()
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
			log.Warn("sources: availability refresh failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// DeviceKey implements jobs.Registry: the claim key recorded for source's
// device, empty while unknown.
func (s *Service) DeviceKey(ctx context.Context, q store.Queryer, source domain.SourceID) (string, error) {
	var key sql.NullString
	err := q.QueryRowContext(ctx, `SELECT device_key FROM sources WHERE id = ?`, string(source)).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.Errorf(domain.CodeUnknownSource, "unknown source %q", source)
	}
	if err != nil {
		return "", err
	}
	// Pool keys are the runner's own; a volume never claims under one.
	if strings.HasPrefix(key.String, "workers:") {
		return "", nil
	}
	return key.String, nil
}

// SetUnresponsive implements jobs.Registry: it records the earliest start of
// the filesystem calls on source flagged by the watchdog, and a zero since
// clears the flag.
func (s *Service) SetUnresponsive(ctx context.Context, source domain.SourceID, since time.Time) error {
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		if since.IsZero() {
			_, err := tx.ExecContext(ctx, `UPDATE sources SET unresponsive_since = NULL WHERE id = ?`, string(source))
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE sources SET unresponsive_since = COALESCE(unresponsive_since, ?) WHERE id = ?`,
			clock.Millis(since), string(source))
		return err
	})
}

// mountsView is one reading of the mount table, read again after a probe.
type mountsView struct {
	fs     fsaccess.FS
	mounts []fsaccess.Mount
}

func (s *Service) mountView() (*mountsView, error) {
	m, err := s.fs.Mounts()
	if err != nil {
		return nil, err
	}
	return &mountsView{fs: s.fs, mounts: m}, nil
}

func (v *mountsView) reread() error {
	m, err := v.fs.Mounts()
	if err != nil {
		return err
	}
	v.mounts = m
	return nil
}

// observation is what resolving one source found.
type observation struct {
	state      State
	reason     string
	mountPoint string
	caps       fsaccess.Capabilities
	deviceKey  string
	// dir and abs are set when online.
	dir fsaccess.Dir
	abs string
}

func (o *observation) close() {
	if o.dir != nil {
		o.dir.Close()
		o.dir = nil
	}
}

// applyTo returns src with the observed availability.
func (o *observation) applyTo(src Source) Source {
	src.State, src.StateReason, src.MountPoint = o.state, o.reason, o.mountPoint
	src.Caps, src.Volume.DeviceKey = o.caps, o.deviceKey
	return src
}

// observe resolves src (design D4): a mount of its volume whose root holds
// src's folder, then its root folder opened and checked to be on that mount.
func (s *Service) observe(src Source, view *mountsView) observation {
	m, abs, ok := locate(view.mounts, src)
	var probe fsaccess.Dir
	if !ok && src.Volume.Kind == fsaccess.VolumePath {
		// A backend may list a path volume only once a root on it was opened
		// (the portable backend lists the roots opened so far), so a path
		// source is probed at its only possible location.
		d, err := s.fs.OpenRoot(pathRoot(src))
		if err == nil {
			if err := view.reread(); err == nil {
				m, abs, ok = locate(view.mounts, src)
			}
			if ok {
				probe = d
			} else {
				d.Close()
			}
		}
	}
	if !ok {
		return observation{state: StateOffline, reason: ReasonNotMounted, caps: src.Caps, deviceKey: src.Volume.DeviceKey}
	}
	obs := observation{state: StateUnavailable, mountPoint: m.Point, caps: src.Caps, deviceKey: m.Volume.DeviceKey}
	if caps, err := s.fs.Capabilities(abs); err == nil {
		obs.caps = caps
	}
	fail := func(reason string) observation {
		if probe != nil {
			probe.Close()
		}
		obs.reason = reason
		return obs
	}
	if c, ok := mountFor(view.mounts, abs); !ok || c.Point != m.Point || c.Volume.Kind != m.Volume.Kind || c.Volume.ID != m.Volume.ID {
		return fail(ReasonRootCovered)
	}
	d := probe
	if d == nil {
		var err error
		if d, err = s.fs.OpenRoot(abs); err != nil {
			return fail(rootReason(err))
		}
	}
	probe = d
	info, err := d.FSInfo()
	if err != nil {
		return fail(rootReason(err))
	}
	if info.Mount != nil && filepath.Clean(info.Mount.MountPoint) != m.Point {
		return fail(ReasonRootCovered)
	}
	obs.state, obs.dir, obs.abs = StateOnline, d, abs
	return obs
}

// rootReason names why a root folder could not be opened.
func rootReason(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ReasonRootMissing
	case errors.Is(err, fs.ErrPermission):
		return ReasonRootUnreadable
	}
	if o, ok := fsaccess.OutcomeOf(err); ok && o == domain.OutcomeUnreadable {
		return ReasonRootUnreadable
	}
	return ReasonRootUnavailable
}

// record writes the observations that differ from what srcs hold, in one
// transaction. A source removed meanwhile is left removed.
func (s *Service) record(ctx context.Context, srcs []Source, obs []observation) error {
	type change struct {
		id                          domain.SourceID
		state                       State
		reason, mount, caps, devKey any
	}
	var changes []change
	for i, src := range srcs {
		o := &obs[i]
		if o.state == src.State && o.reason == src.StateReason && o.mountPoint == src.MountPoint &&
			o.caps == src.Caps && o.deviceKey == src.Volume.DeviceKey {
			continue
		}
		caps, err := json.Marshal(o.caps)
		if err != nil {
			return err
		}
		changes = append(changes, change{
			id: src.ID, state: o.state, reason: nullString(o.reason), mount: nullBlob(o.mountPoint),
			caps: string(caps), devKey: nullString(o.deviceKey),
		})
	}
	if len(changes) == 0 {
		return nil
	}
	return s.st.Write(ctx, func(tx *sql.Tx) error {
		for _, c := range changes {
			if _, err := tx.ExecContext(ctx, `UPDATE sources SET state = ?, state_reason = ?, mount_point = ?,
				capabilities = ?, device_key = ? WHERE id = ?`,
				string(c.state), c.reason, c.mount, c.caps, c.devKey, string(c.id)); err != nil {
				return err
			}
		}
		return nil
	})
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullBlob(s string) any {
	if s == "" {
		return nil
	}
	return []byte(s)
}
