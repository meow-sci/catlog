// Package integration holds catlogd's end-to-end tests: they build the real
// binaries, run them on loopback ports and drive them over HTTP.
//
// Everything here is behind the `integration` build tag (§12 WP2), so
// `make server-test` stays fast and hermetic while `make test-integration`
// exercises the whole stack:
//
//	cd server && go test -tags integration ./integration/
//
// No external network is ever touched — the servers bind 127.0.0.1 on ports the
// kernel hands out (D2).
package integration
