package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/meow-sci/catlog/server/internal/adminapi"
	"github.com/meow-sci/catlog/server/internal/identity"
)

// target resolves the `-handle`/`-sub` pair every moderation verb takes. Exactly
// one is required: banning "whichever account matched something" is not a thing
// an operator should be able to do by accident.
func target(handle, sub string) (adminapi.BanRequest, error) {
	switch {
	case handle != "" && sub != "":
		return adminapi.BanRequest{}, errors.New("give either -handle or -sub, not both")
	case handle == "" && sub == "":
		return adminapi.BanRequest{}, errors.New("-handle or -sub is required")
	}
	return adminapi.BanRequest{Handle: handle, Sub: sub}, nil
}

// runBan implements `catlogctl ban` (§5.9).
func runBan(args []string) error {
	fs := flag.NewFlagSet("ban", flag.ContinueOnError)
	handle := fs.String("handle", "", "handle identifying the account")
	sub := fs.String("sub", "", "b64u user_key identifying the account (a license `sub`)")
	reason := fs.String("reason", "", "why (recorded on the player row and the tombstone)")
	purge := fs.Bool("purge", false, "also delete every row the account owns, leaving only the tombstone (§4.7)")
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	timeout := fs.Duration("timeout", 2*time.Minute, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl ban (-handle NAME | -sub B64U) [-reason TEXT] [-purge]\n\n"+
			"Sets banned_at, revokes every live credential, retires the account's handles\n"+
			"(permanently — D9) and refreshes the in-memory deny-list so ingest rejects at\n"+
			"§4.5.3 step 4 rather than step 5. With -purge, also deletes every event, batch,\n"+
			"stream, credential and handle row and leaves only the tombstone.\n\n"+
			"`unban` reverses a plain ban. It cannot reverse -purge.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	req, err := target(*handle, *sub)
	if err != nil {
		fs.Usage()
		return err
	}
	req.Reason, req.Purge = *reason, *purge

	base, err := adminBaseURL(*admin, *cfgPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var res identity.BanResult
	if err := callAdmin(ctx, http.MethodPost, base+"/admin/ban", req, &res); err != nil {
		return err
	}

	fmt.Printf("banned %s (%s) at %s\n", res.Sub, res.IdP, time.UnixMilli(res.BannedAt).UTC().Format(time.RFC3339))
	fmt.Printf("  reason:               %s\n", res.Reason)
	fmt.Printf("  handles retired:      %d %v\n", len(res.Handles), res.Handles)
	fmt.Printf("  credentials revoked:  %d\n", len(res.Credentials))
	if res.Purge != nil {
		printPurge(*res.Purge)
	}
	return nil
}

// runUnban implements `catlogctl unban` (§5.9).
func runUnban(args []string) error {
	fs := flag.NewFlagSet("unban", flag.ContinueOnError)
	handle := fs.String("handle", "", "handle identifying the account")
	sub := fs.String("sub", "", "b64u user_key identifying the account")
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	timeout := fs.Duration("timeout", 30*time.Second, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl unban (-handle NAME | -sub B64U)\n\n"+
			"Clears banned_at, un-retires the handles the account still holds and restores\n"+
			"exactly the credentials that ban revoked — matched on the ban's timestamp, so a\n"+
			"credential the player revoked themselves stays revoked.\n\n"+
			"A purged account cannot be unbanned: its rows are gone and its tombstone keeps\n"+
			"it on the deny-list forever (§4.7).\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	req, err := target(*handle, *sub)
	if err != nil {
		fs.Usage()
		return err
	}
	base, err := adminBaseURL(*admin, *cfgPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var res identity.UnbanResult
	if err := callAdmin(ctx, http.MethodPost, base+"/admin/unban", req, &res); err != nil {
		return err
	}
	fmt.Printf("unbanned %s\n", res.Sub)
	fmt.Printf("  handles restored:     %d %v\n", len(res.Handles), res.Handles)
	fmt.Printf("  credentials restored: %d\n", res.Credentials)
	return nil
}

// runPurge implements `catlogctl purge` (§5.9).
func runPurge(args []string) error {
	fs := flag.NewFlagSet("purge", flag.ContinueOnError)
	handle := fs.String("handle", "", "handle identifying the account")
	sub := fs.String("sub", "", "b64u user_key identifying the account")
	reason := fs.String("reason", "", "why (recorded on the tombstone)")
	yes := fs.Bool("yes", false, "required: a purge cannot be undone")
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	timeout := fs.Duration("timeout", 10*time.Minute, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl purge (-handle NAME | -sub B64U) -yes [-reason TEXT]\n\n"+
			"Deletes every event, ingest batch, stream, credential, handle and the player row\n"+
			"itself, deletes the archive prefix when an archive store is wired, and leaves the\n"+
			"minimal tombstone {user_key, reason, at} (§4.7). The handles are retired forever.\n\n"+
			"Irreversible. Projections heal on the next `catlogctl rebuild`.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if !*yes {
		fs.Usage()
		return errors.New("-yes is required: a purge deletes data permanently")
	}

	req, err := target(*handle, *sub)
	if err != nil {
		fs.Usage()
		return err
	}
	req.Reason = *reason

	base, err := adminBaseURL(*admin, *cfgPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var res identity.PurgeResult
	if err := callAdmin(ctx, http.MethodPost, base+"/admin/purge", req, &res); err != nil {
		return err
	}
	printPurge(res)
	return nil
}

func printPurge(res identity.PurgeResult) {
	fmt.Printf("purged %s at %s\n", res.Sub, time.UnixMilli(res.At).UTC().Format(time.RFC3339))
	fmt.Printf("  reason:              %s\n", res.Reason)
	fmt.Printf("  handles retired:     %d %v\n", len(res.Handles), res.Handles)
	fmt.Printf("  credentials revoked: %d\n", len(res.Revoked))
	fmt.Printf("  events deleted:      %d\n", res.Deleted.Events)
	fmt.Printf("  batches deleted:     %d\n", res.Deleted.Batches)
	fmt.Printf("  streams deleted:     %d\n", res.Deleted.Streams)
	fmt.Printf("  archive prefix:      %s\n", archiveState(res.Archived))
	fmt.Println("  tombstone kept; run `catlogctl rebuild` to heal the projections")
}

func archiveState(deleted bool) string {
	if deleted {
		return "deleted"
	}
	return "no archive store configured (WP10)"
}

// runDenylist implements `catlogctl denylist` (§5.9).
func runDenylist(args []string) error {
	fs := flag.NewFlagSet("denylist", flag.ContinueOnError)
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	verbose := fs.Bool("v", false, "list the banned subs")
	timeout := fs.Duration("timeout", 30*time.Second, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl denylist [-v]\n\n"+
			"Rebuilds the in-memory deny-list from events.db (tombstones + banned players +\n"+
			"revoked credentials), reloads the handle directory and re-signs the document\n"+
			"served at "+identity.DenyListPath+" (§5.8).\n\n"+
			"Also the repair verb: run it if the in-memory set and the database are ever\n"+
			"suspected of having drifted.\n\nflags:\n")
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
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var res adminapi.DenyListResponse
	if err := callAdmin(ctx, http.MethodPost, base+"/admin/denylist/publish", struct{}{}, &res); err != nil {
		return err
	}
	fmt.Printf("deny-list v%d published at %s (%s)\n",
		res.Ver, res.Path, time.UnixMilli(res.UpdatedAt).UTC().Format(time.RFC3339))
	fmt.Printf("  banned subs:   %d\n", res.BannedSubs)
	fmt.Printf("  revoked jkts:  %d\n", res.RevokedJKTs)
	if *verbose {
		for _, s := range res.Subs {
			fmt.Printf("    %s\n", s)
		}
	}
	return nil
}

// runBackup implements `catlogctl backup` (§5.9).
func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	timeout := fs.Duration("timeout", 30*time.Minute, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl backup <dest-dir>\n\n"+
			"Quiesces the events.db writer and copies events.db and its -wal sidecar into\n"+
			"dest-dir (§5.9). The path is resolved on the **server**, not here.\n\n"+
			"The -wal is copied deliberately: Turso's WAL never auto-checkpoints, so the\n"+
			"main file alone can be nearly empty (docs/DECISIONS.md, WP1).\n"+
			"projections.db is not copied — it is rebuildable by design (D8).\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("exactly one destination directory is required")
	}
	dest := fs.Arg(0)
	if abs, err := filepath.Abs(dest); err == nil {
		dest = abs
	}

	base, err := adminBaseURL(*admin, *cfgPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var res adminapi.BackupResponse
	if err := callAdmin(ctx, http.MethodPost, base+"/admin/backup", adminapi.BackupRequest{Dest: dest}, &res); err != nil {
		return err
	}
	fmt.Printf("backup written to %s in %d ms\n", res.Dest, res.DurationMS)
	for _, f := range res.Files {
		fmt.Printf("  %-20s %d B\n", f.Name, f.Bytes)
	}
	return nil
}
