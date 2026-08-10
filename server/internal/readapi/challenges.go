package readapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// ChallengesResponse is GET /v1/challenges.
type ChallengesResponse struct {
	Now        int64              `json:"now"`
	Challenges []ChallengeSummary `json:"challenges"`
}

// ChallengeSummary is the public definition plus current server-clock state
// and the raw scoped-row entrant denominator.
type ChallengeSummary struct {
	Challenge string `json:"challenge"`
	Title     string `json:"title"`
	Blurb     string `json:"blurb"`
	Unit      string `json:"unit"`
	Ascending bool   `json:"ascending"`
	Scope     string `json:"scope"`
	Opens     int64  `json:"opens"`
	Closes    int64  `json:"closes"`
	State     string `json:"state"`
	Entrants  int64  `json:"entrants"`
}

// ChallengeResponse is one visible ranked page. Closed challenges use this
// exact response too: their retained rows are the archive.
type ChallengeResponse struct {
	ChallengeSummary
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
	Rows   []ChallengeRow `json:"rows"`
}

// ChallengeRow is one visible challenge entrant. Save fields exist only for a
// career-scoped definition; System exists only for a system-scoped one.
type ChallengeRow struct {
	Rank    int             `json:"rank"`
	Handle  string          `json:"handle"`
	Save    int64           `json:"save,omitempty"`
	SaveID  string          `json:"save_id,omitempty"`
	System  *SystemRef      `json:"system,omitempty"`
	Value   float64         `json:"value"`
	Context json.RawMessage `json:"context,omitempty"`
	Updated int64           `json:"updated"`
	Rewound bool            `json:"rewound,omitempty"`
}

func (s *Server) handleChallenges(w http.ResponseWriter, r *http.Request) {
	out, err := s.ChallengeList(r.Context())
	if err != nil {
		s.fail(w, r, err, "read challenges")
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleChallenge(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := s.paging(w, r)
	if !ok {
		return
	}
	out, known, err := s.Challenge(r.Context(), r.PathValue("challenge"), limit, offset)
	switch {
	case err != nil:
		s.fail(w, r, err, "read challenge")
	case !known:
		s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "no such challenge")
	default:
		s.writeJSON(w, http.StatusOK, out)
	}
}

// ChallengeList assembles the challenge catalogue against one reading of the
// server clock. A CDN may retain this state for CacheControl's 30-second
// s-maxage across an opening/closing boundary; bounded presentation staleness
// never changes the receive-time gate used by projection.
func (s *Server) ChallengeList(ctx context.Context) (ChallengesResponse, error) {
	now := s.deps.Now().UnixMilli()
	defs := stats.Challenges()
	out := ChallengesResponse{Now: now, Challenges: make([]ChallengeSummary, 0, len(defs))}
	err := s.deps.Projections.With(func(p *store.Projections) error {
		for _, c := range defs {
			entrants, err := p.ChallengeEntrants(ctx, c.Key, "")
			if err != nil {
				return err
			}
			out.Challenges = append(out.Challenges, challengeSummary(c, now, entrants))
		}
		return nil
	})
	if err != nil {
		return ChallengesResponse{}, err
	}
	sortChallengeSummaries(out.Challenges)
	return out, nil
}

// Challenge assembles one visible challenge page. Hidden players are removed
// by visiblePage, so ranks close around them; Entrants deliberately remains the
// raw matching-row census, consistent with board and badge denominators.
func (s *Server) Challenge(
	ctx context.Context, key string, limit, offset int,
) (ChallengeResponse, bool, error) {
	c, known := stats.ChallengeByKey(key)
	if !known {
		return ChallengeResponse{}, false, nil
	}
	now := s.deps.Now().UnixMilli()
	limit, offset = ClampPaging(limit, offset)
	var entrants int64
	if err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		entrants, err = p.ChallengeEntrants(ctx, key, "")
		return err
	}); err != nil {
		return ChallengeResponse{}, true, err
	}
	source := func(page, scanned int) ([]store.ChallengeRow, error) {
		var rows []store.ChallengeRow
		err := s.deps.Projections.With(func(p *store.Projections) error {
			var err error
			rows, err = p.ChallengeLeaderboard(ctx, key, "", c.Ascending, page, scanned)
			return err
		})
		return rows, err
	}
	rows, handles, err := visiblePage(s, limit, offset, func(row store.ChallengeRow) int64 { return row.PlayerID }, source)
	if err != nil {
		return ChallengeResponse{}, true, err
	}

	seqs := make([]int64, 0, len(rows))
	careerKeys := make([]careerKey, 0, len(rows))
	systemHashes := make([]string, 0, len(rows))
	for _, row := range rows {
		seqs = append(seqs, row.UpdatedSeq)
		if c.Scope == stats.ScopeCareer {
			careerKeys = append(careerKeys, careerKey{player: row.PlayerID, career: row.Career})
		}
		if c.Scope == stats.ScopeSystem {
			systemHashes = append(systemHashes, row.System)
		}
	}
	times, err := s.deps.Events.RecvTimes(ctx, seqs)
	if err != nil {
		return ChallengeResponse{}, true, err
	}
	ordinals, err := s.challengeCareerOrdinals(ctx, careerKeys)
	if err != nil {
		return ChallengeResponse{}, true, err
	}
	rewound, err := s.rewoundCareers(ctx, careerKeys)
	if err != nil {
		return ChallengeResponse{}, true, err
	}
	systems, err := s.systemRefs(ctx, systemHashes)
	if err != nil {
		return ChallengeResponse{}, true, err
	}

	out := ChallengeResponse{
		ChallengeSummary: challengeSummary(c, now, entrants),
		Limit:            limit, Offset: offset, Rows: make([]ChallengeRow, 0, len(rows)),
	}
	for i, row := range rows {
		public := ChallengeRow{
			Rank: offset + i + 1, Handle: handles[i], Value: row.Value,
			Context: Redact(row.PlayerID, row.Context), Updated: times[row.UpdatedSeq],
		}
		switch c.Scope {
		case stats.ScopeCareer:
			ck := careerKey{player: row.PlayerID, career: row.Career}
			ordinal, ok := ordinals[ck]
			if !ok || row.Career == "" {
				return ChallengeResponse{}, true, fmt.Errorf("readapi: no save metadata for challenge row %d/%q", row.PlayerID, row.Career)
			}
			public.Save, public.SaveID, public.Rewound = ordinal, Label(row.PlayerID, kindCareer, row.Career), rewound[ck]
		case stats.ScopeSystem:
			ref, ok := systems[row.System]
			if !ok || row.System == "" {
				return ChallengeResponse{}, true, fmt.Errorf("readapi: no system metadata for challenge row %d/%q", row.PlayerID, row.System)
			}
			public.System = &ref
		}
		out.Rows = append(out.Rows, public)
	}
	return out, true, nil
}

