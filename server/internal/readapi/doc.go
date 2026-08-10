// Package readapi serves the public, CDN-cacheable /v1/* JSON endpoints and
// their cache headers (§4.8).
//
//	GET /v1/leaderboards               board metadata + row counts
//	GET /v1/leaderboards/{stat}        one board, ?limit=50&offset=0, limit ≤ 200
//	GET /v1/systems                    celestial-system headers + player/save counts
//	GET /v1/systems/{slug}             one complete celestial catalogue, slug or hash
//	GET /v1/players?q=                 handle search, handles only
//	GET /v1/players/{handle}           one profile, 404 for unknown or banned
//	GET /v1/players/{handle}/saves     that player's saves in first-seen order
//	GET /v1/players/{handle}/saves/{n} one save's career-scoped board rows
//	GET /v1/players/{handle}/events    that player's raw event log, redacted
//	GET /v1/compare?handles=a,b,c      the same board rows for up to 8 handles
//	GET /v1/feed                       the activity feed snapshot (feed.go)
//
// # What may never be published
//
// `user_key`, and anything derived from the mod's install id — which is one
// value per machine and therefore per *person*, not per account. privacy.go is
// the whole of that rule and the reasoning behind it; read it before adding a
// field to any response here.
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
