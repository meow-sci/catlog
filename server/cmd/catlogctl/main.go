// Command catlogctl is the catlog admin CLI: a thin HTTP client for catlogd's
// loopback admin API (§5.9) plus the local-only key and test-vector utilities.
//
// It never opens a database file directly — Turso allows exactly one process per
// DB file (§5.4), so every stateful verb goes through the admin API.
//
// WP0 scaffolding: verbs are declared here for discoverability and all report
// "not yet implemented"; they land in WP1 (keygen), WP2 (issue, testvectors),
// WP3 (ban/unban/purge/denylist), WP4 (rebuild/seed/stats) and WP10 (archive).
package main

import (
	"flag"
	"fmt"
	"os"
)

const version = "0.1.0-dev"

// verb is an admin command: name → the work package that implements it.
type verb struct {
	name string
	wp   string
	help string
}

var verbs = []verb{
	{"keygen", "WP1", "create data/keys/{license-signing.pem,session.key,pepper.key} if missing"},
	{"issue", "WP2", "issue a credential for a handle (dev/test path)"},
	{"testvectors", "WP2", "generate the cross-language conformance vectors (§4.10)"},
	{"ban", "WP3", "ban a player, revoke credentials, retire handles"},
	{"unban", "WP3", "lift a ban"},
	{"purge", "WP3", "delete all data for a player and leave a tombstone"},
	{"denylist", "WP3", "regenerate the signed deny-list"},
	{"rebuild", "WP4", "rebuild projections from seq 0 and swap"},
	{"seed", "WP4", "insert the deterministic demo dataset"},
	{"stats", "WP4", "print server counters"},
	{"archive", "WP10", "run an archive pass"},
	{"backup", "WP10", "quiesce the writer and copy events.db to a destination"},
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
		if v.name == args[0] {
			fmt.Fprintf(os.Stderr, "catlogctl %s: not yet implemented (%s)\n", v.name, v.wp)
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stderr, "catlogctl: unknown verb %q\n\n", args[0])
	usage()
	os.Exit(2)
}

func usage() {
	fmt.Fprintf(os.Stderr, "catlogctl %s — catlog admin CLI\n\nusage: catlogctl [flags] <verb> [args]\n\nverbs:\n", version)
	for _, v := range verbs {
		fmt.Fprintf(os.Stderr, "  %-12s %s (%s)\n", v.name, v.help, v.wp)
	}
	fmt.Fprintf(os.Stderr, "\nflags:\n")
	flag.PrintDefaults()
}
