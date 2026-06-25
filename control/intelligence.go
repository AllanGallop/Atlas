package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type DomainRecord struct {
	ID        string    `json:"id"`
	Domain    string    `json:"domain"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

type CertificateRecord struct {
	ID               string     `json:"id"`
	FingerprintSHA256 string    `json:"fingerprint_sha256"`
	SubjectCN        *string    `json:"subject_cn,omitempty"`
	Issuer           *string    `json:"issuer,omitempty"`
	NotBefore        *time.Time `json:"not_before,omitempty"`
	NotAfter         *time.Time `json:"not_after,omitempty"`
	FirstSeen        time.Time  `json:"first_seen"`
	SourceLogID      *string    `json:"source_log_id,omitempty"`
	Names            []string   `json:"names,omitempty"`
}

type RDAPRecord struct {
	ID          string          `json:"id"`
	DomainID    string          `json:"domain_id"`
	Registrar   *string         `json:"registrar,omitempty"`
	Registry    *string         `json:"registry,omitempty"`
	CreatedAt   *time.Time      `json:"created_at,omitempty"`
	UpdatedAt   *time.Time      `json:"updated_at,omitempty"`
	ExpiresAt   *time.Time      `json:"expires_at,omitempty"`
	Statuses    json.RawMessage `json:"statuses"`
	Nameservers json.RawMessage `json:"nameservers"`
	Entities    json.RawMessage `json:"entities"`
	Redacted    bool            `json:"redacted"`
	RawJSON     json.RawMessage `json:"raw_json,omitempty"`
	FetchedAt   time.Time       `json:"fetched_at"`
}

type DNSRecord struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	RecordType string    `json:"record_type"`
	Value      string    `json:"value"`
	TTL        *int      `json:"ttl,omitempty"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
}

