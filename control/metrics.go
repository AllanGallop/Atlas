package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type MetricsSnapshot struct {
	CollectedAt time.Time         `json:"collected_at"`
	Campaigns   map[string]int    `json:"campaigns"`
	Jobs        JobMetrics        `json:"jobs"`
	Intelligence IntelligenceMetrics `json:"intelligence"`
	CT          CTMetrics         `json:"ct"`
	Infra       InfraMetrics      `json:"infra"`
}

type JobMetrics struct {
	ByStatus   map[string]int            `json:"by_status"`
	ByCollector map[string]CollectorJobs `json:"by_collector"`
	Total      int                       `json:"total"`
}

type CollectorJobs struct {
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Total     int `json:"total"`
}

type IntelligenceMetrics struct {
	Domains          int `json:"domains"`
	Hosts            int `json:"hosts"`
	Certificates     int `json:"certificates"`
	CertificateNames int `json:"certificate_names"`
	GraphEdges       int `json:"graph_edges"`
	DNSRecords       int `json:"dns_records"`
	RDAPRecords      int `json:"rdap_records"`
	HTTPFingerprints int `json:"http_fingerprints"`
	Entities         int `json:"entities"`
	CampaignEdges    int `json:"campaign_edges"`
	Observations     int `json:"observations"`
}

type CTMetrics struct {
	LogsTotal          int     `json:"logs_total"`
	LogsActive         int     `json:"logs_active"`
	LogsReadonly       int     `json:"logs_readonly"`
	EntriesFetched     int64   `json:"entries_fetched"`
	EntriesAvailable   int64   `json:"entries_available"`
	AggregateProgressPct float64 `json:"aggregate_progress_pct"`
	Certificates       int     `json:"certificates"`
	CertificateNames   int     `json:"certificate_names"`
	Domains            int     `json:"domains"`
}

type InfraMetrics struct {
	NATS  string `json:"nats"`
	Redis string `json:"redis"`
}

func (s *Store) CollectMetrics(ctx context.Context) (*MetricsSnapshot, error) {
	snap := &MetricsSnapshot{
		CollectedAt: time.Now().UTC(),
		Campaigns:   map[string]int{},
	}

	if err := s.collectCampaignMetrics(ctx, snap); err != nil {
		return nil, err
	}
	if err := s.collectJobMetrics(ctx, snap); err != nil {
		return nil, err
	}
	if err := s.collectIntelligenceMetrics(ctx, snap); err != nil {
		return nil, err
	}
	if err := s.collectCTMetrics(ctx, snap); err != nil {
		return nil, err
	}

	return snap, nil
}

func (s *Store) collectCampaignMetrics(ctx context.Context, snap *MetricsSnapshot) error {
	rows, err := s.pool.Query(ctx, `
		SELECT status, COUNT(*)::int FROM campaigns GROUP BY status
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	total := 0
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return err
		}
		snap.Campaigns[status] = count
		total += count
	}
	snap.Campaigns["total"] = total
	return rows.Err()
}

func (s *Store) collectJobMetrics(ctx context.Context, snap *MetricsSnapshot) error {
	snap.Jobs = JobMetrics{
		ByStatus:    map[string]int{},
		ByCollector: map[string]CollectorJobs{},
	}

	rows, err := s.pool.Query(ctx, `
		SELECT status, COUNT(*)::int FROM crawl_jobs GROUP BY status
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return err
		}
		snap.Jobs.ByStatus[status] = count
		snap.Jobs.Total += count
	}
	if err := rows.Err(); err != nil {
		return err
	}

	collectorRows, err := s.pool.Query(ctx, `
		SELECT collector, status, COUNT(*)::int
		FROM crawl_jobs
		GROUP BY collector, status
		ORDER BY collector ASC
	`)
	if err != nil {
		return err
	}
	defer collectorRows.Close()

	for collectorRows.Next() {
		var collector, status string
		var count int
		if err := collectorRows.Scan(&collector, &status, &count); err != nil {
			return err
		}
		cj := snap.Jobs.ByCollector[collector]
		switch status {
		case JobQueued:
			cj.Queued = count
		case JobRunning:
			cj.Running = count
		case JobCompleted:
			cj.Completed = count
		case JobFailed:
			cj.Failed = count
		}
		cj.Total += count
		snap.Jobs.ByCollector[collector] = cj
	}
	return collectorRows.Err()
}

