package readapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/meow-sci/catlog/server/internal/authz"
	"github.com/meow-sci/catlog/server/internal/stats"
	"github.com/meow-sci/catlog/server/internal/store"
)

// SavesResponse is GET /v1/players/{handle}/saves.
type SavesResponse struct {
	Handle string        `json:"handle"`
	Saves  []SaveSummary `json:"saves"`
}

// SaveSummary is one save in a player's own first-seen order.
type SaveSummary struct {
	Save          int64      `json:"save"`
	SaveID        string     `json:"save_id"`
	System        *SystemRef `json:"system,omitempty"`
	SystemChanged bool       `json:"system_changed,omitempty"`
	PlaytimeMS    float64    `json:"playtime_ms"`
	First         int64      `json:"first"`
	Last          int64      `json:"last"`
	Rewound       bool       `json:"rewound,omitempty"`
	Boards        int        `json:"boards"`
}

// SaveResponse is GET /v1/players/{handle}/saves/{ordinal}.
type SaveResponse struct {
	Handle        string     `json:"handle"`
	Save          int64      `json:"save"`
	SaveID        string     `json:"save_id"`
	System        *SystemRef `json:"system,omitempty"`
	SystemChanged bool       `json:"system_changed,omitempty"`
	PlaytimeMS    float64    `json:"playtime_ms"`
	Rewound       bool       `json:"rewound,omitempty"`
	Stats         []SaveStat `json:"stats"`
}

// SaveStat is one career-scoped board placement for one exact save.
type SaveStat struct {
	Stat      string          `json:"stat"`
	Title     string          `json:"title"`
	Unit      string          `json:"unit"`
	Value     float64         `json:"value"`
	Ascending bool            `json:"ascending"`
	Rank      int             `json:"rank"`
	Entrants  int64           `json:"entrants"`
	Context   json.RawMessage `json:"context,omitempty"`
	Updated   int64           `json:"updated"`
}

func (s *Server) handleSaves(w http.ResponseWriter, r *http.Request) {
	out, known, err := s.Saves(r.Context(), r.PathValue("handle"))
	switch {
	case err != nil:
		s.fail(w, r, err, "read player saves")
	case !known:
		s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "no such player")
	default:
		s.writeJSON(w, http.StatusOK, out)
	}
}

func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	entry, known := s.deps.Directory.Lookup(r.PathValue("handle"))
	if !known {
		s.writeError(w, http.StatusNotFound, authz.CodeNotFound, "no such player")
		return
	}
	ordinal, err := strconv.ParseInt(r.PathValue("ordinal"), 10, 64)
	if err != nil || ordinal < 1 {
		s.writeError(w, http.StatusNotFound, authz.CodeNotFound,
			"catlog has no such save for this player")
		return
	}
	out, found, err := s.save(r.Context(), entry.PlayerID, entry.Handle, ordinal)
	switch {
	case err != nil:
		s.fail(w, r, err, "read player save")
	case !found:
		s.writeError(w, http.StatusNotFound, authz.CodeNotFound,
			"catlog has no such save for this player")
	default:
		s.writeJSON(w, http.StatusOK, out)
	}
}

// Saves assembles a player's saves in ordinal order. Unknown, retired and
// hidden handles are the same answer because the directory omits all three.
func (s *Server) Saves(ctx context.Context, handle string) (SavesResponse, bool, error) {
	entry, ok := s.deps.Directory.Lookup(handle)
	if !ok {
		return SavesResponse{}, false, nil
	}
	var careers []store.CareerRow
	boardCounts := map[string]int{}
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		careers, err = p.PlayerCareers(ctx, entry.PlayerID)
		if err != nil {
			return err
		}
		for _, career := range careers {
			rows, err := p.CareerStatsForPlayer(ctx, entry.PlayerID, career.Career)
			if err != nil {
				return err
			}
			boardCounts[career.Career] = len(rows)
		}
		return nil
	})
	if err != nil {
		return SavesResponse{}, true, err
	}

	seqs := distinctCareerSeqs(careers)
	times, err := s.deps.Events.RecvTimes(ctx, seqs)
	if err != nil {
		return SavesResponse{}, true, err
	}
	hashes := make([]string, 0, len(careers))
	for _, career := range careers {
		if career.System != "" {
			hashes = append(hashes, career.System)
		}
	}
	refs, err := s.systemRefs(ctx, hashes)
	if err != nil {
		return SavesResponse{}, true, err
	}

	out := SavesResponse{Handle: entry.Handle, Saves: make([]SaveSummary, 0, len(careers))}
	for _, career := range careers {
		ref, err := saveSystemRef(career.System, refs)
		if err != nil {
			return SavesResponse{}, true, err
		}
		out.Saves = append(out.Saves, SaveSummary{
			Save: career.Ordinal, SaveID: Label(entry.PlayerID, kindCareer, career.Career),
			System: ref, SystemChanged: career.SystemChanged,
			PlaytimeMS: career.MaxSimT * 1000, First: times[career.FirstSeq], Last: times[career.LastSeq],
			Rewound: career.Rewound, Boards: boardCounts[career.Career],
		})
	}
	return out, true, nil
}

