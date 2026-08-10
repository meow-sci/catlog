package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/meow-sci/catlog/server/internal/ids"
	"github.com/meow-sci/catlog/server/internal/keys"
)

// Errors from the events.db typed queries. Callers map them onto the §4.9
// registry; the store deliberately has no opinion about HTTP status.
var (
	// ErrNotFound is returned instead of sql.ErrNoRows so callers need not
	// import database/sql to test for a miss.
	ErrNotFound = errors.New("store: not found")
	// ErrHandleTaken means the case-insensitive handle is already claimed —
	// by this player or another; first claim wins and is permanent (D9).
	ErrHandleTaken = errors.New("store: handle already claimed")
	// ErrHandleRetired means the handle belonged to a banned or deleted
	// account and can never be claimed again (D9). Distinct from
	// ErrHandleTaken for logging; both surface as handle_taken (§4.9).
	ErrHandleRetired = errors.New("store: handle retired")
)

// --- players ---------------------------------------------------------------

// Player is a row of `player` (§5.4).
type Player struct {
	ID        int64
	UserKey   keys.UserKey
	IdP       string
	CreatedAt int64 // unix ms
	BannedAt  sql.NullInt64
	BanReason sql.NullString
}

// Banned reports whether this player is banned (§4.5.3 step 4).
func (p Player) Banned() bool { return p.BannedAt.Valid }

// EnsurePlayer returns the player_id for uk, creating the row on first sight.
// idp and now are used only when creating.
//
// Idempotent by construction: the INSERT is OR IGNORE against the UNIQUE index
// on user_key, then the id is read back, so a concurrent first login cannot
// produce two players (and cannot be told apart from a repeat login).
func (e *Events) EnsurePlayer(ctx context.Context, q Querier, uk keys.UserKey, idp string, now int64) (int64, error) {
	if q == nil {
		q = e.autocommit()
	}
	if idp == "" {
		return 0, errors.New("store: EnsurePlayer with empty idp")
	}
	if _, err := q.ExecContext(ctx,
		`INSERT OR IGNORE INTO player (user_key, idp, created_at) VALUES (?, ?, ?)`,
		uk.Bytes(), idp, now); err != nil {
		return 0, fmt.Errorf("store: insert player: %w", err)
	}
	var id int64
	err := q.QueryRowContext(ctx, `SELECT player_id FROM player WHERE user_key = ?`, uk.Bytes()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: read player back: %w", err)
	}
	return id, nil
}

// PlayerByUserKey looks a player up by user_key, returning [ErrNotFound] on a miss.
func (e *Events) PlayerByUserKey(ctx context.Context, uk keys.UserKey) (Player, error) {
	return e.scanPlayer(e.Reader().QueryRowContext(ctx, playerSelect+` WHERE user_key = ?`, uk.Bytes()))
}

// PlayerByID looks a player up by player_id, returning [ErrNotFound] on a miss.
func (e *Events) PlayerByID(ctx context.Context, id int64) (Player, error) {
	return e.scanPlayer(e.Reader().QueryRowContext(ctx, playerSelect+` WHERE player_id = ?`, id))
}

const playerSelect = `SELECT player_id, user_key, idp, created_at, banned_at, ban_reason FROM player`

func (e *Events) scanPlayer(row *sql.Row) (Player, error) {
	var p Player
	var uk []byte
	err := row.Scan(&p.ID, &uk, &p.IdP, &p.CreatedAt, &p.BannedAt, &p.BanReason)
	if errors.Is(err, sql.ErrNoRows) {
		return Player{}, ErrNotFound
	}
	if err != nil {
		return Player{}, fmt.Errorf("store: scan player: %w", err)
	}
	if p.UserKey, err = keys.UserKeyFromBytes(uk); err != nil {
		return Player{}, fmt.Errorf("store: player %d: %w", p.ID, err)
	}
	return p, nil
}

// SetBan sets or clears a player's ban. reason is ignored when at is zero
// (an unban). Credential revocation and handle retirement are the caller's job
// (§4.7) — they are separate rows and belong in the same transaction.
func (e *Events) SetBan(ctx context.Context, q Querier, playerID int64, at int64, reason string) error {
	if q == nil {
		q = e.autocommit()
	}
	var err error
	if at == 0 {
		_, err = q.ExecContext(ctx, `UPDATE player SET banned_at = NULL, ban_reason = NULL WHERE player_id = ?`, playerID)
	} else {
		_, err = q.ExecContext(ctx, `UPDATE player SET banned_at = ?, ban_reason = ? WHERE player_id = ?`, at, reason, playerID)
	}
	if err != nil {
		return fmt.Errorf("store: set ban on player %d: %w", playerID, err)
	}
	return nil
}

