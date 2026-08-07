// Command catlogctl is the catlog admin CLI: a thin HTTP client for catlogd's
// loopback admin API (§5.9) plus the local-only key and test-vector utilities.
//
// It never opens a database file directly — Turso takes an exclusive whole-file
// lock that shuts other processes out entirely, even for reads (§5.4) — so
// every stateful verb goes through the admin API, and running catlogctl against
// a live server is safe.
//
// WP1 implements `keygen`, which touches no database and no network. The rest
// still report "not yet implemented" and land in WP2 (issue, testvectors), WP3
// (ban/unban/purge/denylist), WP4 (rebuild/seed/stats) and WP10 (archive).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/meow-sci/catlog/server/internal/config"
	"github.com/meow-sci/catlog/server/internal/keys"
)

const version = "0.1.0-dev"

// verb is an admin command: name → the work package that implements it.
type verb struct {
	name string
	wp   string
	help string
	run  func(args []string) error // nil until its WP lands
}

var verbs = []verb{
	{name: "keygen", wp: "WP1", help: "create data/keys/{license-signing.pem,session.key,pepper.key} if missing", run: runKeygen},
	{name: "issue", wp: "WP2", help: "issue a credential for a handle (dev/test path)"},
	{name: "testvectors", wp: "WP2", help: "generate the cross-language conformance vectors (§4.10)"},
	{name: "ban", wp: "WP3", help: "ban a player, revoke credentials, retire handles"},
	{name: "unban", wp: "WP3", help: "lift a ban"},
	{name: "purge", wp: "WP3", help: "delete all data for a player and leave a tombstone"},
	{name: "denylist", wp: "WP3", help: "regenerate the signed deny-list"},
	{name: "rebuild", wp: "WP4", help: "rebuild projections from seq 0 and swap"},
	{name: "seed", wp: "WP4", help: "insert the deterministic demo dataset"},
	{name: "stats", wp: "WP4", help: "print server counters"},
	{name: "archive", wp: "WP10", help: "run an archive pass"},
	{name: "backup", wp: "WP10", help: "quiesce the writer and copy events.db to a destination"},
}

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("catlogctl", version)
		return
	}

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	for _, v := range verbs {
		if v.name != args[0] {
			continue
		}
		if v.run == nil {
			fmt.Fprintf(os.Stderr, "catlogctl %s: not yet implemented (%s)\n", v.name, v.wp)
			os.Exit(1)
		}
		if err := v.run(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "catlogctl %s: %v\n", v.name, err)
			os.Exit(1)
		}
		return
	}

	fmt.Fprintf(os.Stderr, "catlogctl: unknown verb %q\n\n", args[0])
	usage()
	os.Exit(2)
}

func usage() {
	fmt.Fprintf(os.Stderr, "catlogctl %s — catlog admin CLI\n\nusage: catlogctl [flags] <verb> [args]\n\nverbs:\n", version)
	for _, v := range verbs {
		status := ""
		if v.run == nil {
			status = " (" + v.wp + ", not yet implemented)"
		}
		fmt.Fprintf(os.Stderr, "  %-12s %s%s\n", v.name, v.help, status)
	}
	fmt.Fprintf(os.Stderr, "\nflags:\n")
	flag.PrintDefaults()
}

// runKeygen creates the three long-lived secrets under <data dir>/keys, leaving
// any that already exist untouched (§4.5.1, §4.5.4, §4.7).
//
// It is purely local: no database, no network, no admin API. That is what makes
// it safe to run before catlogd has ever started — and it is idempotent, so
// running it against a configured server changes nothing.
func runKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "catlogd TOML config to read [data].dir from (optional)")
	dataDir := fs.String("data", "", "data directory, overriding the config (§3: ./data)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl keygen [-config catlogd.toml] [-data ./data]\n\n"+
			"Creates, if missing:\n"+
			"  <data>/keys/license-signing.pem   P-256 license signing key (§4.5.1)\n"+
			"  <data>/keys/session.key           32 B session cookie HMAC key (§4.5.4)\n"+
			"  <data>/keys/pepper.key            32 B user_key pepper (D17, §4.7)\n\n"+
			"Existing files are never overwritten. Nothing secret is printed.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *dataDir != "" {
		cfg.Data.Dir = *dataDir
	}

	// Ask what is missing before creating anything, so the report below can say
	// "created" versus "kept" honestly.
	dir := cfg.KeysDir()
	missing, err := keys.Created(dir)
	if err != nil {
		return err
	}

	set, err := keys.LoadOrCreate(dir)
	if err != nil {
		return err
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}

	// Print what happened and the public metadata only — never key material
	// (§5.11). The JWKS is public by definition (§4.5.1), so the kid is safe.
	fmt.Printf("keys directory: %s\n", abs)
	for _, name := range []string{keys.SigningFile, keys.SessionFile, keys.PepperFile} {
		state := "kept"
		for _, m := range missing {
			if m == name {
				state = "created"
				break
			}
		}
		fmt.Printf("  %-24s %s\n", name, state)
	}
	fmt.Printf("license signing kid: %s\n", set.Signing.KID)
	for _, r := range set.Retired {
		fmt.Printf("  retired (verify-only): %s\n", r.KID)
	}
	return nil
}
