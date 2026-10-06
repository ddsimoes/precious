package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"precious/internal/store"
)

// runBackup implements `precious backup --config <file> <destination>`: a
// consistent online copy of <state_dir>/precious.db (state-store spec "Consistent
// online backup"). It never creates the state directory or the database.
func runBackup(ctx context.Context, e env, args []string) int {
	flags, cfgPath := newFlags(e, "backup")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "usage: precious backup --config <file> <destination>")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(e.stderr, "precious backup: exactly one destination path is required")
		flags.Usage()
		return 2
	}
	cfg, ok := loadConfig(e.stderr, *cfgPath)
	if !ok {
		return 1
	}
	dest, err := filepath.Abs(flags.Arg(0))
	if err != nil {
		fmt.Fprintln(e.stderr, "precious backup:", err)
		return 1
	}

	dbPath := filepath.Join(cfg.StateDir, store.FileName)
	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(e.stderr, "precious backup: database %s does not exist; check state_dir in %s\n", dbPath, *cfgPath)
		return 1
	}
	if err := store.Backup(ctx, dbPath, dest); err != nil {
		fmt.Fprintln(e.stderr, "precious backup:", err)
		return 1
	}
	fmt.Fprintln(e.stdout, dest)
	return 0
}
