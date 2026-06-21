package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) seedDomains(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domains    []string `json:"domains"`
		Collectors []string `json:"collectors"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Domains) == 0 {
		http.Error(w, "domains is required", http.StatusBadRequest)
		return
	}

	collectors := normalizeCollectors(req.Collectors)
	if !collectorsIncludeIntelligence(collectors) {
		collectors = append(collectors, "rdap")
	}

	queued := 0
	results := []map[string]interface{}{}

	for _, raw := range req.Domains {
		domain, err := normalizeDomain(raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		apex := registrableDomain(domain)
		domainID, err := s.store.UpsertDomain(r.Context(), apex)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		msg := EnrichDomainMessage{Domain: domain, Collectors: collectors}
		if err := s.publishEnrichJob(msg); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		queued++
		entry := map[string]interface{}{
			"domain":    domain,
			"apex":      apex,
			"domain_id": domainID,
			"status":    "queued",
		}
		results = append(results, entry)
	}

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"domains":      results,
		"jobs_queued":  queued,
		"collectors":   collectors,
	})
}

func collectorsIncludeIntelligence(collectors []string) bool {
	for _, c := range collectors {
		if c == "rdap" {
			return true
		}
	}
	return false
}

func (s *Server) publishEnrichJob(msg EnrichDomainMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.nc.Publish("atlas.enrich.domain", payload)
}

func domainHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/domains/")
		parts := strings.Split(strings.Trim(path, "/"), "/")

		if len(parts) == 0 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}

		domain, err := normalizeDomain(parts[0])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, apex, err := apexDomain(domain)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if len(parts) == 1 && r.Method == http.MethodGet {
			s.getDomain(w, r, apex, domain)
			return
		}
		if len(parts) == 2 {
			switch parts[1] {
			case "subdomains":
				if r.Method == http.MethodGet {
					s.getDomainSubdomains(w, r, apex)
					return
				}
			case "certificates":
				if r.Method == http.MethodGet {
					s.getDomainCertificates(w, r, apex)
					return
				}
			case "rdap":
				if r.Method == http.MethodGet {
					s.getDomainRDAP(w, r, apex, domain)
					return
				}
			case "dns":
				if r.Method == http.MethodGet {
					s.getDomainDNS(w, r, apex, domain)
					return
				}
			case "pivots":
				if r.Method == http.MethodGet {
					s.getDomainPivots(w, r, apex)
					return
				}
			case "enrich":
				if r.Method == http.MethodPost {
					s.enrichDomain(w, r, domain)
					return
				}
			}
		}

		http.NotFound(w, r)
	}
}

func pivotHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/pivots/")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}

		pivotType := parts[0]
		value := parts[1]

		var (
			results []PivotDomainResult
			err     error
		)

		switch pivotType {
		case "nameserver":
			results, err = s.store.PivotByNameserver(r.Context(), value)
		case "registrar":
			results, err = s.store.PivotByRegistrar(r.Context(), value)
		case "certificate":
			results, err = s.store.PivotByCertificate(r.Context(), value)
		case "mx":
			results, err = s.store.PivotByMX(r.Context(), value)
		case "favicon":
			results, err = s.store.PivotByFavicon(r.Context(), value)
		case "entity":
			results, err = s.store.PivotByEntity(r.Context(), value)
		case "tracker":
			results, err = s.store.PivotByTracker(r.Context(), value)
		default:
			http.NotFound(w, r)
			return
		}

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"pivot_type": pivotType,
			"value":      value,
			"domains":    results,
			"count":      len(results),
		})
	}
}

func (s *Server) getDomain(w http.ResponseWriter, r *http.Request, apex string, queriedHost string) {
	record, err := s.store.GetDomainByName(r.Context(), apex)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if record == nil {
		http.NotFound(w, r)
		return
	}

	edges, _ := s.store.ListGraphEdgesForDomain(r.Context(), apex)

	resp := map[string]interface{}{
		"domain": record,
		"edges":  edges,
	}
	if queriedHost != apex {
		resp["queried_host"] = queriedHost
		resp["apex"] = apex
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getDomainSubdomains(w http.ResponseWriter, r *http.Request, domain string) {
	names, err := s.store.ListSubdomainsForDomain(r.Context(), domain)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"domain":     domain,
		"subdomains": names,
		"count":      len(names),
	})
}

func (s *Server) getDomainCertificates(w http.ResponseWriter, r *http.Request, domain string) {
	certs, err := s.store.ListCertificatesForDomain(r.Context(), domain)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"domain":       domain,
		"certificates": certs,
		"count":        len(certs),
	})
}

func (s *Server) getDomainRDAP(w http.ResponseWriter, r *http.Request, apex string, queriedHost string) {
	record, err := s.store.GetDomainByName(r.Context(), apex)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if record == nil {
		http.NotFound(w, r)
		return
	}

	rdap, err := s.store.GetLatestRDAP(r.Context(), record.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rdap == nil {
		resp := map[string]interface{}{
			"domain": apex,
			"rdap":   nil,
		}
		if queriedHost != apex {
			resp["queried_host"] = queriedHost
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp := map[string]interface{}{
		"domain": apex,
		"rdap":   rdap,
	}
	if queriedHost != apex {
		resp["queried_host"] = queriedHost
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getDomainDNS(w http.ResponseWriter, r *http.Request, apex string, queriedHost string) {
	records, err := s.store.ListDNSForDomain(r.Context(), apex)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if queriedHost != apex {
		filtered := make([]DNSRecord, 0, len(records))
		for _, rec := range records {
			if rec.Name == queriedHost || strings.HasSuffix(rec.Name, "."+queriedHost) {
				filtered = append(filtered, rec)
			}
		}
		records = filtered
	}
	resp := map[string]interface{}{
		"domain":  apex,
		"records": records,
		"count":   len(records),
	}
	if queriedHost != apex {
		resp["queried_host"] = queriedHost
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getDomainPivots(w http.ResponseWriter, r *http.Request, domain string) {
	pivots, err := s.store.GetDomainPivots(r.Context(), domain)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"domain": domain,
		"pivots": pivots,
	})
}

func (s *Server) enrichDomain(w http.ResponseWriter, r *http.Request, domain string) {
	var req struct {
		Collectors []string `json:"collectors"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	collectors := normalizeCollectors(req.Collectors)
	apex := registrableDomain(domain)
	domainID, err := s.store.UpsertDomain(r.Context(), apex)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	msg := EnrichDomainMessage{Domain: domain, Collectors: collectors}
	if err := s.publishEnrichJob(msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"domain":     domain,
		"apex":       apex,
		"domain_id":  domainID,
		"status":     "queued",
		"collectors": collectors,
	})
}