func (s *Store) collectIntelligenceMetrics(ctx context.Context, snap *MetricsSnapshot) error {
	m := &snap.Intelligence
	queries := []struct {
		dest *int
		sql  string
	}{
		{&m.Domains, `SELECT COUNT(*)::int FROM domains`},
		{&m.Hosts, `SELECT COUNT(*)::int FROM hosts`},
		{&m.Certificates, `SELECT COUNT(*)::int FROM certificates`},
		{&m.CertificateNames, `SELECT COUNT(*)::int FROM certificate_names`},
		{&m.GraphEdges, `SELECT COUNT(*)::int FROM graph_edges`},
		{&m.DNSRecords, `SELECT COUNT(*)::int FROM dns_records`},
		{&m.RDAPRecords, `SELECT COUNT(*)::int FROM rdap_records`},
		{&m.HTTPFingerprints, `SELECT COUNT(*)::int FROM http_fingerprints`},
		{&m.Entities, `SELECT COUNT(*)::int FROM entities`},
		{&m.CampaignEdges, `SELECT COUNT(*)::int FROM edges`},
		{&m.Observations, `SELECT COUNT(*)::int FROM observations`},
	}

	for _, q := range queries {
		if err := s.pool.QueryRow(ctx, q.sql).Scan(q.dest); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) collectCTMetrics(ctx context.Context, snap *MetricsSnapshot) error {
	m := &snap.CT

	rows, err := s.pool.Query(ctx, `
		SELECT state, COUNT(*)::int,
		       COALESCE(SUM(last_fetched_index), 0)::bigint,
		       COALESCE(SUM(last_tree_size), 0)::bigint
		FROM ct_logs
		GROUP BY state
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var state string
		var count int
		var fetched, available int64
		if err := rows.Scan(&state, &count, &fetched, &available); err != nil {
			return err
		}
		m.LogsTotal += count
		m.EntriesFetched += fetched
		m.EntriesAvailable += available
		switch state {
		case "active":
			m.LogsActive = count
		case "readonly":
			m.LogsReadonly = count
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if m.EntriesAvailable > 0 {
		m.AggregateProgressPct = float64(m.EntriesFetched) / float64(m.EntriesAvailable) * 100
	}

	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM certificates`).Scan(&m.Certificates)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM certificate_names`).Scan(&m.CertificateNames)
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM domains`).Scan(&m.Domains)

	return nil
}

func renderPrometheusMetrics(snap *MetricsSnapshot) string {
	var b strings.Builder
	ts := snap.CollectedAt.Unix()

	writeFamily := func(name, help string, samples func()) {
		b.WriteString(fmt.Sprintf("# HELP %s %s\n", name, help))
		b.WriteString(fmt.Sprintf("# TYPE %s gauge\n", name))
		samples()
	}

	writeSample := func(name string, value float64, labels map[string]string) {
		if len(labels) == 0 {
			b.WriteString(fmt.Sprintf("%s %.0f %d\n", name, value, ts))
			return
		}
		parts := make([]string, 0, len(labels))
		for k, v := range labels {
			parts = append(parts, fmt.Sprintf(`%s="%s"`, k, escapePromLabel(v)))
		}
		b.WriteString(fmt.Sprintf("%s{%s} %.0f %d\n", name, strings.Join(parts, ","), value, ts))
	}

	writeFamily("atlas_campaigns", "Campaign count by status", func() {
		for status, count := range snap.Campaigns {
			writeSample("atlas_campaigns", float64(count), map[string]string{"status": status})
		}
	})

	writeFamily("atlas_crawl_jobs", "Crawl jobs by status", func() {
		for status, count := range snap.Jobs.ByStatus {
			writeSample("atlas_crawl_jobs", float64(count), map[string]string{"status": status})
		}
	})

	writeFamily("atlas_crawl_jobs_by_collector", "Crawl jobs by collector and status", func() {
		for collector, cj := range snap.Jobs.ByCollector {
			writeSample("atlas_crawl_jobs_by_collector", float64(cj.Queued), map[string]string{"collector": collector, "status": "queued"})
			writeSample("atlas_crawl_jobs_by_collector", float64(cj.Running), map[string]string{"collector": collector, "status": "running"})
			writeSample("atlas_crawl_jobs_by_collector", float64(cj.Completed), map[string]string{"collector": collector, "status": "completed"})
			writeSample("atlas_crawl_jobs_by_collector", float64(cj.Failed), map[string]string{"collector": collector, "status": "failed"})
		}
	})

	writeFamily("atlas_intelligence_records", "Intelligence graph record counts", func() {
		i := snap.Intelligence
		for table, count := range map[string]int{
			"domains": i.Domains, "hosts": i.Hosts, "certificates": i.Certificates,
			"certificate_names": i.CertificateNames, "graph_edges": i.GraphEdges,
			"dns_records": i.DNSRecords, "rdap_records": i.RDAPRecords,
			"http_fingerprints": i.HTTPFingerprints, "entities": i.Entities,
			"campaign_edges": i.CampaignEdges, "observations": i.Observations,
		} {
			writeSample("atlas_intelligence_records", float64(count), map[string]string{"table": table})
		}
	})

	ct := snap.CT
	writeFamily("atlas_ct_logs", "CT logs by state", func() {
		writeSample("atlas_ct_logs", float64(ct.LogsActive), map[string]string{"state": "active"})
		writeSample("atlas_ct_logs", float64(ct.LogsReadonly), map[string]string{"state": "readonly"})
	})
	writeFamily("atlas_ct_entries_fetched", "Total CT entries fetched across logs", func() {
		writeSample("atlas_ct_entries_fetched", float64(ct.EntriesFetched), nil)
	})
	writeFamily("atlas_ct_entries_available", "Total CT entries available across logs", func() {
		writeSample("atlas_ct_entries_available", float64(ct.EntriesAvailable), nil)
	})
	writeFamily("atlas_ct_aggregate_progress_pct", "Aggregate CT ingestion progress percent", func() {
		writeSample("atlas_ct_aggregate_progress_pct", ct.AggregateProgressPct, nil)
	})

	writeFamily("atlas_infra_up", "Infrastructure dependency status (1=up)", func() {
		writeSample("atlas_infra_up", infraUp(snap.Infra.NATS), map[string]string{"component": "nats"})
		writeSample("atlas_infra_up", infraUp(snap.Infra.Redis), map[string]string{"component": "redis"})
	})

	return b.String()
}

func infraUp(status string) float64 {
	if status == "connected" {
		return 1
	}
	return 0
}

func escapePromLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return value
}
