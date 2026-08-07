package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/meow-sci/catlog/server/internal/store"
)

// MaxEventsPerRun is §5.10's cap: one archive pass copies at most this many
// events, then advances the cursor and returns. A nightly run over a quiet
// server does everything in one pass; a run over a backlog makes progress in
// bounded, resumable steps.
const MaxEventsPerRun = 100_000

// scanPageSize is how many events one read of the log returns. The cap above is
// on the run; this is on memory, and it keeps a single query's result set small
// regardless of how large the run is.
const scanPageSize = 5_000

// Options configures an [Archiver].
type Options struct {
	// Events is the log being archived and the home of the cursor. Required.
	Events *store.Events
	// Store is where chunks go. Required.
	Store Store
	// Log receives one line per run. Optional.
	Log *slog.Logger
	// Now is the clock, injectable so a test can make a whole run — manifest
	// included — byte-deterministic. Optional.
	Now func() time.Time
	// MaxEvents overrides [MaxEventsPerRun]. Tests use it to force a run to
	// stop mid-log and prove the cursor resumes. Optional.
	MaxEvents int
}

// Archiver copies the raw event log into a [Store] and deletes a player's
// prefix when they are purged (§5.10, §4.7).
//
// Every method assumes the caller holds the §5.4 admin write mutex: a run reads
// the log and writes the cursor, so it must not interleave with the ingest
// writer's transactions.
type Archiver struct {
	events    *store.Events
	store     Store
	log       *slog.Logger
	now       func() time.Time
	maxEvents int
}

