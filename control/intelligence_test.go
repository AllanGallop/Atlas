package main

import (
	"testing"
)

func TestNormalizeDomain(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"Example.COM", "example.com", false},
		{"*.example.com", "example.com", false},
		{"example.com.", "example.com", false},
		{"  api.example.com  ", "api.example.com", false},
		{"", "", true},
		{"localhost", "", true},
		{"notadomain", "", true},
	}

	for _, tc := range tests {
		got, err := normalizeDomain(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalizeDomain(%q) expected error", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeDomain(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizeDomain(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestRegistrableDomainForSeed(t *testing.T) {
	apex, ok := registrableDomainForSeed("api.example.co.uk")
	if !ok || apex != "example.co.uk" {
		t.Fatalf("registrableDomainForSeed = (%q, %v), want (example.co.uk, true)", apex, ok)
	}

	_, ok = registrableDomainForSeed("localhost")
	if ok {
		t.Fatal("expected invalid host")
	}
}

func TestTrimPivotValue(t *testing.T) {
	if got := trimPivotValue("  ns1.example.net"); got != "ns1.example.net" {
		t.Fatalf("trimPivotValue = %q", got)
	}
	if got := trimPivotValue("/entity-handle"); got != "entity-handle" {
		t.Fatalf("trimPivotValue leading slash = %q", got)
	}
}

func TestDefaultCTConfig(t *testing.T) {
	cfg := defaultCTConfig()
	if len(cfg.TargetTLDs) == 0 {
		t.Fatal("expected default target TLDs")
	}
	if cfg.BatchesPerCycle <= 0 || cfg.BatchSize <= 0 {
		t.Fatalf("invalid batch defaults: %+v", cfg)
	}
}
