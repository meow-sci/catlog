package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/meow-sci/catlog/server/internal/adminapi"
	"github.com/meow-sci/catlog/server/internal/projector"
)

// runSeed implements `catlogctl seed` (§5.9): install the deterministic demo
// dataset and wait for it to be folded, so the boards are readable the moment
// the command returns.
func runSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	timeout := fs.Duration("timeout", 2*time.Minute, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl seed [-admin URL]\n\n"+
			"Inserts the deterministic demo dataset (demo_ace, demo_tumbler, demo_crasher)\n"+
			"and folds it, so /v1/leaderboards answers immediately (§5.9).\n"+
			"Idempotent: every event id is derived, so a second run is entirely deduped.\n\nflags:\n")
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

	var res adminapi.SeedResponse
	if err := callAdmin(ctx, http.MethodPost, base+"/admin/seed", struct{}{}, &res); err != nil {
		return err
	}
	fmt.Printf("seeded %d players, %d events (%d new, %d already present)\n",
		len(res.Players), res.Events, res.Accepted, res.Deduped)
	for _, h := range res.Players {
		fmt.Printf("  %s\n", h)
	}
	fmt.Printf("projections folded to seq %d\n", res.FoldedTo)
	return nil
}

// runRebuild implements `catlogctl rebuild` (§5.9): the §5.6 rebuild + swap,
// which is the correctness backstop D22 leans on.
func runRebuild(args []string) error {
	fs := flag.NewFlagSet("rebuild", flag.ContinueOnError)
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	timeout := fs.Duration("timeout", 30*time.Minute, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl rebuild [-admin URL]\n\n"+
			"Rebuilds every projection from seq 0 into projections.rebuild.db and swaps it\n"+
			"into place (§5.6). This is what heals a late flight.flagged and applies the\n"+
			"refinements the incremental fold cannot (D22). Run nightly in production.\n\nflags:\n")
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

	var res projector.RebuildResult
	if err := callAdmin(ctx, http.MethodPost, base+"/admin/projections/rebuild", struct{}{}, &res); err != nil {
		return err
	}
	fmt.Printf("rebuilt %s in %d ms\n", res.Path, res.DurationMS)
	fmt.Printf("  events folded:  %d (%d skipped)\n", res.Events, res.Skipped)
	fmt.Printf("  checkpoint:     %d\n", res.LastSeq)
	fmt.Printf("  flights:        %d\n", res.Flights)
	fmt.Printf("  stat rows:      %d\n", res.Stats)
	fmt.Printf("  feed rows:      %d\n", res.Feed)
	fmt.Printf("  kia flights:    %d\n", res.KIAFlights)
	return nil
}

// runStats implements `catlogctl stats` (§5.9).
func runStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	admin := fs.String("admin", "", "admin API base URL (default http://<config admin_listen>)")
	cfgPath := fs.String("config", "", "catlogd TOML config to read [server].admin_listen from (optional)")
	asJSON := fs.Bool("json", false, "print the raw JSON response")
	timeout := fs.Duration("timeout", 10*time.Second, "admin API request timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl stats [-admin URL] [-json]\n\n"+
			"Prints event counts, ingest queue depth, projector checkpoint lag and the\n"+
			"projection census (§5.9).\n\nflags:\n")
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

	var res adminapi.StatsResponse
	if err := callAdmin(ctx, http.MethodGet, base+"/admin/stats", nil, &res); err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}

	fmt.Printf("events        %d stored, head seq %d\n", res.Events.Total, res.Events.MaxSeq)
	fmt.Printf("players       %d (%d banned), %d live handles\n", res.Events.Players, res.Events.Banned, res.Events.Handles)
	fmt.Printf("ingest queue  %d / %d\n", res.Ingest.QueueDepth, res.Ingest.QueueCap)
	fmt.Printf("projector     checkpoint %d, lag %d, %d folds\n",
		res.Projector.CheckpointSeq, res.Projector.LagSeq, len(res.Projector.Folds))
	fmt.Printf("projections   %d stat rows, %d flights (%d flagged), %d kittens, %d feed rows\n",
		res.Projections.PlayerStat, res.Projections.FlightState, res.Projections.FlaggedFlights,
		res.Projections.Kitten, res.Projections.Feed)
	fmt.Printf("storage       events %d B (+%d B wal), projections %d B (+%d B wal)\n",
		res.Storage.EventsDBBytes, res.Storage.EventsWALBytes,
		res.Storage.ProjectionsDBBytes, res.Storage.ProjectionsWALBytes)
	for _, b := range res.Boards {
		if b.Count > 0 {
			fmt.Printf("  %-32s %d\n", b.Stat, b.Count)
		}
	}
	return nil
}

// callAdmin is postAdmin's general form: any method, any response shape. The
// §4.9 error body is turned into a Go error the same way.
func callAdmin(ctx context.Context, method, url string, body, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w (is catlogd running?)", method, url, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error  string `json:"error"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			if e.Detail != "" {
				return fmt.Errorf("%s: %s (%s)", url, e.Error, e.Detail)
			}
			return fmt.Errorf("%s: %s", url, e.Error)
		}
		return fmt.Errorf("%s: HTTP %d: %s", url, res.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("response is not JSON: %w", err)
	}
	return nil
}
