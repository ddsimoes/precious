package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/BurntSushi/toml"

	"precious/internal/config"
)

// runCheckConfig implements `precious check-config --config <file>`. It loads
// the file strictly (unknown keys and every startup safety rule) without
// touching the state directory. On success it prints the effective settings,
// file values over defaults, as TOML on stdout and exits 0; when the file is
// invalid it prints every problem on stderr and exits 1.
func runCheckConfig(_ context.Context, e env, args []string) int {
	fs, path := newFlags(e, "check-config")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(e.stderr, "precious check-config: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	cfg, ok := loadConfig(e.stderr, *path)
	if !ok {
		return 1
	}
	if err := writeEffectiveConfig(e.stdout, *path, cfg); err != nil {
		fmt.Fprintln(e.stderr, "precious:", err)
		return 1
	}
	return 0
}

// writeEffectiveConfig prints cfg as TOML that config.Load accepts unchanged.
func writeEffectiveConfig(w io.Writer, path string, cfg config.Config) error {
	// Print empty lists explicitly so every setting appears.
	if cfg.Server.TrustedProxies == nil {
		cfg.Server.TrustedProxies = []string{}
	}
	if cfg.Sources.AllowedRoots == nil {
		cfg.Sources.AllowedRoots = []string{}
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Effective precious configuration loaded from %q (file values over defaults).\n", path)
	if len(cfg.Sources.AllowedRoots) == 0 {
		b.WriteString("# sources.allowed_roots is empty: the picker offers the platform default roots.\n")
	}
	b.WriteString("\n")
	if err := toml.NewEncoder(&b).Encode(cfg); err != nil {
		return fmt.Errorf("encode effective configuration: %w", err)
	}
	_, err := w.Write(b.Bytes())
	return err
}
