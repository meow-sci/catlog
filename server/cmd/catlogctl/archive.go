package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/meow-sci/catlog/server/internal/adminapi"
	"github.com/meow-sci/catlog/server/internal/archive"
	"github.com/meow-sci/catlog/server/internal/projector"
)

// runArchive implements `catlogctl archive` (§5.9, §5.10): copy the event log
// past the archive cursor into the archive store.
//
// It archives; it never prunes. The local event log is left exactly as it was —
// the archive is a second copy, and deciding when a first copy may be deleted is
// a separate decision that has not been taken (§5.10).
func runArchive(args []string) error {
	fs := flag.NewFlagSet("archive", flag.ContinueOnError)
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	asJSON := fs.Bool("json", false, "print the raw JSON response")
	all := fs.Bool("all", false, "keep running passes until the log is fully archived")
	timeout := fs.Duration("timeout", 30*time.Minute, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl archive [-admin URL] [-all] [-json]\n\n"+
			"Copies every event past the archive cursor into data/archive/ as one zstd\n"+
			"NDJSON chunk per player, updates each player's manifest.json and advances the\n"+
			"cursor (§5.10). One pass copies at most 100k events; -all repeats until the\n"+
			"log is caught up.\n\n"+
			"Archiving copies and never deletes: nothing leaves events.db.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	base, err := adminBaseURL(*admin, *cfgPath)
	if err != nil {
		return err
	}

	for pass := 1; ; pass++ {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		var res archive.RunResult
		err := callAdmin(ctx, http.MethodPost, base+"/admin/archive/run", struct{}{}, &res)
		cancel()
		if err != nil {
			return err
		}

		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(res); err != nil {
				return err
			}
		} else {
			printRun(res)
		}
		if !*all || !res.Truncated {
			return nil
		}
		fmt.Printf("\n-- pass %d hit the per-run cap; continuing --\n", pass)
	}
}

func printRun(res archive.RunResult) {
	if res.Events == 0 {
		fmt.Printf("archive up to date at seq %d — nothing to copy\n", res.ToSeq)
		return
	}
	fmt.Printf("archived %d events (seq %d → %d) for %d player(s) in %d ms\n",
		res.Events, res.FromSeq, res.ToSeq, len(res.Players), res.DurationMS)
	for _, p := range res.Players {
		// The key already contains the full sub, so there is nothing to redact
		// here that printing the key would not reveal; the §5.11 rule is about
		// log lines, and this is a local operator's terminal.
		fmt.Printf("  %-9d %6d events  %8d B  %s\n", p.PlayerID, p.Events, p.Bytes, p.Key)
	}
	fmt.Printf("total %d compressed bytes\n", res.Bytes)
	if res.Truncated {
		fmt.Printf("more log remains past seq %d — run again (or use -all)\n", res.ToSeq)
	}
}

// runArchiveRestore implements `catlogctl archive-restore <dir>` (§12 WP10):
// replay an archive into a server's events.db.
//
// This is the disaster-recovery path made real rather than aspirational. Point a
// freshly started catlogd at an empty data directory, hand this verb the archive
// tree, then rebuild: the projections come back identical, because the restore
// puts every event back at its original seq under its original player_id.
func runArchiveRestore(args []string) error {
	fs := flag.NewFlagSet("archive-restore", flag.ContinueOnError)
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	asJSON := fs.Bool("json", false, "print the raw JSON response")
	rebuild := fs.Bool("rebuild", false, "rebuild the projections from the restored log when the replay finishes")
	timeout := fs.Duration("timeout", 30*time.Minute, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl archive-restore [-admin URL] [-rebuild] <dir>\n\n"+
			"Replays every chunk under <dir>/players/ into the running server's events.db,\n"+
			"restoring each event at its original seq and each player at its original\n"+
			"player_id. <dir> is a path on the *server's* filesystem (catlogctl never opens\n"+
			"a database itself — §5.4).\n\n"+
			"Restore the raw event log only: handles, credentials and bans are identity\n"+
			"state and are not archived (D8). Pair this with a `catlogctl backup` copy of\n"+
			"events.db to bring an account back whole.\n\n"+
			"Restore into a *fresh* data directory. Replaying over a populated log fails\n"+
			"loudly rather than merging two histories.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("exactly one archive directory is required")
	}

	// Absolute, so a relative path means the same thing to the operator's shell
	// and to catlogd — which is only true when they share a working directory,
	// and is confusing precisely when they do not.
	dir, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}
	base, err := adminBaseURL(*admin, *cfgPath)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var res archive.RestoreResult
	if err := callAdmin(ctx, http.MethodPost, base+"/admin/archive/restore",
		adminapi.RestoreRequest{Dir: dir}, &res); err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return err
		}
	} else {
		fmt.Printf("restored %d events from %d chunk(s) across %d player(s) in %d ms\n",
			res.Events, res.Chunks, len(res.Players), res.DurationMS)
		for _, p := range res.Players {
			fmt.Printf("  player %-6d %-8s %3d chunk(s)  %6d events  (%d new, %d already present)\n",
				p.PlayerID, p.IdP, p.Chunks, p.Events, p.Inserted, p.Deduped)
		}
		fmt.Printf("seq range %d → %d; archive cursor left at %d\n", res.FirstSeq, res.LastSeq, res.Cursor)
	}

	if !*rebuild {
		fmt.Println("\nnow run `catlogctl rebuild` to rebuild the projections from the restored log")
		return nil
	}

	rctx, rcancel := context.WithTimeout(context.Background(), *timeout)
	defer rcancel()

	var rb projector.RebuildResult
	if err := callAdmin(rctx, http.MethodPost, base+"/admin/projections/rebuild", struct{}{}, &rb); err != nil {
		return err
	}
	fmt.Printf("\nrebuilt %s in %d ms\n", rb.Path, rb.DurationMS)
	fmt.Printf("  events folded:  %d (%d skipped)\n", rb.Events, rb.Skipped)
	fmt.Printf("  checkpoint:     %d\n", rb.LastSeq)
	fmt.Printf("  stat rows:      %d\n", rb.Stats)
	return nil
}
