package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}

	store := &Store{pool: pool}
	if err := store.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS campaigns (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL DEFAULT 'queued',
			depth INT NOT NULL DEFAULT 0,
			max_depth INT NOT NULL DEFAULT 2,
			max_entities INT NOT NULL DEFAULT 5000,
			collectors JSONB NOT NULL DEFAULT '[]',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE TABLE IF NOT EXISTS entities (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			value TEXT NOT NULL,
			first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (type, value)
		);

		CREATE TABLE IF NOT EXISTS campaign_entities (
			campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
			entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			depth INT NOT NULL DEFAULT 0,
			PRIMARY KEY (campaign_id, entity_id)
		);

		CREATE TABLE IF NOT EXISTS observations (
			id TEXT PRIMARY KEY,
			campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
			collector TEXT NOT NULL,
			entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			raw_json JSONB NOT NULL DEFAULT '{}',
			source TEXT NOT NULL DEFAULT ''
		);

		CREATE INDEX IF NOT EXISTS observations_campaign_idx ON observations (campaign_id, observed_at);

		CREATE TABLE IF NOT EXISTS edges (
			id TEXT PRIMARY KEY,
			campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
			from_entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			relation TEXT NOT NULL,
			to_entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			confidence REAL NOT NULL DEFAULT 1.0,
			first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			evidence_observation_ids JSONB NOT NULL DEFAULT '[]',
			UNIQUE (campaign_id, from_entity_id, relation, to_entity_id)
		);

		CREATE INDEX IF NOT EXISTS edges_campaign_idx ON edges (campaign_id);

		CREATE TABLE IF NOT EXISTS crawl_jobs (
			id TEXT PRIMARY KEY,
			campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
			entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			collector TEXT NOT NULL,
			depth INT NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'queued',
			attempts INT NOT NULL DEFAULT 0,
			error TEXT,
			queued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			started_at TIMESTAMPTZ,
			completed_at TIMESTAMPTZ,
			UNIQUE (campaign_id, entity_id, collector)
		);

		CREATE INDEX IF NOT EXISTS crawl_jobs_campaign_status_idx ON crawl_jobs (campaign_id, status);

		CREATE TABLE IF NOT EXISTS campaign_events (
			id BIGSERIAL PRIMARY KEY,
			campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
			event_type TEXT NOT NULL,
			message TEXT NOT NULL,
			payload JSONB NOT NULL DEFAULT '{}',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS campaign_events_campaign_idx ON campaign_events (campaign_id, id);

		CREATE TABLE IF NOT EXISTS expansion_suggestions (
			id TEXT PRIMARY KEY,
			campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
			entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			depth INT NOT NULL,
			reason TEXT NOT NULL,
			suggested_collectors JSONB NOT NULL DEFAULT '[]',
			status TEXT NOT NULL DEFAULT 'pending',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (campaign_id, entity_id)
		);

		ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS seeds JSONB NOT NULL DEFAULT '[]';

		-- Intelligence graph schema (global, cross-campaign)
		CREATE TABLE IF NOT EXISTS ct_logs (
			id TEXT PRIMARY KEY,
			url TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT 'active',
			last_tree_size BIGINT NOT NULL DEFAULT 0,
			last_fetched_index BIGINT NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE TABLE IF NOT EXISTS certificates (
			id TEXT PRIMARY KEY,
			fingerprint_sha256 TEXT NOT NULL UNIQUE,
			subject_cn TEXT,
			issuer TEXT,
			not_before TIMESTAMPTZ,
			not_after TIMESTAMPTZ,
			raw_der BYTEA,
			first_seen TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			source_log_id TEXT REFERENCES ct_logs(id) ON DELETE SET NULL
		);

		CREATE INDEX IF NOT EXISTS certificates_first_seen_idx ON certificates (first_seen DESC);

		CREATE TABLE IF NOT EXISTS certificate_names (
			id TEXT PRIMARY KEY,
			certificate_id TEXT NOT NULL REFERENCES certificates(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			registered_domain TEXT NOT NULL,
			is_wildcard BOOLEAN NOT NULL DEFAULT FALSE,
			first_seen TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (certificate_id, name)
		);

		CREATE INDEX IF NOT EXISTS certificate_names_domain_idx ON certificate_names (registered_domain);
		CREATE INDEX IF NOT EXISTS certificate_names_name_idx ON certificate_names (name);

		CREATE TABLE IF NOT EXISTS domains (
			id TEXT PRIMARY KEY,
			domain TEXT NOT NULL UNIQUE,
			first_seen TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE TABLE IF NOT EXISTS rdap_records (
			id TEXT PRIMARY KEY,
			domain_id TEXT NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
			registrar TEXT,
			registry TEXT,
			created_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ,
			expires_at TIMESTAMPTZ,
			statuses JSONB NOT NULL DEFAULT '[]',
			nameservers JSONB NOT NULL DEFAULT '[]',
			entities JSONB NOT NULL DEFAULT '[]',
			redacted BOOLEAN NOT NULL DEFAULT FALSE,
			raw_json JSONB NOT NULL DEFAULT '{}',
			fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS rdap_records_domain_idx ON rdap_records (domain_id, fetched_at DESC);
		CREATE INDEX IF NOT EXISTS rdap_records_registrar_idx ON rdap_records (registrar);

		CREATE TABLE IF NOT EXISTS dns_records (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			record_type TEXT NOT NULL,
			value TEXT NOT NULL,
			ttl INT,
			first_seen TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (name, record_type, value)
		);

		CREATE INDEX IF NOT EXISTS dns_records_name_idx ON dns_records (name);
		CREATE INDEX IF NOT EXISTS dns_records_value_idx ON dns_records (record_type, value);

		CREATE TABLE IF NOT EXISTS hosts (
			id TEXT PRIMARY KEY,
			hostname TEXT NOT NULL UNIQUE,
			registered_domain TEXT NOT NULL,
			first_seen TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS hosts_registered_domain_idx ON hosts (registered_domain);

		CREATE TABLE IF NOT EXISTS http_fingerprints (
			id TEXT PRIMARY KEY,
			host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
			scheme TEXT NOT NULL DEFAULT 'https',
			status_code INT,
			title TEXT,
			server_header TEXT,
			headers JSONB NOT NULL DEFAULT '{}',
			favicon_hash TEXT,
			technologies JSONB NOT NULL DEFAULT '[]',
			tracker_ids JSONB NOT NULL DEFAULT '[]',
			final_url TEXT,
			fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS http_fingerprints_host_idx ON http_fingerprints (host_id, fetched_at DESC);
		CREATE INDEX IF NOT EXISTS http_fingerprints_favicon_idx ON http_fingerprints (favicon_hash);

		CREATE TABLE IF NOT EXISTS graph_edges (
			id TEXT PRIMARY KEY,
			source_type TEXT NOT NULL,
			source_id TEXT NOT NULL,
			relationship TEXT NOT NULL,
			target_type TEXT NOT NULL,
			target_id TEXT NOT NULL,
			confidence REAL NOT NULL DEFAULT 1.0,
			first_seen TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			source TEXT NOT NULL DEFAULT '',
			UNIQUE (source_type, source_id, relationship, target_type, target_id)
		);

		CREATE INDEX IF NOT EXISTS graph_edges_source_idx ON graph_edges (source_type, source_id);
		CREATE INDEX IF NOT EXISTS graph_edges_target_idx ON graph_edges (target_type, target_id);
		CREATE INDEX IF NOT EXISTS graph_edges_relationship_idx ON graph_edges (relationship);

		CREATE TABLE IF NOT EXISTS ingestor_config (
			key TEXT PRIMARY KEY,
			value JSONB NOT NULL DEFAULT '{}',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		INSERT INTO ingestor_config (key, value) VALUES ('ct', '{
			"target_tlds": ["com", "net", "org", "io", "co.uk", "com.au"],
			"backfill_mode": true,
			"include_readonly": true,
			"batches_per_cycle": 20,
			"batch_size": 512
		}'::jsonb) ON CONFLICT (key) DO NOTHING;
	`)
	return err
}

func (s *Store) InsertCampaign(ctx context.Context, c CampaignRecord) error {
	collectorsJSON, _ := json.Marshal(c.Collectors)
	seedsJSON, _ := json.Marshal(c.Seeds)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO campaigns (id, status, depth, max_depth, max_entities, collectors, seeds, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, c.ID, c.Status, c.Depth, c.MaxDepth, c.MaxEntities, collectorsJSON, seedsJSON, c.CreatedAt, c.UpdatedAt)
	return err
}

func (s *Store) UpsertEntity(ctx context.Context, entityType, value string) (string, error) {
	id := "ent_" + uuid.New().String()
	now := time.Now()
	err := s.pool.QueryRow(ctx, `
		INSERT INTO entities (id, type, value, first_seen_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $4)
		ON CONFLICT (type, value) DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at
		RETURNING id
	`, id, entityType, value, now).Scan(&id)
	return id, err
}

func (s *Store) LinkCampaignEntity(ctx context.Context, campaignID, entityID string, depth int) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO campaign_entities (campaign_id, entity_id, depth)
		VALUES ($1, $2, $3)
		ON CONFLICT (campaign_id, entity_id) DO UPDATE SET depth = LEAST(campaign_entities.depth, EXCLUDED.depth)
	`, campaignID, entityID, depth)
	return err
}

func (s *Store) InsertCrawlJob(ctx context.Context, campaignID, entityID, collector string, depth int) (string, error) {
	jobID := "job_" + uuid.New().String()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO crawl_jobs (id, campaign_id, entity_id, collector, depth, status, queued_at)
		VALUES ($1, $2, $3, $4, $5, 'queued', NOW())
		ON CONFLICT (campaign_id, entity_id, collector) DO NOTHING
	`, jobID, campaignID, entityID, collector, depth)
	if err != nil {
		return "", err
	}

	var existingID string
	err = s.pool.QueryRow(ctx, `
		SELECT id FROM crawl_jobs WHERE campaign_id = $1 AND entity_id = $2 AND collector = $3
	`, campaignID, entityID, collector).Scan(&existingID)
	return existingID, err
}

func (s *Store) InsertEvent(ctx context.Context, campaignID, eventType, message string, payload map[string]interface{}) error {
	payloadJSON, _ := json.Marshal(payload)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO campaign_events (campaign_id, event_type, message, payload)
		VALUES ($1, $2, $3, $4)
	`, campaignID, eventType, message, payloadJSON)
	return err
}

func (s *Store) GetCampaign(ctx context.Context, campaignID string) (*CampaignRecord, error) {
	var c CampaignRecord
	var collectorsJSON []byte
	var seedsJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, status, depth, max_depth, max_entities, collectors, seeds, created_at, updated_at
		FROM campaigns WHERE id = $1
	`, campaignID).Scan(&c.ID, &c.Status, &c.Depth, &c.MaxDepth, &c.MaxEntities, &collectorsJSON, &seedsJSON, &c.CreatedAt, &c.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(collectorsJSON, &c.Collectors)
	_ = json.Unmarshal(seedsJSON, &c.Seeds)
	return &c, nil
}

func (s *Store) ListCampaigns(ctx context.Context, filter CampaignListFilter) ([]CampaignRecord, int, error) {
	args := []interface{}{}
	conditions := []string{}

	if len(filter.Statuses) > 0 {
		args = append(args, filter.Statuses)
		conditions = append(conditions, fmt.Sprintf("status = ANY($%d)", len(args)))
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	countQuery := "SELECT COUNT(*)::int FROM campaigns " + where
	var total int
	if err := s.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	args = append(args, limit, offset)
	limitArg := len(args) - 1
	offsetArg := len(args)

	query := fmt.Sprintf(`
		SELECT id, status, depth, max_depth, max_entities, collectors, seeds, created_at, updated_at
		FROM campaigns
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, limitArg, offsetArg)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	campaigns := []CampaignRecord{}
	for rows.Next() {
		var c CampaignRecord
		var collectorsJSON []byte
		var seedsJSON []byte
		if err := rows.Scan(
			&c.ID, &c.Status, &c.Depth, &c.MaxDepth, &c.MaxEntities,
			&collectorsJSON, &seedsJSON, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}
		_ = json.Unmarshal(collectorsJSON, &c.Collectors)
		_ = json.Unmarshal(seedsJSON, &c.Seeds)
		campaigns = append(campaigns, c)
	}

	return campaigns, total, rows.Err()
}

func (s *Store) UpdateCampaignStatus(ctx context.Context, campaignID, status string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE campaigns SET status = $2, updated_at = NOW() WHERE id = $1
	`, campaignID, status)
	return err
}

func (s *Store) GetProgress(ctx context.Context, campaignID string) (*ProgressResponse, error) {
	campaign, err := s.GetCampaign(ctx, campaignID)
	if err != nil || campaign == nil {
		return nil, err
	}

	p := &ProgressResponse{
		CampaignID:  campaignID,
		Status:      campaign.Status,
		MaxEntities: campaign.MaxEntities,
		MaxDepth:    campaign.MaxDepth,
	}

	err = s.pool.QueryRow(ctx, `
		SELECT
			COUNT(*)::int,
			COUNT(*) FILTER (WHERE status = 'queued')::int,
			COUNT(*) FILTER (WHERE status = 'running')::int,
			COUNT(*) FILTER (WHERE status = 'completed')::int,
			COUNT(*) FILTER (WHERE status = 'failed')::int
		FROM crawl_jobs WHERE campaign_id = $1
	`, campaignID).Scan(&p.TotalJobs, &p.QueuedJobs, &p.RunningJobs, &p.CompletedJobs, &p.FailedJobs)
	if err != nil {
		return nil, err
	}

	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM campaign_entities WHERE campaign_id = $1`, campaignID).Scan(&p.EntityCount)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM edges WHERE campaign_id = $1`, campaignID).Scan(&p.EdgeCount)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM observations WHERE campaign_id = $1`, campaignID).Scan(&p.ObservationCount)
	_ = s.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM expansion_suggestions
		WHERE campaign_id = $1 AND status = 'pending'
	`, campaignID).Scan(&p.ExpansionSuggestions)

	if p.FailedJobs > 0 {
		p.Errors, err = s.ListJobErrors(ctx, campaignID)
		if err != nil {
			return nil, err
		}
	}

	return p, nil
}

func (s *Store) ListJobErrors(ctx context.Context, campaignID string) ([]CampaignJobError, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT j.id, j.collector, j.entity_id, e.type, e.value, COALESCE(j.error, ''), j.completed_at
		FROM crawl_jobs j
		JOIN entities e ON e.id = j.entity_id
		WHERE j.campaign_id = $1 AND j.status = 'failed'
		ORDER BY j.completed_at ASC NULLS LAST, j.collector ASC
	`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanJobErrors(rows)
}

func (s *Store) ListJobErrorsForCampaigns(ctx context.Context, campaignIDs []string) (map[string][]CampaignJobError, error) {
	out := map[string][]CampaignJobError{}
	if len(campaignIDs) == 0 {
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT j.campaign_id, j.id, j.collector, j.entity_id, e.type, e.value, COALESCE(j.error, ''), j.completed_at
		FROM crawl_jobs j
		JOIN entities e ON e.id = j.entity_id
		WHERE j.campaign_id = ANY($1) AND j.status = 'failed'
		ORDER BY j.campaign_id ASC, j.completed_at ASC NULLS LAST, j.collector ASC
	`, campaignIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var campaignID string
		var item CampaignJobError
		var completedAt *time.Time
		if err := rows.Scan(
			&campaignID,
			&item.JobID,
			&item.Collector,
			&item.EntityID,
			&item.EntityType,
			&item.EntityValue,
			&item.Error,
			&completedAt,
		); err != nil {
			return nil, err
		}
		item.CompletedAt = completedAt
		out[campaignID] = append(out[campaignID], item)
	}

	return out, rows.Err()
}

func scanJobErrors(rows pgx.Rows) ([]CampaignJobError, error) {
	out := []CampaignJobError{}
	for rows.Next() {
		var item CampaignJobError
		var completedAt *time.Time
		if err := rows.Scan(
			&item.JobID,
			&item.Collector,
			&item.EntityID,
			&item.EntityType,
			&item.EntityValue,
			&item.Error,
			&completedAt,
		); err != nil {
			return nil, err
		}
		item.CompletedAt = completedAt
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListEvents(ctx context.Context, campaignID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT event_type, message, payload, created_at
		FROM campaign_events
		WHERE campaign_id = $1
		ORDER BY id ASC
	`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	lines := []string{}
	for rows.Next() {
		var eventType, message string
		var payloadJSON []byte
		var createdAt time.Time
		if err := rows.Scan(&eventType, &message, &payloadJSON, &createdAt); err != nil {
			return nil, err
		}
		var payload map[string]interface{}
		_ = json.Unmarshal(payloadJSON, &payload)
		line, _ := json.Marshal(map[string]interface{}{
			"event_type": eventType,
			"message":    message,
			"payload":    payload,
			"created_at": createdAt.Format(time.RFC3339),
		})
		lines = append(lines, string(line))
	}
	return lines, rows.Err()
}

func (s *Store) ListEntities(ctx context.Context, campaignID string) ([]EntityRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.type, e.value, ce.depth, e.first_seen_at, e.last_seen_at
		FROM entities e
		JOIN campaign_entities ce ON ce.entity_id = e.id
		WHERE ce.campaign_id = $1
		ORDER BY ce.depth ASC, e.type ASC, e.value ASC
	`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []EntityRecord{}
	for rows.Next() {
		var e EntityRecord
		if err := rows.Scan(&e.ID, &e.Type, &e.Value, &e.Depth, &e.FirstSeenAt, &e.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ListEdges(ctx context.Context, campaignID string) ([]EdgeRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ed.id, ed.from_entity_id, fe.type, fe.value, ed.relation,
		       ed.to_entity_id, te.type, te.value, ed.confidence,
		       ed.first_seen_at, ed.last_seen_at, ed.evidence_observation_ids
		FROM edges ed
		JOIN entities fe ON fe.id = ed.from_entity_id
		JOIN entities te ON te.id = ed.to_entity_id
		WHERE ed.campaign_id = $1
		ORDER BY ed.relation ASC, fe.value ASC
	`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []EdgeRecord{}
	for rows.Next() {
		var e EdgeRecord
		var evidenceJSON []byte
		if err := rows.Scan(
			&e.ID, &e.FromEntityID, &e.FromType, &e.FromValue, &e.Relation,
			&e.ToEntityID, &e.ToType, &e.ToValue, &e.Confidence,
			&e.FirstSeenAt, &e.LastSeenAt, &evidenceJSON,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(evidenceJSON, &e.EvidenceObservationIDs)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ListExpansionSuggestions(ctx context.Context, campaignID string) ([]ExpansionSuggestion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT es.entity_id, e.type, e.value, es.depth, es.reason, es.suggested_collectors
		FROM expansion_suggestions es
		JOIN entities e ON e.id = es.entity_id
		WHERE es.campaign_id = $1 AND es.status = 'pending'
		ORDER BY es.depth ASC, e.value ASC
	`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ExpansionSuggestion{}
	for rows.Next() {
		var s ExpansionSuggestion
		var collectorsJSON []byte
		if err := rows.Scan(&s.EntityID, &s.EntityType, &s.EntityValue, &s.Depth, &s.Reason, &collectorsJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(collectorsJSON, &s.Collectors)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (s *Store) ListAllExpansionSuggestions(ctx context.Context, campaignID string) ([]ExpansionSuggestion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT es.entity_id, e.type, e.value, es.depth, es.reason, es.suggested_collectors, es.status
		FROM expansion_suggestions es
		JOIN entities e ON e.id = es.entity_id
		WHERE es.campaign_id = $1
		ORDER BY es.depth ASC, es.status ASC, e.value ASC
	`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ExpansionSuggestion{}
	for rows.Next() {
		var item ExpansionSuggestion
		var collectorsJSON []byte
		if err := rows.Scan(
			&item.EntityID, &item.EntityType, &item.EntityValue,
			&item.Depth, &item.Reason, &collectorsJSON, &item.Status,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(collectorsJSON, &item.Collectors)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ResolveCampaignSeeds(ctx context.Context, campaign *CampaignRecord) ([]string, error) {
	if len(campaign.Seeds) > 0 {
		return campaign.Seeds, nil
	}

	var payloadJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT payload
		FROM campaign_events
		WHERE campaign_id = $1 AND event_type = 'campaign.created'
		ORDER BY id ASC
		LIMIT 1
	`, campaign.ID).Scan(&payloadJSON)
	if err == nil {
		var payload struct {
			Seeds []string `json:"seeds"`
		}
		if json.Unmarshal(payloadJSON, &payload) == nil && len(payload.Seeds) > 0 {
			return payload.Seeds, nil
		}
	}

	rows, err := s.pool.Query(ctx, `
		SELECT e.value
		FROM entities e
		JOIN campaign_entities ce ON ce.entity_id = e.id
		WHERE ce.campaign_id = $1 AND ce.depth = 0
		ORDER BY e.value ASC
	`, campaign.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seeds := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		seeds = append(seeds, value)
	}
	return seeds, rows.Err()
}

func (s *Store) BuildCampaignReport(ctx context.Context, campaignID string) (*CampaignReport, error) {
	campaign, err := s.GetCampaign(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	if campaign == nil {
		return nil, nil
	}

	seeds, err := s.ResolveCampaignSeeds(ctx, campaign)
	if err != nil {
		return nil, err
	}

	entities, err := s.ListEntities(ctx, campaignID)
	if err != nil {
		return nil, err
	}

	edges, err := s.ListEdges(ctx, campaignID)
	if err != nil {
		return nil, err
	}

	expansions, err := s.ListAllExpansionSuggestions(ctx, campaignID)
	if err != nil {
		return nil, err
	}

	progress, err := s.GetProgress(ctx, campaignID)
	if err != nil {
		return nil, err
	}

	pendingExpansions := 0
	approvedExpansions := 0
	for _, expansion := range expansions {
		switch expansion.Status {
		case "approved":
			approvedExpansions++
		default:
			pendingExpansions++
		}
	}

	return &CampaignReport{
		CampaignID: campaign.ID,
		Status:     campaign.Status,
		Collectors: campaign.Collectors,
		Limits: CampaignLimits{
			MaxEntities: campaign.MaxEntities,
			MaxDepth:    campaign.MaxDepth,
		},
		CreatedAt:  campaign.CreatedAt,
		UpdatedAt:  campaign.UpdatedAt,
		Seeds:      seeds,
		Entities:   entities,
		Edges:      edges,
		Expansions: expansions,
		Errors:     progress.Errors,
		Summary: CampaignReportSummary{
			SeedCount:          len(seeds),
			EntityCount:        len(entities),
			EdgeCount:          len(edges),
			ExpansionCount:     len(expansions),
			PendingExpansions:  pendingExpansions,
			ApprovedExpansions: approvedExpansions,
			ObservationCount:   progress.ObservationCount,
			CompletedJobs:      progress.CompletedJobs,
			FailedJobs:         progress.FailedJobs,
		},
	}, nil
}

func (s *Store) GetEntity(ctx context.Context, entityID string) (entityType, value string, err error) {
	err = s.pool.QueryRow(ctx, `SELECT type, value FROM entities WHERE id = $1`, entityID).Scan(&entityType, &value)
	return
}

func (s *Store) EntityCount(ctx context.Context, campaignID string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM campaign_entities WHERE campaign_id = $1`, campaignID).Scan(&count)
	return count, err
}

func (s *Store) ReconcileStaleJobs(ctx context.Context, staleAfter time.Duration) error {
	if staleAfter <= 0 {
		staleAfter = 15 * time.Minute
	}

	_, err := s.pool.Exec(ctx, `
		UPDATE crawl_jobs
		SET status = 'failed',
		    completed_at = NOW(),
		    error = 'job timed out (worker did not complete)'
		WHERE status = 'running'
		  AND started_at IS NOT NULL
		  AND started_at < NOW() - $1::interval
	`, fmt.Sprintf("%f seconds", staleAfter.Seconds()))
	return err
}

func (s *Store) ReconcileCampaignStatus(ctx context.Context, campaignID string) error {
	campaign, err := s.GetCampaign(ctx, campaignID)
	if err != nil || campaign == nil {
		return err
	}
	if campaign.Status == StatusCancelled {
		return nil
	}

	var queued, running, failed int
	err = s.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status = 'queued')::int,
			COUNT(*) FILTER (WHERE status = 'running')::int,
			COUNT(*) FILTER (WHERE status = 'failed')::int
		FROM crawl_jobs WHERE campaign_id = $1
	`, campaignID).Scan(&queued, &running, &failed)
	if err != nil {
		return err
	}

	newStatus := campaign.Status
	switch {
	case running > 0 || queued > 0:
		newStatus = StatusRunning
	case failed > 0:
		newStatus = StatusCompletedWithErrors
	default:
		newStatus = StatusCompleted
	}

	if newStatus != campaign.Status {
		return s.UpdateCampaignStatus(ctx, campaignID, newStatus)
	}
	return nil
}

func (s *Store) MarkExpansionApproved(ctx context.Context, campaignID string, entityIDs []string) error {
	if len(entityIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE expansion_suggestions SET status = 'approved'
		WHERE campaign_id = $1 AND entity_id = ANY($2)
	`, campaignID, entityIDs)
	return err
}

func waitForPostgres(ctx context.Context, databaseURL string) (*Store, error) {
	var store *Store
	var err error
	for i := 0; i < 30; i++ {
		store, err = NewStore(ctx, databaseURL)
		if err == nil {
			return store, nil
		}
		time.Sleep(1 * time.Second)
	}
	return nil, fmt.Errorf("failed to connect to postgres: %w", err)
}
