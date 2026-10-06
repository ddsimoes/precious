// Command gencorpus writes the regression corpus (precious-spec §15, design
// D18) into a directory, and its ground truth next to it.
//
//	go run ./tools/gencorpus -out DIR [-fat] [-truth FILE]
//
// DIR must be missing or empty. The ground truth goes to FILE, by default
// ground_truth.json in DIR's parent, so that DIR holds exactly the entries the
// ground truth lists. -fat writes the small FAT-capability fixture instead of
// the corpus. The corpus's folder privado gets mode 000; run chmod 755 on it
// before removing DIR.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"precious/internal/corpus"
)

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "gencorpus:", err)
		}
		os.Exit(2)
	}
}

func run(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("gencorpus", flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "", "directory to write the tree into (missing or empty)")
	fat := flags.Bool("fat", false, "write the FAT-capability fixture instead of the corpus")
	truth := flags.String("truth", "", "ground truth file (default: ground_truth.json next to -out)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" || flags.NArg() > 0 {
		flags.Usage()
		return errors.New("usage: gencorpus -out DIR [-fat] [-truth FILE]")
	}
	dir, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if *truth == "" {
		*truth = filepath.Join(filepath.Dir(dir), "ground_truth.json")
	}
	tree := corpus.Corpus()
	if *fat {
		tree = corpus.FATFixture()
	}
	g, err := corpus.WriteDir(dir, tree)
	if err != nil {
		return err
	}
	if err := g.WriteFile(*truth); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "gencorpus: %d entries, %d bytes in %s; ground truth in %s\n", len(g.Entries), tree.Size(), dir, *truth)
	return nil
}