func distinctCareerSeqs(careers []store.CareerRow) []int64 {
	seen := make(map[int64]bool, len(careers)*2)
	out := make([]int64, 0, len(careers)*2)
	for _, career := range careers {
		for _, seq := range []int64{career.FirstSeq, career.LastSeq} {
			if !seen[seq] {
				seen[seq] = true
				out = append(out, seq)
			}
		}
	}
	return out
}

func (s *Server) save(ctx context.Context, playerID int64, handle string, ordinal int64) (SaveResponse, bool, error) {
	var career store.CareerRow
	var rows []store.CareerStatRow
	var found bool
	err := s.deps.Projections.With(func(p *store.Projections) error {
		var err error
		career, found, err = p.CareerByOrdinal(ctx, playerID, ordinal)
		if err != nil || !found {
			return err
		}
		rows, err = p.CareerStatsForPlayer(ctx, playerID, career.Career)
		return err
	})
	if err != nil || !found {
		return SaveResponse{}, found, err
	}

	refs, err := s.systemRefs(ctx, []string{career.System})
	if err != nil {
		return SaveResponse{}, true, err
	}
	ref, err := saveSystemRef(career.System, refs)
	if err != nil {
		return SaveResponse{}, true, err
	}

	knownRows := make([]saveStatRow, 0, len(rows))
	for _, row := range rows {
		board, known := stats.Describe(row.Stat)
		if known {
			knownRows = append(knownRows, saveStatRow{row: row, board: board})
		}
	}
	ranks, err := s.saveRanks(ctx, knownRows)
	if err != nil {
		return SaveResponse{}, true, err
	}
	seqs := make([]int64, 0, len(knownRows))
	for _, row := range knownRows {
		seqs = append(seqs, row.row.UpdatedSeq)
	}
	times, err := s.deps.Events.RecvTimes(ctx, seqs)
	if err != nil {
		return SaveResponse{}, true, err
	}

	out := SaveResponse{
		Handle: handle, Save: career.Ordinal, SaveID: Label(playerID, kindCareer, career.Career),
		System: ref, SystemChanged: career.SystemChanged, PlaytimeMS: career.MaxSimT * 1000,
		Rewound: career.Rewound, Stats: make([]SaveStat, 0, len(knownRows)),
	}
	for _, row := range knownRows {
		rank := ranks[row.row.Stat]
		out.Stats = append(out.Stats, SaveStat{
			Stat: row.row.Stat, Title: row.board.Title, Unit: row.board.Unit,
			Value: row.row.Value, Ascending: row.board.Ascending,
			Rank: rank.rank, Entrants: rank.entrants,
			Context: Redact(playerID, row.row.Context), Updated: times[row.row.UpdatedSeq],
		})
	}
	return out, true, nil
}

type saveStatRow struct {
	row   store.CareerStatRow
	board stats.Board
}

type saveRank struct {
	rank     int
	entrants int64
}

func (s *Server) saveRanks(ctx context.Context, rows []saveStatRow) (map[string]saveRank, error) {
	banned := s.deps.Directory.BannedIDs()
	out := make(map[string]saveRank, len(rows))
	err := s.deps.Projections.With(func(p *store.Projections) error {
		for _, candidate := range rows {
			row, board := candidate.row, candidate.board
			ahead, err := p.CareerStatAhead(ctx, row.Stat, "", row.Value, row.UpdatedSeq, board.Ascending)
			if err != nil {
				return err
			}
			entrants, err := p.CareerStatEntrants(ctx, row.Stat, "")
			if err != nil {
				return err
			}
			var hiddenAhead int64
			if len(banned) > 0 {
				hidden, err := p.CareerStatsForPlayers(ctx, row.Stat, "", banned)
				if err != nil {
					return err
				}
				for _, hiddenRow := range hidden {
					better := hiddenRow.Value > row.Value
					if board.Ascending {
						better = hiddenRow.Value < row.Value
					}
					if better || (hiddenRow.Value == row.Value && hiddenRow.UpdatedSeq < row.UpdatedSeq) {
						hiddenAhead++
					}
				}
			}
			out[row.Stat] = saveRank{rank: int(max(ahead-hiddenAhead, 0)) + 1, entrants: entrants}
		}
		return nil
	})
	return out, err
}

func saveSystemRef(hash string, refs map[string]SystemRef) (*SystemRef, error) {
	if hash == "" {
		return nil, nil
	}
	ref, ok := refs[hash]
	if !ok {
		return nil, fmt.Errorf("readapi: no metadata for system %q", hash)
	}
	return &ref, nil
}
