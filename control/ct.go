package main

import (
	"context"
	"encoding/json"
	"time"
)

type CTIngestorConfig struct {
	TargetTLDs       []string `json:"target_tlds"`
	BackfillMode     bool     `json:"backfill_mode"`
	IncludeReadonly  bool     `json:"include_readonly"`
	BatchesPerCycle  int      `json:"batches_per_cycle"`
	BatchSize        int      `json:"batch_size"`
}

type CTLogStatus struct {
	ID               string    `json:"id"`
	URL              string    `json:"url"`
	Description      string    `json:"description"`
	State            string    `json:"state"`
	LastTreeSize     int64     `json:"last_tree_size"`
	LastFetchedIndex int64     `json:"last_fetched_index"`
	ProgressPct      float64   `json:"progress_pct"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type CTIngestorStatus struct {
	Config              CTIngestorConfig `json:"config"`
	Logs                []CTLogStatus    `json:"logs"`
	CertificateCount    int              `json:"certificate_count"`
	CertificateNameCount int             `json:"certificate_name_count"`
	DomainCount         int              `json:"domain_count"`
}

func defaultCTConfig() CTIngestorConfig {
	return CTIngestorConfig{
		TargetTLDs:      []string{"com", "net", "org", "io", "co.uk", "com.au"},
		BackfillMode:    true,
		IncludeReadonly: true,
		BatchesPerCycle: 20,
		BatchSize:       512,
	}
}

func (s *Store) GetCTConfig(ctx context.Context) (CTIngestorConfig, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM ingestor_config WHERE key = 'ct'`).Scan(&raw)
	if err != nil {
		return defaultCTConfig(), nil
	}

	cfg := defaultCTConfig()
	_ = json.Unmarshal(raw, &cfg)
	if len(cfg.TargetTLDs) == 0 {
		cfg.TargetTLDs = defaultCTConfig().TargetTLDs
	}
	if cfg.BatchesPerCycle <= 0 {
		cfg.BatchesPerCycle = 20
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 512
	}
	return cfg, nil
}

func (s *Store) SetCTConfig(ctx context.Context, cfg CTIngestorConfig) error {
	if len(cfg.TargetTLDs) == 0 {
		cfg.TargetTLDs = defaultCTConfig().TargetTLDs
	}
	if cfg.BatchesPerCycle <= 0 {
		cfg.BatchesPerCycle = 20
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 512
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO ingestor_config (key, value, updated_at)
		VALUES ('ct', $1, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()
	`, raw)
	return err
}

func (s *Store) GetCTStatus(ctx context.Context) (*CTIngestorStatus, error) {
	cfg, err := s.GetCTConfig(ctx)
	if err != nil {
		return nil, err
	}

	status := &CTIngestorStatus{Config: cfg}

	rows, err := s.pool.Query(ctx, `
		SELECT id, url, description, state, last_tree_size, last_fetched_index, updated_at
		FROM ct_logs
		ORDER BY state ASC, updated_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var log CTLogStatus
		if err := rows.Scan(
			&log.ID, &log.URL, &log.Description, &log.State,
			&log.LastTreeSize, &log.LastFetchedIndex, &log.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if log.LastTreeSize > 0 {
			log.ProgressPct = float64(log.LastFetchedIndex) / float64(log.LastTreeSize) * 100
		}
		status.Logs = append(status.Logs, log)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM certificates`).Scan(&status.CertificateCount)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM certificate_names`).Scan(&status.CertificateNameCount)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM domains`).Scan(&status.DomainCount)

	return status, nil
}

func (s *Store) PivotByEntity(ctx context.Context, handle string) ([]PivotDomainResult, error) {
	handle = trimPivotValue(handle)
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT d.domain, d.first_seen, d.last_seen
		FROM domains d
		JOIN graph_edges ge ON ge.source_type = 'domain' AND ge.source_id = d.id
		WHERE ge.relationship = 'has_entity'
		  AND ge.target_type = 'entity'
		  AND ge.target_id = $1
		ORDER BY d.domain ASC
		LIMIT 500
	`, handle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPivotDomains(rows)
}

func (s *Store) PivotByTracker(ctx context.Context, trackerID string) ([]PivotDomainResult, error) {
	trackerID = trimPivotValue(trackerID)
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT d.domain, d.first_seen, d.last_seen
		FROM domains d
		JOIN graph_edges ge ON ge.source_type = 'domain' AND ge.source_id = d.id
		WHERE ge.relationship = 'shares_tracker'
		  AND ge.target_type = 'tracker_id'
		  AND ge.target_id = $1
		ORDER BY d.domain ASC
		LIMIT 500
	`, trackerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPivotDomains(rows)
}

func trimPivotValue(value string) string {
	for len(value) > 0 {
		if value[0] == ' ' || value[0] == '/' {
			value = value[1:]
			continue
		}
		break
	}
	return value
}
