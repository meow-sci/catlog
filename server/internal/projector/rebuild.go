package projector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// RebuildSuffix names the scratch database §5.6 builds into: `projections.db`
// becomes `projections.rebuild.db`, beside it in the data directory so the
// rename that swaps them is same-filesystem and therefore atomic.
const RebuildSuffix = ".rebuild"

// ErrRebuildNotSupported is returned for an in-memory projections database:
// there is no file to rename, and the rebuild's whole point is the swap.
var ErrRebuildNotSupported = errors.New("projector: rebuild needs a file-backed projections database")

// RebuildResult is what `POST /admin/projections/rebuild` returns (§5.9).
type RebuildResult struct {
	// Events is how many event rows the second pass read.
	Events int64 `json:"events"`
	// Skipped is how many of those this build could not fold (§4.1).
	Skipped int64 `json:"skipped"`
	// LastSeq is the checkpoint the rebuilt database starts life at.
	LastSeq int64 `json:"last_seq"`
	// Flights, Stats and Feed are the row census of the new database.
	Flights int64 `json:"flights"`
	Stats   int64 `json:"stats"`
	Feed    int64 `json:"feed"`
	// KIAFlights is how many flights had a kitten.kia, i.e. how many the ±2 s
	// crew-survival window could apply to (§4.2).
	KIAFlights int64 `json:"kia_flights"`
	// DurationMS is wall time for the whole rebuild including the swap.
	DurationMS int64 `json:"duration_ms"`
	// Path is the file that was swapped into place.
	Path string `json:"path"`
}

// Rebuild rebuilds every projection from seq 0 and swaps the result into place
// (§5.6). It is the correctness backstop D22 asks for.
//
// # Why two passes
//
// The incremental path folds an event with only the past in hand, and two of the
// §5.6 rules need the future:
//
//   - A `flight.flagged` can arrive after the flight's scoring events — the mod
//     ships an outbox, and detection is not instantaneous. Incrementally those
//     events already scored.
//   - `biggest_lithobrake_survived` needs to know whether a `kitten.kia` lands
//     within ±2 s of the impact (§4.2), and `peak_g_survived` needs the flight's
//     eventual `ended_reason` (§5.6).
//
// So the first pass applies only the flight-state fold and indexes every
// `kitten.kia` by flight and sim time; the second pass scores the boards against
// a flight_state that is already complete for the whole history, with
// [stats.FlightStateReader.Refined] on. For a history with no late flags, no
// scuttled kittens and flights that ended recovered, the two paths agree exactly
// — which is what the equivalence test asserts.
//
// The incremental loop is blocked for the duration; events that arrive meanwhile
// are folded normally once the new database is in place.
func (p *Projector) Rebuild(ctx context.Context) (RebuildResult, error) {
	started := time.Now()

	p.applyMu.Lock()
	defer p.applyMu.Unlock()

	livePath := p.live.Path()
	if livePath == "" || livePath == store.MemoryPath {
		return RebuildResult{}, ErrRebuildNotSupported
	}
	rebuildPath := rebuildPathFor(livePath)

	head, err := p.events.MaxSeq(ctx)
	if err != nil {
		return RebuildResult{}, err
	}

	if err := removeDatabaseFiles(rebuildPath); err != nil {
		return RebuildResult{}, err
	}
	scratch, err := store.OpenProjections(ctx, rebuildPath, p.storeOpts)
	if err != nil {
		return RebuildResult{}, fmt.Errorf("projector: open rebuild database: %w", err)
	}

	res := RebuildResult{LastSeq: head, Path: livePath}
	kia, err := p.rebuildPass1(ctx, scratch, head)
	if err != nil {
		scratch.Close()
		return RebuildResult{}, err
	}
	res.KIAFlights = int64(len(kia))

	if err := p.rebuildPass2(ctx, scratch, head, kia, &res); err != nil {
		scratch.Close()
		return RebuildResult{}, err
	}

	counts, err := scratch.Counts(ctx)
	if err != nil {
		scratch.Close()
		return RebuildResult{}, err
	}
	res.Flights, res.Stats, res.Feed = counts.FlightState, counts.PlayerStat, counts.Feed

	// Close before the swap: the file lock must be released and the WAL folded
	// back into the main file, or the rename would move a database whose newest
	// writes live in a sidecar we are about to orphan (§5.4, WP1 A1).
	if err := scratch.Close(); err != nil {
		return RebuildResult{}, fmt.Errorf("projector: close rebuild database: %w", err)
	}

	if err := p.swapIn(ctx, livePath, rebuildPath); err != nil {
		return RebuildResult{}, err
	}

	p.publishLag(ctx, head)
	metricRebuilds.Add(1)
	res.DurationMS = time.Since(started).Milliseconds()
	p.log.Info("projections rebuilt",
		"events", res.Events, "skipped", res.Skipped, "last_seq", res.LastSeq,
		"flights", res.Flights, "stats", res.Stats, "feed", res.Feed,
		"kia_flights", res.KIAFlights, "duration_ms", res.DurationMS)
	return res, nil
}

