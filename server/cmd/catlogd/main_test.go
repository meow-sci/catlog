package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthz pins the §4.4 health contract: 200, JSON content type, exact body.
func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	if got, want := res.Header.Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := rec.Body.String(), `{"ok":true}`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}
