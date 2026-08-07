// Package readapi serves the public, CDN-cacheable /v1/* JSON endpoints and
// their cache headers (§4.8).
//
//	GET /v1/leaderboards            board metadata + row counts
//	GET /v1/leaderboards/{stat}     one board, ?limit=50&offset=0, limit ≤ 200
//	GET /v1/players/{handle}        one profile, 404 for unknown or banned
//
// # Everything here is cacheable
//
// Every response carries `Cache-Control: public, s-maxage=30,
// stale-while-revalidate=300` exactly as §4.8 specifies — including the 404s,
// which are as much a stable public fact as a board is, and which are the
// responses a scraper enumerating handles would otherwise hammer uncached. The
// SSE feed (WP5) is the documented exception and is not served from here.
//
// # Banned players are filtered on the fast path
//
// §4.7 leaves purged data to heal on the next rebuild and requires the read path
// to filter banned players itself. It does so through the in-memory handle
// directory: a banned player has no directory entry, so they resolve to no
// handle, cannot be looked up by handle, and are dropped from every board page.
// Because the drop happens in Go rather than in SQL, a board page over-fetches
// and filters until it has a full page — and a rank subtracts the banned players
// that outrank the profile being rendered, so the visible ranks stay 1, 2, 3
// with no gaps.
package readapi