type GraphEdgeRecord struct {
	ID           string    `json:"id"`
	SourceType   string    `json:"source_type"`
	SourceID     string    `json:"source_id"`
	Relationship string    `json:"relationship"`
	TargetType   string    `json:"target_type"`
	TargetID     string    `json:"target_id"`
	Confidence   float64   `json:"confidence"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	Source       string    `json:"source"`
}

type DomainPivotSummary struct {
	Nameservers []string `json:"nameservers"`
	Registrars  []string `json:"registrars"`
	Certificates []string `json:"certificates"`
	MXHosts     []string `json:"mx_hosts"`
	FaviconHashes []string `json:"favicon_hashes"`
	EntityHandles []string `json:"entity_handles"`
}

type PivotDomainResult struct {
	Domain    string    `json:"domain"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

func (s *Store) UpsertDomain(ctx context.Context, domain string) (string, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	id := "dom_" + uuid.New().String()
	err := s.pool.QueryRow(ctx, `
		INSERT INTO domains (id, domain, first_seen, last_seen)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (domain) DO UPDATE SET last_seen = NOW()
		RETURNING id
	`, id, domain).Scan(&id)
	return id, err
}

func (s *Store) GetDomainByName(ctx context.Context, domain string) (*DomainRecord, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	var d DomainRecord
	err := s.pool.QueryRow(ctx, `
		SELECT id, domain, first_seen, last_seen FROM domains WHERE domain = $1
	`, domain).Scan(&d.ID, &d.Domain, &d.FirstSeen, &d.LastSeen)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) ListSubdomainsForDomain(ctx context.Context, apex string) ([]string, error) {
	apex = strings.ToLower(strings.TrimSpace(apex))
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT cn.name
		FROM certificate_names cn
		WHERE cn.registered_domain = $1
		  AND cn.name != $1
		  AND cn.name LIKE '%.' || $1
		ORDER BY cn.name ASC
	`, apex)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (s *Store) ListCertificatesForDomain(ctx context.Context, apex string) ([]CertificateRecord, error) {
	apex = strings.ToLower(strings.TrimSpace(apex))
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT c.id, c.fingerprint_sha256, c.subject_cn, c.issuer,
		       c.not_before, c.not_after, c.first_seen, c.source_log_id
		FROM certificates c
		JOIN certificate_names cn ON cn.certificate_id = c.id
		WHERE cn.registered_domain = $1
		ORDER BY c.first_seen DESC
	`, apex)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []CertificateRecord{}
	for rows.Next() {
		var c CertificateRecord
		if err := rows.Scan(
			&c.ID, &c.FingerprintSHA256, &c.SubjectCN, &c.Issuer,
			&c.NotBefore, &c.NotAfter, &c.FirstSeen, &c.SourceLogID,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetLatestRDAP(ctx context.Context, domainID string) (*RDAPRecord, error) {
	var r RDAPRecord
	var statuses, nameservers, entities, rawJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, domain_id, registrar, registry, created_at, updated_at, expires_at,
		       statuses, nameservers, entities, redacted, raw_json, fetched_at
		FROM rdap_records
		WHERE domain_id = $1
		ORDER BY fetched_at DESC
		LIMIT 1
	`, domainID).Scan(
		&r.ID, &r.DomainID, &r.Registrar, &r.Registry,
		&r.CreatedAt, &r.UpdatedAt, &r.ExpiresAt,
		&statuses, &nameservers, &entities, &r.Redacted, &rawJSON, &r.FetchedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Statuses = statuses
	r.Nameservers = nameservers
	r.Entities = entities
	r.RawJSON = rawJSON
	return &r, nil
}

func (s *Store) ListDNSForDomain(ctx context.Context, apex string) ([]DNSRecord, error) {
	apex = strings.ToLower(strings.TrimSpace(apex))
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, record_type, value, ttl, first_seen, last_seen
		FROM dns_records
		WHERE name = $1 OR name LIKE '%.' || $1
		ORDER BY name ASC, record_type ASC, value ASC
	`, apex)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanDNSRecords(rows)
}

func scanDNSRecords(rows pgx.Rows) ([]DNSRecord, error) {
	out := []DNSRecord{}
	for rows.Next() {
		var d DNSRecord
		if err := rows.Scan(&d.ID, &d.Name, &d.RecordType, &d.Value, &d.TTL, &d.FirstSeen, &d.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDomainPivots(ctx context.Context, apex string) (*DomainPivotSummary, error) {
	apex = strings.ToLower(strings.TrimSpace(apex))
	summary := &DomainPivotSummary{
		Nameservers:   []string{},
		Registrars:    []string{},
		Certificates:  []string{},
		MXHosts:       []string{},
		FaviconHashes: []string{},
		EntityHandles: []string{},
	}

	domain, err := s.GetDomainByName(ctx, apex)
	if err != nil {
		return nil, err
	}

	if domain != nil {
		rdap, err := s.GetLatestRDAP(ctx, domain.ID)
		if err != nil {
			return nil, err
		}
		if rdap != nil {
			if rdap.Registrar != nil && *rdap.Registrar != "" {
				summary.Registrars = append(summary.Registrars, *rdap.Registrar)
			}
			var ns []string
			_ = json.Unmarshal(rdap.Nameservers, &ns)
			summary.Nameservers = append(summary.Nameservers, ns...)

			var entities []map[string]interface{}
			_ = json.Unmarshal(rdap.Entities, &entities)
			for _, ent := range entities {
				if handle, ok := ent["handle"].(string); ok && handle != "" {
					summary.EntityHandles = append(summary.EntityHandles, handle)
				}
			}
		}
	}

	certs, err := s.ListCertificatesForDomain(ctx, apex)
	if err != nil {
		return nil, err
	}
	for _, c := range certs {
		summary.Certificates = append(summary.Certificates, c.FingerprintSHA256)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT value FROM dns_records
		WHERE (name = $1 OR name LIKE '%.' || $1) AND record_type = 'MX'
		ORDER BY value ASC
	`, apex)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var mx string
		if err := rows.Scan(&mx); err != nil {
			rows.Close()
			return nil, err
		}
		summary.MXHosts = append(summary.MXHosts, mx)
	}
	rows.Close()

	faviconRows, err := s.pool.Query(ctx, `
		SELECT DISTINCT hf.favicon_hash
		FROM http_fingerprints hf
		JOIN hosts h ON h.id = hf.host_id
		WHERE h.registered_domain = $1 AND hf.favicon_hash IS NOT NULL AND hf.favicon_hash != ''
		ORDER BY hf.favicon_hash ASC
	`, apex)
	if err != nil {
		return nil, err
	}
	for faviconRows.Next() {
		var hash string
		if err := faviconRows.Scan(&hash); err != nil {
			faviconRows.Close()
			return nil, err
		}
		summary.FaviconHashes = append(summary.FaviconHashes, hash)
	}
	faviconRows.Close()

	entityRows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ge.target_id
		FROM graph_edges ge
		JOIN domains d ON d.id = ge.source_id
		WHERE d.domain = $1
		  AND ge.relationship = 'has_entity'
		  AND ge.target_type = 'entity'
		ORDER BY ge.target_id ASC
	`, apex)
	if err != nil {
		return nil, err
	}
	for entityRows.Next() {
		var handle string
		if err := entityRows.Scan(&handle); err != nil {
			entityRows.Close()
			return nil, err
		}
		summary.EntityHandles = append(summary.EntityHandles, handle)
	}
	entityRows.Close()

	return summary, nil
}

func (s *Store) PivotByNameserver(ctx context.Context, ns string) ([]PivotDomainResult, error) {
	return s.pivotDomainsByDNSValue(ctx, "NS", ns)
}

func (s *Store) PivotByMX(ctx context.Context, mx string) ([]PivotDomainResult, error) {
	return s.pivotDomainsByDNSValue(ctx, "MX", mx)
}

func (s *Store) pivotDomainsByDNSValue(ctx context.Context, recordType, value string) ([]PivotDomainResult, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT d.domain, d.first_seen, d.last_seen
		FROM domains d
		JOIN dns_records dr ON dr.name = d.domain OR dr.name LIKE '%.' || d.domain
		WHERE dr.record_type = $1 AND LOWER(dr.value) = $2
		ORDER BY d.domain ASC
		LIMIT 500
	`, recordType, value)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPivotDomains(rows)
}

func (s *Store) PivotByRegistrar(ctx context.Context, registrar string) ([]PivotDomainResult, error) {
	registrar = strings.TrimSpace(registrar)
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT d.domain, d.first_seen, d.last_seen
		FROM domains d
		JOIN rdap_records r ON r.domain_id = d.id
		WHERE r.registrar ILIKE $1
		ORDER BY d.domain ASC
		LIMIT 500
	`, registrar)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPivotDomains(rows)
}

func (s *Store) PivotByCertificate(ctx context.Context, fingerprint string) ([]PivotDomainResult, error) {
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT d.domain, d.first_seen, d.last_seen
		FROM domains d
		JOIN certificate_names cn ON cn.registered_domain = d.domain
		JOIN certificates c ON c.id = cn.certificate_id
		WHERE c.fingerprint_sha256 = $1
		ORDER BY d.domain ASC
		LIMIT 500
	`, fingerprint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPivotDomains(rows)
}

func (s *Store) PivotByFavicon(ctx context.Context, hash string) ([]PivotDomainResult, error) {
	hash = strings.TrimSpace(hash)
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT d.domain, d.first_seen, d.last_seen
		FROM domains d
		JOIN hosts h ON h.registered_domain = d.domain
		JOIN http_fingerprints hf ON hf.host_id = h.id
		WHERE hf.favicon_hash = $1
		ORDER BY d.domain ASC
		LIMIT 500
	`, hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPivotDomains(rows)
}

func scanPivotDomains(rows pgx.Rows) ([]PivotDomainResult, error) {
	out := []PivotDomainResult{}
	for rows.Next() {
		var p PivotDomainResult
		if err := rows.Scan(&p.Domain, &p.FirstSeen, &p.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ListGraphEdgesForDomain(ctx context.Context, apex string) ([]GraphEdgeRecord, error) {
	domain, err := s.GetDomainByName(ctx, apex)
	if err != nil {
		return nil, err
	}
	if domain == nil {
		return []GraphEdgeRecord{}, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, source_type, source_id, relationship, target_type, target_id,
		       confidence, first_seen, last_seen, source
		FROM graph_edges
		WHERE (source_type = 'domain' AND source_id = $1)
		   OR (target_type = 'domain' AND target_id = $1)
		ORDER BY relationship ASC, last_seen DESC
		LIMIT 1000
	`, domain.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []GraphEdgeRecord{}
	for rows.Next() {
		var e GraphEdgeRecord
		if err := rows.Scan(
			&e.ID, &e.SourceType, &e.SourceID, &e.Relationship,
			&e.TargetType, &e.TargetID, &e.Confidence,
			&e.FirstSeen, &e.LastSeen, &e.Source,
		); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type EnrichDomainMessage struct {
	Domain     string   `json:"domain"`
	Collectors []string `json:"collectors"`
}

func normalizeDomain(domain string) (string, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimPrefix(domain, "*.")
	domain = strings.TrimSuffix(domain, ".")
	if domain == "" || !strings.Contains(domain, ".") {
		return "", fmt.Errorf("invalid domain: %s", domain)
	}
	return domain, nil
}

func registrableDomainForSeed(host string) (string, bool) {
	domain, err := normalizeDomain(host)
	if err != nil {
		return "", false
	}
	return registrableDomain(domain), true
}
