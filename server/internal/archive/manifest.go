package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

// ManifestVer is the manifest schema version. Bumping it is how a future format
// change stays readable by an older restore (which refuses what it cannot
// understand rather than guessing).
const ManifestVer = 1

// maxManifestBytes caps a manifest read. A manifest is a few hundred bytes per
// chunk; anything past this is not one.
const maxManifestBytes = 64 << 20

// Manifest is `players/<sub>/manifest.json` (§5.10): the chunk list and the
// counts, plus the handful of player columns a restore needs to put the account
// back.
//
// It is an index, not a source of truth — the chunks are. Everything here can be
// recomputed by reading them, which is what makes it safe to overwrite on every
// run.
type Manifest struct {
	Ver int    `json:"ver"`
	Sub string `json:"sub"`
	// PlayerID and IdP are the two `player` columns a restore cannot derive.
	// Keeping player_id is what lets a restored log rebuild into byte-identical
	// projections (see store.RestorePlayer).
	PlayerID  int64  `json:"player_id"`
	IdP       string `json:"idp"`
	CreatedAt int64  `json:"created_at"`
	// Events, FirstSeq and LastSeq are the totals across every chunk.
	Events   int64 `json:"events"`
	FirstSeq int64 `json:"first_seq"`
	LastSeq  int64 `json:"last_seq"`
	// UpdatedAt is when the last run touched this player, unix ms.
	UpdatedAt int64 `json:"updated_at"`
	// Chunks are ordered by FirstSeq, which is also archive-run order.
	Chunks []ChunkRef `json:"chunks"`
}

// ChunkRef is one archived chunk.
type ChunkRef struct {
	Key      string `json:"key"`
	FirstSeq int64  `json:"first_seq"`
	LastSeq  int64  `json:"last_seq"`
	Events   int64  `json:"events"`
	// Bytes and SHA256 describe the compressed object exactly as stored, so a
	// restore can prove it read back what was written before it trusts a single
	// event out of it.
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// newManifest starts an empty manifest for a player.
func newManifest(sub string, playerID int64, idp string, createdAt int64) *Manifest {
	return &Manifest{Ver: ManifestVer, Sub: sub, PlayerID: playerID, IdP: idp, CreatedAt: createdAt}
}

// addChunk appends (or replaces) a chunk and recomputes the totals.
//
// Replace rather than append-only, because a run that wrote its chunk and then
// failed before advancing the cursor will write the same key again on the next
// attempt (§5.10 cursor handling). Same key, same content, one entry.
func (m *Manifest) addChunk(c ChunkRef) {
	replaced := false
	for i := range m.Chunks {
		if m.Chunks[i].Key == c.Key {
			m.Chunks[i] = c
			replaced = true
			break
		}
	}
	if !replaced {
		m.Chunks = append(m.Chunks, c)
	}
	sort.Slice(m.Chunks, func(i, j int) bool {
		if m.Chunks[i].FirstSeq != m.Chunks[j].FirstSeq {
			return m.Chunks[i].FirstSeq < m.Chunks[j].FirstSeq
		}
		return m.Chunks[i].Key < m.Chunks[j].Key
	})
	m.recount()
}

func (m *Manifest) recount() {
	m.Events, m.FirstSeq, m.LastSeq = 0, 0, 0
	for _, c := range m.Chunks {
		m.Events += c.Events
		if m.FirstSeq == 0 || c.FirstSeq < m.FirstSeq {
			m.FirstSeq = c.FirstSeq
		}
		if c.LastSeq > m.LastSeq {
			m.LastSeq = c.LastSeq
		}
	}
}

// Validate checks a manifest read back from a store before anything trusts it.
func (m *Manifest) Validate() error {
	if m.Ver != ManifestVer {
		return fmt.Errorf("archive: manifest version %d, this build understands %d", m.Ver, ManifestVer)
	}
	if err := ValidateSub(m.Sub); err != nil {
		return err
	}
	if m.PlayerID <= 0 {
		return fmt.Errorf("archive: manifest for %s has player_id %d", subLog(m.Sub), m.PlayerID)
	}
	if m.IdP == "" {
		return fmt.Errorf("archive: manifest for %s has no idp", subLog(m.Sub))
	}
	var events int64
	for i, c := range m.Chunks {
		if c.Key != ChunkKey(m.Sub, c.FirstSeq, c.LastSeq) {
			return fmt.Errorf("archive: manifest for %s: chunk %d key %q does not match its seq range",
				subLog(m.Sub), i, c.Key)
		}
		if c.FirstSeq <= 0 || c.LastSeq < c.FirstSeq {
			return fmt.Errorf("archive: manifest for %s: chunk %s has seq range %d-%d",
				subLog(m.Sub), c.Key, c.FirstSeq, c.LastSeq)
		}
		if c.Events <= 0 {
			return fmt.Errorf("archive: manifest for %s: chunk %s claims %d events", subLog(m.Sub), c.Key, c.Events)
		}
		if i > 0 && c.FirstSeq <= m.Chunks[i-1].LastSeq {
			return fmt.Errorf("archive: manifest for %s: chunks %s and %s overlap",
				subLog(m.Sub), m.Chunks[i-1].Key, c.Key)
		}
		events += c.Events
	}
	if events != m.Events {
		return fmt.Errorf("archive: manifest for %s totals %d events, its chunks hold %d",
			subLog(m.Sub), m.Events, events)
	}
	return nil
}

// encode renders a manifest for storage: indented, newline-terminated, and
// deterministic — encoding/json emits struct fields in declaration order, so the
// same manifest always produces the same bytes.
func (m *Manifest) encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("archive: encode manifest for %s: %w", subLog(m.Sub), err)
	}
	return buf.Bytes(), nil
}

// loadManifest reads a player's manifest, returning nil when there is none yet.
// A store that cannot be read at all (no [Getter]) is a programming error, not a
// missing manifest, and says so.
func loadManifest(ctx context.Context, s Store, sub string) (*Manifest, error) {
	g, ok := s.(Getter)
	if !ok {
		return nil, fmt.Errorf("archive: %T cannot read objects back, so manifests cannot be updated", s)
	}
	rc, err := g.Get(ctx, ManifestKey(sub))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	raw, err := io.ReadAll(io.LimitReader(rc, maxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("archive: read manifest for %s: %w", subLog(sub), err)
	}
	if int64(len(raw)) > maxManifestBytes {
		return nil, fmt.Errorf("archive: manifest for %s is larger than %d bytes", subLog(sub), maxManifestBytes)
	}

	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("archive: parse manifest for %s: %w", subLog(sub), err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// saveManifest writes a player's manifest.
func saveManifest(ctx context.Context, s Store, m *Manifest) error {
	raw, err := m.encode()
	if err != nil {
		return err
	}
	return s.Put(ctx, ManifestKey(m.Sub), bytes.NewReader(raw))
}
