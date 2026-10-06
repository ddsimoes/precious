package main

import (
	"context"
	"fmt"
	"runtime/debug"
)

// version is set at build time with -ldflags "-X main.version=<v>".
var version = "dev"

func buildVersion() string {
	v := version
	if info, ok := debug.ReadBuildInfo(); ok {
		var rev, modified string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				modified = s.Value
			}
		}
		if rev != "" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			v += " (" + rev
			if modified == "true" {
				v += "-dirty"
			}
			v += ")"
		}
		v += " " + info.GoVersion
	}
	return v
}

func runVersion(_ context.Context, e env, _ []string) int {
	fmt.Fprintln(e.stdout, "precious", buildVersion())
	return 0
}
