package readapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// BadgesResponse is GET /v1/badges.
type BadgesResponse struct {
	MinPlayers int            `json:"min_players"`
	Badges     []BadgeSummary `json:"badges"`
}

// BadgeSummary is public badge metadata plus its lifetime-holder census.
type BadgeSummary struct {
	Badge   string `json:"badge"`
	Title   string `json:"title"`
	Blurb   string `json:"blurb"`
	Group   string `json:"group"`
	Tier    int    `json:"tier,omitempty"`
	Holders int64  `json:"holders"`
}

// BadgeResponse is one page of lifetime holders, or one representative save
// per player when System is filtered.
type BadgeResponse struct {
	Badge   string           `json:"badge"`
	Title   string           `json:"title"`
	Blurb   string           `json:"blurb"`
	Group   string           `json:"group"`
	Tier    int              `json:"tier,omitempty"`
	Holders int64            `json:"holders"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
	Rows    []BadgeHolderRow `json:"rows"`
}

// BadgeHolderRow is one visible player in first-earned order.
type BadgeHolderRow struct {
	Rank    int             `json:"rank"`
	Handle  string          `json:"handle"`
	Save    int64           `json:"save,omitempty"`
	SaveID  string          `json:"save_id,omitempty"`
	System  *SystemRef      `json:"system,omitempty"`
	Earned  int64           `json:"earned"`
	SimT    *float64        `json:"sim_t,omitempty"`
	Context json.RawMessage `json:"context,omitempty"`
}

// PlayerBadgesResponse is the lifetime or exact-save badge checklist.
type PlayerBadgesResponse struct {
	Handle   string         `json:"handle"`
	Earned   []BadgeAward   `json:"earned"`
	Unearned []BadgeSummary `json:"unearned"`
}

// BadgeAward is one earned checklist row. It contains only public save
// identity: raw career keys remain inputs to response assembly.
type BadgeAward struct {
	Badge   string          `json:"badge"`
	Title   string          `json:"title"`
	Blurb   string          `json:"blurb"`
	Group   string          `json:"group"`
	Tier    int             `json:"tier,omitempty"`
	Save    int64           `json:"save,omitempty"`
	SaveID  string          `json:"save_id,omitempty"`
	System  *SystemRef      `json:"system,omitempty"`
	Earned  int64           `json:"earned"`
	SimT    *float64        `json:"sim_t,omitempty"`
	Context json.RawMessage `json:"context,omitempty"`
}

func summary(b stats.Badge, holders int64) BadgeSummary {
	return BadgeSummary{Badge: b.Key, Title: b.Title, Blurb: b.Blurb, Group: b.Group, Tier: b.Tier, Holders: holders}
}

func (s *Server) handleBadges(w http.ResponseWriter, r *http.Request) {
	out, err := s.BadgeList(r.Context())
	if err != nil {
		s.fail(w, r, err, "read badge counts")
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleBadge(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := s.paging(w, r)
	if !ok {
		return
	}
	var system *SystemRef
	if key := r.URL.Query().Get("system"); key != "" {
		ref, found, err := s.ResolveSystem(r.Context(), key)
		if err != nil {
			s.fail(w, r, err, "resolve celestial system")
			return
		}
		if !found {
			s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "catlog has never seen a system by that name")
			return
		}
		system = &ref
	}
	out, known, err := s.Badge(r.Context(), r.PathValue("badge"), system, limit, offset)
	switch {
	case err != nil:
		s.fail(w, r, err, "read badge holders")
	case !known:
		s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "no such badge")
	default:
		s.writeJSON(w, http.StatusOK, out)
	}
}

func (s *Server) handlePlayerBadges(w http.ResponseWriter, r *http.Request) {
	out, known, err := s.PlayerBadges(r.Context(), r.PathValue("handle"))
	switch {
	case err != nil:
		s.fail(w, r, err, "read player badges")
	case !known:
		s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "no such player")
	default:
		s.writeJSON(w, http.StatusOK, out)
	}
}

func (s *Server) handleSaveBadges(w http.ResponseWriter, r *http.Request) {
	entry, known := s.deps.Directory.Lookup(r.PathValue("handle"))
	if !known {
		s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "no such player")
		return
	}
	ordinal, err := strconv.ParseInt(r.PathValue("ordinal"), 10, 64)
	if err != nil || ordinal < 1 {
		s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "catlog has no such save for this player")
		return
	}
	out, found, err := s.playerBadges(r.Context(), entry.PlayerID, entry.Handle, ordinal)
	switch {
	case err != nil:
		s.fail(w, r, err, "read save badges")
	case !found:
		s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "catlog has no such save for this player")
	default:
		s.writeJSON(w, http.StatusOK, out)
	}
}

// BadgeList assembles the stable fixed catalogue and family members meeting
// the same public min-player gate as dynamic leaderboards.
func (s *Server) BadgeList(ctx context.Context) (BadgesResponse, error) {
	var counts map[string]int64
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		counts, err = p.BadgeCounts(ctx)
		return err
	})
	if err != nil {
		return BadgesResponse{}, err
	}
	all := stats.BadgeCatalog(counts, s.minBoardPlayers)
	out := BadgesResponse{MinPlayers: s.minBoardPlayers, Badges: make([]BadgeSummary, 0, len(all))}
	for _, badge := range all {
		out.Badges = append(out.Badges, summary(badge, counts[badge.Key]))
	}
	return out, nil
}

// Badge assembles one visible holder page. The holder denominator intentionally
// remains the raw distinct-player census, like board counts; hidden rows close
// ranks but are not an externally queryable ban census.
func (s *Server) Badge(ctx context.Context, key string, system *SystemRef, limit, offset int) (BadgeResponse, bool, error) {
	limit, offset = ClampPaging(limit, offset)
	systemHash := ""
	if system != nil {
		systemHash = system.Hash
	}
	var global, holders int64
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		global, err = p.BadgeHolderCount(ctx, key, "")
		if err == nil {
			holders, err = p.BadgeHolderCount(ctx, key, systemHash)
		}
		return err
	})
	if err != nil {
		return BadgeResponse{}, false, err
	}
	meta, known := stats.KnownBadge(key, global)
	if !known {
		return BadgeResponse{}, false, nil
	}

	source := func(page, scanned int) ([]store.BadgeRow, error) {
		var rows []store.BadgeRow
		err := s.deps.Projections.With(func(p *store.Projections) error {
			var err error
			rows, err = p.BadgeHolders(ctx, key, systemHash, page, scanned)
			return err
		})
		return rows, err
	}
	rows, handles, err := visiblePage(s, limit, offset, func(row store.BadgeRow) int64 { return row.PlayerID }, source)
	if err != nil {
		return BadgeResponse{}, true, err
	}
	careers, refs, err := s.badgeRowRefs(ctx, rows, nil)
	if err != nil {
		return BadgeResponse{}, true, err
	}
	out := BadgeResponse{
		Badge: meta.Key, Title: meta.Title, Blurb: meta.Blurb, Group: meta.Group, Tier: meta.Tier,
		Holders: holders, Limit: limit, Offset: offset,
		Rows: make([]BadgeHolderRow, 0, len(rows)),
	}
	for i, row := range rows {
		career := row.FirstCareer
		if systemHash != "" {
			career = row.Career
		}
		ordinal := careers[careerKey{player: row.PlayerID, career: career}]
		out.Rows = append(out.Rows, BadgeHolderRow{
			Rank: offset + i + 1, Handle: handles[i], Save: ordinal,
			SaveID: Label(row.PlayerID, kindCareer, career), System: badgeSystemRef(refs, row.System),
			Earned: row.EarnedAt, SimT: nullableFloat(row.EarnedSimT), Context: Redact(row.PlayerID, row.Context),
		})
	}
	return out, true, nil
}

// PlayerBadges returns lifetime awards and only the bounded fixed-badge
// checklist. Dynamic family members have no single system denominator here.
func (s *Server) PlayerBadges(ctx context.Context, handle string) (PlayerBadgesResponse, bool, error) {
	entry, ok := s.deps.Directory.Lookup(handle)
	if !ok {
		return PlayerBadgesResponse{}, false, nil
	}
	return s.playerBadges(ctx, entry.PlayerID, entry.Handle, 0)
}

func (s *Server) playerBadges(ctx context.Context, playerID int64, handle string, ordinal int64) (PlayerBadgesResponse, bool, error) {
	var (
		career store.CareerRow
		rows   []store.BadgeRow
		bodies []store.SystemBodyRow
		counts map[string]int64
		found  = true
	)
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		careerKey := ""
		if ordinal > 0 {
			career, found, err = p.CareerByOrdinal(ctx, playerID, ordinal)
			if err != nil || !found {
				return err
			}
			careerKey = career.Career
		}
		rows, err = p.BadgesForPlayer(ctx, playerID, careerKey)
		if err != nil {
			return err
		}
		counts, err = p.BadgeCounts(ctx)
		if err != nil {
			return err
		}
		if ordinal > 0 && career.System != "" {
			var header store.SystemRow
			header, found, err = p.SystemBySlugOrHash(ctx, career.System)
			if err != nil {
				return err
			}
			// A save with an orphaned binding is still a known save. It simply has
			// no system ref and no dynamic checklist.
			if !found {
				found = true
				return nil
			}
			career.System = header.Hash
			if header.Complete {
				bodies, err = p.SystemBodies(ctx, header.Hash)
			}
		}
		return err
	})
	if err != nil || !found {
		return PlayerBadgesResponse{}, found, err
	}

	ordered := orderedBadgeRows(rows)
	careers, refs, err := s.badgeRowRefs(ctx, ordered, nil)
	if err != nil {
		return PlayerBadgesResponse{}, true, err
	}
	out := PlayerBadgesResponse{Handle: handle, Earned: make([]BadgeAward, 0, len(ordered))}
	earned := make(map[string]bool, len(ordered))
	for _, row := range ordered {
		meta, ok := stats.DescribeBadge(row.Badge)
		if !ok {
			continue
		}
		rawCareer := row.FirstCareer
		if ordinal > 0 {
			rawCareer = row.Career
		}
		earned[row.Badge] = true
		out.Earned = append(out.Earned, BadgeAward{
			Badge: meta.Key, Title: meta.Title, Blurb: meta.Blurb, Group: meta.Group, Tier: meta.Tier,
			Save:   careers[careerKey{player: playerID, career: rawCareer}],
			SaveID: Label(playerID, kindCareer, rawCareer), System: badgeSystemRef(refs, row.System),
			Earned: row.EarnedAt, SimT: nullableFloat(row.EarnedSimT), Context: Redact(playerID, row.Context),
		})
	}

	candidates := stats.FixedBadges()
	if ordinal > 0 && len(bodies) > 0 {
		counts := make(map[string]int64, len(bodies)*3)
		for _, body := range bodies {
			for _, key := range bodyBadgeKeys(body.Body) {
				counts[key] = 1
			}
		}
		candidates = stats.BadgeCatalog(counts, 1)
	}
	out.Unearned = make([]BadgeSummary, 0, len(candidates))
	for _, meta := range candidates {
		if !earned[meta.Key] {
			out.Unearned = append(out.Unearned, summary(meta, counts[meta.Key]))
		}
	}
	return out, true, nil
}

func bodyBadgeKeys(body string) []string {
	keys := make([]string, 0, 3)
	for _, makeKey := range []func(string) (string, bool){stats.ReachedBadge, stats.OrbitedBadge, stats.LandedOnBadge} {
		if key, ok := makeKey(body); ok {
			keys = append(keys, key)
		}
	}
	return keys
}

func orderedBadgeRows(rows []store.BadgeRow) []store.BadgeRow {
	byKey := make(map[string]store.BadgeRow, len(rows))
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		byKey[row.Badge], counts[row.Badge] = row, 1
	}
	out := make([]store.BadgeRow, 0, len(rows))
	for _, meta := range stats.BadgeCatalog(counts, 1) {
		if row, ok := byKey[meta.Key]; ok {
			out = append(out, row)
		}
	}
	return out
}

// badgeRowRefs resolves public save ordinals and system identities without
// allowing either raw career column into a response struct.
func (s *Server) badgeRowRefs(ctx context.Context, rows []store.BadgeRow, extraSystems []string) (map[careerKey]int64, map[string]SystemRef, error) {
	byPlayer := map[int64]bool{}
	hashes := append([]string(nil), extraSystems...)
	for _, row := range rows {
		byPlayer[row.PlayerID] = true
		if row.System != "" {
			hashes = append(hashes, row.System)
		}
	}
	ordinals := map[careerKey]int64{}
	err := s.deps.Projections.With(func(p *store.Projections) error {
		for playerID := range byPlayer {
			careers, err := p.PlayerCareers(ctx, playerID)
			if err != nil {
				return err
			}
			for _, career := range careers {
				ordinals[careerKey{player: playerID, career: career.Career}] = career.Ordinal
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	refs, err := s.systemRefs(ctx, hashes)
	return ordinals, refs, err
}

func badgeSystemRef(refs map[string]SystemRef, hash string) *SystemRef {
	ref, ok := refs[hash]
	if !ok {
		return nil
	}
	return &ref
}

func nullableFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}
