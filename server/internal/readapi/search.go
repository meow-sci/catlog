package readapi

import (
	"net/http"
	"strings"

	"github.com/meow-sci/catlog/server/internal/authz"
)

// Handle search: `GET /v1/players?q=`.
//
// # What it returns and why that is all
//
// Handles. Nothing else. A search result is a list of links to profiles, and a
// profile is one request away — attaching stats to each hit would multiply the
// cost of the cheapest endpoint in the API by the number of results, to render
// something no search box shows.
//
// # Cost, because this one is free to call
//
// It is unauthenticated and CDN-fronted, so its cost has to be bounded by
// construction rather than by politeness:
//
//   - it never touches either database. [directory.Directory.Search] scans the
//     in-memory handle map, which is the same map every board page already
//     resolves player ids through, and which already excludes banned players.
//   - [MinQueryLen] keeps the single-character queries — the ones that match
//     everything, sort the whole directory and cache the worst — off the origin
//     entirely.
//   - [MaxQueryLen] bounds the cache key. A query longer than a handle can be
//     cannot match anything, so nothing is lost by refusing it, and the refusal
//     stops an attacker minting unbounded distinct URLs that all miss the CDN.
//   - the result is a pure function of (q, limit), so the CDN can actually hold
//     it for the §4.8 30 seconds.
//
// A bad `q` is a 400 rather than an empty 200, and — like every other response
// here — it carries the §4.8 cache header, so a client hammering a malformed
// query is answered by the CDN too.
const (
	// MinQueryLen is the shortest query that reaches the origin. Two characters
	// is enough to be a real search and short enough for a two-letter handle.
	MinQueryLen = 2
	// MaxQueryLen is the longest query accepted. §4.7 caps a handle at 150
	// characters; nothing longer can match, so nothing longer is scanned.
	MaxQueryLen = 150
	// DefaultSearchLimit is what a search box shows without asking.
	DefaultSearchLimit = 20
	// MaxSearchLimit is the ceiling, clamped rather than rejected for the same
	// cache reason as [MaxLimit].
	MaxSearchLimit = 50
)

// SearchResponse is `GET /v1/players?q=`.
type SearchResponse struct {
	// Query echoes the effective query — trimmed, but in the caller's casing.
	Query string `json:"query"`
	// Limit echoes the effective cap after clamping.
	Limit int `json:"limit"`
	// Handles are the matches: prefix matches first, then substring matches,
	// each group in lexicographic order of the lowercase handle. Display casing
	// is preserved (§4.7).
	Handles []string `json:"handles"`
	// Truncated reports that more handles matched than Limit allowed. The way
	// to see them is a narrower query, not a deeper page: there is no offset
	// here, because a paged search over a live directory is a promise this
	// cannot keep and nobody asked for.
	Truncated bool `json:"truncated,omitempty"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	switch {
	case len(q) < MinQueryLen:
		s.writeError(w, http.StatusBadRequest, authz.CodeBadRequest,
			"q must be at least 2 characters")
		return
	case len(q) > MaxQueryLen:
		s.writeError(w, http.StatusBadRequest, authz.CodeBadRequest,
			"q is longer than any handle can be")
		return
	}
	limit, ok := s.intParam(w, r, "limit", DefaultSearchLimit)
	if !ok {
		return
	}
	s.writeJSON(w, http.StatusOK, s.Search(q, limit))
}

// Search assembles `GET /v1/players?q=`. It does no I/O and takes no context:
// the directory is in memory.
func (s *Server) Search(q string, limit int) SearchResponse {
	limit = min(max(limit, 1), MaxSearchLimit)
	handles, truncated := s.deps.Directory.Search(q, limit)
	if handles == nil {
		handles = []string{}
	}
	return SearchResponse{Query: strings.TrimSpace(q), Limit: limit, Handles: handles, Truncated: truncated}
}
