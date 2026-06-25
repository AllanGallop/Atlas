package main

import (
	"encoding/json"
	"net/http"
)

func (s *Server) getCTConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetCTConfig(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) setCTConfig(w http.ResponseWriter, r *http.Request) {
	var cfg CTIngestorConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.store.SetCTConfig(r.Context(), cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "updated",
		"config": cfg,
	})
}

func (s *Server) startCTBackfill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetTLDs      []string `json:"target_tlds"`
		IncludeReadonly *bool    `json:"include_readonly"`
		BatchesPerCycle *int     `json:"batches_per_cycle"`
		BatchSize       *int     `json:"batch_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && r.ContentLength > 0 {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cfg, err := s.store.GetCTConfig(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if len(req.TargetTLDs) > 0 {
		cfg.TargetTLDs = req.TargetTLDs
	}
	cfg.BackfillMode = true
	if req.IncludeReadonly != nil {
		cfg.IncludeReadonly = *req.IncludeReadonly
	} else {
		cfg.IncludeReadonly = true
	}
	if req.BatchesPerCycle != nil && *req.BatchesPerCycle > 0 {
		cfg.BatchesPerCycle = *req.BatchesPerCycle
	}
	if req.BatchSize != nil && *req.BatchSize > 0 {
		cfg.BatchSize = *req.BatchSize
	}

	if err := s.store.SetCTConfig(r.Context(), cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"status":  "backfill_enabled",
		"config":  cfg,
		"message": "ct-ingestor will pick up config on next cycle",
	})
}

func (s *Server) getCTStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.store.GetCTStatus(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func ctHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ct/config":
			switch r.Method {
			case http.MethodGet:
				s.getCTConfig(w, r)
			case http.MethodPut, http.MethodPost:
				s.setCTConfig(w, r)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		case "/ct/backfill":
			if r.Method == http.MethodPost {
				s.startCTBackfill(w, r)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		case "/ct/status":
			if r.Method == http.MethodGet {
				s.getCTStatus(w, r)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		default:
			http.NotFound(w, r)
		}
	}
}
