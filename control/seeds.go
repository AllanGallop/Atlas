package main

import (
	"net"
	"net/url"
	"strings"
)

var multiPartPublicSuffixes = map[string]bool{
	"co.uk": true, "org.uk": true, "ac.uk": true, "gov.uk": true,
	"net.uk": true, "sch.uk": true, "com.au": true, "net.au": true,
	"org.au": true, "co.nz": true, "co.jp": true, "com.br": true,
	"com.mx": true, "co.za": true,
}

func classifySeed(seed string) (entityType, value string) {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return "domain", ""
	}

	if strings.Contains(seed, "@") {
		return "email", strings.ToLower(seed)
	}

	if ip := net.ParseIP(seed); ip != nil {
		return "ip", ip.String()
	}

	if strings.Contains(seed, "://") {
		parsed, err := url.Parse(seed)
		if err == nil && parsed.Host != "" {
			host := parsed.Hostname()
			if ip := net.ParseIP(host); ip != nil {
				return "ip", ip.String()
			}
			return classifyHost(host), strings.ToLower(host)
		}
	}

	host := strings.TrimSuffix(seed, ".")
	host = strings.ToLower(host)

	if ip := net.ParseIP(host); ip != nil {
		return "ip", ip.String()
	}

	return classifyHost(host), host
}

func registrableDomain(host string) string {
	host = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "*.")
	parts := strings.Split(host, ".")
	if len(parts) >= 3 {
		suffix := parts[len(parts)-2] + "." + parts[len(parts)-1]
		if multiPartPublicSuffixes[suffix] {
			return parts[len(parts)-3] + "." + suffix
		}
	}
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "." + parts[len(parts)-1]
	}
	return host
}

func classifyHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if net.ParseIP(host) != nil {
		return "ip"
	}
	if host == registrableDomain(host) {
		return "domain"
	}
	return "subdomain"
}

func normalizeCollectors(requested []string) []string {
	if len(requested) == 0 {
		return []string{"dns", "http", "tls", "ct"}
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(requested))
	for _, c := range requested {
		c = strings.ToLower(strings.TrimSpace(c))
		if !ValidCollectors[c] {
			continue
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	if len(out) == 0 {
		return []string{"dns", "http", "tls", "ct"}
	}
	return out
}

func collectorsForEntity(entityType string, requested []string) []string {
	out := make([]string, 0, len(requested))
	for _, c := range requested {
		if !MVPCollectors[c] {
			continue
		}
		switch c {
		case "dns":
			if entityType == "domain" || entityType == "subdomain" || entityType == "nameserver" {
				out = append(out, c)
			}
		case "ct":
			if entityType == "domain" || entityType == "subdomain" {
				out = append(out, c)
			}
		case "http", "tls":
			if entityType == "domain" || entityType == "subdomain" || entityType == "url" || entityType == "ip" {
				out = append(out, c)
			}
		case "rdap":
			if entityType == "domain" || entityType == "subdomain" {
				out = append(out, c)
			}
		}
	}
	return out
}
