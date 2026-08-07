package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/meow-sci/catlog/server/internal/testvectors"
)

// runTestvectors implements `catlogctl testvectors generate <dir>` (§4.10,
// §5.9).
//
// Purely local: no database, no network, no clock. Regenerating over an
// existing directory rewrites every file with identical bytes, which is what
// `make testvectors` relies on to leave a clean working tree.
func runTestvectors(args []string) error {
	fs := flag.NewFlagSet("testvectors", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl testvectors generate <dir>\n\n"+
			"Writes the §4.10 cross-language conformance vectors into <dir>\n"+
			"(conventionally contracts/testdata). Generation is deterministic:\n"+
			"fixed keys, fixed timestamps, RFC 6979 signatures — regenerating is\n"+
			"byte-identical, and both the Go and C# suites read the result.\n")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 || fs.Arg(0) != "generate" {
		fs.Usage()
		return fmt.Errorf("expected: testvectors generate <dir>")
	}

	dir := fs.Arg(1)
	if err := testvectors.Generate(dir); err != nil {
		return err
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	fmt.Printf("conformance vectors written: %s\n", abs)
	for _, rel := range testvectors.Files {
		fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		fmt.Printf("  %-32s %6d B\n", rel, fi.Size())
	}
	return nil
}
