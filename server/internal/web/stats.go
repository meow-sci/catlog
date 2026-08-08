package web

import (
	"net/http"

	"github.com/meow-sci/catlog/server/internal/readapi"
)

// `/stats` — the stats of the stats.
//
// Every other page here is about a player. This one is about the collection:
// how many events catlog is holding, of what kinds, arriving how fast, since
// when, and how much has been derived from them. Data for nerds about the data
// collected by nerds, and deliberately not a leaderboard — no records, no
// ranking, nobody's handle.
//
// It renders `GET /v1/stats` and nothing else, through the same [Read] seam
// every other page goes through, so the page and the endpoint cannot drift and
// the read API's memoisation covers both.

// statsData is the response plus the two denominators a template cannot work
// out for itself.
//
// html/template has no arithmetic, and both the daily chart and the per-type
// bars are "this count as a fraction of the biggest one". Computing that here
// is one pass over data already in memory; the alternative is a template
// function that takes a slice and gets called once per row.
type statsData struct {
	readapi.StatsResponse
	// DailyMax is the tallest column in the daily series, and TypeMax the
	// longest bar in the all-time type table. Zero when there is nothing to
	// scale, which the bar helper reads as "no bar".
	DailyMax int64
	TypeMax  int64
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Read.Stats(r.Context())
	if err != nil {
		s.serverError(w, r, err, "read the collection stats")
		return
	}
	data := statsData{StatsResponse: out}
	for _, d := range out.Events.Daily {
		data.DailyMax = max(data.DailyMax, d.Count)
	}
	for _, t := range out.Events.Types {
		data.TypeMax = max(data.TypeMax, t.Count)
	}

	s.render(w, r, http.StatusOK, "stats", publicCache, page{
		Title: "Stats of stats — catlog",
		Nav:   "stats",
		Data:  data,
	})
}

// barWidth is one bar's length as a whole percentage of the longest one.
//
// Floored at 1 for any non-zero count, because a bar chart whose smallest bar
// is invisible is a bar chart that lies about which days had events: on a
// ninety-day series with one busy afternoon in it, every other day would round
// away to nothing.
func barWidth(n, peak int64) int {
	switch {
	case n <= 0 || peak <= 0:
		return 0
	case n >= peak:
		return 100
	}
	// Integer division floors, so this cannot reach 100 on its own — the case
	// above is the only way to a full bar.
	return int(max(n*100/peak, 1))
}

// percent turns a 0–1 share into the number a reader sees beside a bar.
//
// It is a plain float rather than a formatted string because it goes through
// `numUnit … "%"` like every other number on the site — three significant
// figures, and grouped in the reader's locale by intl.js.
func percent(share float64) float64 { return share * 100 }
