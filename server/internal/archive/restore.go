package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/meow-sci/catlog/server/internal/store"
)

// restoreBatch is how many events go into one write transaction. Large enough
// that a restore is not a per-row round trip, small enough that a failure does
// not roll back an hour of work.
const restoreBatch = 1_000

// RestoreResult reports a completed restore (§5.9 `catlogctl archive-restore`).
type RestoreResult struct {
	// Source is the archive root the chunks were read from.
	Source string `json:"source"`
	// Players is one entry per player prefix found, in seq order.
	Players []RestoredPlayer `json:"players"`
	// Chunks and Events are the totals read; Inserted and Deduped split the
	// events by what the target database did with them.
	Chunks   int   `json:"chunks"`
	Events   int64 `json:"events"`
	Inserted int64 `json:"inserted"`
	Deduped  int64 `json:"deduped"`
	// FirstSeq and LastSeq bound the restored log.
	FirstSeq int64 `json:"first_seq"`
	LastSeq  int64 `json:"last_seq"`
	// Cursor is where the archive cursor was left: a restored log has by
	// definition already been archived, so the next run must not copy it again.
	Cursor     int64 `json:"cursor"`
	DurationMS int64 `json:"duration_ms"`
}

// RestoredPlayer is one player's contribution to a restore.
type RestoredPlayer struct {
	Sub      string `json:"sub"`
	PlayerID int64  `json:"player_id"`
	IdP      string `json:"idp"`
	Chunks   int    `json:"chunks"`
	Events   int64  `json:"events"`
	Inserted int64  `json:"inserted"`
	Deduped  int64  `json:"deduped"`
}

// Restore replays an archive into an events database — the disaster-recovery
// path (§12 WP10).
//
// What comes back is the raw event log and the `player` rows behind it, at their
// original seq and player_id. That is everything the archive holds (D8: only the
// log is archived), and it is enough: a projections rebuild over a restored log
// produces the same `player_stat` rows as the original, which is the property the
// WP10 round-trip test asserts.
//
// What does not come back: handles, credentials, bans and tombstones. Those are
// identity state, they are not in the archive, and they are what a `catlogctl
// backup` copy of events.db is for. A restored server therefore serves the
// leaderboards' *data* but resolves no handles until identity is restored
// alongside it — a deliberate consequence of archiving only the log.
//
// Restore is idempotent. Running it twice, or over a partially recovered log,
// converges: every insert is `INSERT OR IGNORE` against the (player, event_id)
// index, and an event already present at a *different* seq is a hard error
// rather than a silent divergence.
func Restore(ctx context.Context, events *store.Events, src Store, log *slog.Logger) (RestoreResult, error) {
	if events == nil {
		return RestoreResult{}, errors.New("archive: restore without an events store")
	}
	if src == nil {
		return RestoreResult{}, errors.New("archive: restore without a source store")
	}
	if log == nil {
		log = slog.Default()
	}
	g, ok := src.(Getter)
	if !ok {
		return RestoreResult{}, fmt.Errorf("archive: %T cannot read objects back, so it cannot be restored from", src)
	}
	start := time.Now()

	res := RestoreResult{}
	if fs, ok := src.(*FSStore); ok {
		res.Source = fs.Root()
	}

	subs, err := listSubs(ctx, src)
	if err != nil {
		return res, err
	}
	if len(subs) == 0 {
		return res, fmt.Errorf("archive: no player archives found under %q", PlayersPrefix)
	}

	for _, sub := range subs {
		rp, first, last, err := restorePlayer(ctx, events, src, g, sub, log)
		if err != nil {
			return res, err
		}
		res.Players = append(res.Players, rp)
		res.Chunks += rp.Chunks
		res.Events += rp.Events
		res.Inserted += rp.Inserted
		res.Deduped += rp.Deduped
		if res.FirstSeq == 0 || (first > 0 && first < res.FirstSeq) {
			res.FirstSeq = first
		}
		if last > res.LastSeq {
			res.LastSeq = last
		}
	}

	// The restored log came *out* of the archive, so it is already archived.
	// Leaving the cursor at zero would make the next `catlogctl archive` copy
	// the whole log again under new chunk boundaries.
	cursor, err := events.ArchiveCursor(ctx, nil)
	if err != nil {
		return res, err
	}
	if res.LastSeq > cursor {
		if err := events.SetArchiveCursor(ctx, nil, res.LastSeq); err != nil {
			return res, err
		}
		cursor = res.LastSeq
	}
	res.Cursor = cursor
	res.DurationMS = time.Since(start).Milliseconds()

	log.Info("archive restored",
		"source", res.Source, "players", len(res.Players), "chunks", res.Chunks,
		"events", res.Events, "inserted", res.Inserted, "deduped", res.Deduped,
		"first_seq", res.FirstSeq, "last_seq", res.LastSeq, "cursor", res.Cursor,
		"duration_ms", res.DurationMS)
	return res, nil
}

// listSubs finds every player prefix in a store, sorted.
func listSubs(ctx context.Context, src Store) ([]string, error) {
	all, err := src.List(ctx, PlayersPrefix)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var subs []string
	for _, key := range all {
		sub, ok := SubFromKey(key)
		if !ok || seen[sub] {
			continue
		}
		if err := ValidateSub(sub); err != nil {
			return nil, fmt.Errorf("archive: %q is not a player prefix: %w", key, err)
		}
		seen[sub] = true
		subs = append(subs, sub)
	}
	sort.Strings(subs)
	return subs, nil
}

