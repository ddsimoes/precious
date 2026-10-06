// Command precious is the Precious server and its local administration
// commands.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// env carries process I/O so commands are testable.
type env struct {
	stdin  *os.File
	stdout io.Writer
	stderr io.Writer
}

// command is one subcommand. Each lives in its own file.
type command struct {
	name    string
	usage   string
	summary string
	run     func(ctx context.Context, e env, args []string) int
}

func commands() []command {
	return []command{
		{"serve", "serve --config <file>", "run the web server and job runner", runServe},
		{"check-config", "check-config --config <file>", "validate the configuration and print effective settings", runCheckConfig},
		{"admin", "admin set-password --config <file>", "set or reset the administrator password (interactive)", runAdmin},
		{"backup", "backup --config <file> <destination>", "write a consistent copy of the database", runBackup},
		{"version", "version", "print the build version", runVersion},
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, env{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}, os.Args[1:])
	stop()
	os.Exit(code)
}

func run(ctx context.Context, e env, args []string) int {
	if len(args) == 0 {
		usage(e.stderr)
		return 2
	}
	for _, c := range commands() {
		if c.name == args[0] {
			return c.run(ctx, e, args[1:])
		}
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(e.stdout)
		return 0
	}
	fmt.Fprintf(e.stderr, "precious: unknown command %q\n\n", args[0])
	usage(e.stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: precious <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-44s %s\n", c.usage, c.summary)
	}
}
