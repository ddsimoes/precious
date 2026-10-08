package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"
	// Rescan schedules name IANA zones (r2b design D6); the embedded
	// database resolves them where the system has none, such as Windows.
	_ "time/tzdata"

	"precious/internal/auth"
	"precious/internal/cleanup"
	"precious/internal/clock"
	commandapi "precious/internal/commands"
	"precious/internal/config"
	"precious/internal/content"
	"precious/internal/dates"
	"precious/internal/decisions"
	"precious/internal/domain"
	"precious/internal/executor"
	"precious/internal/fsaccess"
	"precious/internal/index"
	"precious/internal/jobs"
	"precious/internal/organize"
	"precious/internal/relations"
	"precious/internal/review"
	"precious/internal/rules"
	"precious/internal/schedule"
	"precious/internal/sources"
	"precious/internal/store"
	"precious/internal/viewer"
	"precious/internal/web/api"
	"precious/internal/web/apierr"
	"precious/internal/web/authhttp"
	"precious/internal/web/clientip"
	"precious/internal/web/middleware"
	"precious/internal/web/spa"
	"precious/web"
)

// Periodic maintenance while serving.
const (
	checkpointInterval = 5 * time.Minute
	shutdownGrace      = 15 * time.Second
)

// serveDeps are the process-level dependencies of serve; tests inject them.
type serveDeps struct {
	Clock clock.Clock
	// UI is the built single-page app rooted at its index.html (web.Dist).
	UI fs.FS
	// Listener replaces listening on cfg.Server.Listen when non-nil.
	Listener net.Listener
	// Ready, when set, is called with the bound address once requests are served.
	Ready func(addr string)
}

func runServe(ctx context.Context, e env, args []string) int {
	fs, cfgPath := newFlags(e, "serve")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(e.stderr, "usage: precious serve --config <file>")
		return 2
	}
	cfg, ok := loadConfig(e.stderr, *cfgPath)
	if !ok {
		return 1
	}
	log := slog.New(slog.NewJSONHandler(e.stderr, nil))
	if err := serve(ctx, cfg, log, serveDeps{Clock: clock.Real{}, UI: web.Dist()}); err != nil {
		fmt.Fprintln(e.stderr, "precious serve:", err)
		return 1
	}
	return 0
}