// restorePlayer replays one player's chunks, in manifest order.
func restorePlayer(ctx context.Context, events *store.Events, src Store, g Getter, sub string, log *slog.Logger) (RestoredPlayer, int64, int64, error) {
	rp := RestoredPlayer{Sub: sub}

	m, err := loadManifest(ctx, src, sub)
	if err != nil {
		return rp, 0, 0, err
	}
	if m == nil {
		// The chunks alone cannot say which player_id and idp to restore under,
		// so a missing manifest is fatal and worth naming precisely.
		return rp, 0, 0, fmt.Errorf("archive: %s has chunks but no %s", PlayerPrefix(subLog(sub)), ManifestName)
	}
	rp.PlayerID, rp.IdP = m.PlayerID, m.IdP

	uk, err := parseSub(sub)
	if err != nil {
		return rp, 0, 0, err
	}
	if err := events.RestorePlayer(ctx, nil, m.PlayerID, uk, m.IdP, m.CreatedAt); err != nil {
		return rp, 0, 0, err
	}

	// Every chunk the manifest lists must exist, and every chunk present must be
	// listed. A restore that quietly skipped an unlisted chunk would lose events
	// and report success.
	present, err := src.List(ctx, ChunkPrefix(sub))
	if err != nil {
		return rp, 0, 0, err
	}
	listed := map[string]bool{}
	for _, c := range m.Chunks {
		listed[c.Key] = true
	}
	for _, key := range present {
		if !listed[key] && strings.HasSuffix(key, ChunkSuffix) {
			return rp, 0, 0, fmt.Errorf("archive: %s is not listed in %s's manifest", key, subLog(sub))
		}
	}

	var first, last int64
	for _, c := range m.Chunks {
		evs, err := readChunk(ctx, g, c)
		if err != nil {
			return rp, 0, 0, err
		}
		inserted, deduped, err := writeChunk(ctx, events, m.PlayerID, evs)
		if err != nil {
			return rp, 0, 0, fmt.Errorf("archive: restore %s: %w", c.Key, err)
		}
		rp.Chunks++
		rp.Events += int64(len(evs))
		rp.Inserted += inserted
		rp.Deduped += deduped
		if first == 0 {
			first = c.FirstSeq
		}
		last = c.LastSeq
	}

	log.Debug("player archive restored", "sub", subLog(sub), "player", m.PlayerID,
		"chunks", rp.Chunks, "events", rp.Events, "inserted", rp.Inserted, "deduped", rp.Deduped)
	return rp, first, last, nil
}

// readChunk fetches, verifies and decodes one chunk.
//
// Verification is not ceremony. This runs when a server has already been lost;
// silently restoring a truncated or swapped chunk would turn one disaster into
// two, so the stored digest, the event count and the seq range are all checked
// against what the manifest promised before a single row is written.
func readChunk(ctx context.Context, g Getter, c ChunkRef) ([]store.StoredEvent, error) {
	rc, err := g.Get(ctx, c.Key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	compressed, err := io.ReadAll(io.LimitReader(rc, c.Bytes+1))
	if err != nil {
		return nil, fmt.Errorf("archive: read %s: %w", c.Key, err)
	}
	if int64(len(compressed)) != c.Bytes {
		return nil, fmt.Errorf("archive: %s is %d bytes, the manifest says %d", c.Key, len(compressed), c.Bytes)
	}
	if sum := sha256.Sum256(compressed); hex.EncodeToString(sum[:]) != c.SHA256 {
		return nil, fmt.Errorf("archive: %s does not match its manifest digest", c.Key)
	}

	raw, err := decompressChunk(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("%w (%s)", err, c.Key)
	}
	evs, err := decodeChunk(raw)
	if err != nil {
		return nil, fmt.Errorf("archive: %s: %w", c.Key, err)
	}

	if int64(len(evs)) != c.Events {
		return nil, fmt.Errorf("archive: %s holds %d events, the manifest says %d", c.Key, len(evs), c.Events)
	}
	for i, se := range evs {
		if se.Seq < c.FirstSeq || se.Seq > c.LastSeq {
			return nil, fmt.Errorf("archive: %s holds seq %d, outside its declared range %d-%d",
				c.Key, se.Seq, c.FirstSeq, c.LastSeq)
		}
		if i > 0 && se.Seq <= evs[i-1].Seq {
			return nil, fmt.Errorf("archive: %s is not in seq order at line %d", c.Key, i+1)
		}
	}
	if len(evs) > 0 && (evs[0].Seq != c.FirstSeq || evs[len(evs)-1].Seq != c.LastSeq) {
		return nil, fmt.Errorf("archive: %s spans %d-%d, the manifest says %d-%d",
			c.Key, evs[0].Seq, evs[len(evs)-1].Seq, c.FirstSeq, c.LastSeq)
	}
	return evs, nil
}

// writeChunk inserts one chunk's events in bounded transactions.
func writeChunk(ctx context.Context, events *store.Events, playerID int64, evs []store.StoredEvent) (inserted, deduped int64, err error) {
	for start := 0; start < len(evs); start += restoreBatch {
		batch := evs[start:min(start+restoreBatch, len(evs))]
		err := events.WithWriteTx(ctx, func(tx *sql.Tx) error {
			ins, dup, err := events.RestoreEvents(ctx, tx, playerID, batch)
			if err != nil {
				return err
			}
			inserted += int64(ins)
			deduped += int64(dup)
			return nil
		})
		if err != nil {
			return inserted, deduped, err
		}
	}
	return inserted, deduped, nil
}