// rebuildPass1 applies only the flight-state fold over the whole log and returns
// the kitten.kia index the crew-survival window needs (§4.2).
func (p *Projector) rebuildPass1(ctx context.Context, proj *store.Projections, head int64) (map[ids.ID][]float64, error) {
	kia := map[ids.ID][]float64{}
	folds := p.stateFolds

	err := p.scan(ctx, head, func(evs []store.StoredEvent) error {
		decoded := p.decodeAll(ctx, evs)
		return proj.WithWriteTx(ctx, func(tx *sql.Tx) error {
			b := stats.NewBatch(tx, stats.BatchOptions{FlushRows: p.flushRows})
			for i, d := range decoded {
				if !d.ok {
					p.logSkipOnce(evs[i], d.err)
					continue
				}
				if err := p.applyFolds(ctx, b, folds, d.ev); err != nil {
					return err
				}
				// Only a KIA that names a flight goes in the index, and that
				// is not a formality: the mod attributes a death to a flight
				// only when it can prove one (the crew read taken inside
				// KillCrew, or the kitten's own EVA vehicle), and leaves
				// `flight` null otherwise. A null one indexed against anything
				// would void an impact record on a flight that had nothing to
				// do with the death — the one outcome worse than missing a
				// disqualification, since it cannot be appealed.
				if d.ev.Type == "kitten.kia" && d.ev.HasFlight() && d.ev.HasSimTime {
					kia[d.ev.FlightID] = append(kia[d.ev.FlightID], d.ev.SimTime)
				}
			}
			return b.Flush(ctx)
		})
	})
	if err != nil {
		return nil, err
	}
	return kia, nil
}

// rebuildPass2 scores every board against the complete flight state, with the
// §5.6 refinements on.
func (p *Projector) rebuildPass2(ctx context.Context, proj *store.Projections, head int64, kia map[ids.ID][]float64, res *RebuildResult) error {
	return p.scan(ctx, head, func(evs []store.StoredEvent) error {
		decoded := p.decodeAll(ctx, evs)
		var feed []store.FeedRow
		return proj.WithWriteTx(ctx, func(tx *sql.Tx) error {
			feed = feed[:0]
			b := stats.NewRefinedBatch(tx, kia, stats.BatchOptions{FlushRows: p.flushRows})
			last := int64(0)
			for i, d := range decoded {
				last = d.seq
				res.Events++
				if !d.ok {
					res.Skipped++
					p.logSkipOnce(evs[i], d.err)
					continue
				}
				if err := p.applyFolds(ctx, b, p.boardFolds, d.ev); err != nil {
					return err
				}
				row, ok, err := p.feedRow(ctx, b, d.ev)
				if err != nil {
					return err
				}
				if ok {
					feed = append(feed, row)
				}
			}
			if err := b.Flush(ctx); err != nil {
				return err
			}
			if len(feed) > 0 {
				var err error
				if feed, err = proj.InsertFeedRows(ctx, tx, feed); err != nil {
					return err
				}
				if err := proj.CapFeed(ctx, tx, store.FeedCap); err != nil {
					return err
				}
			}
			return proj.SetCheckpoint(ctx, tx, store.AllProjections, last)
		})
	})
}

