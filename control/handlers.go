package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

type Server struct {
	store *Store
	nc    *nats.Conn
	redis *redis.Client
}

func (s *Server) listCampaigns(w http.ResponseWriter, r *http.Request) {
	statuses, err := parseStatusFilters(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	limit := parseIntQuery(r, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := parseIntQuery(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}

	campaigns, total, err := s.store.ListCampaigns(r.Context(), CampaignListFilter{
		Statuses: statuses,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	summaries := make([]CampaignSummary, 0, len(campaigns))
	errorCampaignIDs := []string{}
	for _, c := range campaigns {
		summaries = append(summaries, campaignToSummary(c))
		if c.Status == StatusCompletedWithErrors {
			errorCampaignIDs = append(errorCampaignIDs, c.ID)
		}
	}

	if len(errorCampaignIDs) > 0 {
		errorsByCampaign, err := s.store.ListJobErrorsForCampaigns(r.Context(), errorCampaignIDs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for i := range summaries {
			if errs, ok := errorsByCampaign[summaries[i].ID]; ok {
				summaries[i].Errors = errs
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"campaigns": summaries,
		"count":     len(summaries),
		"total":     total,
		"limit":     limit,
		"offset":    offset,
		"filters": map[string]interface{}{
			"status": statuses,
		},
	})
}

func campaignToSummary(c CampaignRecord) CampaignSummary {
	return CampaignSummary{
		ID:          c.ID,
		Status:      c.Status,
		Collectors:  c.Collectors,
		MaxDepth:    c.MaxDepth,
		MaxEntities: c.MaxEntities,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}
}

func parseStatusFilters(r *http.Request) ([]string, error) {
	raw := r.URL.Query()["status"]
	if len(raw) == 0 {
		return nil, nil
	}

	seen := map[string]bool{}
	out := []string{}

	for _, value := range raw {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(strings.ToLower(token))
			if token == "" {
				continue
			}

			if aliases, ok := StatusFilterAliases[token]; ok {
				for _, status := range aliases {
					if !seen[status] {
						seen[status] = true
						out = append(out, status)
					}
				}
				continue
			}

			if !ValidCampaignStatuses[token] {
				return nil, fmt.Errorf("unknown status filter: %s", token)
			}
			if !seen[token] {
				seen[token] = true
				out = append(out, token)
			}
		}
	}

	return out, nil
}

func parseIntQuery(r *http.Request, key string, fallback int) int {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		return fallback
	}
	return parsed
}

func (s *Server) createCampaign(w http.ResponseWriter, r *http.Request) {
	var req CreateCampaignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Seeds) == 0 {
		http.Error(w, "at least one seed is required", http.StatusBadRequest)
		return
	}

	collectors := normalizeCollectors(req.Collectors)
	maxDepth := req.Limits.MaxDepth
	if maxDepth <= 0 {
		maxDepth = req.Depth
	}
	if maxDepth <= 0 {
		maxDepth = 2
	}
	maxEntities := req.Limits.MaxEntities
	if maxEntities <= 0 {
		maxEntities = 5000
	}

	campaignID := "cmp_" + uuid.New().String()
	now := time.Now()

	campaign := CampaignRecord{
		ID:          campaignID,
		Status:      StatusQueued,
		Depth:       0,
		MaxDepth:    maxDepth,
		MaxEntities: maxEntities,
		Collectors:  collectors,
		Seeds:       req.Seeds,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.store.InsertCampaign(r.Context(), campaign); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jobsQueued := 0
	for _, seed := range req.Seeds {
		entityType, value := classifySeed(seed)
		if value == "" {
			continue
		}

		entityID, err := s.store.UpsertEntity(r.Context(), entityType, value)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if entityType == "domain" || entityType == "subdomain" {
			if apex, ok := registrableDomainForSeed(value); ok {
				_, _ = s.store.UpsertDomain(r.Context(), apex)
			}
		}

		if err := s.store.LinkCampaignEntity(r.Context(), campaignID, entityID, 0); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		for _, collector := range collectorsForEntity(entityType, collectors) {
			if !MVPCollectors[collector] {
				continue
			}
			jobID, err := s.store.InsertCrawlJob(r.Context(), campaignID, entityID, collector, 0)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if jobID == "" {
				continue
			}

			dedupeKey := fmt.Sprintf("atlas:dedupe:%s:%s:%s", campaignID, entityID, collector)
			if s.redis != nil {
				set, err := s.redis.SetNX(r.Context(), dedupeKey, "1", 24*time.Hour).Result()
				if err == nil && !set {
					continue
				}
			}

			msg := CrawlJobMessage{
				JobID:       jobID,
				CampaignID:  campaignID,
				EntityID:    entityID,
				EntityType:  entityType,
				EntityValue: value,
				Collector:   collector,
				Depth:       0,
			}
			if err := s.publishJob(msg); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			jobsQueued++
		}
	}

	_ = s.store.InsertEvent(r.Context(), campaignID, "campaign.created", "campaign queued", map[string]interface{}{
		"seeds":      req.Seeds,
		"collectors": collectors,
		"jobs":       jobsQueued,
	})

	if jobsQueued > 0 {
		_ = s.store.UpdateCampaignStatus(r.Context(), campaignID, StatusRunning)
	} else {
		_ = s.store.UpdateCampaignStatus(r.Context(), campaignID, StatusCompleted)
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":         campaignID,
		"status":     StatusRunning,
		"collectors": collectors,
		"limits": map[string]int{
			"max_entities": maxEntities,
			"max_depth":    maxDepth,
		},
		"jobs_queued": jobsQueued,
	})
}

func (s *Server) getCampaign(w http.ResponseWriter, r *http.Request, campaignID string) {
	_ = s.store.ReconcileStaleJobs(r.Context(), 15*time.Minute)
	_ = s.store.ReconcileCampaignStatus(r.Context(), campaignID)

	campaign, err := s.store.GetCampaign(r.Context(), campaignID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if campaign == nil {
		http.NotFound(w, r)
		return
	}

	suggestions, _ := s.store.ListExpansionSuggestions(r.Context(), campaignID)

	response := map[string]interface{}{
		"id":                   campaign.ID,
		"status":               campaign.Status,
		"collectors":           campaign.Collectors,
		"max_depth":            campaign.MaxDepth,
		"max_entities":         campaign.MaxEntities,
		"created_at":           campaign.CreatedAt.Format(time.RFC3339),
		"updated_at":           campaign.UpdatedAt.Format(time.RFC3339),
		"suggested_expansions": suggestions,
	}

	if campaign.Status == StatusCompletedWithErrors {
		errors, err := s.store.ListJobErrors(r.Context(), campaignID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		response["errors"] = errors
	}

	writeJSON(w, http.StatusOK, response)
}

func (s *Server) getProgress(w http.ResponseWriter, r *http.Request, campaignID string) {
	_ = s.store.ReconcileStaleJobs(r.Context(), 15*time.Minute)
	_ = s.store.ReconcileCampaignStatus(r.Context(), campaignID)

	progress, err := s.store.GetProgress(r.Context(), campaignID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if progress == nil {
		http.NotFound(w, r)
		return
	}

	writeJSON(w, http.StatusOK, progress)
}

func (s *Server) getEvents(w http.ResponseWriter, r *http.Request, campaignID string) {
	campaign, err := s.store.GetCampaign(r.Context(), campaignID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if campaign == nil {
		http.NotFound(w, r)
		return
	}

	events, err := s.store.ListEvents(r.Context(), campaignID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	for _, line := range events {
		_, _ = w.Write([]byte(line + "\n"))
	}
}

func (s *Server) getEntities(w http.ResponseWriter, r *http.Request, campaignID string) {
	campaign, err := s.store.GetCampaign(r.Context(), campaignID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if campaign == nil {
		http.NotFound(w, r)
		return
	}

	entities, err := s.store.ListEntities(r.Context(), campaignID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"campaign_id": campaignID,
		"entities":    entities,
		"count":       len(entities),
	})
}

func (s *Server) getEdges(w http.ResponseWriter, r *http.Request, campaignID string) {
	campaign, err := s.store.GetCampaign(r.Context(), campaignID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if campaign == nil {
		http.NotFound(w, r)
		return
	}

	edges, err := s.store.ListEdges(r.Context(), campaignID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"campaign_id": campaignID,
		"edges":       edges,
		"count":       len(edges),
	})
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request, campaignID string) {
	_ = s.store.ReconcileStaleJobs(r.Context(), 15*time.Minute)
	_ = s.store.ReconcileCampaignStatus(r.Context(), campaignID)

	report, err := s.store.BuildCampaignReport(r.Context(), campaignID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if report == nil {
		http.NotFound(w, r)
		return
	}

	writeJSON(w, http.StatusOK, report)
}

func (s *Server) expandCampaign(w http.ResponseWriter, r *http.Request, campaignID string) {
	campaign, err := s.store.GetCampaign(r.Context(), campaignID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if campaign == nil {
		http.NotFound(w, r)
		return
	}

	var req ExpandCampaignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.EntityIDs) == 0 {
		http.Error(w, "entity_ids is required", http.StatusBadRequest)
		return
	}

	collectors := campaign.Collectors
	if len(req.Collectors) > 0 {
		collectors = normalizeCollectors(req.Collectors)
	}

	entityCount, err := s.store.EntityCount(r.Context(), campaignID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_ = s.store.UpdateCampaignStatus(r.Context(), campaignID, StatusExpanding)

	jobsQueued := 0
	for _, entityID := range req.EntityIDs {
		entityType, value, err := s.store.GetEntity(r.Context(), entityID)
		if err == pgx.ErrNoRows {
			continue
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var depth int
		_ = s.store.pool.QueryRow(r.Context(), `
			SELECT depth FROM campaign_entities WHERE campaign_id = $1 AND entity_id = $2
		`, campaignID, entityID).Scan(&depth)

		nextDepth := depth + 1
		if nextDepth > campaign.MaxDepth {
			continue
		}
		if entityCount >= campaign.MaxEntities {
			break
		}

		for _, collector := range collectorsForEntity(entityType, collectors) {
			if !MVPCollectors[collector] {
				continue
			}

			jobID, err := s.store.InsertCrawlJob(r.Context(), campaignID, entityID, collector, nextDepth)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if jobID == "" {
				continue
			}

			dedupeKey := fmt.Sprintf("atlas:dedupe:%s:%s:%s", campaignID, entityID, collector)
			if s.redis != nil {
				set, err := s.redis.SetNX(r.Context(), dedupeKey, "1", 24*time.Hour).Result()
				if err == nil && !set {
					continue
				}
			}

			msg := CrawlJobMessage{
				JobID:       jobID,
				CampaignID:  campaignID,
				EntityID:    entityID,
				EntityType:  entityType,
				EntityValue: value,
				Collector:   collector,
				Depth:       nextDepth,
			}
			if err := s.publishJob(msg); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			jobsQueued++
		}
	}

	_ = s.store.MarkExpansionApproved(r.Context(), campaignID, req.EntityIDs)
	_ = s.store.InsertEvent(r.Context(), campaignID, "campaign.expanding", "expansion approved", map[string]interface{}{
		"entity_ids":  req.EntityIDs,
		"jobs_queued": jobsQueued,
	})

	if jobsQueued > 0 {
		_ = s.store.UpdateCampaignStatus(r.Context(), campaignID, StatusRunning)
	} else {
		_ = s.store.ReconcileCampaignStatus(r.Context(), campaignID)
	}

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"campaign_id": campaignID,
		"status":      StatusRunning,
		"jobs_queued": jobsQueued,
	})
}

func (s *Server) publishJob(msg CrawlJobMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	subject := "atlas.jobs." + msg.Collector
	return s.nc.Publish(subject, payload)
}

func (s *Server) startStatusReconciler(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.store.ReconcileStaleJobs(ctx, 15*time.Minute)

			rows, err := s.store.pool.Query(ctx, `
				SELECT id FROM campaigns
				WHERE status IN ('running', 'expanding', 'queued')
			`)
			if err != nil {
				continue
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					continue
				}
				_ = s.store.ReconcileCampaignStatus(ctx, id)
			}
			rows.Close()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func campaignHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/campaigns/")
		parts := strings.Split(strings.Trim(path, "/"), "/")

		if len(parts) == 0 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}

		campaignID := parts[0]

		if len(parts) == 1 && r.Method == http.MethodGet {
			s.getCampaign(w, r, campaignID)
			return
		}
		if len(parts) == 2 && parts[1] == "progress" && r.Method == http.MethodGet {
			s.getProgress(w, r, campaignID)
			return
		}
		if len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet {
			s.getEvents(w, r, campaignID)
			return
		}
		if len(parts) == 2 && parts[1] == "entities" && r.Method == http.MethodGet {
			s.getEntities(w, r, campaignID)
			return
		}
		if len(parts) == 2 && parts[1] == "edges" && r.Method == http.MethodGet {
			s.getEdges(w, r, campaignID)
			return
		}
		if len(parts) == 2 && parts[1] == "report" && r.Method == http.MethodGet {
			s.getReport(w, r, campaignID)
			return
		}
		if len(parts) == 2 && parts[1] == "expand" && r.Method == http.MethodPost {
			s.expandCampaign(w, r, campaignID)
			return
		}

		http.NotFound(w, r)
	}
}
