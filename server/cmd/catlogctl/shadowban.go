package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/meow-sci/catlog/server/internal/adminapi"
)

// The operator surface for shadow bans (§5.9).
//
// Four verbs, deliberately separate rather than one with flags: three of them
// are reversible and one is not, and a destructive action should never be one
// mistyped character away from a safe one.
//
//	shadowban          withhold the log, keep accepting new events
//	unshadowban        give it back
//	shadowban-list     the roster, and what is in it — the review queue
//	shadowban-review   read the withheld events
//	shadowban-delete   destroy them, permanently

// shadowbanTarget resolves the `-handle`/`-sub` pair, with the same
// exactly-one rule the ban verbs use.
func shadowbanTarget(handle, sub string) (adminapi.ShadowbanRequest, error) {
	req, err := target(handle, sub)
	if err != nil {
		return adminapi.ShadowbanRequest{}, err
	}
	return adminapi.ShadowbanRequest{Handle: req.Handle, Sub: req.Sub}, nil
}

// runShadowban implements `catlogctl shadowban`.
func runShadowban(args []string) error {
	fs := flag.NewFlagSet("shadowban", flag.ContinueOnError)
	handle := fs.String("handle", "", "handle identifying the account")
	sub := fs.String("sub", "", "b64u user_key identifying the account (a license `sub`)")
	reason := fs.String("reason", "", "why (recorded on the roster row and in the log)")
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	watch := fs.Bool("watch", true, "follow the rebuild that removes them from the boards")
	timeout := fs.Duration("timeout", 2*time.Minute, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl shadowban (-handle NAME | -sub B64U) [-reason TEXT]\n\n"+
			"Moves every event the account owns out of the live log into shadowban_event,\n"+
			"and hides their handles from every public surface immediately.\n\n"+
			"Their credential keeps working. The mod goes on shipping and goes on being\n"+
			"answered normally; everything it sends is withheld too. That is the point —\n"+
			"a review needs the evidence to keep arriving, and a loud ban stops it.\n\n"+
			"Nothing is deleted. `unshadowban` gives the log back at its original seq;\n"+
			"`shadowban-delete` destroys it for good. Both stay available until you pick.\n\n"+
			"A rebuild is queued to take their rows off the boards, and followed unless\n"+
			"-watch=false.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	req, err := shadowbanTarget(*handle, *sub)
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

	var res adminapi.ShadowbanResponse
	if err := callAdmin(ctx, http.MethodPost, base+"/admin/shadowban", req, &res); err != nil {
		return err
	}
	sb := res.Shadowban
	if sb == nil {
		return fmt.Errorf("the server accepted the shadow ban but reported nothing about it")
	}

	fmt.Printf("shadowbanned %s (%s) at %s\n", sb.Sub, sb.IdP, time.UnixMilli(sb.At).UTC().Format(time.RFC3339))
	fmt.Printf("  reason:            %s\n", sb.Reason)
	fmt.Printf("  events withheld:   %d\n", sb.Withheld)
	fmt.Printf("  handles hidden:    %d %v\n", len(sb.Handles), sb.Handles)
	fmt.Println("  their client keeps working; new events are withheld as they arrive")
	return followRebuild(base, *watch)
}

// runUnshadowban implements `catlogctl unshadowban`.
func runUnshadowban(args []string) error {
	fs := flag.NewFlagSet("unshadowban", flag.ContinueOnError)
	handle := fs.String("handle", "", "handle identifying the account")
	sub := fs.String("sub", "", "b64u user_key identifying the account")
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	watch := fs.Bool("watch", true, "follow the rebuild that puts them back on the boards")
	timeout := fs.Duration("timeout", 2*time.Minute, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl unshadowban (-handle NAME | -sub B64U)\n\n"+
			"Moves the withheld events back into the log at the seq they left from, so the\n"+
			"rebuild that follows scores their records against the same history — including\n"+
			"the tie-break that gives a record to whoever reached it first.\n\n"+
			"Restores nothing if the withheld events were already deleted, which is honest\n"+
			"rather than an error.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	req, err := shadowbanTarget(*handle, *sub)
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

	var res adminapi.ShadowbanResponse
	if err := callAdmin(ctx, http.MethodPost, base+"/admin/unshadowban", req, &res); err != nil {
		return err
	}
	un := res.Unshadowban
	if un == nil {
		return fmt.Errorf("the server lifted the shadow ban but reported nothing about it")
	}

	fmt.Printf("shadow ban lifted on %s\n", un.Sub)
	fmt.Printf("  events restored:   %d\n", un.Restored)
	fmt.Printf("  handles restored:  %d %v\n", len(un.Handles), un.Handles)
	if un.Restored == 0 {
		fmt.Println("  (nothing to restore — the withheld events had been deleted)")
	}
	return followRebuild(base, *watch)
}

// runShadowbanList implements `catlogctl shadowban-list`: the review queue.
func runShadowbanList(args []string) error {
	fs := flag.NewFlagSet("shadowban-list", flag.ContinueOnError)
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	asJSON := fs.Bool("json", false, "print the raw JSON response")
	timeout := fs.Duration("timeout", 30*time.Second, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl shadowban-list [-json]\n\n"+
			"Every shadowbanned account, when it was applied, why, and how many of their\n"+
			"events are being held. Read the events themselves with `shadowban-review`.\n\nflags:\n")
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

	var res adminapi.ShadowbanListResponse
	if err := callAdmin(ctx, http.MethodGet, base+"/admin/shadowban", nil, &res); err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}

	if len(res.Shadowbans) == 0 {
		fmt.Println("no shadow bans")
		return nil
	}
	fmt.Printf("%-24s  %-20s  %8s  %s\n", "HANDLES", "SINCE", "EVENTS", "REASON")
	for _, sb := range res.Shadowbans {
		handles := strings.Join(sb.Handles, ",")
		if handles == "" {
			handles = "(none)"
		}
		fmt.Printf("%-24s  %-20s  %8d  %s\n",
			truncate(handles, 24),
			time.UnixMilli(sb.At).UTC().Format("2006-01-02 15:04Z"),
			sb.Events, sb.Reason)
	}
	fmt.Printf("\n%d accounts, %d events withheld\n", len(res.Shadowbans), res.Total)
	return nil
}

// runShadowbanReview implements `catlogctl shadowban-review`: the content.
func runShadowbanReview(args []string) error {
	fs := flag.NewFlagSet("shadowban-review", flag.ContinueOnError)
	handle := fs.String("handle", "", "handle identifying the account")
	sub := fs.String("sub", "", "b64u user_key identifying the account")
	limit := fs.Int("limit", 50, "how many events to read (max 200)")
	before := fs.Int64("before", 0, "read events older than this seq — the cursor from a previous page")
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	asJSON := fs.Bool("json", false, "print the raw JSON response")
	timeout := fs.Duration("timeout", 60*time.Second, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl shadowban-review (-handle NAME | -sub B64U) [-limit N] [-before SEQ]\n\n"+
			"Reads a page of the account's withheld events, newest first, with payloads\n"+
			"unredacted — this is the surface the decision to restore or delete is made on.\n\n"+
			"Page with -before, passing the `next` seq the previous page printed.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	req, err := shadowbanTarget(*handle, *sub)
	if err != nil {
		fs.Usage()
		return err
	}

	base, err := adminBaseURL(*admin, *cfgPath)
	if err != nil {
		return err
	}
	q := url.Values{}
	if req.Handle != "" {
		q.Set("handle", req.Handle)
	}
	if req.Sub != "" {
		q.Set("sub", req.Sub)
	}
	q.Set("limit", fmt.Sprint(*limit))
	if *before > 0 {
		q.Set("before", fmt.Sprint(*before))
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var res adminapi.ShadowbanEventsResponse
	if err := callAdmin(ctx, http.MethodGet, base+"/admin/shadowban/events?"+q.Encode(), nil, &res); err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}

	if res.Ban != nil {
		fmt.Printf("%s — shadowbanned %s, %d events withheld, reason %q\n\n",
			res.Sub, time.UnixMilli(res.Ban.At).UTC().Format(time.RFC3339), res.Ban.Events, res.Ban.Reason)
	}
	for _, ev := range res.Events {
		fmt.Printf("%10d  %-22s  %s  %s\n", ev.Seq, ev.Type,
			time.UnixMilli(ev.RecvTime).UTC().Format("2006-01-02 15:04:05Z"), ev.Payload)
	}
	if res.Next > 0 {
		fmt.Printf("\nmore: catlogctl shadowban-review -handle %s -before %d\n", req.Handle, res.Next)
	}
	return nil
}

// runShadowbanDelete implements `catlogctl shadowban-delete`.
func runShadowbanDelete(args []string) error {
	fs := flag.NewFlagSet("shadowban-delete", flag.ContinueOnError)
	handle := fs.String("handle", "", "handle identifying the account")
	sub := fs.String("sub", "", "b64u user_key identifying the account")
	confirm := fs.Bool("yes", false, "confirm — this cannot be undone")
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	timeout := fs.Duration("timeout", 5*time.Minute, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl shadowban-delete (-handle NAME | -sub B64U) -yes\n\n"+
			"Destroys the account's withheld events permanently. The shadow ban stays on, so\n"+
			"anything they ship next is withheld too.\n\n"+
			"This is the end of a review and it cannot be undone — `unshadowban` afterwards\n"+
			"restores nothing. It does NOT delete the account: that is `purge`.\n\n"+
			"-yes is required.\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	req, err := shadowbanTarget(*handle, *sub)
	if err != nil {
		fs.Usage()
		return err
	}
	if !*confirm {
		fs.Usage()
		return fmt.Errorf("refusing to delete withheld events without -yes")
	}

	base, err := adminBaseURL(*admin, *cfgPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var res adminapi.ShadowbanResponse
	if err := callAdmin(ctx, http.MethodPost, base+"/admin/shadowban/delete", req, &res); err != nil {
		return err
	}
	if res.Deleted == nil {
		return fmt.Errorf("the server deleted the withheld events but reported nothing about it")
	}
	fmt.Printf("deleted %d withheld events for %s\n", res.Deleted.Deleted, res.Deleted.Sub)
	fmt.Println("the shadow ban is still on; anything they ship next is withheld too")
	return nil
}

// truncate shortens a field for the roster table without hiding that it did.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
