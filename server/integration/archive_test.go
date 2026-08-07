//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/meow-sci/catlog/server/internal/archive"
	"github.com/meow-sci/catlog/server/internal/store"
)

// TestArchiveAndRestoreDisasterRecovery is the WP10 disaster-recovery drill,
// run against real processes rather than in-process fakes.
//
// A live catlogd is seeded, `catlogctl archive` copies its log to
// data/archive/, the server is thrown away, a *second* catlogd is booted on an
// empty data directory, `catlogctl archive-restore -rebuild` replays the archive
// into it — and the rebuilt `player_stat` rows are compared, column for column,
// against the ones the original server had.
//
// Everything that runs here is a shipped artefact: the catlogd binary, the
// catlogctl binary, the admin API, the filesystem archive store and the §5.6
// rebuild. The only thing the test does directly is read the two projections
// databases after their servers have released them.
func TestArchiveAndRestoreDisasterRecovery(t *testing.T) {
	ctx := context.Background()

	// --- a populated server -------------------------------------------------
	origin := startServer(t)
	t.Logf("catlogctl seed:\n%s", origin.catlogctlRun(t, "seed", "-admin", origin.adminURL))

	out := origin.catlogctlRun(t, "archive", "-admin", origin.adminURL)
	t.Logf("catlogctl archive:\n%s", out)
	if !strings.Contains(out, "archived") {
		t.Errorf("`catlogctl archive` did not report archiving anything:\n%s", out)
	}

	archiveDir := filepath.Join(origin.dataDir, "archive")
	tree := archiveTree(t, archiveDir)
	t.Logf("data/archive tree:\n%s", treeListing(t, archiveDir))
	assertLayout(t, tree)

	// Running it again is a no-op: the cursor is what makes the nightly timer
	// (§11) safe to fire on a quiet server.
	again := origin.catlogctlRun(t, "archive", "-admin", origin.adminURL)
	if !strings.Contains(again, "up to date") {
		t.Errorf("a second archive pass was not a no-op:\n%s", again)
	}
	if after := archiveTree(t, archiveDir); len(after) != len(tree) {
		t.Errorf("a second pass changed the tree: %v → %v", tree, after)
	}

	// The log is untouched — archiving copies (§5.10).
	origin.stop()
	originStats := readPlayerStats(t, ctx, filepath.Join(origin.dataDir, "projections.db"))
	originEvents := countEvents(t, ctx, filepath.Join(origin.dataDir, "events.db"))
	if len(originStats) == 0 {
		t.Fatal("the seeded server produced no player_stat rows; the comparison would prove nothing")
	}
	t.Logf("original server: %d events, %d player_stat rows", originEvents, len(originStats))

	// --- the recovery -------------------------------------------------------
	recovered := startServer(t)
	if n := countEventsOverAdmin(t, recovered); n != 0 {
		t.Fatalf("the recovery server started with %d events; it must be empty", n)
	}

	out = recovered.catlogctlRun(t, "archive-restore", "-admin", recovered.adminURL, "-rebuild", archiveDir)
	t.Logf("catlogctl archive-restore -rebuild:\n%s", out)
	if !strings.Contains(out, "restored") || !strings.Contains(out, "rebuilt") {
		t.Fatalf("the restore did not report both a replay and a rebuild:\n%s", out)
	}

	recovered.stop()
	recoveredStats := readPlayerStats(t, ctx, filepath.Join(recovered.dataDir, "projections.db"))
	recoveredEvents := countEvents(t, ctx, filepath.Join(recovered.dataDir, "events.db"))

	// --- the proof ----------------------------------------------------------
	if recoveredEvents != originEvents {
		t.Errorf("the recovered log holds %d events, the original held %d", recoveredEvents, originEvents)
	}
	if len(recoveredStats) != len(originStats) {
		t.Fatalf("recovered %d player_stat rows, the original had %d\n original: %v\n recovered: %v",
			len(recoveredStats), len(originStats), originStats, recoveredStats)
	}
	for i := range originStats {
		if recoveredStats[i] != originStats[i] {
			t.Errorf("player_stat row %d differs:\n original:  %s\n recovered: %s", i, originStats[i], recoveredStats[i])
		}
	}
	t.Logf("all %d player_stat rows match after archive → restore → rebuild:\n%s",
		len(originStats), formatStats(originStats))
}

// assertLayout checks the §5.10 key layout on disk.
func assertLayout(t *testing.T, tree []string) {
	t.Helper()
	var chunks, manifests int
	for _, key := range tree {
		switch {
		case strings.HasSuffix(key, archive.ManifestName):
			manifests++
		case strings.HasSuffix(key, archive.ChunkSuffix):
			chunks++
		default:
			t.Errorf("unexpected object in the archive: %s", key)
			continue
		}
		if !strings.HasPrefix(key, archive.PlayersPrefix) {
			t.Errorf("%s is not under %s", key, archive.PlayersPrefix)
		}
		sub, ok := archive.SubFromKey(key)
		if !ok {
			t.Errorf("%s has no sub segment", key)
			continue
		}
		if err := archive.ValidateSub(sub); err != nil {
			t.Errorf("%s: %v", key, err)
		}
	}
	if chunks == 0 || manifests != chunks {
		t.Errorf("tree holds %d chunks and %d manifests; want one manifest per player, each with a chunk",
			chunks, manifests)
	}
}