// serve runs the web server, the job runner, the source refresh loop, and the
// rescan schedule loop until ctx is cancelled.
func serve(ctx context.Context, cfg config.Config, log *slog.Logger, d serveDeps) error {
	if cfg.Dates.TimeZone == "" {
		log.Warn(unsetZoneWarning, "zone", describeZone(time.Local, d.Clock.Now()))
	}
	origin, err := middleware.ParseOrigin(cfg.Server.ExternalOrigin)
	if err != nil {
		return err
	}
	trusted, err := clientip.ParseTrusted(cfg.Server.TrustedProxies)
	if err != nil {
		return err
	}
	if err := config.EnsureStateDir(cfg.StateDir); err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.StateDir, store.Options{Logger: log})
	if err != nil {
		return err
	}
	defer st.Close()

	srcs, err := sources.New(st, fsaccess.NewOS(), cfg.Sources, d.Clock)
	if err != nil {
		return err
	}
	runner, err := jobs.NewRunner(jobs.Options{Store: st, Registry: srcs, Config: cfg.Jobs, Clock: d.Clock, Logger: log})
	if err != nil {
		return err
	}
	// One policy value classifies at scan time and answers the read API, so
	// both always agree (design D9).
	pol := rules.Default()
	scanner := index.NewHandler(st, srcs, pol, d.Clock, cfg.Scan)
	hashing := content.NewService(st, srcs, d.Clock, cfg.Hashing, cfg.Archives, cfg.Duplicates)
	hashing.Register(runner)
	relate := relations.NewHandler(st, d.Clock, cfg.Duplicates, func(ctx context.Context, gen int64) error {
		return review.Refresh(ctx, st, gen)
	})
	relate.Register(runner)
	// Media dates (r5 design D2–D11): derived in the zone [dates] time_zone
	// resolves to; organize's date plans re-derive their targets with it.
	// The media job waits while its source is being organized, as a scan
	// does (D4).
	zone, err := cfg.Dates.Location()
	if err != nil {
		return err
	}
	mediaDates := dates.New(dates.Options{Store: st, Runner: runner, Sources: srcs, Zone: zone, Clock: d.Clock,
		Logger: log})
	mediaDates.DeferWhile(executor.OrganizeActive)
	mediaDates.Register(runner)
	// After each scan: hashing for every online source (sizes are shared
	// across sources), a relate pass, because classification feeds the
	// review lists even when no content changed (design D5), and the
	// source's media job.
	scanner.OnScanDone(func(ctx context.Context, src domain.SourceID) {
		hashing.AfterScan(ctx, src)
		if err := runner.Write(ctx, relations.RequestRefresh); err != nil && ctx.Err() == nil {
			log.Error("relate refresh after a scan", "source", src, "err", err)
		}
		mediaDates.AfterScan(ctx, src)
	})
	// Organizing (r3 design D3–D14): the executor follows each done step in
	// the index through organize's adapter, and a scan waits while a change
	// of its source is queued, running, or has a step recorded as started
	// (D10).
	org := organize.New(organize.Options{Store: st, Policy: pol, AllowWrites: cfg.Sources.AllowWrites,
		Clock: d.Clock, Logger: log, Dates: mediaDates})
	exec := executor.New(executor.Options{Store: st, Sources: srcs, Index: org.Index(),
		AllowWrites: cfg.Sources.AllowWrites, Clock: d.Clock, Logger: log, Content: hashing})
	exec.Register(runner)
	scanner.DeferWhile(executor.OrganizeActive)
	scanner.Register(runner)
	// Cleanup (r4 design D1–D16): its plans run on the executor through
	// run-action; its pre-delete check is a read-only job of its own.
	clean := cleanup.New(cleanup.Options{Store: st, Runner: runner, Sources: srcs, Content: hashing, Policy: pol,
		Executor: exec, AllowWrites: cfg.Sources.AllowWrites, Clock: d.Clock, Logger: log})
	clean.Register(runner)
	authSvc := auth.New(st, d.Clock, cfg.Auth, auth.Options{})
	shell, err := spa.New(d.UI)
	if err != nil {
		return err
	}
	handler := newHandler(handlerDeps{
		cfg:       cfg,
		log:       log,
		origin:    origin,
		trusted:   trusted,
		store:     st,
		auth:      authSvc,
		runner:    runner,
		sources:   srcs,
		policy:    pol,
		decisions: decisions.New(d.Clock, pol, index.StartScan),
		hashing:   hashing,
		organize:  org,
		cleanup:   clean,
		spa:       shell,
	})

	ln := d.Listener
	if ln == nil {
		if ln, err = net.Listen("tcp", cfg.Server.Listen); err != nil {
			return err
		}
	}
	// Request contexts derive from baseCtx, which is cancelled at shutdown so
	// long-lived event streams end instead of holding Shutdown open.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	if err := runner.Start(ctx); err != nil {
		ln.Close()
		return err
	}
	if err := hashing.Startup(ctx); err != nil {
		log.Error("enqueue hashing at start", "err", err)
	}
	if err := relate.Startup(ctx, runner); err != nil {
		log.Error("enqueue relate at start", "err", err)
	}
	if err := exec.Startup(ctx, runner); err != nil {
		log.Error("check interrupted changes at start", "err", err)
	}
	if err := mediaDates.Startup(ctx); err != nil {
		log.Error("request the media jobs at start", "err", err)
	}
	refreshCtx, stopRefresh := context.WithCancel(ctx)
	defer stopRefresh()
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		srcs.Run(refreshCtx, log)
	}()
	// Scheduled rescans (r2b design D6) start their scans on the runner, so
	// the loop stops before it.
	scheduleCtx, stopSchedule := context.WithCancel(ctx)
	defer stopSchedule()
	scheduleDone := make(chan struct{})
	go func() {
		defer close(scheduleDone)
		schedule.New(st, runner, srcs, d.Clock, log).Run(scheduleCtx)
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	log.Info("serving", "addr", ln.Addr().String(), "origin", origin.String())
	if d.Ready != nil {
		d.Ready(ln.Addr().String())
	}

	checkpoint := time.NewTicker(checkpointInterval)
	defer checkpoint.Stop()
	var runErr error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case err := <-serveErr:
			runErr = err
			break loop
		case <-checkpoint.C:
			if err := st.Checkpoint(ctx); err != nil && ctx.Err() == nil {
				log.Warn("wal checkpoint failed", "err", err)
			}
		}
	}

	log.Info("shutting down")
	cancelBase()
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	shutdownErr := srv.Shutdown(sctx)
	stopSchedule()
	<-scheduleDone
	// Running jobs stop at their next checkpoint and resume at the next start.
	stopErr := runner.Stop(sctx)
	stopRefresh()
	<-refreshDone
	if errors.Is(runErr, http.ErrServerClosed) {
		runErr = nil
	}
	return errors.Join(runErr, shutdownErr, stopErr)
}

