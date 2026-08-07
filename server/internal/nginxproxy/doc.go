// Package nginxproxy holds the testcontainers suite that drives a real nginx
// container in front of catlogd's real handlers (D7, §6.3).
//
// There is no production code here and there never will be: nginx is
// configuration, not a Go dependency. The package exists so that
// infra/nginx/dev.conf is executable — every directive in it that the system
// depends on (brotli body passthrough, X-Forwarded-For, the 2 MiB body cap,
// the per-IP limit_req zone, `proxy_buffering off` on the SSE feed, and the
// /admin/ 403) is asserted against the file that actually ships, rather than
// against a paraphrase of it.
//
// The tests live behind the `docker` build tag and are run by `make
// test-nginx`. Nothing in `make test` touches docker (§9.4): without a
// reachable daemon the suite skips with the reason, and `go test ./...`
// without the tag does not compile it at all.
package nginxproxy
