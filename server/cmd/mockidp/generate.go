package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// The generative endpoint (`POST /generate`) exists for one caller: the
// `catlog.loadgen` harness, which needs hundreds of *distinct* players to sign
// in through the real OAuth flow. The committed cast in `server/mockidp.toml`
// is a fixed list of five buttons — right for a browser test, useless for a
// load test.
//
// # Why this is safe
//
// mockidp is a development binary. It is never deployed, never proxied and
// never reachable from anything but 127.0.0.1 in `make dev`, `make e2e` and the
// Go test suite. Nothing about it is a security boundary; it exists to *be* the
// three real providers on a laptop.
//
// What matters is what this does NOT do:
//
//   - It does not touch `catlogd`. Every generated subject goes through the same
//     authorize → code → token → userinfo (or id_token) dance the static cast
//     uses, so catlogd runs its real code exchange, its real ES256 id_token
//     verification for Google, its real `user_key = HMAC(pepper, "idp:sub")`
//     derivation, its real session cookie and its real account-age, handle and
//     quota rules. No flag, config key or endpoint on catlogd changed.
//   - It does not touch the static cast. Generated accounts live in their own
//     map and are **never rendered on a consent page or the index**, so the
//     `#login-as-<slug(label)>` DOM ids that `TestDOMIdsAreStable` and the
//     playwright suite depend on are byte-for-byte what they were.
//   - It does not weaken the account-age gate. Quite the opposite: a generated
//     Discord subject carries a real aged snowflake and a generated GitHub
//     account a real `created_at`, and `new_every` deliberately mints a
//     proportion of accounts that are *too new*, so the harness can assert that
//     catlogd refuses them with `account_too_new`.
//
// Determinism is a requirement, not a nicety: the same `seed` produces the same
// subjects, so a load run can be replayed against the same identities.

// MaxGeneratedPerRequest bounds one `POST /generate` call. Generous for a load
// run, small enough that a typo cannot ask for a billion accounts.
const MaxGeneratedPerRequest = 5000

// MaxGeneratedAccounts bounds the registry over the process's lifetime. mockidp
// holds nothing worth persisting and a load harness is a short-lived thing, so
// this is a memory guard rather than a policy.
const MaxGeneratedAccounts = 200_000

// DefaultGeneratedAgeDays is how old a generated account is when the request
// does not say. Comfortably past catlogd's 30-day gate (§4.7).
const DefaultGeneratedAgeDays = 400

// MaxGenerateBodyBytes caps the request body. A GenerateRequest is a few
// hundred bytes.
const MaxGenerateBodyBytes = 64 << 10

// GenerateRequest is the `POST /generate` body. Every field is optional; an
// empty body mints one aged Discord account.
type GenerateRequest struct {
	// Count is how many accounts to mint; defaults to 1, capped at
	// [MaxGeneratedPerRequest].
	Count int `json:"count"`
	// IdP is "discord", "google" or "github". Empty round-robins across all
	// three, which is what exercises the Google id_token path and the GitHub
	// created_at gate alongside Discord's snowflake.
	IdP string `json:"idp"`
	// Seed namespaces the derivation. Two requests with the same seed, count,
	// idp and ages produce byte-identical subjects, on a fresh mockidp, for the
	// whole UTC day (creation instants are quantised to the day — see
	// [Server.mintGeneratedLocked] — because a Discord snowflake encodes its own
	// creation millisecond and would otherwise vary with the wall clock).
	Seed string `json:"seed"`
	// Prefix is the display-name prefix. Defaults to "gen".
	Prefix string `json:"prefix"`
	// AgeDays is how old the accounts are. Defaults to
	// [DefaultGeneratedAgeDays].
	AgeDays int `json:"age_days"`
	// NewEvery makes every Nth account brand new, so it fails catlogd's
	// account-age gate. 0 (the default) makes none. Google is never made new:
	// it publishes no account age and §4.7 gates it on quotas alone.
	NewEvery int `json:"new_every"`
}