func sortChallengeSummaries(summaries []ChallengeSummary) {
	slices.SortStableFunc(summaries, func(a, b ChallengeSummary) int {
		if d := challengeStateOrder(a.State) - challengeStateOrder(b.State); d != 0 {
			return d
		}
		if a.Opens != b.Opens {
			if a.Opens > b.Opens {
				return -1
			}
			return 1
		}
		if a.Closes != b.Closes {
			if a.Closes > b.Closes {
				return -1
			}
			return 1
		}
		return 0 // stable catalogue order for an identical window
	})
}

func challengeSummary(c stats.Challenge, now, entrants int64) ChallengeSummary {
	return ChallengeSummary{
		Challenge: c.Key, Title: c.Title, Blurb: c.Blurb, Unit: c.Unit,
		Ascending: c.Ascending, Scope: c.Scope, Opens: c.Opens, Closes: c.Closes,
		State: challengeState(c, now), Entrants: entrants,
	}
}

func challengeState(c stats.Challenge, now int64) string {
	switch {
	case now < c.Opens:
		return "upcoming"
	case c.Open(now):
		return "open"
	default:
		return "closed"
	}
}

func challengeStateOrder(state string) int {
	switch state {
	case "open":
		return 0
	case "upcoming":
		return 1
	default:
		return 2
	}
}

func (s *Server) challengeCareerOrdinals(ctx context.Context, keys []careerKey) (map[careerKey]int64, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	byPlayer := map[int64]bool{}
	for _, key := range keys {
		byPlayer[key.player] = true
	}
	out := make(map[careerKey]int64, len(keys))
	err := s.deps.Projections.With(func(p *store.Projections) error {
		for playerID := range byPlayer {
			careers, err := p.PlayerCareers(ctx, playerID)
			if err != nil {
				return err
			}
			for _, career := range careers {
				out[careerKey{player: playerID, career: career.Career}] = career.Ordinal
			}
		}
		return nil
	})
	return out, err
}
