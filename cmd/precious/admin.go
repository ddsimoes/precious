package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"precious/internal/auth"
	"precious/internal/clock"
)

// passwordTerminal reads passwords without echo. The command uses the real
// standard input; tests substitute a fake terminal.
type passwordTerminal interface {
	// IsTerminal reports whether input is an interactive terminal.
	IsTerminal() bool
	// ReadPassword shows prompt and reads one line without echo. It returns
	// ctx's error when ctx ends first (Ctrl-C).
	ReadPassword(ctx context.Context, prompt string) ([]byte, error)
}

// stdinTerminal reads from the process's standard input and prompts on
// standard error, so standard output stays clean.
type stdinTerminal struct {
	in     *os.File
	prompt io.Writer
}

func (t stdinTerminal) IsTerminal() bool {
	return t.in != nil && term.IsTerminal(int(t.in.Fd()))
}

func (t stdinTerminal) ReadPassword(ctx context.Context, prompt string) ([]byte, error) {
	fd := int(t.in.Fd())
	saved, err := term.GetState(fd)
	if err != nil {
		return nil, err
	}
	fmt.Fprint(t.prompt, prompt)
	type result struct {
		b   []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		b, err := term.ReadPassword(fd)
		done <- result{b, err}
	}()
	select {
	case r := <-done:
		fmt.Fprintln(t.prompt)
		return r.b, r.err
	case <-ctx.Done():
		// main traps SIGINT, so Ctrl-C arrives here rather than killing the
		// process; turn echo back on before giving up on the blocked read.
		_ = term.Restore(fd, saved)
		fmt.Fprintln(t.prompt)
		return nil, ctx.Err()
	}
}

// runAdmin implements `precious admin set-password --config <file>`.
func runAdmin(ctx context.Context, e env, args []string) int {
	return adminCommand(ctx, e, args, stdinTerminal{in: e.stdin, prompt: e.stderr})
}

func adminCommand(ctx context.Context, e env, args []string, tty passwordTerminal) int {
	if len(args) == 0 || args[0] != "set-password" {
		fmt.Fprintln(e.stderr, "usage: precious admin set-password --config <file>")
		return 2
	}
	fs, cfgPath := newFlags(e, "admin set-password")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(e.stderr, "usage: precious admin set-password --config <file>")
		return 2
	}
	cfg, ok := loadConfig(e.stderr, *cfgPath)
	if !ok {
		return 1
	}
	if !tty.IsTerminal() {
		fmt.Fprintln(e.stderr, "precious admin set-password: standard input is not a terminal; run the command interactively (passwords are never read from files or pipes)")
		return 1
	}
	password, err := readNewPassword(ctx, tty)
	if err != nil {
		fmt.Fprintln(e.stderr, "precious admin set-password:", err)
		return 1
	}
	defer clear(password)

	st, ok := openState(ctx, e.stderr, cfg)
	if !ok {
		return 1
	}
	defer st.Close()
	svc := auth.New(st, clock.Real{}, cfg.Auth, auth.Options{})
	revoked, err := svc.SetPassword(ctx, password)
	if err != nil {
		fmt.Fprintln(e.stderr, "precious admin set-password:", err)
		return 1
	}
	fmt.Fprintf(e.stdout, "Administrator password set for user %q; %d session(s) revoked.\n", auth.AdminUsername, revoked)
	return 0
}

// readNewPassword reads the password twice and checks length and match.
func readNewPassword(ctx context.Context, tty passwordTerminal) ([]byte, error) {
	first, err := tty.ReadPassword(ctx, fmt.Sprintf("New administrator password (%d-%d bytes): ", auth.MinPasswordBytes, auth.MaxPasswordBytes))
	if err != nil {
		return nil, fmt.Errorf("read password: %w", err)
	}
	if err := auth.ValidatePassword(first); err != nil {
		clear(first)
		return nil, err
	}
	second, err := tty.ReadPassword(ctx, "Repeat the password: ")
	if err != nil {
		clear(first)
		return nil, fmt.Errorf("read password: %w", err)
	}
	defer clear(second)
	if subtle.ConstantTimeCompare(first, second) != 1 {
		clear(first)
		return nil, errors.New("the passwords do not match; nothing was changed")
	}
	return first, nil
}
