package main

import (
	"net/http/httptest"
	"testing"
)

func TestParseStatusFilters(t *testing.T) {
	req := httptest.NewRequest("GET", "/campaigns?status=running,completed", nil)
	got, err := parseStatusFilters(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "running" || got[1] != "completed" {
		t.Fatalf("got %v", got)
	}

	req = httptest.NewRequest("GET", "/campaigns?status=active", nil)
	got, err = parseStatusFilters(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantActive := map[string]bool{}
	for _, s := range StatusFilterAliases["active"] {
		wantActive[s] = true
	}
	for _, s := range got {
		if !wantActive[s] {
			t.Fatalf("active alias expanded to unexpected status %q: %v", s, got)
		}
	}

	req = httptest.NewRequest("GET", "/campaigns?status=nope", nil)
	_, err = parseStatusFilters(req)
	if err == nil {
		t.Fatal("expected error for unknown status")
	}
}

func TestParseIntQuery(t *testing.T) {
	req := httptest.NewRequest("GET", "/campaigns?limit=25&offset=10", nil)
	if got := parseIntQuery(req, "limit", 50); got != 25 {
		t.Fatalf("limit = %d", got)
	}
	if got := parseIntQuery(req, "offset", 0); got != 10 {
		t.Fatalf("offset = %d", got)
	}
	if got := parseIntQuery(req, "missing", 7); got != 7 {
		t.Fatalf("fallback = %d", got)
	}
}