// GeneratedAccount is one minted subject, as the harness needs it.
type GeneratedAccount struct {
	IdP string `json:"idp"`
	// Sub is the exact string to pass as `?user=` on the authorize endpoint,
	// and the exact string the provider's user endpoint will report.
	Sub   string `json:"sub"`
	Name  string `json:"name"`
	Label string `json:"label"`
	// CreatedAt is RFC 3339 — what catlogd's age gate reads, directly for
	// GitHub and via the snowflake for Discord.
	CreatedAt string `json:"created_at"`
	// TooNew is true when this account is younger than catlogd's gate, so a
	// login with it is expected to be refused with `account_too_new`.
	TooNew bool `json:"too_new"`
	// AuthorizePath is the provider's authorize route on this process, so the
	// caller does not have to hard-code the three different shapes.
	AuthorizePath string `json:"authorize_path"`
}

// GenerateResponse is the `POST /generate` reply.
type GenerateResponse struct {
	Count    int                `json:"count"`
	Accounts []GeneratedAccount `json:"accounts"`
}

// authorizePaths maps an IdP to the authorize route [Server.Handler] mounts.
var authorizePaths = map[string]string{
	IdPDiscord: "/discord/oauth/authorize",
	IdPGoogle:  "/google/authorize",
	IdPGitHub:  "/github/login/oauth/authorize",
}

// generateOrder is the round-robin used when the request names no IdP. All
// three, so a load run exercises every subject-resolution path catlogd has.
var generateOrder = []string{IdPDiscord, IdPGoogle, IdPGitHub}

func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeGenerate(w, r)
	if !ok {
		return
	}

	now := s.now()

	s.mu.Lock()
	if len(s.generated)+req.Count > MaxGeneratedAccounts {
		held := len(s.generated)
		s.mu.Unlock()
		s.oauthError(w, http.StatusInsufficientStorage, "server_error",
			fmt.Sprintf("mockidp holds %d generated accounts and the ceiling is %d; restart it",
				held, MaxGeneratedAccounts))
		return
	}

	out := make([]GeneratedAccount, 0, req.Count)
	for i := range req.Count {
		idp := req.IdP
		if idp == "" {
			idp = generateOrder[i%len(generateOrder)]
		}
		// Google publishes no account age, so "too new" is meaningless there
		// and catlogd would wave it through — which would make an assertion
		// about refusal wrong rather than strict.
		tooNew := req.NewEvery > 0 && i%req.NewEvery == 0 && idp != IdPGoogle

		acct := s.mintGeneratedLocked(idp, req, i, now, tooNew)
		s.generated[idp+":"+acct.Sub] = acct

		out = append(out, GeneratedAccount{
			IdP:           idp,
			Sub:           acct.Sub,
			Name:          acct.Name,
			Label:         acct.Label,
			CreatedAt:     acct.CreatedAt.Format(time.RFC3339),
			TooNew:        tooNew,
			AuthorizePath: authorizePaths[idp],
		})
	}
	held := len(s.generated)
	s.mu.Unlock()

	s.log.Info("accounts generated",
		"count", len(out), "idp", req.IdP, "seed", req.Seed, "held", held)
	writeJSON(w, http.StatusOK, GenerateResponse{Count: len(out), Accounts: out})
}

// decodeGenerate reads and normalises the request body, writing the rejection
// itself when it cannot.
func (s *Server) decodeGenerate(w http.ResponseWriter, r *http.Request) (GenerateRequest, bool) {
	var req GenerateRequest

	body := http.MaxBytesReader(w, r.Body, MaxGenerateBodyBytes)
	dec := json.NewDecoder(body)
	// A typo in a field name must not silently mint the default population.
	dec.DisallowUnknownFields()
	// An absent body is `{}`, so `curl -XPOST .../generate` works.
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.oauthError(w, http.StatusBadRequest, "invalid_request", "request body: "+err.Error())
		return req, false
	}

	switch {
	case req.Count < 0:
		s.oauthError(w, http.StatusBadRequest, "invalid_request", "count must not be negative")
		return req, false
	case req.Count > MaxGeneratedPerRequest:
		s.oauthError(w, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("count %d exceeds the %d-account limit for one request",
				req.Count, MaxGeneratedPerRequest))
		return req, false
	case req.IdP != "" && authorizePaths[req.IdP] == "":
		s.oauthError(w, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("idp %q, want discord, google, github or empty", req.IdP))
		return req, false
	case req.NewEvery < 0:
		s.oauthError(w, http.StatusBadRequest, "invalid_request", "new_every must not be negative")
		return req, false
	}

	if req.Count == 0 {
		req.Count = 1
	}
	if req.AgeDays <= 0 {
		req.AgeDays = DefaultGeneratedAgeDays
	}
	if req.Prefix == "" {
		req.Prefix = "gen"
	}
	return req, true
}