// archiveTree lists the archive's keys, sorted — the tree an operator would see.
func archiveTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

// treeListing renders the same tree with sizes, for the test log — this is the
// artefact a WP10 reviewer actually wants to see.
func treeListing(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	for _, key := range archiveTree(t, root) {
		fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(key)))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "  %8d B  %s\n", fi.Size(), key)
	}
	return b.String()
}

// statRow is a `player_stat` row, every column included: `updated_seq` and
// `context` are exactly what would drift if a restore renumbered anything.
type statRow struct {
	PlayerID   int64
	Stat       string
	Value      float64
	Context    string
	UpdatedSeq int64
}

func (r statRow) String() string {
	return fmt.Sprintf("player=%d %-28s value=%-12v seq=%-4d context=%s",
		r.PlayerID, r.Stat, r.Value, r.UpdatedSeq, r.Context)
}

func formatStats(rows []statRow) string {
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "  %s\n", r)
	}
	return b.String()
}

// readPlayerStats opens a projections database a stopped server has released
// and dumps every row.
func readPlayerStats(t *testing.T, ctx context.Context, path string) []statRow {
	t.Helper()
	db, err := store.OpenProjections(ctx, path, store.Options{})
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()

	rows, err := db.Reader().QueryContext(ctx,
		`SELECT player_id, stat, value, context, updated_seq FROM player_stat ORDER BY player_id, stat`)
	if err != nil {
		t.Fatalf("read player_stat: %v", err)
	}
	defer rows.Close()

	var out []statRow
	for rows.Next() {
		var (
			r   statRow
			ctx []byte
		)
		if err := rows.Scan(&r.PlayerID, &r.Stat, &r.Value, &ctx, &r.UpdatedSeq); err != nil {
			t.Fatalf("scan player_stat: %v", err)
		}
		r.Context = string(ctx)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read player_stat: %v", err)
	}
	return out
}

func countEvents(t *testing.T, ctx context.Context, path string) int64 {
	t.Helper()
	db, err := store.OpenEvents(ctx, path, store.Options{})
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	n, err := db.CountEvents(ctx, 0)
	if err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

// countEventsOverAdmin asks a *running* server how many events it holds, since
// its database file cannot be opened by anyone else (§5.4).
func countEventsOverAdmin(t *testing.T, s *server) int64 {
	t.Helper()
	out := s.catlogctlRun(t, "stats", "-json", "-admin", s.adminURL)
	var stats struct {
		Events struct {
			Total int64 `json:"total"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		t.Fatalf("catlogctl stats -json: %v\n%s", err, out)
	}
	return stats.Events.Total
}

// TestPurgeDeletesTheArchivePrefixEndToEnd walks the §4.7 purge path on a live
// server with a real archive behind it: seed, archive, purge one player, and
// watch that player's prefix — and only that player's — leave the disk.
func TestPurgeDeletesTheArchivePrefixEndToEnd(t *testing.T) {
	s := startServer(t)
	s.catlogctlRun(t, "seed", "-admin", s.adminURL)
	s.catlogctlRun(t, "archive", "-admin", s.adminURL)

	archiveDir := filepath.Join(s.dataDir, "archive")
	before := archiveTree(t, archiveDir)
	if len(before) < 6 { // three seeded players, a chunk and a manifest each
		t.Fatalf("archive holds %v, want three players' worth", before)
	}

	out := s.catlogctlRun(t, "purge", "-handle", "demo_crasher", "-yes", "-reason", "integration test", "-admin", s.adminURL)
	t.Logf("catlogctl purge:\n%s", out)
	if !strings.Contains(out, "archive") {
		t.Errorf("the purge did not mention the archive:\n%s", out)
	}

	after := archiveTree(t, archiveDir)
	t.Logf("archive after the purge:\n%s", treeListing(t, archiveDir))
	if len(after) != len(before)-2 {
		t.Errorf("the purge removed %d objects, want exactly the purged player's 2:\n before: %v\n after:  %v",
			len(before)-len(after), before, after)
	}

	// Every remaining object still belongs to a player who was not purged, and
	// no empty husk is left behind.
	subs := map[string]bool{}
	for _, key := range after {
		sub, ok := archive.SubFromKey(key)
		if !ok {
			t.Errorf("%s has no sub segment", key)
			continue
		}
		subs[sub] = true
	}
	if len(subs) != 2 {
		t.Errorf("after purging one of three players, %d remain: %v", len(subs), after)
	}
	entries, err := os.ReadDir(filepath.Join(archiveDir, "players"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("players/ holds %d directories, want 2 — a purged player left a husk", len(entries))
	}
}