// BannedUserKeys lists the user_key of every banned player — the ban half of
// the in-memory deny-list loaded at start (§5.8, §4.5.3 step 4).
func (e *Events) BannedUserKeys(ctx context.Context) ([]keys.UserKey, error) {
	rows, err := e.Reader().QueryContext(ctx,
		`SELECT user_key FROM player WHERE banned_at IS NOT NULL ORDER BY player_id`)
	if err != nil {
		return nil, fmt.Errorf("store: list banned players: %w", err)
	}
	defer rows.Close()

	var out []keys.UserKey
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("store: scan banned player: %w", err)
		}
		uk, err := keys.UserKeyFromBytes(raw)
		if err != nil {
			return nil, fmt.Errorf("store: banned player: %w", err)
		}
		out = append(out, uk)
	}
	return out, rows.Err()
}

// --- handles ---------------------------------------------------------------

// Handle is a row of `handle` (§5.4).
type Handle struct {
	Handle    string // display casing
	HandleLC  string // lowercase, the uniqueness key
	PlayerID  int64
	CreatedAt int64 // unix ms
}

// LC lowercases a handle the way `handle_lc` does. ASCII-only by construction:
// §4.7 restricts handles to US-ASCII, so there is no Unicode case folding to
// get wrong.
func LC(handle string) string { return strings.ToLower(handle) }

// ClaimHandle claims handle for playerID, in one transaction so the
// retired-handle check and the insert cannot interleave with another claim.
//
// It implements the DB half of §4.7: case-insensitive global uniqueness against
// `handle`, and permanent exclusion against `retired_handle`. Format, reserved
// words and per-account quotas are policy and belong to the identity layer.
//
// Returns [ErrHandleRetired] if the handle was ever retired, [ErrHandleTaken]
// if it is live under any casing.
func (e *Events) ClaimHandle(ctx context.Context, playerID int64, handle string, now int64) error {
	if handle == "" {
		return errors.New("store: ClaimHandle with empty handle")
	}
	lc := LC(handle)
	return e.WithWriteTx(ctx, func(tx *sql.Tx) error {
		retired, err := handleRetired(ctx, tx, lc)
		if err != nil {
			return err
		}
		if retired {
			return fmt.Errorf("%w: %q", ErrHandleRetired, handle)
		}

		// OR IGNORE rather than catching the constraint error: tursogo maps
		// every violation onto one sentinel with no extended result code, so
		// UNIQUE(handle_lc) and PRIMARY KEY(handle) would be indistinguishable.
		res, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO handle (handle, handle_lc, player_id, created_at) VALUES (?, ?, ?, ?)`,
			handle, lc, playerID, now)
		if err != nil {
			return fmt.Errorf("store: insert handle: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: insert handle: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: %q", ErrHandleTaken, handle)
		}
		return nil
	})
}

// RetireHandle deletes a live handle and records it as permanently retired
// (D9). Retiring an already-retired handle is a no-op, so ban-then-purge is
// safe to run twice.
func (e *Events) RetireHandle(ctx context.Context, q Querier, handle, reason string, at int64) error {
	if q == nil {
		q = e.autocommit()
	}
	lc := LC(handle)
	if _, err := q.ExecContext(ctx, `DELETE FROM handle WHERE handle_lc = ?`, lc); err != nil {
		return fmt.Errorf("store: delete handle %q: %w", handle, err)
	}
	if _, err := q.ExecContext(ctx,
		`INSERT OR IGNORE INTO retired_handle (handle_lc, reason, retired_at) VALUES (?, ?, ?)`,
		lc, reason, at); err != nil {
		return fmt.Errorf("store: retire handle %q: %w", handle, err)
	}
	return nil
}

// HandleByLC looks up a live handle case-insensitively, returning [ErrNotFound]
// on a miss.
func (e *Events) HandleByLC(ctx context.Context, handle string) (Handle, error) {
	var h Handle
	err := e.Reader().QueryRowContext(ctx,
		`SELECT handle, handle_lc, player_id, created_at FROM handle WHERE handle_lc = ?`, LC(handle)).
		Scan(&h.Handle, &h.HandleLC, &h.PlayerID, &h.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Handle{}, ErrNotFound
	}
	if err != nil {
		return Handle{}, fmt.Errorf("store: scan handle: %w", err)
	}
	return h, nil
}

// HandlesForPlayer lists a player's live handles, oldest first. Its length is
// what the §4.7 handle quota is checked against.
func (e *Events) HandlesForPlayer(ctx context.Context, playerID int64) ([]Handle, error) {
	rows, err := e.Reader().QueryContext(ctx,
		`SELECT handle, handle_lc, player_id, created_at FROM handle WHERE player_id = ? ORDER BY created_at, handle`, playerID)
	if err != nil {
		return nil, fmt.Errorf("store: list handles: %w", err)
	}
	defer rows.Close()

	var out []Handle
	for rows.Next() {
		var h Handle
		if err := rows.Scan(&h.Handle, &h.HandleLC, &h.PlayerID, &h.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan handle: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// HandleRetired reports whether a handle has been permanently retired (D9).
func (e *Events) HandleRetired(ctx context.Context, handle string) (bool, error) {
	return handleRetired(ctx, e.Reader(), LC(handle))
}

func handleRetired(ctx context.Context, q Querier, lc string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM retired_handle WHERE handle_lc = ?`, lc).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("store: check retired handle: %w", err)
	default:
		return true, nil
	}
}

