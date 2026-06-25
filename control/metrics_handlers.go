package main

import (
	"context"
	"net/http"
	"strings"
)

func (s *Server) getMetrics(w http.ResponseWriter, r *http.Request) {
	snap, err := s.store.CollectMetrics(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	snap.Infra = s.infraMetrics(r.Context())

	if wantsPrometheus(r) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(renderPrometheusMetrics(snap)))
		return
	}

	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) infraMetrics(ctx context.Context) InfraMetrics {
	out := InfraMetrics{NATS: "disconnected", Redis: "unavailable"}

	if s.nc != nil && s.nc.IsConnected() {
		out.NATS = "connected"
	}
	if s.redis != nil {
		if err := s.redis.Ping(ctx).Err(); err == nil {
			out.Redis = "connected"
		}
	}

	return out
}

func wantsPrometheus(r *http.Request) bool {
	if r.URL.Path == "/metrics/prometheus" {
		return true
	}
	if r.URL.Query().Get("format") == "prometheus" {
		return true
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/plain")
}

func metricsHandler(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.getMetrics(w, r)
	}
}
