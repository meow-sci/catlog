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
		q = e.Writer()
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
		q = e.Writer()
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
		q = e.Writer()
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
		q = e.Writer()
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
		q = e.Writer()
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
		q = e.Writer()
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

// InsertEvents writes a batch with `INSERT OR IGNORE` against the
// (player_id, event_id) unique index, giving the idempotent union-merge D19
// promises: resending a batch changes nothing and is reported as deduped.
//
// q is normally the caller's transaction — §4.5.3 step 13 puts the events, the
// stream_state upsert and the ingest_batch row in one commit.
func (e *Events) InsertEvents(ctx context.Context, q Querier, playerID int64, evs []Event) (accepted, deduped int, err error) {
	if q == nil {
		q = e.Writer()
	}
	const q1 = `INSERT OR IGNORE INTO event
	  (event_id, player_id, flight_id, session_id, career, type, ver, sim_time, wall_time, recv_time, payload)
	  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	recv := e.nowMillis()
	for i, ev := range evs {
		if ev.Type == "" {
			return accepted, deduped, fmt.Errorf("store: event %d has no type", i)
		}
		payload := ev.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}
		res, err := q.ExecContext(ctx, q1,
			ids.Bytes(ev.ID), playerID, ids.NullBytes(ev.FlightID), ids.NullBytes(ev.SessionID),
			nullString(ev.Career), ev.Type, ev.Ver, ev.SimTime, ev.WallTime, recv, string(payload))
		if err != nil {
			return accepted, deduped, fmt.Errorf("store: insert event %s: %w", ids.String(ev.ID), err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return accepted, deduped, fmt.Errorf("store: insert event %s: %w", ids.String(ev.ID), err)
		}
		if n == 1 {
			accepted++
		} else {
			deduped++
		}
	}
	return accepted, deduped, nil
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
		`SELECT seq, event_id, player_id, flight_id, session_id, career, type, ver, sim_time, wall_time, recv_time, payload
		 FROM event WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("store: read events: %w", err)
	}
	defer rows.Close()

	var out []StoredEvent
	for rows.Next() {
		var (
			se                    StoredEvent
			eventID, flight, sess []byte
			career                sql.NullString
			payload               string
		)
		if err := rows.Scan(&se.Seq, &eventID, &se.PlayerID, &flight, &sess, &career, &se.Type, &se.Ver,
			&se.SimTime, &se.WallTime, &se.RecvTime, &payload); err != nil {
			return nil, fmt.Errorf("store: scan event: %w", err)
		}
		se.Career = career.String
		if se.ID, err = ids.FromBytes(eventID); err != nil {
			return nil, fmt.Errorf("store: event seq %d: %w", se.Seq, err)
		}
		if se.FlightID, err = ids.FromNullBytes(flight); err != nil {
			return nil, fmt.Errorf("store: event seq %d flight_id: %w", se.Seq, err)
		}
		if se.SessionID, err = ids.FromNullBytes(sess); err != nil {
			return nil, fmt.Errorf("store: event seq %d session_id: %w", se.Seq, err)
		}
		se.Payload = json.RawMessage(payload)
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
		q = e.Writer()
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
		q = e.Writer()
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
		q = e.Writer()
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
