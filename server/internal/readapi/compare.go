package readapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/meow-sci/catlog/server/internal/stats"
)

// N-handle comparison: `GET /v1/compare?handles=a,b,c`.
//
// # Why it is one request
//
// "Me against my friends" is the third of the three things the site is for, and
// doing it client-side means N profile requests whose answers can disagree — a
// projection commit between the first and the last shows one player's new
// record next to another's stale rank. One request reads them all against one
// view of the projections, so the table a reader sees is internally consistent.
//
// # It is the profile endpoint, pivoted
//
// Each handle is assembled by exactly the code behind `GET /v1/players/{handle}`
// and then pivoted board-first. That is deliberate: the rank arithmetic that
// discounts banned players (readapi.go) and the redaction of install-derived
// keys (privacy.go) are subtle enough that a second implementation of either
// would eventually drift, and the drift would be a wrong rank or a leak. The
// price is that a comparison costs N profiles, which is why [MaxCompareHandles]
// is small.
//
// # The three decisions this endpoint had to make
//
//   - **A handle that is not there** is reported as `found: false` rather than
//     failing the request. Unknown, retired and banned are one answer, exactly
//     as `GET /v1/players/{handle}` 404s all three (§4.8): this says no more
//     than asking for that one profile already says, and 404ing the whole
//     comparison because one of five friends deleted their account would be
//     worse for everyone and no better for them.
//   - **A board only some of them are on** lists only the rows that exist. An
//     absent player is absent, not zero — the same rule the folds follow for a
//     missing `peak_g` — and a UI renders the gap as a gap. The board itself is
//     included as long as one of the compared handles is on it.
//   - **The `min_players` listing rule does not apply here**, for the same
//     reason it does not apply to a profile: a board somebody is actually on is
//     shown to them whether or not the public index is ready to list it. The
//     index is where that threshold lives.
const (
	// MaxCompareHandles caps N. Eight is past the point where a side-by-side
	// table is readable, and it is what bounds the cost: a comparison is N
	// profiles, and a profile is one rank query per board its player is on.
	// Over the cap the extras are dropped rather than rejected — the effective
	// list is echoed back in Handles, so a client can see what it got, and the
	// answer stays cacheable.
	MaxCompareHandles = 8
)

// CompareResponse is `GET /v1/compare`.
type CompareResponse struct {
	// Handles are the requested handles in request order, deduplicated, capped
	// at [MaxCompareHandles] — the column order a table should use.
	Handles []ComparePlayer `json:"handles"`
	// Boards are every board at least one of them appears on, in the same
	// display order as `GET /v1/leaderboards`.
	Boards []CompareBoard `json:"boards"`
}

// ComparePlayer is one column header.
type ComparePlayer struct {
	// Handle is the display casing when the player exists, and otherwise the
	// string as it was asked for.
	Handle string `json:"handle"`
	// Found is false for an unknown, retired or banned handle — one answer for
	// all three, on purpose.
	Found bool `json:"found"`
	// Since is when the handle was claimed, unix ms. Absent when Found is false.
	Since int64 `json:"since,omitempty"`
}

// CompareBoard is one row of the comparison: a board, and whichever of the
// compared players hold a value on it.
type CompareBoard struct {
	Stat  string `json:"stat"`
	Title string `json:"title"`
	Unit  string `json:"unit"`
	// Ascending reports that the smallest value ranks first (§4.8).
	Ascending bool `json:"ascending"`
	// Players is how many players hold a value on the board — the same
	// banned-inclusive row count as [PlayerRow.Players].
	Players int64 `json:"players"`
	// Rows are the compared players who are on this board, in the same order as
	// Handles. A handle missing from here is not on the board.
	Rows []CompareRow `json:"rows"`
}

// CompareRow is one player's placement on one compared board.
type CompareRow struct {
	Handle string  `json:"handle"`
	Value  float64 `json:"value"`
	// System is the friendly content identity of the save that set this value,
	// when the winning row carries enough provenance to resolve one.
	System *SystemRef `json:"system,omitempty"`
	// Rank is the position among visible players on the whole board, not among
	// the compared handles: "3rd in the world", not "2nd of your friends".
	Rank    int             `json:"rank"`
	Context json.RawMessage `json:"context,omitempty"`
	Updated int64           `json:"updated"`
	// Rewound qualifies a career-time value; see [BoardRow.Rewound].
	Rewound bool `json:"rewound,omitempty"`
}

