package stats

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
)

// systemFold maintains the immutable celestial catalogue and binds a career to
// the first system discovered for it. It is intentionally a state fold: board
// folds read that binding from the same Batch.
type systemFold struct{}

func (systemFold) Name() string { return "system" }

func (systemFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	switch ev.Type {
	case "system.discovered":
		p, ok := payloadOf[SystemDiscovered](ev)
		if !ok || p.System == "" {
			return nil
		}
		if err := b.UpsertSystem(ctx, p, ev.Seq); err != nil {
			return err
		}
		return b.BindCareerSystem(ctx, ev.PlayerID, ev.Career, p.System, ev.Seq)
	case "system.body":
		p, ok := payloadOf[SystemBody](ev)
		if !ok || p.System == "" || p.Body == "" {
			return nil
		}
		return b.InsertSystemBody(ctx, p, ev.Seq)
	default:
		return nil
	}
}

const systemSlugMax = 48

func systemSlug(name, hash string) string {
	var out strings.Builder
	out.Grow(min(len(name), systemSlugMax))
	hyphen := false
	for i := 0; i < len(name) && out.Len() < systemSlugMax; i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			if hyphen && out.Len() > 0 && out.Len() < systemSlugMax {
				out.WriteByte('-')
			}
			hyphen = false
			if out.Len() < systemSlugMax {
				out.WriteByte(c)
			}
			continue
		}
		hyphen = true
	}
	base := strings.Trim(out.String(), "-")
	if base != "" {
		return base
	}
	return hash[:min(8, len(hash))]
}

type systemEntry struct {
	systemID         string
	name             string
	slug             string
	homeBody         string
	bodyCount        int64
	reportedComplete bool
	firstSeq         int64
	exists           bool
	dirty            bool
}

func (b *Batch) systemEntry(ctx context.Context, hash string) (*systemEntry, error) {
	if e, ok := b.systems[hash]; ok {
		return e, nil
	}
	e := &systemEntry{}
	var complete int64
	err := b.tx.QueryRowContext(ctx,
		`SELECT system_id, name, slug, home_body, body_count, reported_complete, first_seq
		 FROM system WHERE hash = ?`, hash).
		Scan(&e.systemID, &e.name, &e.slug, &e.homeBody, &e.bodyCount, &complete, &e.firstSeq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("stats: read system %q: %w", hash, err)
	default:
		e.exists, e.reportedComplete = true, complete != 0
	}
	b.systems[hash] = e
	return e, nil
}

func (b *Batch) touchSystem(hash string, e *systemEntry) {
	if !e.dirty {
		e.dirty = true
		b.dirtySystems = append(b.dirtySystems, hash)
	}
}

func (b *Batch) slugOwner(ctx context.Context, slug string) (string, error) {
	for hash, e := range b.systems {
		if e.exists && e.slug == slug {
			return hash, nil
		}
	}
	var hash string
	err := b.tx.QueryRowContext(ctx, `SELECT hash FROM system WHERE slug = ?`, slug).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("stats: resolve system slug %q: %w", slug, err)
	}
	return hash, nil
}

func (b *Batch) allocateSystemSlug(ctx context.Context, name, hash string) (string, error) {
	base := systemSlug(name, hash)
	for suffix := 1; ; suffix++ {
		candidate := base
		if suffix > 1 {
			candidate = base + "-" + strconv.Itoa(suffix)
		}
		owner, err := b.slugOwner(ctx, candidate)
		if err != nil {
			return "", err
		}
		if owner == "" || owner == hash {
			return candidate, nil
		}
	}
}

// UpsertSystem applies immutable-first-write identity and monotone completeness.
func (b *Batch) UpsertSystem(ctx context.Context, p SystemDiscovered, seq int64) error {
	e, err := b.systemEntry(ctx, p.System)
	if err != nil {
		return err
	}
	if e.exists {
		if e.systemID != p.ID || e.name != p.Name || e.homeBody != p.Home || e.bodyCount != int64(p.Bodies) {
			slog.Warn("system discovery conflicts with immutable first write",
				"hash", p.System, "seq", seq, "first_seq", e.firstSeq)
			return nil
		}
		if p.Complete && !e.reportedComplete {
			e.reportedComplete = true
			b.touchSystem(p.System, e)
		}
		return nil
	}
	slug, err := b.allocateSystemSlug(ctx, p.Name, p.System)
	if err != nil {
		return err
	}
	e.systemID, e.name, e.slug, e.homeBody = p.ID, p.Name, slug, p.Home
	e.bodyCount, e.reportedComplete, e.firstSeq = int64(p.Bodies), p.Complete, seq
	e.exists = true
	b.touchSystem(p.System, e)
	return nil
}

func (b *Batch) flushSystems(ctx context.Context) error {
	if len(b.dirtySystems) == 0 {
		return nil
	}
	b.systemKeys = append(b.systemKeys[:0], b.dirtySystems...)
	slices.Sort(b.systemKeys)
	err := b.write(ctx, len(b.systemKeys), 8,
		`INSERT INTO system (hash, system_id, name, slug, home_body, body_count, reported_complete, first_seq) VALUES `,
		` ON CONFLICT (hash) DO UPDATE SET
		   reported_complete = CASE WHEN system.reported_complete <> 0 OR excluded.reported_complete <> 0 THEN 1 ELSE 0 END`,
		func(i int, args []any) []any {
			hash := b.systemKeys[i]
			e := b.systems[hash]
			e.dirty = false
			return append(args, hash, e.systemID, e.name, e.slug, e.homeBody, e.bodyCount, boolInt(e.reportedComplete), e.firstSeq)
		})
	if err != nil {
		return fmt.Errorf("stats: flush system: %w", err)
	}
	b.dirtySystems = b.dirtySystems[:0]
	return nil
}