// --- credentials -----------------------------------------------------------

// Credential is a row of `credential` (§5.4): one issued license, keyed by the
// RFC 7638 thumbprint of the client's public key.
type Credential struct {
	JKT        string
	PlayerID   int64
	Handle     string
	LicenseJTI string
	IssuedAt   int64 // unix ms
	ExpiresAt  int64 // unix ms
	RevokedAt  sql.NullInt64
}

// Revoked reports whether the credential has been revoked (§4.5.3 step 5).
func (c Credential) Revoked() bool { return c.RevokedAt.Valid }

// InsertCredential records an issued license.
func (e *Events) InsertCredential(ctx context.Context, q Querier, c Credential) error {
	if q == nil {
		q = e.autocommit()
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO credential (jkt, player_id, handle, license_jti, issued_at, expires_at, revoked_at)
		 VALUES (?, ?, ?, ?, ?, ?, NULL)`,
		c.JKT, c.PlayerID, c.Handle, c.LicenseJTI, c.IssuedAt, c.ExpiresAt); err != nil {
		return fmt.Errorf("store: insert credential: %w", err)
	}
	return nil
}

// CredentialByJKT is the §4.5.3 step-5 lookup, returning [ErrNotFound] on a miss.
func (e *Events) CredentialByJKT(ctx context.Context, jkt string) (Credential, error) {
	var c Credential
	err := e.Reader().QueryRowContext(ctx,
		`SELECT jkt, player_id, handle, license_jti, issued_at, expires_at, revoked_at FROM credential WHERE jkt = ?`, jkt).
		Scan(&c.JKT, &c.PlayerID, &c.Handle, &c.LicenseJTI, &c.IssuedAt, &c.ExpiresAt, &c.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("store: scan credential: %w", err)
	}
	return c, nil
}

// CredentialsForPlayer lists a player's credentials, newest first — what the
// dashboard shows (§5.7).
func (e *Events) CredentialsForPlayer(ctx context.Context, playerID int64) ([]Credential, error) {
	rows, err := e.Reader().QueryContext(ctx,
		`SELECT jkt, player_id, handle, license_jti, issued_at, expires_at, revoked_at
		 FROM credential WHERE player_id = ? ORDER BY issued_at DESC, jkt`, playerID)
	if err != nil {
		return nil, fmt.Errorf("store: list credentials: %w", err)
	}
	defer rows.Close()

	var out []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.JKT, &c.PlayerID, &c.Handle, &c.LicenseJTI, &c.IssuedAt, &c.ExpiresAt, &c.RevokedAt); err != nil {
			return nil, fmt.Errorf("store: scan credential: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RevokeCredential marks one credential revoked. Already-revoked credentials
// keep their original timestamp.
func (e *Events) RevokeCredential(ctx context.Context, q Querier, jkt string, at int64) error {
	if q == nil {
		q = e.autocommit()
	}
	if _, err := q.ExecContext(ctx,
		`UPDATE credential SET revoked_at = ? WHERE jkt = ? AND revoked_at IS NULL`, at, jkt); err != nil {
		return fmt.Errorf("store: revoke credential: %w", err)
	}
	return nil
}

// RevokeCredentialsForPlayer revokes every live credential a player holds —
// the ban path (§4.7).
func (e *Events) RevokeCredentialsForPlayer(ctx context.Context, q Querier, playerID int64, at int64) error {
	if q == nil {
		q = e.autocommit()
	}
	if _, err := q.ExecContext(ctx,
		`UPDATE credential SET revoked_at = ? WHERE player_id = ? AND revoked_at IS NULL`, at, playerID); err != nil {
		return fmt.Errorf("store: revoke player %d credentials: %w", playerID, err)
	}
	return nil
}

// RevokedJKTs lists every revoked thumbprint — half of the deny-list loaded at
// start (§5.8).
func (e *Events) RevokedJKTs(ctx context.Context) ([]string, error) {
	rows, err := e.Reader().QueryContext(ctx, `SELECT jkt FROM credential WHERE revoked_at IS NOT NULL ORDER BY jkt`)
	if err != nil {
		return nil, fmt.Errorf("store: list revoked jkts: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var jkt string
		if err := rows.Scan(&jkt); err != nil {
			return nil, fmt.Errorf("store: scan revoked jkt: %w", err)
		}
		out = append(out, jkt)
	}
	return out, rows.Err()
}

// --- events ----------------------------------------------------------------

// Event is a validated envelope ready to store (§4.1). ULIDs are values here
// and 16-byte BLOBs in the table.
type Event struct {
	ID        ids.ID          // envelope `id`, the dedup key
	FlightID  ids.ID          // ids.Zero → NULL (session and roster events)
	SessionID ids.ID          // never zero in a valid envelope
	Career    string          // envelope `career`: which save this session is playing (§4.1)
	Type      string          // e.g. "vehicle.rud"
	Ver       int             // payload schema version, ≥1
	SimTime   sql.NullFloat64 // Universe sim seconds since the career began
	WallTime  int64           // client unix ms, untrusted
	Payload   json.RawMessage // per-type object, may be {}
}

// StoredEvent is an Event read back with its server-assigned seq and receive
// time — what the projector and the archiver consume.
type StoredEvent struct {
	Seq      int64
	PlayerID int64
	RecvTime int64
	Event
}

// EventInsertChunk is how many event rows one insert statement carries — the
// same shape as [FeedInsertChunk] and stats.Batch's flush, and for the same
// reason: a tursogo statement costs ~15 µs regardless of what it says, so a
// 2000-event batch as 2000 statements was paying for the round trips, not the
// rows. 500 rows × 12 columns keeps the bound-parameter count well under
// SQLite's ceiling.
const EventInsertChunk = 500

// InsertEvents writes a batch with `INSERT OR IGNORE` against the
// (player_id, event_id) unique index, giving the idempotent union-merge D19
// promises: resending a batch changes nothing and is reported as deduped.
//
// The batch goes in as chunked multi-row inserts. Per-row acceptance is not
// observable that way — RowsAffected reports only how many rows the chunk
// actually inserted — but nothing ever consumed it: the §4.4 response is the
// two aggregates, and SQLite ignores an intra-statement duplicate exactly as
// it ignores a stored one, so the totals cannot drift.
//
// `seq` is assigned from the `event_seq` allocator rather than left to the
// rowid (0004), because rowid allocation reuses a number whose row was deleted
// and both the projector and the archiver have already passed it.
//
// q is normally the caller's transaction — §4.5.3 step 13 puts the events, the
// stream_state upsert and the ingest_batch row in one commit. A nil q gets a
// transaction of its own, which the allocator's read-then-write requires.
func (e *Events) InsertEvents(ctx context.Context, q Querier, playerID int64, evs []Event) (accepted, deduped int, err error) {
	if q == nil {
		// The seq allocator is a read followed by a write (0004), so it needs a
		// transaction of its own when the caller did not bring one: the writer
		// handle serializes *statements*, not sequences of them, and two
		// autocommit inserts interleaving between the SELECT and the UPDATE
		// would hand the same seq to both.
		txErr := e.WithWriteTx(ctx, func(tx *sql.Tx) error {
			var err error
			accepted, deduped, err = e.InsertEvents(ctx, tx, playerID, evs)
			return err
		})
		if txErr != nil {
			return 0, 0, txErr
		}
		return accepted, deduped, nil
	}
	for i, ev := range evs {
		if ev.Type == "" {
			return 0, 0, fmt.Errorf("store: event %d has no type", i)
		}
	}
	if len(evs) == 0 {
		return 0, 0, nil
	}

	// seq is assigned explicitly rather than left to the rowid, so that a
	// deletion can never cause one to be handed out twice (0004).
	seq, err := e.reserveSeqs(ctx, q, len(evs))
	if err != nil {
		return 0, 0, err
	}

	// The one fork on the write path (0005). A shadowbanned player's batch is
	// accepted, verified, deduped, sequenced and stored exactly like anyone
	// else's — into the withheld table instead of the log. Nothing above this
	// line knows, and nothing the client receives differs, which is what makes
	// the ban silent: their mod keeps working and keeps producing the evidence
	// the review reads.
	withheld, err := e.Shadowbanned(ctx, q, playerID)
	if err != nil {
		return 0, 0, err
	}
	if withheld {
		return e.insertWithheldEvents(ctx, q, playerID, evs, seq)
	}

	recv := e.nowMillis()
	var sb strings.Builder
	for start := 0; start < len(evs); start += EventInsertChunk {
		end := min(start+EventInsertChunk, len(evs))

		sb.Reset()
		sb.WriteString(`INSERT OR IGNORE INTO event
	  (seq, event_id, player_id, flight_id, session_id, career, type, ver, sim_time, wall_time, recv_time, payload, enc)
	  VALUES `)
		args := make([]any, 0, (end-start)*13)
		for i := start; i < end; i++ {
			ev := evs[i]
			payload, enc := e.encodePayload(ev.Payload)
			if i > start {
				sb.WriteByte(',')
			}
			sb.WriteString("(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
			args = append(args,
				seq+int64(i), ids.Bytes(ev.ID), playerID, ids.NullBytes(ev.FlightID), ids.NullBytes(ev.SessionID),
				nullString(ev.Career), ev.Type, ev.Ver, ev.SimTime, ev.WallTime, recv, payload, enc)
		}
		res, err := q.ExecContext(ctx, sb.String(), args...)
		if err != nil {
			return accepted, deduped, fmt.Errorf("store: insert events: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return accepted, deduped, fmt.Errorf("store: insert events: %w", err)
		}
		accepted += int(n)
		deduped += (end - start) - int(n)
	}
	return accepted, deduped, nil
}

// reserveSeqs hands out a contiguous run of n sequence numbers and advances the
// allocator, both inside the caller's transaction (0004).
//
// The run is reserved whether or not every row in it lands: a batch that dedups
// against `ev_dedup` leaves its reserved numbers unused, and that is the correct
// outcome. Every reader scans `seq > cursor`; none assumes the column is dense.
func (e *Events) reserveSeqs(ctx context.Context, q Querier, n int) (int64, error) {
	if n <= 0 {
		return 0, nil
	}
	var first int64
	err := q.QueryRowContext(ctx, `SELECT next_seq FROM event_seq WHERE id = 1`).Scan(&first)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("store: the event seq allocator row is missing")
	}
	if err != nil {
		return 0, fmt.Errorf("store: reserve %d seqs: %w", n, err)
	}
	if _, err := q.ExecContext(ctx,
		`UPDATE event_seq SET next_seq = ? WHERE id = 1`, first+int64(n)); err != nil {
		return 0, fmt.Errorf("store: advance the seq allocator: %w", err)
	}
	return first, nil
}

// reconcileSeqAllocator lifts the allocator to the head of the log at open.
//
// It should never have anything to do: the allocator is the only source of a
// live seq, and the one path that inserts at an explicit one raises the floor
// itself ([Events.RestoreEvents]). It runs anyway, once per open, because the
// failure it prevents is silent — a `next_seq` sitting below an occupied row
// makes `INSERT OR IGNORE` drop a real event and report it as deduped — and
// because the alternative to a cheap invariant here is trusting that no future
// caller, migration or operator ever wrote a row another way.
func (e *Events) reconcileSeqAllocator(ctx context.Context) error {
	head, err := e.MaxSeq(ctx)
	if err != nil {
		return err
	}
	// A withheld row owns its seq exactly as firmly as a live one: it is coming
	// back to that position if the ban is lifted (0005).
	withheld, err := e.MaxWithheldSeq(ctx)
	if err != nil {
		return err
	}
	return e.RaiseSeqFloor(ctx, nil, max(head, withheld))
}

// RaiseSeqFloor lifts the allocator past seq, so that nothing inserted at an
// explicit sequence number can later be collided with by an allocated one.
//
// It is what [Events.RestoreEvents] calls: a restore re-inserts archived events
// at their original seq (§5.10), which the allocator knows nothing about, and a
// restore into a database whose allocator sits below the restored range would
// hand those numbers out a second time.
//
// Lowering is impossible by construction — the allocator only ever moves
// forward, so calling this with an old seq is a no-op rather than an error.
func (e *Events) RaiseSeqFloor(ctx context.Context, q Querier, seq int64) error {
	if q == nil {
		q = e.autocommit()
	}
	if seq <= 0 {
		return nil
	}
	if _, err := q.ExecContext(ctx,
		`UPDATE event_seq SET next_seq = ? WHERE id = 1 AND next_seq <= ?`, seq+1, seq); err != nil {
		return fmt.Errorf("store: raise the seq floor to %d: %w", seq, err)
	}
	return nil
}

// nullString stores "" as SQL NULL. Every ULID column already distinguishes
// "absent" from "zero bytes" that way (ids.NullBytes); career is text and gets
// the same treatment, so a pre-career row and a row whose client omitted it are
// the same thing to every reader.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// EventsSince reads up to limit events with seq > after, in seq order — the
// projector's cursor scan (§5.6) and the archiver's chunk scan (§5.10).
func (e *Events) EventsSince(ctx context.Context, after int64, limit int) ([]StoredEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := e.Reader().QueryContext(ctx,
		eventSelect+` WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("store: read events: %w", err)
	}
	return e.scanStoredEvents(rows, limit)
}