// scan walks the log from seq 0 to head in batches, handing each batch to fn.
// Both rebuild passes use it, so they see exactly the same rows in exactly the
// same order.
func (p *Projector) scan(ctx context.Context, head int64, fn func([]store.StoredEvent) error) error {
	after := int64(0)
	for after < head {
		if err := ctx.Err(); err != nil {
			return err
		}
		evs, err := p.events.EventsSince(ctx, after, p.batchSize)
		if err != nil {
			return err
		}
		if len(evs) == 0 {
			return nil
		}
		// Everything past the head captured at the start of the rebuild belongs
		// to the incremental loop, which resumes from the new checkpoint.
		if evs[len(evs)-1].Seq > head {
			cut := len(evs)
			for i, se := range evs {
				if se.Seq > head {
					cut = i
					break
				}
			}
			evs = evs[:cut]
			if len(evs) == 0 {
				return nil
			}
		}
		if err := fn(evs); err != nil {
			return err
		}
		after = evs[len(evs)-1].Seq
	}
	return nil
}

// swapIn performs §5.6's atomic swap: close the live handle, move the rebuilt
// file onto its path and reopen — all with the read-side RWMutex held, so no
// query ever sees the gap.
//
// The old file is kept as `<path>.old` until the reopen succeeds. Without that,
// a failure to reopen would leave the process with no projections database and
// nothing to fall back to.
func (p *Projector) swapIn(ctx context.Context, livePath, rebuildPath string) error {
	oldPath := livePath + ".old"
	return p.live.exclusive(func(cur *store.Projections) (*store.Projections, error) {
		if err := cur.Close(); err != nil {
			return nil, fmt.Errorf("projector: close live projections: %w", err)
		}
		// The WAL was truncated by Close; removing the sidecars keeps a stale
		// one from being replayed onto the file we are about to move in.
		_ = removeSidecars(livePath)
		_ = os.Remove(oldPath)
		_ = removeDatabaseFiles(oldPath)
		if err := os.Rename(livePath, oldPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("projector: set aside the old projections: %w", err)
		}
		if err := os.Rename(rebuildPath, livePath); err != nil {
			// Put the old one back before giving up.
			if rbErr := os.Rename(oldPath, livePath); rbErr == nil {
				if reopened, oErr := store.OpenProjections(ctx, livePath, p.storeOpts); oErr == nil {
					return reopened, fmt.Errorf("projector: swap rebuilt projections: %w", err)
				}
			}
			return nil, fmt.Errorf("projector: swap rebuilt projections: %w", err)
		}
		_ = removeSidecars(rebuildPath)

		next, err := store.OpenProjections(ctx, livePath, p.storeOpts)
		if err != nil {
			return nil, fmt.Errorf("projector: reopen projections after swap: %w", err)
		}
		_ = removeDatabaseFiles(oldPath)
		return next, nil
	})
}

// rebuildPathFor turns `…/projections.db` into `…/projections.rebuild.db`.
func rebuildPathFor(path string) string {
	if ext := ".db"; strings.HasSuffix(path, ext) {
		return strings.TrimSuffix(path, ext) + RebuildSuffix + ext
	}
	return path + RebuildSuffix
}

// removeDatabaseFiles deletes a database and its sidecars. Used to clear a
// scratch file left behind by an interrupted rebuild.
func removeDatabaseFiles(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("projector: remove %s: %w", path, err)
	}
	return removeSidecars(path)
}

// removeSidecars deletes the -wal and -shm files beside a database.
func removeSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("projector: remove %s%s: %w", path, suffix, err)
		}
	}
	return nil
}