type systemBodyKey struct{ hash, body string }

type systemBodyEntry struct {
	p        SystemBody
	firstSeq int64
	exists   bool
	dirty    bool
}

func (b *Batch) systemBodyEntry(ctx context.Context, k systemBodyKey) (*systemBodyEntry, error) {
	if e, ok := b.systemBodies[k]; ok {
		return e, nil
	}
	e := &systemBodyEntry{}
	err := b.tx.QueryRowContext(ctx,
		`SELECT first_seq FROM system_body WHERE hash = ? AND body = ?`, k.hash, k.body).Scan(&e.firstSeq)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("stats: read system body %q/%q: %w", k.hash, k.body, err)
	default:
		e.exists = true
	}
	b.systemBodies[k] = e
	return e, nil
}

// InsertSystemBody retains the first row for a (hash, body), including when a
// body arrives before its discovery header.
func (b *Batch) InsertSystemBody(ctx context.Context, p SystemBody, seq int64) error {
	k := systemBodyKey{p.System, p.Body}
	e, err := b.systemBodyEntry(ctx, k)
	if err != nil || e.exists {
		return err
	}
	e.p, e.firstSeq, e.exists, e.dirty = p, seq, true, true
	b.dirtySystemBodies = append(b.dirtySystemBodies, k)
	if catalogue, loaded := b.systemCatalogues[p.System]; loaded {
		catalogue[p.Body] = p.Kind
	}
	return nil
}

// systemCatalogue loads one immutable system's body→normalized-kind set and
// merges any bodies not yet flushed by this Batch.
func (b *Batch) systemCatalogue(ctx context.Context, hash string) (map[string]string, error) {
	if catalogue, ok := b.systemCatalogues[hash]; ok {
		return catalogue, nil
	}
	rows, err := b.tx.QueryContext(ctx,
		`SELECT body, kind FROM system_body WHERE hash = ?`, hash)
	if err != nil {
		return nil, fmt.Errorf("stats: read system catalogue %q: %w", hash, err)
	}
	defer rows.Close()
	catalogue := map[string]string{}
	for rows.Next() {
		var body, kind string
		if err := rows.Scan(&body, &kind); err != nil {
			return nil, fmt.Errorf("stats: scan system catalogue %q: %w", hash, err)
		}
		catalogue[body] = kind
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stats: read system catalogue %q: %w", hash, err)
	}
	for key, entry := range b.systemBodies {
		if key.hash == hash && entry.exists && entry.p.Body != "" {
			catalogue[key.body] = entry.p.Kind
		}
	}
	b.systemCatalogues[hash] = catalogue
	return catalogue, nil
}

func (b *Batch) flushSystemBodies(ctx context.Context) error {
	if len(b.dirtySystemBodies) == 0 {
		return nil
	}
	b.systemBodyKeys = append(b.systemBodyKeys[:0], b.dirtySystemBodies...)
	slices.SortFunc(b.systemBodyKeys, func(a, z systemBodyKey) int {
		if n := strings.Compare(a.hash, z.hash); n != 0 {
			return n
		}
		return strings.Compare(a.body, z.body)
	})
	err := b.write(ctx, len(b.systemBodyKeys), 28,
		`INSERT INTO system_body (
		 hash, body, name, class, kind, rank, parent,
		 radius_m, mass_kg, soi_m, atmo_m, ocean_m, angvel, axis_x, axis_y, axis_z,
		 sma_m, ecc, inc_deg, lan_deg, argp_deg, t_pe, period_s,
		 ccf_to_cce_t0_x, ccf_to_cce_t0_y, ccf_to_cce_t0_z, ccf_to_cce_t0_w, first_seq) VALUES `,
		` ON CONFLICT (hash, body) DO NOTHING`,
		func(i int, args []any) []any {
			k := b.systemBodyKeys[i]
			e := b.systemBodies[k]
			p := e.p
			e.dirty = false
			return append(args,
				p.System, p.Body, p.Name, p.Class, p.Kind, p.Rank, nullableString(p.Parent),
				p.RadiusM, p.MassKg, p.SoiM, p.AtmoM, p.OceanM, p.AngVel,
				p.Axis.X, p.Axis.Y, p.Axis.Z,
				nullableFloat(p.SmaM), nullableFloat(p.Ecc), nullableFloat(p.IncDeg),
				nullableFloat(p.LanDeg), nullableFloat(p.ArgpDeg), nullableFloat(p.TPe), nullableFloat(p.PeriodS),
				p.CcfToCceT0.X, p.CcfToCceT0.Y, p.CcfToCceT0.Z, p.CcfToCceT0.W, e.firstSeq)
		})
	if err != nil {
		return fmt.Errorf("stats: flush system_body: %w", err)
	}
	b.dirtySystemBodies = b.dirtySystemBodies[:0]
	return nil
}

func nullableString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