// PlayerEvents reads one page of a player's own log, **newest first** — the
// §4.8 raw-event view.
//
// before is the exclusive upper bound on `seq`: zero or less starts at the
// newest event, and paging on means passing back the seq of the oldest row of
// the previous page. A cursor rather than an offset because the log is
// append-only at the head, so an offset would drift under a page whenever the
// player shipped between two requests.
//
// It runs off `ev_player (player_id, seq)`, so the cost is the page, not the
// player's history. The seq column looks redundant — an index entry ends in
// the rowid, and seq IS the rowid — but on tursogo it is load-bearing: the
// optimizer only turns `seq < ?` into an index seek (SeekLT) when seq is a
// named column; against ev_player(player_id) it walks entries newest-first
// and filters, making page N cost N pages (TestEvPlayerIndexPlans pins this).
func (e *Events) PlayerEvents(ctx context.Context, playerID, before int64, limit int) ([]StoredEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	q := eventSelect + ` WHERE player_id = ? ORDER BY seq DESC LIMIT ?`
	args := []any{playerID, limit}
	if before > 0 {
		q = eventSelect + ` WHERE player_id = ? AND seq < ? ORDER BY seq DESC LIMIT ?`
		args = []any{playerID, before, limit}
	}
	rows, err := e.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: read player %d events: %w", playerID, err)
	}
	return e.scanStoredEvents(rows, limit)
}

