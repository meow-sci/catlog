package readapi

import (
	"net/http"
	"slices"
)

// Cross-origin access to the public read API.
//
// # Why this exists at all
//
// `spa/` is a second frontend built the opposite way to `site/`: a static React
// bundle hosted on GitHub Pages that fetches every number from these endpoints.
// It runs on a different origin, so without these headers the browser refuses to
// hand it any response body.
//
// # Why it stops here
//
// This middleware is applied by [Server.Register] and by nothing else. That is
// the whole security argument, and it is worth stating plainly:
//
//   - `/api/*` (the dashboard: claim, reissue, revoke, delete, logout) and the
//     `/auth/*` flows are authenticated by the `catlog_session` cookie. Adding
//     an `Access-Control-Allow-Origin` to those would let any page the allow-list
//     names read a signed-in user's account — and, with a matching
//     `Allow-Credentials`, act as them. They are registered by package identity
//     and never see this code.
//   - `/v1/ingest` is the §4.5.3 proof-of-possession endpoint. It is on the /v1
//     prefix but it is not a read endpoint; package ingest registers it and it
//     stays same-origin too.
//   - the admin mux is on its own loopback listener (§5.9) and is not reachable
//     from a browser tab at all.
//
// # Why no credentials, ever
//
// Nothing here emits `Access-Control-Allow-Credentials`. These four endpoints
// are anonymous public facts (§4.8) — there is no per-user answer to leak, so
// there is no reason to let a cross-origin request carry a cookie, and the
// combination of a reflected origin and credentials is the classic way this goes
// wrong.
type cors struct {
	// allowed is the exact-match origin list from [config.CORS]. Empty disables
	// cross-origin access entirely, which is the correct posture for a
	// deployment that has no second frontend.
	allowed []string
}

// corsMaxAge is how long a browser may cache a preflight, in seconds. Ten
// minutes: long enough that a page of leaderboard requests preflights once,
// short enough that removing an origin from the allow-list takes effect within a
// coffee break.
const corsMaxAge = "600"

// allow reports whether origin is on the list. An absent or foreign origin is
// not an error — it is a same-origin request, a curl, or a browser that will
// enforce the block itself.
func (c cors) allow(origin string) bool {
	return origin != "" && slices.Contains(c.allowed, origin)
}

// wrap adds the cross-origin read headers to a §4.8 handler.
//
// `Vary: Origin` is set unconditionally, including on responses that carry no
// `Access-Control-Allow-Origin` at all. These endpoints are served with
// `s-maxage=30` (§4.8) and are meant to sit behind a CDN: without the Vary a
// shared cache could store the answer it gave an allow-listed origin and hand
// the same bytes — headers included — to everybody else, which turns a narrow
// allow-list into an accidental wildcard.
func (c cors) wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		if origin := r.Header.Get("Origin"); c.allow(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		h(w, r)
	}
}

// preflight answers `OPTIONS` for a §4.8 route.
//
// A GET with no custom headers is a CORS "simple request" and is never
// preflighted, so in practice the SPA does not reach this — until somebody adds
// a header, at which point the absence of an OPTIONS route would be a 405 that
// looks like a network failure in the browser console. It is answered here so
// that day is uneventful.
//
// A preflight from an origin that is not allow-listed gets a bare 204 with no
// CORS headers, which is exactly the "no" the browser is asking about. It is not
// a 403: the response tells the caller nothing either way, and 403 would suggest
// the resource itself is forbidden.
func (c cors) preflight(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "Origin")
	w.Header().Add("Vary", "Access-Control-Request-Headers")
	// Never cached as a §4.8 public response: a preflight answer is per-origin.
	w.Header().Set("Cache-Control", "no-store")

	origin := r.Header.Get("Origin")
	if !c.allow(origin) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	// GET and HEAD are the only methods these routes have. Listing them (rather
	// than echoing Access-Control-Request-Method) means a preflight for a DELETE
	// is refused here instead of at the handler.
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	// Echoed rather than fixed: the set of headers a client might add is open
	// (Accept, a trace header, a cache-buster), and none of them can do anything
	// on a read endpoint that takes no auth. What must not be echoed is
	// credentials, and that is above.
	if want := r.Header.Get("Access-Control-Request-Headers"); want != "" {
		w.Header().Set("Access-Control-Allow-Headers", want)
	}
	w.Header().Set("Access-Control-Max-Age", corsMaxAge)
	w.WriteHeader(http.StatusNoContent)
}
