package authz

import (
	"fmt"
	"net/http"
)

// The §4.9 error-code registry. Every rejection anywhere in catlog carries one
// of these, and the JSON shape is always `{"error": code, "detail"?: s,
// "server_time"?: ms}`.
const (
	CodeBadRequest          = "bad_request"
	CodeMalformedBatch      = "malformed_batch"
	CodeUnsupportedEncoding = "unsupported_encoding"
	CodeLicenseInvalid      = "license_invalid"
	CodeLicenseExpired      = "license_expired"
	CodeLicenseRevoked      = "license_revoked"
	CodeProofInvalid        = "proof_invalid"
	CodeClockSkew           = "clock_skew"
	CodeBanned              = "banned"
	CodeStreamFork          = "stream_fork"
	CodeRateLimited         = "rate_limited"
	CodeTooLarge            = "too_large"
	CodeNotFound            = "not_found"
	CodeHandleTaken         = "handle_taken"
	CodeHandleInvalid       = "handle_invalid"
	CodeHandleReserved      = "handle_reserved"
	CodeQuotaExceeded       = "quota_exceeded"
	CodeAccountTooNew       = "account_too_new"
	CodeInternal            = "internal"
)

// codeStatus maps every §4.9 code onto its HTTP status (§4.4). Declared as one
// table so a handler can never invent a different status for a code.
var codeStatus = map[string]int{
	CodeBadRequest:          http.StatusBadRequest,
	CodeMalformedBatch:      http.StatusBadRequest,
	CodeUnsupportedEncoding: http.StatusUnsupportedMediaType,
	CodeLicenseInvalid:      http.StatusUnauthorized,
	CodeLicenseExpired:      http.StatusUnauthorized,
	CodeLicenseRevoked:      http.StatusUnauthorized,
	CodeProofInvalid:        http.StatusUnauthorized,
	CodeClockSkew:           http.StatusUnauthorized,
	CodeBanned:              http.StatusUnauthorized,
	CodeStreamFork:          http.StatusConflict,
	CodeRateLimited:         http.StatusTooManyRequests,
	CodeTooLarge:            http.StatusRequestEntityTooLarge,
	CodeNotFound:            http.StatusNotFound,
	CodeHandleTaken:         http.StatusConflict,
	CodeHandleInvalid:       http.StatusBadRequest,
	CodeHandleReserved:      http.StatusConflict,
	CodeQuotaExceeded:       http.StatusTooManyRequests,
	CodeAccountTooNew:       http.StatusForbidden,
	CodeInternal:            http.StatusInternalServerError,
}

// Status returns the HTTP status for a §4.9 code, or 500 for an unregistered
// one (which is a programming error, not a client error).
func Status(code string) int {
	if s, ok := codeStatus[code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// Codes lists every registered §4.9 code. Used to pre-register the
// `ingest_rejected_<code>` expvars (§5.9) so a counter exists before its first
// rejection.
func Codes() []string {
	out := make([]string, 0, len(codeStatus))
	for c := range codeStatus {
		out = append(out, c)
	}
	return out
}

// Error is a rejection: a §4.9 code, the §4.5.3 step that produced it, and a
// short detail safe to hand back to the client.
//
// Step is what makes the verification order testable — the chain is a
// DoS-resistance property, so "which step rejected this" is part of the
// contract, not debug noise.
type Error struct {
	// Code is the §4.9 error code. It alone determines the HTTP status.
	Code string
	// Step is the §4.5.3 step number, or 0 for rejections outside the chain
	// (the Content-Encoding check, the body decoder).
	Step int
	// Detail is the `detail` field of the response body. It must never carry
	// key material, license or proof contents (§5.11).
	Detail string
	// RetryAfter, when non-zero, is emitted as the Retry-After header (§4.3
	// rate limiting, §5.5 backpressure).
	RetryAfter int
	// cause is for the log line only; it never reaches the client.
	cause error
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("authz: %s (step %d)", e.Code, e.Step)
	}
	return fmt.Sprintf("authz: %s (step %d): %s", e.Code, e.Step, e.Detail)
}

// Unwrap exposes the underlying cause so callers can test for, say, a store
// failure behind an `internal` rejection.
func (e *Error) Unwrap() error { return e.cause }

// Status is the HTTP status this rejection maps to (§4.4).
func (e *Error) Status() int { return Status(e.Code) }

// fail builds an Error at a given chain step.
func fail(step int, code, detail string) *Error {
	return &Error{Code: code, Step: step, Detail: detail}
}

// failf builds an Error with a formatted detail.
func failf(step int, code, format string, args ...any) *Error {
	return &Error{Code: code, Step: step, Detail: fmt.Sprintf(format, args...)}
}

// internalErr wraps an unexpected failure (a database error, say). The detail is
// deliberately generic: the cause goes to the log, not to the client.
func internalErr(step int, cause error) *Error {
	return &Error{Code: CodeInternal, Step: step, Detail: "internal error", cause: cause}
}