type handlerDeps struct {
	cfg       config.Config
	log       *slog.Logger
	origin    middleware.Origin
	trusted   []netip.Prefix
	store     *store.Store
	auth      *auth.Service
	runner    *jobs.Runner
	sources   *sources.Service
	policy    *rules.Policy
	decisions *decisions.Service
	hashing   *content.Service
	organize  *organize.Service
	cleanup   *cleanup.Service
	spa       http.Handler
}

// newHandler composes the routes and the browser-protection chain, outermost
// first: security headers, client address, body limit, session, authentication,
// CSRF/origin, routes. Unknown /api paths answer 404 not_found as JSON; every
// other path is the single-page app (design D13).
func newHandler(d handlerDeps) http.Handler {
	cookie := middleware.NewSessionCookie(d.origin)
	mux := http.NewServeMux()
	authhttp.Register(mux, d.auth, cookie)

	cmds := commandapi.New(commandapi.Options{
		Store:        d.store,
		Jobs:         d.runner,
		MaxBodyBytes: d.cfg.Server.MaxRequestBytes,
		Logger:       d.log,
	})
	sources.RegisterCommands(cmds, d.sources)
	index.RegisterCommands(cmds)
	decisions.RegisterCommands(cmds, d.decisions)
	content.RegisterCommands(cmds, d.hashing)
	review.RegisterCommands(cmds, d.decisions)
	d.organize.RegisterCommands(cmds)
	d.cleanup.RegisterCommands(cmds)
	mux.Handle("POST /api/commands/{name}", cmds)
	mux.Handle("GET /api/jobs/{id}", jobs.NewStatusHandler(d.runner))
	mux.Handle("GET /api/events", jobs.NewEventsHandler(d.runner))
	sources.Register(mux, d.sources, d.log)
	api.Register(mux, d.store, d.policy, d.log)
	d.organize.Routes(mux)
	d.cleanup.Routes(mux)
	viewer.Register(mux, d.store, d.sources, d.hashing, d.log)
	mux.HandleFunc("/api", apiNotFound)
	mux.HandleFunc("/api/", apiNotFound)
	mux.Handle("/", d.spa)

	var h http.Handler = mux
	h = middleware.CSRF(d.origin)(h)
	h = middleware.RequireAuth(h)
	h = middleware.LoadSession(d.auth, cookie)(h)
	h = middleware.BodyLimit(d.cfg.Server.MaxRequestBytes)(h)
	h = clientip.Middleware(d.trusted)(h)
	return middleware.SecurityHeaders(h)
}

func apiNotFound(w http.ResponseWriter, _ *http.Request) {
	apierr.Write(w, http.StatusNotFound, domain.CodeNotFound, "unknown API endpoint")
}