// mintGeneratedLocked builds one account. Caller holds s.mu.
//
// The creation instant is derived per index rather than shared, which is what
// makes Discord snowflakes unique by construction: a snowflake's high bits are
// its millisecond timestamp, so one distinct millisecond per account is already
// a distinct id before the hashed low bits are mixed in.
func (s *Server) mintGeneratedLocked(idp string, req GenerateRequest, index int, now time.Time, tooNew bool) Account {
	// Quantised, then spread one second per index. Both halves earn their keep:
	// the quantisation is what makes a Discord snowflake — whose high bits *are*
	// its creation millisecond — reproducible for a given seed rather than a
	// function of when the request happened to arrive, and the per-index second
	// makes those high bits unique by construction so uniqueness does not rest
	// on 22 hashed bits alone.
	var created time.Time
	if tooNew {
		// Minted within the last hour or so. Well inside catlogd's 30-day gate,
		// which is the point.
		created = now.UTC().Truncate(time.Minute)
	} else {
		created = now.UTC().Add(-time.Duration(req.AgeDays) * 24 * time.Hour).Truncate(24 * time.Hour)
	}
	created = created.Add(-time.Duration(index) * time.Second)

	digest := sha256.Sum256(
		[]byte("catlog-mockidp-generate:" + req.Seed + ":" + idp + ":" + strconv.Itoa(index)))
	mixed := binary.BigEndian.Uint64(digest[:8])

	name := fmt.Sprintf("%s-%s-%04d", req.Prefix, idp, index)
	acct := Account{
		IdP:       idp,
		Name:      name,
		Label:     name,
		CreatedAt: created,
		// Deliberately not a `login-as-` id: generated accounts are never
		// rendered, and giving them one would invite exactly that.
		ElementID: "generated-" + idp + "-" + strconv.Itoa(index),
	}

	switch idp {
	case IdPDiscord:
		// `(id >> 22) + DiscordEpoch` is the account's creation time (§4.7), so
		// building it in that direction produces a snowflake catlogd reads back
		// as genuinely aged. The low 22 bits are a real snowflake's worker and
		// sequence fields, and are opaque to catlog.
		high := (created.UnixMilli() - DiscordEpoch) << SnowflakeShift
		acct.Sub = strconv.FormatInt(high|int64(mixed&0x3F_FFFF), 10)

	case IdPGitHub:
		// Above the static cast's 4242/4243 and above anything a hand-written
		// fixture would pick, so the two populations cannot collide.
		acct.Sub = strconv.FormatInt(1_000_000_000+int64(mixed%8_000_000_000), 10)

	default: // google — the sub is an opaque string
		acct.Sub = fmt.Sprintf("gen-%s-%d-%08x", Slug(req.Seed), index, uint32(mixed))
	}

	// A collision is vanishingly unlikely and silently catastrophic — two
	// "players" sharing one user_key — so it is resolved rather than assumed
	// away.
	for bump := 1; s.subTakenLocked(idp, acct.Sub); bump++ {
		acct.Sub = bumpSub(idp, acct.Sub, bump)
	}
	return acct
}

// subTakenLocked reports whether an (idp, sub) pair is already spoken for by
// either population. Caller holds s.mu.
func (s *Server) subTakenLocked(idp, sub string) bool {
	if _, ok := s.generated[idp+":"+sub]; ok {
		return true
	}
	for _, a := range s.accounts {
		if a.IdP == idp && a.Sub == sub {
			return true
		}
	}
	return false
}

// bumpSub perturbs a colliding subject while keeping its provider's shape: a
// decimal integer for Discord and GitHub, an opaque string for Google.
func bumpSub(idp, sub string, bump int) string {
	switch idp {
	case IdPDiscord, IdPGitHub:
		n, err := strconv.ParseInt(sub, 10, 64)
		if err != nil {
			return sub + strconv.Itoa(bump)
		}
		return strconv.FormatInt(n+int64(bump), 10)
	default:
		return sub + "-" + strconv.Itoa(bump)
	}
}

// generatedAccount looks a subject up in the generated registry.
func (s *Server) generatedAccount(idp, sub string) (Account, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.generated[idp+":"+sub]
	return a, ok
}

// GeneratedCount reports how many accounts have been minted, for tests.
func (s *Server) GeneratedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.generated)
}