// New builds an archiver.
func New(opts Options) (*Archiver, error) {
	if opts.Events == nil {
		return nil, errors.New("archive: no events store")
	}
	if opts.Store == nil {
		return nil, errors.New("archive: no archive store")
	}
	a := &Archiver{
		events:    opts.Events,
		store:     opts.Store,
		log:       opts.Log,
		now:       opts.Now,
		maxEvents: opts.MaxEvents,
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.maxEvents <= 0 {
		a.maxEvents = MaxEventsPerRun
	}
	return a, nil
}

// Store is the backing store — what the restore path and the tests reach for.
func (a *Archiver) Store() Store { return a.store }

// RunResult reports one archive pass (§5.9 `catlogctl archive`).
type RunResult struct {
	// FromSeq is the cursor the run started at; ToSeq is where it left it.
	FromSeq int64 `json:"from_seq"`
	ToSeq   int64 `json:"to_seq"`
	// Events is how many events were copied, Bytes their compressed size.
	Events int64 `json:"events"`
	Bytes  int64 `json:"bytes"`
	// Truncated is true when the run hit its per-run cap and there is more log
	// waiting for the next one.
	Truncated bool `json:"truncated"`
	// Players lists one chunk per player touched, in seq order.
	Players    []PlayerRun `json:"players"`
	DurationMS int64       `json:"duration_ms"`
}

// PlayerRun is the chunk one player got out of one run.
type PlayerRun struct {
	Sub      string `json:"sub"`
	PlayerID int64  `json:"player_id"`
	Key      string `json:"key"`
	FirstSeq int64  `json:"first_seq"`
	LastSeq  int64  `json:"last_seq"`
	Events   int64  `json:"events"`
	Bytes    int64  `json:"bytes"`
}

// Run performs one archive pass (§5.10): read the events past the cursor, group
// them by player, write one zstd NDJSON chunk per player, update each manifest,
// and advance the cursor.
//
// # Order of operations
//
// The cursor moves last, and only after every chunk and manifest has landed. A
// crash anywhere before that leaves the cursor where it was, so the next run
// re-reads the same events and writes the same keys with the same contents —
// which is why the chunk key is derived from the seq range rather than from a
// timestamp, and why the manifest replaces a same-key entry instead of appending
// a second one.
//
// # Copies, never deletes
//
// Nothing is removed from events.db. The log is the source of truth; the archive
// is a second copy of it. Pruning the local log is a separate decision that has
// not been taken (§5.10).
func (a *Archiver) Run(ctx context.Context) (RunResult, error) {
	start := time.Now()

	cursor, err := a.events.ArchiveCursor(ctx, nil)
	if err != nil {
		return RunResult{}, err
	}
	res := RunResult{FromSeq: cursor, ToSeq: cursor}

	groups, head, truncated, err := a.scan(ctx, cursor)
	if err != nil {
		return res, err
	}
	res.Truncated = truncated
	if len(groups) == 0 {
		res.DurationMS = time.Since(start).Milliseconds()
		a.log.Info("archive run: nothing new", "cursor", cursor)
		return res, nil
	}

	// Player order is by player_id so a run's output does not depend on map
	// iteration — determinism starts here, not at the compressor.
	ids := make([]int64, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	now := a.now().UnixMilli()
	for _, playerID := range ids {
		g := groups[playerID]
		pr, err := a.writePlayer(ctx, playerID, g, now)
		if err != nil {
			return res, err
		}
		res.Players = append(res.Players, pr)
		res.Events += pr.Events
		res.Bytes += pr.Bytes
	}

	// Every chunk is durable; the cursor may move.
	if err := a.events.SetArchiveCursor(ctx, nil, head); err != nil {
		return res, err
	}
	res.ToSeq = head
	res.DurationMS = time.Since(start).Milliseconds()

	a.log.Info("archive run complete",
		"from_seq", res.FromSeq, "to_seq", res.ToSeq, "events", res.Events,
		"players", len(res.Players), "bytes", res.Bytes,
		"truncated", res.Truncated, "duration_ms", res.DurationMS)
	return res, nil
}

// group is one player's events from one run, already rendered as NDJSON.
//
// The lines are held raw rather than compressed because compression happens once
// per player at the end, with a single encoder: it is what keeps the output
// deterministic, and it bounds peak memory to one run's worth of text (the
// [MaxEventsPerRun] cap is what makes that a bounded quantity).
type group struct {
	buf      bytes.Buffer
	first    int64
	last     int64
	nEvents  int64
	playerID int64
}

// scan reads the log past the cursor into per-player groups, stopping at the
// per-run cap. head is the highest seq read.
func (a *Archiver) scan(ctx context.Context, cursor int64) (groups map[int64]*group, head int64, truncated bool, err error) {
	groups = map[int64]*group{}
	head = cursor
	remaining := a.maxEvents

	for remaining > 0 {
		limit := min(remaining, scanPageSize)
		page, err := a.events.EventsSince(ctx, head, limit)
		if err != nil {
			return nil, 0, false, err
		}
		if len(page) == 0 {
			return groups, head, false, nil
		}
		for _, se := range page {
			l, err := encodeLine(se)
			if err != nil {
				return nil, 0, false, err
			}
			g := groups[se.PlayerID]
			if g == nil {
				g = &group{playerID: se.PlayerID, first: se.Seq}
				groups[se.PlayerID] = g
			}
			g.buf.Write(l)
			g.buf.WriteByte('\n')
			g.last = se.Seq
			g.nEvents++
			head = se.Seq
		}
		remaining -= len(page)
	}

	// The cap was reached exactly. Whether more log is waiting is one cheap
	// query, and the answer is what tells an operator to run again.
	more, err := a.events.EventsSince(ctx, head, 1)
	if err != nil {
		return nil, 0, false, err
	}
	return groups, head, len(more) > 0, nil
}

// writePlayer compresses one player's group, stores it and updates the manifest.
func (a *Archiver) writePlayer(ctx context.Context, playerID int64, g *group, now int64) (PlayerRun, error) {
	p, err := a.events.PlayerByID(ctx, playerID)
	if err != nil {
		return PlayerRun{}, fmt.Errorf("archive: player %d: %w", playerID, err)
	}
	sub := p.UserKey.B64U()
	if err := ValidateSub(sub); err != nil {
		return PlayerRun{}, err
	}

	compressed, err := compressChunk(g.buf.Bytes())
	if err != nil {
		return PlayerRun{}, err
	}
	sum := sha256.Sum256(compressed)
	ref := ChunkRef{
		Key:      ChunkKey(sub, g.first, g.last),
		FirstSeq: g.first,
		LastSeq:  g.last,
		Events:   g.nEvents,
		Bytes:    int64(len(compressed)),
		SHA256:   hex.EncodeToString(sum[:]),
	}

	if err := a.store.Put(ctx, ref.Key, bytes.NewReader(compressed)); err != nil {
		return PlayerRun{}, err
	}

	m, err := loadManifest(ctx, a.store, sub)
	if err != nil {
		return PlayerRun{}, err
	}
	if m == nil {
		m = newManifest(sub, p.ID, p.IdP, p.CreatedAt)
	}
	// A manifest written before an admin changed nothing still tracks the live
	// player row; refreshing these three keeps a restore faithful if the row was
	// created after an earlier archive run (it cannot legitimately change, but
	// reading it every run costs nothing and removes the assumption).
	m.PlayerID, m.IdP, m.CreatedAt = p.ID, p.IdP, p.CreatedAt
	m.UpdatedAt = now
	m.addChunk(ref)
	if err := saveManifest(ctx, a.store, m); err != nil {
		return PlayerRun{}, err
	}

	// §5.11: the sub is user_key-derived, so only a short prefix is logged.
	a.log.Debug("archived a player chunk",
		"sub", subLog(sub), "player", p.ID, "events", g.nEvents,
		"first_seq", g.first, "last_seq", g.last, "bytes", ref.Bytes)

	return PlayerRun{
		Sub: sub, PlayerID: p.ID, Key: ref.Key,
		FirstSeq: g.first, LastSeq: g.last, Events: g.nEvents, Bytes: ref.Bytes,
	}, nil
}

// DeletePlayerArchive implements identity.ArchivePurger: it deletes everything
// under `players/<sub>/` (§4.7, §5.10).
//
// The identity package defines the interface and this satisfies it structurally,
// so neither package imports the other — which is what lets a deployment with no
// archive configured purge correctly by doing nothing.
//
// Deleting a prefix that holds nothing succeeds. A player who never shipped an
// event has no archive, and a purge must not fail because of it.
func (a *Archiver) DeletePlayerArchive(ctx context.Context, sub string) error {
	if err := ValidateSub(sub); err != nil {
		return err
	}
	prefix := PlayerPrefix(sub)

	// Counted before the delete purely so the log line is useful: "deleted 0
	// objects" and "deleted 14 objects" are very different things to read after
	// a GDPR request.
	before, err := a.store.List(ctx, prefix)
	if err != nil {
		return err
	}
	if err := a.store.Delete(ctx, prefix); err != nil {
		return err
	}
	a.log.Warn("archive prefix deleted", "sub", subLog(sub), "objects", len(before))
	return nil
}

// subLog truncates a sub for logging (§5.11: never more than 8 characters of a
// user_key-derived value).
func subLog(sub string) string {
	if len(sub) <= 8 {
		return sub
	}
	return sub[:8] + "…"
}
