package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCallAdminAcceptsAcceptedResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"phase":"state"}`))
	}))
	defer srv.Close()

	var got struct {
		Phase string `json:"phase"`
	}
	if err := callAdmin(context.Background(), http.MethodPost, srv.URL, struct{}{}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Phase != "state" {
		t.Fatalf("phase = %q, want state", got.Phase)
	}
}