func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	out, err := s.Compare(r.Context(), SplitHandles(strings.Join(r.URL.Query()["handles"], ",")))
	if err != nil {
		s.fail(w, r, err, "compare players")
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// SplitHandles parses `?handles=a,b,c` into a deduplicated, capped list in
// request order.
//
// Repeating `?handles=` is accepted too — `a,b&handles=c` is the same request —
// because a client building a URL from an array will do it either way and a
// comparison is not the place to be strict about which.
//
// Exported because the server-rendered site renders `/compare?handles=` from the
// same query string and must cap, deduplicate and order it identically: two
// surfaces disagreeing about which eight of nine handles were kept is a bug
// nobody would find twice.
func SplitHandles(raw string) []string {
	var (
		out  []string
		seen = map[string]bool{}
	)
	for _, h := range strings.Split(raw, ",") {
		h = strings.TrimSpace(h)
		if h == "" || seen[strings.ToLower(h)] {
			continue
		}
		seen[strings.ToLower(h)] = true
		out = append(out, h)
		if len(out) == MaxCompareHandles {
			break
		}
	}
	return out
}

// Compare assembles `GET /v1/compare`.
//
// An empty list is a valid, empty comparison rather than an error: a UI that
// has not been given anybody to compare yet asks the same URL.
func (s *Server) Compare(ctx context.Context, handles []string) (CompareResponse, error) {
	if len(handles) > MaxCompareHandles {
		handles = handles[:MaxCompareHandles]
	}
	out := CompareResponse{
		Handles: make([]ComparePlayer, 0, len(handles)),
		Boards:  []CompareBoard{},
	}
	if len(handles) == 0 {
		return out, nil
	}

	counts, err := s.statCounts(ctx)
	if err != nil {
		return CompareResponse{}, err
	}

	// Profiles are assembled first so their career bindings and system headers
	// can be resolved once for the whole response rather than once per handle.
	profiles := make([]PlayerResponse, 0, len(handles))
	for _, handle := range handles {
		profile, known, err := s.player(ctx, handle, counts)
		if err != nil {
			return CompareResponse{}, err
		}
		if !known {
			out.Handles = append(out.Handles, ComparePlayer{Handle: handle})
			continue
		}
		out.Handles = append(out.Handles, ComparePlayer{Handle: profile.Handle, Found: true, Since: profile.Since})
		profiles = append(profiles, profile)
	}
	profilePtrs := make([]*PlayerResponse, len(profiles))
	for i := range profiles {
		profilePtrs[i] = &profiles[i]
	}
	if err := s.attachPlayerSystems(ctx, profilePtrs); err != nil {
		return CompareResponse{}, err
	}

	// rowsByStat holds each board's rows in the order the handles were asked
	// for, which is the order a table's columns go in.
	rowsByStat := map[string][]CompareRow{}
	for _, profile := range profiles {
		for _, row := range profile.Stats {
			rowsByStat[row.Stat] = append(rowsByStat[row.Stat], CompareRow{
				Handle: profile.Handle, Value: row.Value, Rank: row.Rank,
				System: row.System, Context: row.Context, Updated: row.Updated, Rewound: row.Rewound,
			})
		}
	}

	// Display order comes from the same catalog the board index uses, with the
	// listing threshold lowered to 1: a board one of these players is on is part
	// of the comparison whether or not it is big enough to be indexed. Anything
	// the catalog does not know is a board this build no longer publishes, and
	// is dropped exactly as a profile drops it.
	for _, board := range stats.Catalog(counts, 1) {
		rows, ok := rowsByStat[board.Stat]
		if !ok {
			continue
		}
		out.Boards = append(out.Boards, CompareBoard{
			Stat: board.Stat, Title: board.Title, Unit: board.Unit,
			Ascending: board.Ascending, Players: counts[board.Stat],
			Rows: rows,
		})
	}
	return out, nil
}
