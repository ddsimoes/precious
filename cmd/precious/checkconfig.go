package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/BurntSushi/toml"

	"precious/internal/config"
)

// runCheckConfig implements `precious check-config --config <file>`. It loads
// the file strictly (unknown keys and every startup safety rule) without
// touching the state directory. On success it prints the effective settings,
// file values over defaults, as TOML on stdout, with the time zone media
// dates are read in, warns on stderr when dates.time_zone is unset, and exits
// 0; when the file is invalid it prints every problem on stderr and exits 1.
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
	now := time.Now()
	if err := writeEffectiveConfig(e.stdout, *path, cfg, now); err != nil {
		fmt.Fprintln(e.stderr, "precious:", err)
		return 1
	}
	if cfg.Dates.TimeZone == "" {
		fmt.Fprintf(e.stderr, "precious check-config: warning: %s\n", unsetZoneWarning)
	}
	return 0
}

// unsetZoneWarning is what check-config and the server's start say when
// dates.time_zone is unset (R5 design D7).
const unsetZoneWarning = "dates.time_zone is unset, so media dates are read in the server's local zone; " +
	"set it to the IANA zone the cameras' clocks were set to, such as \"America/Sao_Paulo\""

// describeZone names loc for the operator with its abbreviation and UTC
// offset at now, such as "America/Sao_Paulo (-03, UTC-03:00)". time.Local is
// "the server's local zone": its name is always "Local".
func describeZone(loc *time.Location, now time.Time) string {
	name := loc.String()
	if loc == time.Local {
		name = "the server's local zone"
	}
	return name + " (" + now.In(loc).Format("MST, UTC-07:00") + ")"
}

// writeEffectiveConfig prints cfg as TOML that config.Load accepts unchanged,
// headed by the time zone media dates are read in, described at now.
func writeEffectiveConfig(w io.Writer, path string, cfg config.Config, now time.Time) error {
	loc, err := cfg.Dates.Location()
	if err != nil {
		return err
	}
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
	if cfg.Dates.TimeZone == "" {
		fmt.Fprintf(&b, "# dates.time_zone is unset: media dates are read in %s.\n", describeZone(loc, now))
	} else {
		fmt.Fprintf(&b, "# Media dates are read in %s.\n", describeZone(loc, now))
	}
	b.WriteString("\n")
	if err := toml.NewEncoder(&b).Encode(cfg); err != nil {
		return fmt.Errorf("encode effective configuration: %w", err)
	}
	_, err = w.Write(b.Bytes())
	return err
}
