package main

import (
	"encoding/json"
	"time"
)

const (
	StatusQueued              = "queued"
	StatusRunning             = "running"
	StatusExpanding           = "expanding"
	StatusWaitingRateLimit    = "waiting_for_rate_limit"
	StatusCompleted           = "completed"
	StatusCompletedWithErrors = "completed_with_errors"
	StatusCancelled           = "cancelled"

	JobQueued    = "queued"
	JobRunning   = "running"
	JobCompleted = "completed"
	JobFailed    = "failed"
)

var ValidCollectors = map[string]bool{
	"dns":  true,
	"http": true,
	"tls":  true,
	"ct":   true,
	"rdap": true,
}

var MVPCollectors = map[string]bool{
	"dns":  true,
	"http": true,
	"tls":  true,
	"ct":   true,
	"rdap": true,
}

type CampaignLimits struct {
	MaxEntities int `json:"max_entities"`
	MaxDepth    int `json:"max_depth"`
}

type CreateCampaignRequest struct {
	Seeds      []string       `json:"seeds"`
	Collectors []string       `json:"collectors"`
	Depth      int            `json:"depth"`
	Limits     CampaignLimits `json:"limits"`
}

type ExpandCampaignRequest struct {
	EntityIDs  []string `json:"entity_ids"`
	Collectors []string `json:"collectors,omitempty"`
}

type CampaignRecord struct {
	ID          string
	Status      string
	Depth       int
	MaxDepth    int
	MaxEntities int
	Collectors  []string
	Seeds       []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CrawlJobMessage struct {
	JobID       string `json:"job_id"`
	CampaignID  string `json:"campaign_id"`
	EntityID    string `json:"entity_id"`
	EntityType  string `json:"entity_type"`
	EntityValue string `json:"entity_value"`
	Collector   string `json:"collector"`
	Depth       int    `json:"depth"`
}

type EntityRecord struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Value       string          `json:"value"`
	Depth       int             `json:"depth,omitempty"`
	FirstSeenAt time.Time       `json:"first_seen_at"`
	LastSeenAt  time.Time       `json:"last_seen_at"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type EdgeRecord struct {
	ID                    string    `json:"id"`
	FromEntityID          string    `json:"from_entity_id"`
	FromValue             string    `json:"from_value,omitempty"`
	FromType              string    `json:"from_type,omitempty"`
	Relation              string    `json:"relation"`
	ToEntityID            string    `json:"to_entity_id"`
	ToValue               string    `json:"to_value,omitempty"`
	ToType                string    `json:"to_type,omitempty"`
	Confidence            float64   `json:"confidence"`
	FirstSeenAt           time.Time `json:"first_seen_at"`
	LastSeenAt            time.Time `json:"last_seen_at"`
	EvidenceObservationIDs []string `json:"evidence_observation_ids,omitempty"`
}

type ProgressResponse struct {
	CampaignID           string             `json:"campaign_id"`
	Status               string             `json:"status"`
	TotalJobs            int                `json:"total_jobs"`
	QueuedJobs           int                `json:"queued_jobs"`
	RunningJobs          int                `json:"running_jobs"`
	CompletedJobs        int                `json:"completed_jobs"`
	FailedJobs           int                `json:"failed_jobs"`
	EntityCount          int                `json:"entity_count"`
	EdgeCount            int                `json:"edge_count"`
	ObservationCount     int                `json:"observation_count"`
	ExpansionSuggestions int                `json:"expansion_suggestions"`
	MaxEntities          int                `json:"max_entities"`
	MaxDepth             int                `json:"max_depth"`
	Errors               []CampaignJobError `json:"errors,omitempty"`
}

type CampaignJobError struct {
	JobID       string     `json:"job_id"`
	Collector   string     `json:"collector"`
	EntityID    string     `json:"entity_id"`
	EntityType  string     `json:"entity_type"`
	EntityValue string     `json:"entity_value"`
	Error       string     `json:"error"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type ExpansionSuggestion struct {
	EntityID    string   `json:"entity_id"`
	EntityType  string   `json:"entity_type"`
	EntityValue string   `json:"entity_value"`
	Depth       int      `json:"depth"`
	Reason      string   `json:"reason"`
	Collectors  []string `json:"suggested_collectors"`
	Status      string   `json:"status,omitempty"`
}

type CampaignReport struct {
	CampaignID string              `json:"campaign_id"`
	Status     string              `json:"status"`
	Collectors []string            `json:"collectors"`
	Limits     CampaignLimits      `json:"limits"`
	CreatedAt  time.Time           `json:"created_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
	Seeds      []string            `json:"seeds"`
	Entities   []EntityRecord      `json:"entities"`
	Edges      []EdgeRecord        `json:"edges"`
	Expansions []ExpansionSuggestion `json:"expansions"`
	Errors     []CampaignJobError  `json:"errors,omitempty"`
	Summary    CampaignReportSummary `json:"summary"`
}

type CampaignReportSummary struct {
	SeedCount           int `json:"seed_count"`
	EntityCount         int `json:"entity_count"`
	EdgeCount           int `json:"edge_count"`
	ExpansionCount      int `json:"expansion_count"`
	PendingExpansions   int `json:"pending_expansions"`
	ApprovedExpansions  int `json:"approved_expansions"`
	ObservationCount    int `json:"observation_count"`
	CompletedJobs       int `json:"completed_jobs"`
	FailedJobs          int `json:"failed_jobs"`
}

type CampaignSummary struct {
	ID          string             `json:"id"`
	Status      string             `json:"status"`
	Collectors  []string           `json:"collectors"`
	MaxDepth    int                `json:"max_depth"`
	MaxEntities int                `json:"max_entities"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	Errors      []CampaignJobError `json:"errors,omitempty"`
}

type CampaignListFilter struct {
	Statuses []string
	Limit    int
	Offset   int
}

var ValidCampaignStatuses = map[string]bool{
	StatusQueued:              true,
	StatusRunning:             true,
	StatusExpanding:           true,
	StatusWaitingRateLimit:    true,
	StatusCompleted:           true,
	StatusCompletedWithErrors: true,
	StatusCancelled:           true,
}

// StatusFilterAliases expand shorthand filter tokens into canonical statuses.
var StatusFilterAliases = map[string][]string{
	"failed": {StatusCompletedWithErrors},
	"active": {StatusQueued, StatusRunning, StatusExpanding, StatusWaitingRateLimit},
	"done":   {StatusCompleted, StatusCompletedWithErrors, StatusCancelled},
}
