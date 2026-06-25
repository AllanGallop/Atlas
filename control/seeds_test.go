package main

import (
	"reflect"
	"testing"
)

func TestClassifySeed(t *testing.T) {
	tests := []struct {
		seed     string
		wantType string
		wantVal  string
	}{
		{"", "domain", ""},
		{"admin@example.com", "email", "admin@example.com"},
		{"203.0.113.10", "ip", "203.0.113.10"},
		{"example.com", "domain", "example.com"},
		{"api.example.com", "subdomain", "api.example.com"},
		{"https://shop.example.co.uk/path", "subdomain", "shop.example.co.uk"},
		{"http://203.0.113.20/", "ip", "203.0.113.20"},
	}

	for _, tc := range tests {
		gotType, gotVal := classifySeed(tc.seed)
		if gotType != tc.wantType || gotVal != tc.wantVal {
			t.Errorf("classifySeed(%q) = (%q, %q), want (%q, %q)",
				tc.seed, gotType, gotVal, tc.wantType, tc.wantVal)
		}
	}
}

func TestRegistrableDomain(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"api.example.com", "example.com"},
		{"*.example.com", "example.com"},
		{"shop.example.co.uk", "example.co.uk"},
		{"deep.shop.example.co.uk", "example.co.uk"},
		{"example.com", "example.com"},
		{"localhost", "localhost"},
	}

	for _, tc := range tests {
		if got := registrableDomain(tc.host); got != tc.want {
			t.Errorf("registrableDomain(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestNormalizeCollectors(t *testing.T) {
	defaults := []string{"dns", "http", "tls", "ct"}

	if got := normalizeCollectors(nil); !reflect.DeepEqual(got, defaults) {
		t.Fatalf("normalizeCollectors(nil) = %v, want %v", got, defaults)
	}

	got := normalizeCollectors([]string{"RDAP", "dns", "rdap", "bogus", "tls"})
	want := []string{"rdap", "dns", "tls"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeCollectors(...) = %v, want %v", got, want)
	}
}

func TestCollectorsForEntity(t *testing.T) {
	requested := []string{"dns", "http", "tls", "ct", "rdap"}

	domainCollectors := collectorsForEntity("domain", requested)
	for _, c := range []string{"dns", "http", "tls", "ct", "rdap"} {
		if !contains(domainCollectors, c) {
			t.Errorf("domain collectors missing %q: %v", c, domainCollectors)
		}
	}

	ipCollectors := collectorsForEntity("ip", requested)
	if contains(ipCollectors, "dns") || contains(ipCollectors, "rdap") {
		t.Fatalf("ip should not run dns/rdap: %v", ipCollectors)
	}
	if !contains(ipCollectors, "http") || !contains(ipCollectors, "tls") {
		t.Fatalf("ip should run http/tls: %v", ipCollectors)
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
