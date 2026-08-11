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
	reason := fs.String("reason", "", "why, recorded on the job and in the server log")
	detach := fs.Bool("detach", false, "start it and exit, instead of following it to the end")
	status := fs.Bool("status", false, "print the running or last rebuild and exit, starting nothing")
	timeout := fs.Duration("timeout", 2*time.Hour, "how long to follow the rebuild before giving up")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: catlogctl rebuild [-detach] [-status] [-reason TEXT]\n\n"+
			"Rebuilds every projection from seq 0 into projections.rebuild.db and swaps it\n"+
			"into place (§5.6). Reads are never interrupted: the old file answers queries\n"+
			"until the new one is renamed over it.\n\n"+
			"This is the routine operation, not the emergency one. It is what applies a new\n"+
			"board to the history that preceded it, what takes a shadowbanned player off the\n"+
			"boards, what heals a late flight.flagged, and the nightly correctness backstop\n"+
			"D22 leans on.\n\n"+
			"The rebuild runs on the server, not in this process: ^C stops watching, it does\n"+
			"not stop the rebuild. Come back with -status.\n\nflags:\n")
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

	if *status {
		st, err := rebuildStatus(base)
		if err != nil {
			return err
		}
		printRebuildStatus(st)
		return nil
	}

	req := adminapi.RebuildRequest{Reason: *reason}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var started projector.RebuildStatus
	if err := callAdmin(ctx, http.MethodPost, base+"/admin/projections/rebuild", req, &started); err != nil {
		return err
	}
	if *detach {
		fmt.Printf("rebuild started (%s)\n", started.Reason)
		fmt.Println("follow it with: catlogctl rebuild -status")
		return nil
	}
	return watchRebuild(base, *timeout)
}

// rebuildStatus reads the job's current state.
func rebuildStatus(base string) (projector.RebuildStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var st projector.RebuildStatus
	err := callAdmin(ctx, http.MethodGet, base+"/admin/projections/rebuild", nil, &st)
	return st, err
}

// followRebuild is what the moderation verbs call after queueing one: watch it
// unless the operator asked not to.
func followRebuild(base string, watch bool) error {
	if !watch {
		fmt.Println("  a rebuild is running; follow it with: catlogctl rebuild -status")
		return nil
	}
	fmt.Println()
	return watchRebuild(base, 2*time.Hour)
}

// watchRebuild polls until the job ends, printing a progress line that rewrites
// itself.
//
// It polls rather than streams because a rebuild is minutes long and the status
// is a handful of integers: a second of latency on a progress bar is not worth
// an SSE endpoint, a subscriber registry and their failure modes.
func watchRebuild(base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	interactive := isTerminal(os.Stdout)
	for {
		st, err := rebuildStatus(base)
		if err != nil {
			return err
		}
		if !st.Phase.Running() {
			if interactive {
				fmt.Print("\r\033[K")
			}
			printRebuildStatus(st)
			if st.Phase == projector.PhaseFailed {
				return fmt.Errorf("rebuild failed: %s", st.Err)
			}
			return nil
		}

		line := rebuildProgressLine(st)
		if interactive {
			fmt.Printf("\r\033[K%s", line)
		} else {
			fmt.Println(line)
		}
		if time.Now().After(deadline) {
			fmt.Println()
			return fmt.Errorf("gave up watching after %s; the rebuild is still running — catlogctl rebuild -status", timeout)
		}
		time.Sleep(rebuildPollInterval)
	}
}

// rebuildPollInterval is how often the watcher asks. Fast enough that a small
// rebuild does not look hung, slow enough that a two-hour one is 1,200 requests
// against a loopback endpoint that reads a mutex.
const rebuildPollInterval = 3 * time.Second

func rebuildProgressLine(st projector.RebuildStatus) string {
	pass := map[projector.RebuildPhase]string{
		projector.PhaseState:    "pass 1/2 (flights and careers)",
		projector.PhaseBoards:   "pass 2/2 (boards)",
		projector.PhaseSwapping: "swapping the file in",
	}[st.Phase]

	elapsed := time.Since(time.UnixMilli(st.StartedAt)).Truncate(time.Second)
	if st.Head <= 0 {
		return fmt.Sprintf("rebuilding — %s, %s elapsed", pass, elapsed)
	}
	pct := float64(st.Scanned) / float64(st.Head) * 100
	return fmt.Sprintf("rebuilding — %s  %d/%d events (%.1f%%), %s elapsed",
		pass, st.Scanned, st.Head, pct, elapsed)
}

func printRebuildStatus(st projector.RebuildStatus) {
	switch st.Phase {
	case projector.PhaseIdle:
		fmt.Println("no rebuild has run since this server started")
	case projector.PhaseFailed:
		fmt.Printf("rebuild FAILED after %s: %s\n", rebuildElapsed(st), st.Err)
		fmt.Println("  nothing changed — the live projections were never swapped")
	case projector.PhaseDone:
		res := st.Result
		if res == nil {
			fmt.Printf("rebuild finished in %s\n", rebuildElapsed(st))
			break
		}
		fmt.Printf("rebuilt %s in %d ms\n", res.Path, res.DurationMS)
		fmt.Printf("  reason:         %s\n", st.Reason)
		fmt.Printf("  events folded:  %d (%d skipped)\n", res.Events, res.Skipped)
		fmt.Printf("  checkpoint:     %d\n", res.LastSeq)
		fmt.Printf("  flights:        %d\n", res.Flights)
		fmt.Printf("  stat rows:      %d\n", res.Stats)
		fmt.Printf("  feed rows:      %d\n", res.Feed)
		fmt.Printf("  kia flights:    %d\n", res.KIAFlights)
		fmt.Printf("  build:          %s\n", res.BuildID)
	default:
		fmt.Println(rebuildProgressLine(st))
		fmt.Printf("  reason:         %s\n", st.Reason)
	}
	if st.Queued {
		fmt.Println("  another rebuild is queued behind this one")
	}
	if st.Suspended {
		fmt.Println("  the fold loop is SUSPENDED until a rebuild lands: the live file was")
		fmt.Println("  built by a different fold set, so boards are stale but never wrong")
	}
}

func rebuildElapsed(st projector.RebuildStatus) time.Duration {
	if st.StartedAt == 0 || st.FinishedAt == 0 {
		return 0
	}
	return time.UnixMilli(st.FinishedAt).Sub(time.UnixMilli(st.StartedAt)).Truncate(time.Millisecond)
}

// isTerminal reports whether a progress line may rewrite itself. Piped output
// gets one line per poll instead, so a log of a rebuild is readable.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
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
	fmt.Printf("streams       %d, %d with a gap across %d player(s)\n",
		res.Streams.Total, res.Streams.Gapped, res.Streams.GappedPlayers)
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
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
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