// RecentEvents reads one page of the whole log, **newest first** — the global
// raw-event view (`GET /v1/events`), which is [Events.PlayerEvents] without the
// player.
//
// before is the same exclusive upper bound on `seq`: zero or less starts at the
// newest event. `seq` is the rowid, so this is a reverse rowid scan — no new
// index, and the cost is the page, not the log.
func (e *Events) RecentEvents(ctx context.Context, before int64, limit int) ([]StoredEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	q := eventSelect + ` ORDER BY seq DESC LIMIT ?`
	args := []any{limit}
	if before > 0 {
		q = eventSelect + ` WHERE seq < ? ORDER BY seq DESC LIMIT ?`
		args = []any{before, limit}
	}
	rows, err := e.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: read recent events: %w", err)
	}
	return e.scanStoredEvents(rows, limit)
}

const eventSelect = `SELECT seq, event_id, player_id, flight_id, session_id, career, type, ver, sim_time, wall_time, recv_time, payload, enc FROM event`

// scanStoredEvents reads a result set of event rows. want is the caller's LIMIT,
// used only to size the result slice.
//
// This is the single read seam every stored payload passes through, which is
// where `enc` is folded away: an enc != 0 payload is decompressed here (see
// payload.go), so every consumer — projector, read API, archive — receives
// JSON byte-identical to what was ingested, whatever the row's encoding.
//
// Two details here are about allocation rather than clarity, and they are worth
// the words because this is the projector's read path: every event catlog has
// ever stored goes through it once per fold and once per rebuild pass.
//
// The payload is scanned into a `[]byte` rather than a `string`. Scanning into a
// string copies the driver's bytes into one, and `json.RawMessage(s)` then
// copies that string into a fresh slice — two copies of every payload in the
// log. Scanning into a byte slice is the one copy that `database/sql` requires
// anyway (the driver's buffer is only valid until the next Next).
//
// The result slice is pre-sized to the caller's limit. A batch of a thousand
// events otherwise grows a slice of ~150-byte structs from nil, which is a
// dozen reallocations and a dozen copies of everything scanned so far.
func (e *Events) scanStoredEvents(rows *sql.Rows, want int) ([]StoredEvent, error) {
	defer rows.Close()
	if want < 0 {
		want = 0
	}
	out := make([]StoredEvent, 0, want)
	for rows.Next() {
		var (
			se                    StoredEvent
			eventID, flight, sess []byte
			career                sql.NullString
			payload               []byte
			enc                   int
		)
		if err := rows.Scan(&se.Seq, &eventID, &se.PlayerID, &flight, &sess, &career, &se.Type, &se.Ver,
			&se.SimTime, &se.WallTime, &se.RecvTime, &payload, &enc); err != nil {
			return nil, fmt.Errorf("store: scan event: %w", err)
		}
		se.Career = career.String
		var err error
		if se.ID, err = ids.FromBytes(eventID); err != nil {
			return nil, fmt.Errorf("store: event seq %d: %w", se.Seq, err)
		}
		if se.FlightID, err = ids.FromNullBytes(flight); err != nil {
			return nil, fmt.Errorf("store: event seq %d flight_id: %w", se.Seq, err)
		}
		if se.SessionID, err = ids.FromNullBytes(sess); err != nil {
			return nil, fmt.Errorf("store: event seq %d session_id: %w", se.Seq, err)
		}
		if se.Payload, err = e.decodePayload(payload, enc); err != nil {
			return nil, fmt.Errorf("store: event seq %d: %w", se.Seq, err)
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// MaxSeq is the highest event seq, or 0 on an empty log — the projector's
// lag calculation (§5.9 stats).
func (e *Events) MaxSeq(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	if err := e.Reader().QueryRowContext(ctx, `SELECT max(seq) FROM event`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("store: read max seq: %w", err)
	}
	return seq.Int64, nil
}

// CountEvents counts a player's events, or every event when playerID is 0.
func (e *Events) CountEvents(ctx context.Context, playerID int64) (int64, error) {
	var (
		n   int64
		err error
	)
	if playerID == 0 {
		err = e.Reader().QueryRowContext(ctx, `SELECT count(*) FROM event`).Scan(&n)
	} else {
		err = e.Reader().QueryRowContext(ctx, `SELECT count(*) FROM event WHERE player_id = ?`, playerID).Scan(&n)
	}
	if err != nil {
		return 0, fmt.Errorf("store: count events: %w", err)
	}
	return n, nil
}

// --- ingest batches --------------------------------------------------------

// BatchSeen reports whether (player, batch_id) has already been stored — the
// §4.5.3 step-11 replay short-circuit. batchID is the proof's jti.
func (e *Events) BatchSeen(ctx context.Context, q Querier, playerID int64, batchID string) (bool, error) {
	if q == nil {
		q = e.Reader()
	}
	var one int
	err := q.QueryRowContext(ctx,
		`SELECT 1 FROM ingest_batch WHERE player_id = ? AND batch_id = ?`, playerID, batchID).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("store: check batch: %w", err)
	default:
		return true, nil
	}
}

// InsertBatch records an accepted batch. OR IGNORE so a racing replay commits
// harmlessly instead of failing the whole transaction.
func (e *Events) InsertBatch(ctx context.Context, q Querier, playerID int64, batchID string, nEvents int, recvTime int64) error {
	if q == nil {
		q = e.autocommit()
	}
	if _, err := q.ExecContext(ctx,
		`INSERT OR IGNORE INTO ingest_batch (player_id, batch_id, n_events, recv_time) VALUES (?, ?, ?, ?)`,
		playerID, batchID, nEvents, recvTime); err != nil {
		return fmt.Errorf("store: insert batch: %w", err)
	}
	return nil
}

// --- stream state ----------------------------------------------------------

// StreamState is a row of `stream_state` (§5.4): the per-stream hash chain the
// §4.5.3 step-12 check walks.
type StreamState struct {
	PlayerID  int64
	SID       ids.ID
	JKT       string
	LastSeq   int64
	LastBH    string // b64u(sha256(previous batch body)), the next batch's `ph`
	Gap       bool   // a seq was skipped; forensic marker only
	UpdatedAt int64
}

// StreamState reads a stream's chain head. found is false for a stream's first
// batch, where §4.5.3 requires seq == 1 and no `ph`.
func (e *Events) StreamState(ctx context.Context, q Querier, playerID int64, sid ids.ID) (StreamState, bool, error) {
	if q == nil {
		q = e.Reader()
	}
	s := StreamState{PlayerID: playerID, SID: sid}
	var gap int
	err := q.QueryRowContext(ctx,
		`SELECT jkt, last_seq, last_bh, gap, updated_at FROM stream_state WHERE player_id = ? AND sid = ?`,
		playerID, ids.Bytes(sid)).Scan(&s.JKT, &s.LastSeq, &s.LastBH, &gap, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return StreamState{}, false, nil
	}
	if err != nil {
		return StreamState{}, false, fmt.Errorf("store: read stream state: %w", err)
	}
	s.Gap = gap != 0
	return s, true, nil
}

// UpsertStreamState advances a stream's chain head. `gap` is sticky: once a
// stream has skipped a seq the marker stays for forensics (§4.5.3 step 12).
func (e *Events) UpsertStreamState(ctx context.Context, q Querier, s StreamState) error {
	if q == nil {
		q = e.autocommit()
	}
	gap := 0
	if s.Gap {
		gap = 1
	}
	if _, err := q.ExecContext(ctx,
		`INSERT INTO stream_state (player_id, sid, jkt, last_seq, last_bh, gap, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (player_id, sid) DO UPDATE SET
		   jkt = excluded.jkt, last_seq = excluded.last_seq, last_bh = excluded.last_bh,
		   gap = max(stream_state.gap, excluded.gap), updated_at = excluded.updated_at`,
		s.PlayerID, ids.Bytes(s.SID), s.JKT, s.LastSeq, s.LastBH, gap, s.UpdatedAt); err != nil {
		return fmt.Errorf("store: upsert stream state: %w", err)
	}
	return nil
}

// StreamCensus is the population of `stream_state`, split by the sticky `gap`
// marker (§4.5.3 step 12) — the numbers `GET /admin/stats` reports.
type StreamCensus struct {
	// Total is how many streams exist across every player.
	Total int64
	// Gapped is how many of them skipped a seq at least once.
	Gapped int64
	// GappedPlayers is how many distinct players own at least one gapped
	// stream. A single player churning through streams inflates Gapped; this
	// says how widely it is happening.
	GappedPlayers int64
}

// StreamCensus counts streams and gaps.
//
// This is the one thing the §4.5.3 stream chain genuinely buys that nothing
// else in the system provides: a batch that never arrived leaves a permanent,
// per-stream mark. It is deliberately loss-tolerant — a gap is accepted and
// recorded, never rejected — so it is only worth maintaining if somebody
// actually looks at it, which is what this query is for.
func (e *Events) StreamCensus(ctx context.Context) (StreamCensus, error) {
	var c StreamCensus
	err := e.Reader().QueryRowContext(ctx,
		`SELECT count(*),
		        coalesce(sum(CASE WHEN gap != 0 THEN 1 ELSE 0 END), 0),
		        count(DISTINCT CASE WHEN gap != 0 THEN player_id END)
		 FROM stream_state`).
		Scan(&c.Total, &c.Gapped, &c.GappedPlayers)
	if err != nil {
		return StreamCensus{}, fmt.Errorf("store: stream census: %w", err)
	}
	return c, nil
}

// --- tombstones ------------------------------------------------------------

// Tombstone is a row of `tombstone` (§4.7): what a purge leaves behind.
type Tombstone struct {
	UserKey keys.UserKey
	Reason  string
	At      int64
}

// InsertTombstone records a purged account. OR IGNORE keeps the original
// reason and timestamp if the purge is repeated.
func (e *Events) InsertTombstone(ctx context.Context, q Querier, t Tombstone) error {
	if q == nil {
		q = e.autocommit()
	}
	if _, err := q.ExecContext(ctx,
		`INSERT OR IGNORE INTO tombstone (user_key, reason, at) VALUES (?, ?, ?)`,
		t.UserKey.Bytes(), t.Reason, t.At); err != nil {
		return fmt.Errorf("store: insert tombstone: %w", err)
	}
	return nil
}

// Tombstones lists every purged account — the other half of the deny-list
// loaded at start (§5.8).
func (e *Events) Tombstones(ctx context.Context) ([]Tombstone, error) {
	rows, err := e.Reader().QueryContext(ctx, `SELECT user_key, reason, at FROM tombstone ORDER BY at, user_key`)
	if err != nil {
		return nil, fmt.Errorf("store: list tombstones: %w", err)
	}
	defer rows.Close()

	var out []Tombstone
	for rows.Next() {
		var (
			t  Tombstone
			uk []byte
		)
		if err := rows.Scan(&uk, &t.Reason, &t.At); err != nil {
			return nil, fmt.Errorf("store: scan tombstone: %w", err)
		}
		if t.UserKey, err = keys.UserKeyFromBytes(uk); err != nil {
			return nil, fmt.Errorf("store: tombstone: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
