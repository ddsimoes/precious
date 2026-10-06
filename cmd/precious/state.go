package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"precious/internal/config"
	"precious/internal/store"
)

// newFlags returns a flag set that reports errors to e.stderr and registers the
// common --config flag.
func newFlags(e env, name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("precious "+name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	cfg := fs.String("config", "", "path to the TOML configuration file (required)")
	return fs, cfg
}

// loadConfig loads and validates the configuration named by --config.
func loadConfig(w io.Writer, path string) (config.Config, bool) {
	if path == "" {
		fmt.Fprintln(w, "precious: --config is required")
		return config.Config{}, false
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(w, "precious:", err)
		return config.Config{}, false
	}
	return cfg, true
}

// openState ensures the private state directory exists and opens the database.
func openState(ctx context.Context, w io.Writer, cfg config.Config) (*store.Store, bool) {
	if err := config.EnsureStateDir(cfg.StateDir); err != nil {
		fmt.Fprintln(w, "precious:", err)
		return nil, false
	}
	st, err := store.Open(ctx, cfg.StateDir, store.Options{Logger: slog.New(slog.NewTextHandler(w, nil))})
	if err != nil {
		fmt.Fprintln(w, "precious:", err)
		return nil, false
	}
	return st, true
}
