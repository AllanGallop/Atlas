package main

import (
	"strings"
	"testing"
	"time"
)

func TestRenderPrometheusMetrics(t *testing.T) {
	snap := &MetricsSnapshot{
		CollectedAt: time.Unix(1700000000, 0).UTC(),
		Campaigns:   map[string]int{"running": 2, "total": 2},
		Jobs: JobMetrics{
			ByStatus: map[string]int{"completed": 10, "failed": 1},
			ByCollector: map[string]CollectorJobs{
				"dns": {Completed: 5, Failed: 1, Total: 6},
			},
			Total: 11,
		},
		Intelligence: IntelligenceMetrics{Domains: 42, GraphEdges: 100},
		CT: CTMetrics{
			LogsActive:           3,
			EntriesFetched:       1000,
			EntriesAvailable:     5000,
			AggregateProgressPct: 20,
		},
		Infra: InfraMetrics{NATS: "connected", Redis: "connected"},
	}

	out := renderPrometheusMetrics(snap)
	if !strings.Contains(out, "# HELP atlas_campaigns") {
		t.Fatal("missing campaign metric family")
	}
	if !strings.Contains(out, `atlas_campaigns{status="running"} 2`) {
		t.Fatal("missing campaign sample")
	}
	if !strings.Contains(out, `atlas_infra_up{component="nats"} 1`) {
		t.Fatal("missing infra sample")
	}
	if !strings.Contains(out, "atlas_ct_aggregate_progress_pct 20") {
		t.Fatal("missing ct progress")
	}
}

func TestEscapePromLabel(t *testing.T) {
	got := escapePromLabel(`say "hello"\`)
	want := `say \"hello\"\\`
	if got != want {
		t.Fatalf("escapePromLabel = %q, want %q", got, want)
	}
}

func TestInfraUp(t *testing.T) {
	if infraUp("connected") != 1 {
		t.Fatal("expected connected = 1")
	}
	if infraUp("disconnected") != 0 {
		t.Fatal("expected disconnected = 0")
	}
}
